package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/auditkeys"
	"github.com/Autumn-27/artex/auditpublisher"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/evidencerevision"
	"github.com/Autumn-27/artex/taskmode"
	"github.com/Autumn-27/artex/transmit"
)

type controlledLocalCheckpoints struct{ h *ControlledReadLocalHost }

func (p controlledLocalCheckpoints) Acquire(ctx context.Context, a auditcheckpoint.Access) (auditcheckpoint.Lease, error) {
	t, err := p.h.enter(ctx)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			t.Release()
		}
	}()
	h := p.h
	if a.Epoch != h.epoch || a.Binding.PublisherRef != h.binding.PublisherRef || a.Binding.KeyRef != h.binding.Key.KeyRef || !validHex(a.Binding.JournalID, 32) || h.checkBound(t.ctx, a.Binding.Scope, h.binding.Execution.IntentID) != nil {
		return nil, ErrControlledReadLocalHost
	}
	if a.Purpose != auditcheckpoint.PublishAccess && a.Purpose != auditcheckpoint.HeadAccess && a.Purpose != auditcheckpoint.OperationAccess {
		return nil, ErrControlledReadLocalHost
	}
	h.mu.Lock()
	if h.journalID == "" && a.Purpose == auditcheckpoint.PublishAccess && a.Publication.Kind == auditcheckpoint.Register && a.Publication.ExpectedRevision == 0 {
		h.journalID = a.Binding.JournalID
	}
	valid := h.journalID != "" && h.journalID == a.Binding.JournalID
	h.mu.Unlock()
	if !valid {
		return nil, ErrControlledReadLocalHost
	}
	// Seal은 기존 controlledCheckpointHost의 실제 drain/CloseWitness 검증 뒤에만 위임된다.
	if a.Purpose == auditcheckpoint.PublishAccess && (a.Publication.Epoch != h.epoch || a.Publication.PublisherRef != h.binding.PublisherRef || a.Publication.Candidate.Scope != a.Binding.Scope || a.Publication.Candidate.JournalID != a.Binding.JournalID || a.Publication.Candidate.KeyRef != a.Binding.KeyRef) {
		return nil, ErrControlledReadLocalHost
	}
	ok = true
	return &controlledLocalCheckpointLease{controlledLocalTicket: t, epoch: h.epoch, binding: a.Binding}, nil
}

type controlledLocalCheckpointLease struct {
	*controlledLocalTicket
	epoch   auditcheckpoint.Epoch
	binding auditcheckpoint.Binding
}

func (l *controlledLocalCheckpointLease) Epoch() auditcheckpoint.Epoch { return l.epoch }
func (l *controlledLocalCheckpointLease) ResolveKey(ctx context.Context, b auditcheckpoint.Binding) ([]byte, error) {
	if b != l.binding || l.ctx.Err() != nil || l.h.CheckRoot(ctx) != nil {
		return nil, ErrControlledReadLocalHost
	}
	return l.h.key, nil
}

type controlledLocalKeys struct{ h *ControlledReadLocalHost }

func (p controlledLocalKeys) WithTrust(ctx context.Context, r auditkeys.TrustRequest, consume func(auditkeys.Attestation) error) error {
	if consume == nil {
		return ErrControlledReadLocalHost
	}
	t, err := p.h.enter(ctx)
	if err != nil {
		return err
	}
	defer t.Release()
	k := p.h.binding.Key
	expected := auditkeys.TrustRequest{Namespace: k.Namespace, PolicyRef: k.PolicyRef, Binding: auditkeys.Binding{KeyRef: k.KeyRef, SecretVersionRef: k.SecretVersionRef, Identity: k.Identity, PredecessorRef: k.PredecessorRef}, Command: auditkeys.Command{KeyRef: k.KeyRef}, CurrentRevision: k.Revision, CurrentState: auditkeys.Active, Inspect: true}
	if r != expected {
		return ErrControlledReadLocalHost
	}
	attestation := p.h.attestation
	attestation.Request = r
	if err = consume(attestation); err != nil {
		return err
	}
	return p.h.CheckRoot(t.ctx)
}

func (h *ControlledReadLocalHost) checkBound(ctx context.Context, s transmit.Scope, intent int64) error {
	if h.CheckRoot(ctx) != nil {
		return ErrControlledReadLocalHost
	}
	h.mu.Lock()
	valid := h.scope != (transmit.Scope{}) && h.scope == s && h.intent == intent
	h.mu.Unlock()
	if !valid {
		return ErrControlledReadLocalHost
	}
	return nil
}

type controlledLocalPathScope struct {
	*controlledLocalTicket
	anchor *os.File
	path   string
}

func (s *controlledLocalPathScope) Anchor() *os.File  { return s.anchor }
func (s *controlledLocalPathScope) Directory() string { return s.path }

func (h *ControlledReadLocalHost) PublisherFactory() ControlledReadPublisherFactory {
	return func(ctx context.Context, r ControlledReadPublisherRequest) (ControlledReadPublisherPlan, error) {
		if h.AuthorizeExecution(ctx, r.Scope, r.IntentID) != nil || r.KeyRef != h.binding.Key.KeyRef || r.Filename != r.Scope.RunRef+".jsonl" {
			return ControlledReadPublisherPlan{}, ErrControlledReadLocalHost
		}
		t, err := h.enter(ctx)
		if err != nil {
			return ControlledReadPublisherPlan{}, err
		}
		h.mu.Lock()
		if h.publisherIssued {
			h.mu.Unlock()
			t.Release()
			return ControlledReadPublisherPlan{}, ErrControlledReadLocalHost
		}
		h.publisherIssued = true
		h.mu.Unlock()
		return ControlledReadPublisherPlan{Host: controlledLocalCheckpoints{h}, Database: h.authorityDB, Epoch: h.epoch, PublisherRef: h.binding.PublisherRef, RootScope: &controlledLocalPathScope{controlledLocalTicket: t, anchor: h.physical.journals, path: h.binding.Journals.Path}, Limits: h.limits}, nil
	}
}

func (h *ControlledReadLocalHost) AuditSourceOptions() ControlledReadAuditSourceOptions {
	return ControlledReadAuditSourceOptions{LatestCheckpoint: func(ctx context.Context, s transmit.Scope) (ControlledReadAuditAnchor, error) {
		if h.checkBound(ctx, s, h.binding.Execution.IntentID) != nil {
			return ControlledReadAuditAnchor{}, ErrControlledReadLocalHost
		}
		h.mu.Lock()
		journal := h.journalID
		h.mu.Unlock()
		store, err := auditcheckpoint.New(h.authorityDB, controlledLocalCheckpoints{h}, h.limits)
		if err != nil {
			return ControlledReadAuditAnchor{}, ErrControlledReadLocalHost
		}
		head, err := store.Head(ctx, auditcheckpoint.Query{Epoch: h.epoch, Binding: auditcheckpoint.Binding{PublisherRef: h.binding.PublisherRef, Scope: s, JournalID: journal, KeyRef: h.binding.Key.KeyRef}})
		if err != nil || head.Phase != auditcheckpoint.Sealed || head.Checkpoint.Scope != s {
			return ControlledReadAuditAnchor{}, ErrControlledReadLocalHost
		}
		ref := sha256.Sum256([]byte(h.epoch.StoreRef + "\x00" + s.RunRef + "\x00" + strconv.FormatUint(head.Revision, 10)))
		return ControlledReadAuditAnchor{Checkpoint: head.Checkpoint, Ref: "checkpoint:" + hex.EncodeToString(ref[:])}, nil
	}, ResolveKey: func(ctx context.Context, r ControlledReadAuditKeyRequest) ([]byte, error) {
		if h.checkBound(ctx, r.Scope, h.binding.Execution.IntentID) != nil || r.KeyRef != h.binding.Key.KeyRef {
			return nil, ErrControlledReadLocalHost
		}
		h.mu.Lock()
		valid := h.journalID != "" && r.JournalID == h.journalID
		h.mu.Unlock()
		if !valid {
			return nil, ErrControlledReadLocalHost
		}
		return h.key, nil
	}, MaxJournalBytes: h.limits.MaxBytes, MaxEvents: h.limits.MaxEvents, Timeout: h.limits.Timeout}
}

type controlledLocalRevisions struct{ h *ControlledReadLocalHost }

func (p controlledLocalRevisions) Acquire(ctx context.Context, a evidencerevision.Access) (evidencerevision.Lease, error) {
	h := p.h
	if a.Purpose != evidencerevision.IssueInitial && a.Purpose != evidencerevision.ReadReceipt || a.TaskID != h.binding.Execution.TaskID || a.RequestDigest == ([32]byte{}) || h.checkBound(ctx, a.Scope, a.IntentID) != nil || a.OperationRef != controlledRevisionOperation(ControlledReadVerificationRequest{Scope: a.Scope, IntentID: a.IntentID}) {
		return nil, ErrControlledReadLocalHost
	}
	t, err := h.enter(ctx)
	if err != nil {
		return nil, err
	}
	markerReader, err := newControlledTaskMarkerReader(controlledTaskMarkerReaderOptions{RootPath: h.Directory(), RootAnchor: h.Anchor(), MaxEntries: maxTaskMarkerEntries, CheckScope: func(call context.Context, id int64) error {
		if id != a.TaskID {
			return ErrControlledReadLocalHost
		}
		return h.checkBound(call, a.Scope, a.IntentID)
	}})
	if err != nil {
		t.Release()
		return nil, ErrControlledReadLocalHost
	}
	marker, err := markerReader(t.ctx, a.TaskID)
	if err != nil || marker.State != taskmode.MarkerPresent || h.CheckRoot(t.ctx) != nil {
		t.Release()
		return nil, ErrControlledReadLocalHost
	}
	return &controlledLocalRevisionLease{controlledLocalTicket: t, marker: marker}, nil
}

type controlledLocalRevisionLease struct {
	*controlledLocalTicket
	marker taskmode.Marker
}

func (l *controlledLocalRevisionLease) MarkerSnapshot() taskmode.Marker { return l.marker }

type controlledLocalDescriptors struct{ h *ControlledReadLocalHost }

func (p controlledLocalDescriptors) AcquireDescriptorRoot(ctx context.Context, a evidencerevision.DescriptorFileAccess) (evidencerevision.DescriptorRootScope, error) {
	h := p.h
	if a.Purpose != evidencerevision.PersistDescriptorFile && a.Purpose != evidencerevision.ReadDescriptorFile {
		return nil, ErrControlledReadLocalHost
	}
	h.mu.Lock()
	scope, intent := h.scope, h.intent
	h.mu.Unlock()
	if h.checkBound(ctx, scope, intent) != nil || a.OperationRef != controlledRevisionOperation(ControlledReadVerificationRequest{Scope: scope, IntentID: intent}) {
		return nil, ErrControlledReadLocalHost
	}
	t, err := h.enter(ctx)
	if err != nil {
		return nil, err
	}
	return &controlledLocalPathScope{controlledLocalTicket: t, anchor: h.physical.descriptors, path: h.binding.Descriptors.Path}, nil
}

type controlledLocalFinalization struct{ h *ControlledReadLocalHost }

func (h *ControlledReadLocalHost) FinalizationScopes() ControlledReadFinalizationScopeProvider {
	return controlledLocalFinalization{h}
}
func (p controlledLocalFinalization) Acquire(ctx context.Context, r ControlledReadVerificationRequest, e evidencebind.Context) (ControlledReadFinalizationScope, error) {
	if r.TaskID != p.h.binding.Execution.TaskID || e.Scope != r.Scope || e.IntentID != r.IntentID || e.ResourceRef != r.ResourceRef || e.Evaluated.Label != r.AccountRef || e.ResourceRef == "" || e.ReadApprovalRef == "" || e.KGPolicyVersion == "" || p.h.checkBound(ctx, r.Scope, r.IntentID) != nil {
		return nil, ErrControlledReadLocalHost
	}
	t, err := p.h.enter(ctx)
	if err != nil {
		return nil, err
	}
	return &controlledLocalFinalizationLease{controlledLocalTicket: t, scope: r.Scope, intent: r.IntentID}, nil
}

type controlledLocalFinalizationLease struct {
	*controlledLocalTicket
	scope  transmit.Scope
	intent int64
}

func (l *controlledLocalFinalizationLease) Check(ctx context.Context) error {
	if l.ctx.Err() != nil {
		return ErrControlledReadLocalHost
	}
	return l.h.checkBound(ctx, l.scope, l.intent)
}

var _ auditcheckpoint.Host = controlledLocalCheckpoints{}
var _ auditkeys.Authority = controlledLocalKeys{}
var _ evidencerevision.ScopeProvider = controlledLocalRevisions{}
var _ evidencerevision.DescriptorRootProvider = controlledLocalDescriptors{}
var _ auditpublisher.RootScope = (*controlledLocalPathScope)(nil)
