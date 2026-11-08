// 승인된 기존 공급자의 수명과 실제 검증 경계만 조립하며 권한을 발급하지 않는다.
package server

import (
	"context"
	"crypto/sha256"
)

func NewWithControlledReadAssembly(ctx context.Context, m *Manager, skillDir, dataDir, keyDir string, options ControlledReadRevisionHostOptions) (*Server, error) {
	return newWithControlledReadAssembly(ctx, m, skillDir, dataDir, keyDir, options, "", nil)
}

func newWithControlledReadAssembly(ctx context.Context, m *Manager, skillDir, dataDir, keyDir string, options ControlledReadRevisionHostOptions, snapshotDir string, authority ControlledReadAssemblyInputAuthority) (*Server, error) {
	if ctx == nil || ctx.Err() != nil || m == nil || m.pg == nil || m.pg.DB == nil ||
		options.Durable.Host.Findings == nil || options.Durable.Publisher == nil ||
		nilFinalizationValue(options.Scope) || nilFinalizationValue(options.DescriptorRoot) || options.Durable.Host.AuditSource == nil {
		return nil, ErrControlledReadHostConfiguration
	}
	mode := options.Durable.Host.Mode
	if mode.RootPath == "" || mode.RootAnchor == nil || mode.CheckRoot == nil || mode.CheckTaskQuiescent == nil {
		return nil, ErrControlledReadHostConfiguration
	}
	audit := *options.Durable.Host.AuditSource
	if audit.LatestCheckpoint == nil || audit.ResolveKey == nil ||
		audit.MaxJournalBytes <= 0 || audit.MaxJournalBytes > maxControlledAuditSourceBytes ||
		audit.MaxEvents == 0 || audit.MaxEvents > maxControlledAuditSourceEvents ||
		audit.Timeout <= 0 || audit.Timeout > maxControlledAuditSourceTimeout {
		return nil, ErrControlledReadHostConfiguration
	}
	if CheckControlledReadAssemblyDatabase(ctx, m.pg.DB) != nil || CheckFirstDiagnosisRuntimeDatabase(ctx, m.pg.DB) != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	var store *controlledReadStore
	var err error
	if snapshotDir != "" {
		store, err = existingControlledReadAssemblyStoreAt(mode.RootPath, snapshotDir)
	} else {
		store, err = existingControlledReadAssemblyStore(mode.RootPath)
	}
	if err != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	if !nilFinalizationValue(authority) {
		store.authorizeInput = func(path string, body []byte) error { return authority.AuthorizeInput(ctx, path, sha256.Sum256(body)) }
	}
	options.Durable.Host.assemblyStore = store
	options.Durable.Host.existingAuthOnly = true
	options.Durable.Host.AuditSource = &audit
	options.Durable.Host.Findings = RequireControlledReadFinalizationScope(options.Durable.Host.Findings)
	return NewWithRevisionedDurableControlledReadHost(ctx, m, skillDir, dataDir, keyDir, options)
}

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
