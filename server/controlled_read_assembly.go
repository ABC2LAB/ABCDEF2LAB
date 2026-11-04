package server

import "context"

// NewWithControlledReadAssembly requires the audit authority in addition to the
// revisioned strict host. It does not provision schemas, keys, roots or epochs.
// All resources remain owned by the caller, including on constructor failure.
func NewWithControlledReadAssembly(ctx context.Context, m *Manager, skillDir, dataDir, keyDir string, options ControlledReadRevisionHostOptions) (*Server, error) {
	if ctx == nil || ctx.Err() != nil || m == nil || m.pg == nil ||
		options.Durable.Host.Findings == nil || options.Durable.Publisher == nil ||
		options.Scope == nil || options.DescriptorRoot == nil || options.Durable.Host.AuditSource == nil {
		return nil, ErrControlledReadHostConfiguration
	}
	mode := options.Durable.Host.Mode
	if mode.RootPath == "" || mode.RootAnchor == nil || mode.CheckRoot == nil || mode.CheckTaskQuiescent == nil {
		return nil, ErrControlledReadHostConfiguration
	}
	// Snapshot the options before construction; the caller need not retain the
	// options pointer. Provider lifetime and concurrent safety remain host duties.
	audit := *options.Durable.Host.AuditSource
	if audit.LatestCheckpoint == nil || audit.ResolveKey == nil ||
		audit.MaxJournalBytes <= 0 || audit.MaxJournalBytes > maxControlledAuditSourceBytes ||
		audit.MaxEvents == 0 || audit.MaxEvents > maxControlledAuditSourceEvents ||
		audit.Timeout <= 0 || audit.Timeout > maxControlledAuditSourceTimeout {
		return nil, ErrControlledReadHostConfiguration
	}
	options.Durable.Host.AuditSource = &audit
	return NewWithRevisionedDurableControlledReadHost(ctx, m, skillDir, dataDir, keyDir, options)
}

// Called only inside strict bootstrap, after mode/publisher configuration and
// before finding admission, task restoration or background loops. Constructing
// the reader here binds it to this server, without an uninitialized callback.
func (s *Server) bindControlledReadAuditSource(findings ControlledReadFindingResolver, options *ControlledReadAuditSourceOptions) (ControlledReadFindingResolver, error) {
	if findings == nil {
		return nil, ErrControlledReadHostConfiguration
	}
	if options == nil {
		return findings, nil
	}
	reader, err := s.NewControlledReadAuditSource(*options)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, request ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
		// Trust loss must prevent the input adapter from doing work as well as
		// reject later Source reads. Each Source invocation rechecks fresh trust.
		if reader.check(ctx) != nil {
			return ControlledReadFindingPlan{}, ErrControlledReadAuditSource
		}
		plan, err := findings(ctx, request)
		if err != nil {
			return ControlledReadFindingPlan{}, err
		}
		if reader.check(ctx) != nil {
			return ControlledReadFindingPlan{}, ErrControlledReadAuditSource
		}
		plan.Source, err = reader.SourceReader(plan.Source)
		if err != nil {
			return ControlledReadFindingPlan{}, err
		}
		return plan, nil
	}, nil
}
