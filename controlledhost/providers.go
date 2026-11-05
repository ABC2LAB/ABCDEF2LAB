package controlledhost

import (
	"context"
	"os"
	"sync"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/auditkeys"
	"github.com/Autumn-27/artex/auditpublisher"
	"github.com/Autumn-27/artex/evidencerevision"
	"github.com/Autumn-27/artex/taskmode"
)

type held struct {
	ctx  context.Context
	once sync.Once
	drop func()
}

func (h *held) Context() context.Context { return h.ctx }
func (h *held) Release()                 { h.once.Do(h.drop) }
func hold(call context.Context, source context.Context, finish func(), release func()) (*held, error) {
	if source == nil || source.Err() != nil || call.Err() != nil {
		release()
		finish()
		return nil, ErrUnavailable
	}
	ctx, unlink := linked(call, source)
	return &held{ctx: ctx, drop: func() { unlink(); release(); finish() }}, nil
}

func (m *Lifetime) Checkpoints() auditcheckpoint.Host { return checkpointHost{m} }

type checkpointHost struct{ m *Lifetime }

func (p checkpointHost) Acquire(ctx context.Context, a auditcheckpoint.Access) (auditcheckpoint.Lease, error) {
	if p.m == nil || absent(p.m.sources.Checkpoints) {
		return nil, ErrUnavailable
	}
	call, finish, err := p.m.enter(ctx)
	if err != nil {
		return nil, err
	}
	l, err := p.m.sources.Checkpoints.Acquire(call, a)
	if err != nil || absent(l) {
		if !absent(l) {
			l.Release()
		}
		finish()
		return nil, ErrUnavailable
	}
	if l.Epoch() != a.Epoch || !p.m.live() {
		l.Release()
		finish()
		return nil, ErrUnavailable
	}
	h, err := hold(call, l.Context(), finish, l.Release)
	if err != nil {
		return nil, err
	}
	return &checkpointLease{held: h, source: l, binding: a.Binding, epoch: a.Epoch}, nil
}

type checkpointLease struct {
	*held
	source  auditcheckpoint.Lease
	binding auditcheckpoint.Binding
	epoch   auditcheckpoint.Epoch
}

func (l *checkpointLease) Epoch() auditcheckpoint.Epoch { return l.epoch }
func (l *checkpointLease) ResolveKey(ctx context.Context, b auditcheckpoint.Binding) ([]byte, error) {
	if ctx == nil || ctx.Err() != nil || l.ctx.Err() != nil || b != l.binding {
		return nil, ErrUnavailable
	}
	call, unlink := linked(ctx, l.ctx)
	defer unlink()
	key, err := l.source.ResolveKey(call, b)
	if err != nil || call.Err() != nil {
		return nil, ErrUnavailable
	}
	return key, nil
}

func (m *Lifetime) Revisions() evidencerevision.ScopeProvider { return revisionProvider{m} }

type revisionProvider struct{ m *Lifetime }

func (p revisionProvider) Acquire(ctx context.Context, a evidencerevision.Access) (evidencerevision.Lease, error) {
	if p.m == nil || absent(p.m.sources.Revisions) {
		return nil, ErrUnavailable
	}
	call, finish, err := p.m.enter(ctx)
	if err != nil {
		return nil, err
	}
	l, err := p.m.sources.Revisions.Acquire(call, a)
	if err != nil || absent(l) {
		if !absent(l) {
			l.Release()
		}
		finish()
		return nil, ErrUnavailable
	}
	if !p.m.live() {
		l.Release()
		finish()
		return nil, ErrUnavailable
	}
	h, err := hold(call, l.Context(), finish, l.Release)
	if err != nil {
		return nil, err
	}
	return &revisionLease{held: h, source: l}, nil
}

type revisionLease struct {
	*held
	source evidencerevision.Lease
}

func (l *revisionLease) MarkerSnapshot() taskmode.Marker { return l.source.MarkerSnapshot() }

func (m *Lifetime) DescriptorRoots() evidencerevision.DescriptorRootProvider {
	return descriptorProvider{m}
}

type descriptorProvider struct{ m *Lifetime }

func (p descriptorProvider) AcquireDescriptorRoot(ctx context.Context, a evidencerevision.DescriptorFileAccess) (evidencerevision.DescriptorRootScope, error) {
	if p.m == nil || absent(p.m.sources.DescriptorRoots) {
		return nil, ErrUnavailable
	}
	call, finish, err := p.m.enter(ctx)
	if err != nil {
		return nil, err
	}
	l, err := p.m.sources.DescriptorRoots.AcquireDescriptorRoot(call, a)
	if err != nil || absent(l) {
		if !absent(l) {
			l.Release()
		}
		finish()
		return nil, ErrUnavailable
	}
	return p.m.root(call, finish, l)
}

type rootScope interface {
	Context() context.Context
	Anchor() *os.File
	Directory() string
	Release()
}
type heldRoot struct {
	*held
	source rootScope
}

func (r *heldRoot) Anchor() *os.File  { return r.source.Anchor() }
func (r *heldRoot) Directory() string { return r.source.Directory() }
func (m *Lifetime) root(call context.Context, finish func(), l rootScope) (*heldRoot, error) {
	if !m.live() || l.Anchor() == nil || l.Directory() == "" {
		l.Release()
		finish()
		return nil, ErrUnavailable
	}
	h, err := hold(call, l.Context(), finish, l.Release)
	if err != nil {
		return nil, err
	}
	return &heldRoot{held: h, source: l}, nil
}

// PublisherRoot tracks acquisition as well as the transferred existing lease.
// acquire must authenticate the exact publisher request; this function never
// substitutes the manager's deployment root for per-execution writer authority.
func (m *Lifetime) PublisherRoot(ctx context.Context, acquire func(context.Context) (auditpublisher.RootScope, error)) (auditpublisher.RootScope, error) {
	if acquire == nil {
		return nil, ErrUnavailable
	}
	call, finish, err := m.enter(ctx)
	if err != nil {
		return nil, err
	}
	l, err := acquire(call)
	if err != nil || absent(l) {
		if !absent(l) {
			l.Release()
		}
		finish()
		return nil, ErrUnavailable
	}
	return m.root(call, finish, l)
}

func (m *Lifetime) Keys() auditkeys.Authority { return keyAuthority{m} }

type keyAuthority struct{ m *Lifetime }

func (p keyAuthority) WithTrust(ctx context.Context, req auditkeys.TrustRequest, consume func(auditkeys.Attestation) error) error {
	if p.m == nil || ctx == nil || ctx.Value(boundContextKey{}) != p.m || consume == nil || absent(p.m.sources.Keys) {
		return ErrUnavailable
	}
	call, finish, err := p.m.enter(ctx)
	if err != nil {
		return err
	}
	defer finish()
	var mu sync.Mutex
	called, returned, finished, violation := false, false, false, false
	var consumeErr error
	outerErr := p.m.sources.Keys.WithTrust(call, req, func(att auditkeys.Attestation) error {
		mu.Lock()
		if called || returned {
			violation = true
			mu.Unlock()
			return ErrUnavailable
		}
		called = true
		mu.Unlock()
		// A separate ticket keeps the root held even if a broken provider
		// returns while its callback is still running. Late callbacks are denied.
		work, release, e := p.m.enter(ctx)
		if e == nil {
			if att.Request != req || work.Err() != nil {
				e = ErrUnavailable
			} else {
				e = consume(att)
			}
			release()
		}
		mu.Lock()
		consumeErr = e
		finished = true
		mu.Unlock()
		return e
	})
	mu.Lock()
	returned = true
	defer mu.Unlock()
	if !called || !finished || violation || call.Err() != nil || !p.m.live() {
		return ErrUnavailable
	}
	if consumeErr != nil {
		return consumeErr
	}
	if outerErr != nil {
		return ErrUnavailable
	}
	return nil
}
