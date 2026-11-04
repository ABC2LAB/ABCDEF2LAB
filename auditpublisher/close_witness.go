package auditpublisher

import (
	"context"
	"sync"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

// CloseWitness is in-process evidence for one publisher's successful local
// Journal.Close. Only Open creates a bound instance. A zero value, including a
// caller-created pointer, never proves Close. It says nothing about transmitter
// drain, task completion, principal identity, or external authorization.
type CloseWitness struct {
	mu      sync.RWMutex // independent of Publisher.mu; Host may Check during Seal
	epoch   auditcheckpoint.Epoch
	binding auditcheckpoint.Binding
	rootCtx context.Context
	closed  auditlog.Checkpoint
	bound   bool
	ready   bool
	failed  bool
}

func newCloseWitness(epoch auditcheckpoint.Epoch, binding auditcheckpoint.Binding, rootCtx context.Context) *CloseWitness {
	return &CloseWitness{epoch: epoch, binding: binding, rootCtx: rootCtx, bound: true}
}

// Check can be called by the trusted Host while it authorizes a Seal. It is
// deliberately nonblocking except for its own short state lock, and never
// calls the Publisher or the checkpoint store. The Host must also verify its
// own StopAndWait, authority, epoch, and key evidence.
func (w *CloseWitness) Check(access auditcheckpoint.Access) bool {
	if w == nil {
		return false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	if !w.bound || !w.ready || w.failed || w.rootCtx == nil || w.rootCtx.Err() != nil {
		return false
	}
	r := access.Publication
	return access.Purpose == auditcheckpoint.PublishAccess && r.Kind == auditcheckpoint.Seal &&
		access.Epoch == w.epoch && r.Epoch == w.epoch &&
		access.Binding == w.binding && r.PublisherRef == w.binding.PublisherRef &&
		r.Candidate == w.closed && r.Previous == w.closed
}

func (w *CloseWitness) arm(cp auditlog.Checkpoint) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.bound && !w.failed && cp.Scope == w.binding.Scope &&
		cp.JournalID == w.binding.JournalID && cp.KeyRef == w.binding.KeyRef {
		w.closed, w.ready = cp, true
	}
}

func (w *CloseWitness) invalidate() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.failed, w.ready = true, false
	w.mu.Unlock()
}
