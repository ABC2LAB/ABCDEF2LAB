package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/evidencerevision"
	"github.com/Autumn-27/artex/transmit"
)

// ControlledReadRevisionHostOptions is trusted Go assembly, never HTTP/model
// input. Both authorities are actual host producers; neither has a default.
// The caller must apply evidencerevision.Migrate explicitly before use.
type ControlledReadRevisionHostOptions struct {
	Durable        ControlledReadDurableHostOptions        `json:"-"`
	Scope          evidencerevision.ScopeProvider          `json:"-"`
	DescriptorRoot evidencerevision.DescriptorRootProvider `json:"-"`
}

// NewWithRevisionedDurableControlledReadHost installs the SQL revision producer
// before restored/background admission. Existing constructors retain their
// explicit compatibility behavior; this constructor cannot silently downgrade.
func NewWithRevisionedDurableControlledReadHost(ctx context.Context, m *Manager, skillDir, dataDir, keyDir string, options ControlledReadRevisionHostOptions) (*Server, error) {
	if ctx == nil || m == nil || m.pg == nil || options.Durable.Publisher == nil {
		return nil, ErrControlledReadHostConfiguration
	}
	revisions, err := newControlledRevisionServices(m, options.Scope, options.DescriptorRoot)
	if err != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	return newControlledReadHost(ctx, m, skillDir, dataDir, keyDir, options.Durable.Host, options.Durable.Publisher, revisions)
}

type controlledRevisionServices struct {
	store *evidencerevision.Store
	files *evidencerevision.DescriptorFiles
}

func newControlledRevisionServices(m *Manager, scope evidencerevision.ScopeProvider, roots evidencerevision.DescriptorRootProvider) (*controlledRevisionServices, error) {
	if m == nil || m.pg == nil {
		return nil, ErrControlledReadHostConfiguration
	}
	store, err := evidencerevision.New(m.pg.DB, scope)
	if err != nil {
		return nil, err
	}
	files, err := evidencerevision.NewDescriptorFiles(roots)
	if err != nil {
		return nil, err
	}
	return &controlledRevisionServices{store: store, files: files}, nil
}

type controlledRevisionFailure struct{ reason, operation string }

func (e controlledRevisionFailure) Error() string { return "controlled_revision_" + e.reason }
func (e controlledRevisionFailure) Unwrap() error { return ErrControlledFindingUnavailable }

// One stable operation identifies this already-authorized run/intent. It does
// not mint a RunRef or retry a run after an uncertain outcome.
func controlledRevisionOperation(request ControlledReadVerificationRequest) string {
	// Scope contains strings and Intent is int64; this closed struct has no
	// custom marshaler or JSON-unsupported value, so encoding cannot fail.
	body, _ := json.Marshal(struct {
		Scope  transmit.Scope
		Intent int64
	}{request.Scope, request.IntentID})
	sum := sha256.Sum256(append([]byte("artex.controlled.revision.operation.v1\x00"), body...))
	return "revision:" + hex.EncodeToString(sum[:])
}

func (c *controlledFindingCollector) revisionSource(ctx context.Context, expected evidencebind.Context) (e3record.Source, error) {
	fail := func(reason string) (e3record.Source, error) {
		err, _ := c.retainRevisionFailure(reason)
		return e3record.Source{}, err
	}
	if err := c.currentRevisionFailure(); err != nil {
		return e3record.Source{}, err
	}
	// A is read only after the engine's completion claim. Its exploration/mode
	// facts select the SQL request; no cached runtime value grants authority.
	a, err := c.plan.Source(ctx, expected)
	if err != nil || ctx.Err() != nil || a.Expected != expected || a.ExplorationID <= 0 || a.ModeVersion == 0 || a.ModeVersion > math.MaxInt64 {
		return fail("source_a_rejected")
	}
	request := evidencerevision.Request{OperationRef: c.revisionOperation, Collected: c.revisionCollected, ExpectedExplorationID: a.ExplorationID, ExpectedModeVersion: a.ModeVersion}
	descriptor, err := evidencerevision.Describe(request)
	if err != nil {
		return fail("descriptor_rejected")
	}
	// A durable descriptor is mandatory before Issue. No descriptor failure can
	// be hidden by successful SQL metadata creation.
	if err = c.revisions.files.Put(ctx, descriptor); err != nil {
		return fail("descriptor_unacknowledged")
	}
	receipt, err := c.revisions.store.Issue(ctx, request)
	if err != nil {
		reason := "issuance_rejected"
		if errors.Is(err, evidencerevision.ErrOutcomeUnknown) {
			reason = "issuance_outcome_unknown"
		}
		if errors.Is(err, evidencerevision.ErrConflict) {
			reason = "issuance_conflict"
		}
		return fail(reason)
	}
	if receipt.OperationRef != descriptor.OperationRef || receipt.Scope != expected.Scope || receipt.TaskID != c.request.TaskID || receipt.IntentID != expected.IntentID || receipt.ExplorationID != a.ExplorationID || receipt.ModeVersion != a.ModeVersion || receipt.EvidenceDigest != c.digest || receipt.BindingDigest != descriptor.RequestDigest || receipt.Revision == 0 || receipt.Revision > math.MaxInt64 || a.EvidenceRevision != 0 && a.EvidenceRevision != receipt.Revision {
		return fail("receipt_rejected")
	}
	// B must independently re-read current audit/key/identity/KG/fixture truth
	// after all descriptor/SQL I/O. No descriptor/issuance I/O intervenes
	// between B and Prepare; existing storage gates still independently recheck.
	b, err := c.plan.Source(ctx, expected)
	if err != nil || ctx.Err() != nil || b.Expected != expected || b.ExplorationID != receipt.ExplorationID || b.ModeVersion != receipt.ModeVersion || b.EvidenceRevision != 0 && b.EvidenceRevision != receipt.Revision {
		return fail("source_b_rejected")
	}
	b.EvidenceRevision = receipt.Revision
	return b, nil
}

// A collection failure can precede the finalizer. Keep the first per-operation
// reason under the same private state lock; diagnostics run after unlocking.
func (c *controlledFindingCollector) retainRevisionFailure(reason string) (error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	first := c.revisionFailure == nil
	if first {
		c.revisionFailure = controlledRevisionFailure{reason: reason, operation: c.revisionOperation}
	}
	return c.revisionFailure, first
}

func (c *controlledFindingCollector) currentRevisionFailure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revisionFailure
}
