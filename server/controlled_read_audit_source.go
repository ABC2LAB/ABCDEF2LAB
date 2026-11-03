package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

var ErrControlledReadAuditSource = errors.New("controlled_read_audit_source_unavailable")

// These are reader resource ceilings, not execution approvals or defaults.
const (
	maxControlledAuditSourceBytes   = int64(64 << 20)
	maxControlledAuditSourceEvents  = uint64(4 * maxControlledRequests)
	maxControlledAuditSourceTimeout = 2 * time.Minute
)

// ControlledReadAuditAnchor comes from a trusted latest-head provider. The
// provider must authenticate the current store epoch, immutable execution/key
// binding and sealed phase. Neither a sidecar nor Checkpoint proves that phase.
// Ref is the provider's non-sensitive reference; the reader never issues one.
type ControlledReadAuditAnchor struct {
	Checkpoint auditlog.Checkpoint
	Ref        string
}

type ControlledReadAuditKeyRequest struct {
	Scope             transmit.Scope
	JournalID, KeyRef string
}

type ControlledReadAuditSourceOptions struct {
	LatestCheckpoint func(context.Context, transmit.Scope) (ControlledReadAuditAnchor, error)
	// ResolveKey must check the immutable KeyRef mapping and current trust state
	// for E3 consumption, including retirement/compromise policy. It returns a
	// borrowed immutable buffer; only reader-owned copies are cleared. No key
	// is selected from a filename, journal hint or model input.
	ResolveKey      func(context.Context, ControlledReadAuditKeyRequest) ([]byte, error)
	MaxJournalBytes int64
	MaxEvents       uint64
	Timeout         time.Duration
}

type ControlledReadAuditSource struct {
	runtime *controlledReadRuntime
	mode    *controlledTaskModeConfig
	options ControlledReadAuditSourceOptions
}

// NewControlledReadAuditSource adds no admission authority. Both providers must
// hold their trusted read scope throughout Read and honor its context. Repeated
// checks detect observed changes; they do not establish an epoch/lease or ABA
// protection. Regular-file kernel I/O cannot be forcibly cancelled by context.
func (s *Server) NewControlledReadAuditSource(options ControlledReadAuditSourceOptions) (*ControlledReadAuditSource, error) {
	if s == nil || s.engine == nil || options.LatestCheckpoint == nil || options.ResolveKey == nil ||
		options.MaxJournalBytes <= 0 || options.MaxJournalBytes > maxControlledAuditSourceBytes ||
		options.MaxEvents == 0 || options.MaxEvents > maxControlledAuditSourceEvents ||
		options.Timeout <= 0 || options.Timeout > maxControlledAuditSourceTimeout {
		return nil, ErrControlledReadAuditSource
	}
	r := s.engine.controlledReads
	if r == nil {
		return nil, ErrControlledReadAuditSource
	}
	r.mu.Lock()
	mode := r.mode
	r.mu.Unlock()
	if mode == nil {
		return nil, ErrControlledReadAuditSource
	}
	reader := &ControlledReadAuditSource{runtime: r, mode: mode, options: options}
	if reader.check(mode.lifecycle) != nil {
		return nil, ErrControlledReadAuditSource
	}
	return reader, nil
}

func (r *ControlledReadAuditSource) check(ctx context.Context) error {
	if r == nil || r.runtime == nil || r.mode == nil || ctx == nil || ctx.Err() != nil ||
		r.mode.lifecycle == nil || r.mode.lifecycle.Err() != nil || r.mode.host.CheckRoot == nil ||
		r.mode.host.RootAnchor == nil {
		return ErrControlledReadAuditSource
	}
	r.runtime.mu.Lock()
	valid := !r.runtime.closed && r.runtime.mode == r.mode && r.runtime.store != nil && r.runtime.store.stateDir == r.mode.host.RootPath
	r.runtime.mu.Unlock()
	if !valid || r.mode.host.CheckRoot(ctx) != nil || ctx.Err() != nil ||
		checkControlledAuditRoot(r.mode.host) != nil {
		return ErrControlledReadAuditSource
	}
	return nil
}

// ControlledReadFinalAudit contains verified metadata only. It carries no
// approval, KG, identity, completion claim or permission to create a finding.
type ControlledReadFinalAudit struct {
	valid            bool
	anchor           ControlledReadAuditAnchor
	digest           [32]byte
	intents, results uint64
}

func (r *ControlledReadAuditSource) Read(ctx context.Context, scope transmit.Scope) (ControlledReadFinalAudit, error) {
	if r == nil || ctx == nil || ctx.Err() != nil {
		return ControlledReadFinalAudit{}, ErrControlledReadAuditSource
	}
	ctx, cancel := context.WithTimeout(ctx, r.options.Timeout)
	defer cancel()
	if r.check(ctx) != nil || !safeFile.MatchString(scope.RunRef) {
		return ControlledReadFinalAudit{}, ErrControlledReadAuditSource
	}
	anchor, err := r.options.LatestCheckpoint(ctx, scope)
	cp := anchor.Checkpoint
	if err != nil || cp.Scope != scope || !safeRef.MatchString(anchor.Ref) || strings.Contains(anchor.Ref, "://") ||
		!validHex(cp.JournalID, 32) || !safeRef.MatchString(cp.KeyRef) || strings.Contains(cp.KeyRef, "://") ||
		cp.Sequence == 0 || cp.Sequence > r.options.MaxEvents {
		return ControlledReadFinalAudit{}, ErrControlledReadAuditSource
	}
	digest, err := ControlledReadCheckpointDigest(cp)
	if err != nil {
		return ControlledReadFinalAudit{}, ErrControlledReadAuditSource
	}
	request := ControlledReadAuditKeyRequest{Scope: scope, JournalID: cp.JournalID, KeyRef: cp.KeyRef}
	borrowed, err := r.options.ResolveKey(ctx, request)
	if err != nil || len(borrowed) != auditlog.KeyBytes {
		return ControlledReadFinalAudit{}, ErrControlledReadAuditSource
	}
	key := bytes.Clone(borrowed)
	defer clear(key)
	body, err := readControlledAuditFiles(ctx, r.mode.host, cp, r.options.MaxJournalBytes)
	if err != nil {
		return ControlledReadFinalAudit{}, ErrControlledReadAuditSource
	}
	defer clear(body)
	summary, err := auditlog.Verify(ctx, bytes.NewReader(body), key, cp)
	if err != nil || summary.Checkpoint != cp || r.check(ctx) != nil {
		return ControlledReadFinalAudit{}, ErrControlledReadAuditSource
	}
	// A successful MAC must not survive a provider's subsequent trust refusal.
	borrowed, err = r.options.ResolveKey(ctx, request)
	if err != nil || len(borrowed) != auditlog.KeyBytes {
		return ControlledReadFinalAudit{}, ErrControlledReadAuditSource
	}
	current := bytes.Clone(borrowed)
	defer clear(current)
	if subtle.ConstantTimeCompare(key, current) != 1 {
		return ControlledReadFinalAudit{}, ErrControlledReadAuditSource
	}
	after, err := r.options.LatestCheckpoint(ctx, scope)
	if err != nil || after != anchor || r.check(ctx) != nil {
		return ControlledReadFinalAudit{}, ErrControlledReadAuditSource
	}
	return ControlledReadFinalAudit{valid: true, anchor: anchor, digest: digest, intents: summary.Intents, results: summary.Results}, nil
}

// Bind supplies only audit fields for the same execution. Conflicting supplied
// audit fields are refused. All source authority and engine disposition fields
// remain exactly as supplied; Prepare and the private E2 collector still decide.
func (a ControlledReadFinalAudit) Bind(source e3record.Source) (e3record.Source, error) {
	cp := a.anchor.Checkpoint
	if !a.valid || source.Expected.Scope != cp.Scope ||
		source.FinalCheckpointRef != "" && source.FinalCheckpointRef != a.anchor.Ref ||
		source.FinalCheckpointDigest != ([32]byte{}) && source.FinalCheckpointDigest != a.digest ||
		source.FinalCheckpointScope != (transmit.Scope{}) && source.FinalCheckpointScope != cp.Scope ||
		source.FinalCheckpointSequence != 0 && source.FinalCheckpointSequence != cp.Sequence {
		return e3record.Source{}, ErrControlledReadAuditSource
	}
	source.FinalCheckpointRef, source.FinalCheckpointDigest = a.anchor.Ref, a.digest
	source.FinalCheckpointScope, source.FinalCheckpointSequence = cp.Scope, cp.Sequence
	return source, nil
}

// SourceReader connects the audit producer to #142's existing trusted metadata
// reader. Each finalization/retry reads fresh audit trust; it does not cache a
// previous audit result or supply missing KG/identity/harness metadata.
func (r *ControlledReadAuditSource) SourceReader(metadata e3record.SourceReader) (e3record.SourceReader, error) {
	if r == nil || r.mode == nil || metadata == nil {
		return nil, ErrControlledReadAuditSource
	}
	return func(ctx context.Context, expected evidencebind.Context) (e3record.Source, error) {
		if ctx == nil || ctx.Err() != nil {
			return e3record.Source{}, ErrControlledReadAuditSource
		}
		source, err := metadata(ctx, expected)
		if err != nil || source.Expected != expected {
			return e3record.Source{}, ErrControlledReadAuditSource
		}
		audit, err := r.Read(ctx, expected.Scope)
		if err != nil {
			return e3record.Source{}, ErrControlledReadAuditSource
		}
		return audit.Bind(source)
	}, nil
}
