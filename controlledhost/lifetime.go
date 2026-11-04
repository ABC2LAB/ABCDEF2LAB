// Package controlledhost preserves already-authenticated host resource lifetimes.
// It does not authenticate peers, issue authority, provision keys/epochs or start
// a server. Missing operational providers remain unavailable.
package controlledhost

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/auditkeys"
	"github.com/Autumn-27/artex/evidencerevision"
)

var ErrUnavailable = errors.New("controlled_host_provider_unavailable")

// RootTrust is a supervisor's existing deployment/restore/exclusive-writer lease.
// CheckRoot and CheckTaskQuiescent must be quick checks of independently held
// evidence, not initiate or wait for child drain (callers may hold SQL locks).
// Anchor is long-held: reopening Directory cannot manufacture continuity.
// Context is stable for this lease's entire lifetime; cancellation is permanent.
// Release must not wait for SQL/children. New takes ownership at entry.
type RootTrust interface {
	Context() context.Context
	Anchor() *os.File
	Directory() string
	CheckRoot(context.Context) error
	CheckTaskQuiescent(context.Context, int64) error
	Release()
}

// Sources are actual authenticated providers supplied by C. A nil provider is
// disabled. These handles must themselves fence exact principal, namespace,
// instance, operation, key mapping and epoch until their lease is released.
type Sources struct {
	Root            RootTrust                               `json:"-"`
	Keys            auditkeys.Authority                     `json:"-"`
	Checkpoints     auditcheckpoint.Host                    `json:"-"`
	Revisions       evidencerevision.ScopeProvider          `json:"-"`
	DescriptorRoots evidencerevision.DescriptorRootProvider `json:"-"`
}

type Lifetime struct {
	sources              Sources
	ctx                  context.Context
	rootCtx              context.Context
	cancel               context.CancelFunc
	mu                   sync.Mutex
	closed               bool
	active               int
	drained              chan struct{}
	released             chan struct{}
	once                 sync.Once
	stopRoot, stopParent func() bool
}

func absent(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan:
		return r.IsNil()
	}
	return false
}

// New only consumes a pre-existing root trust lease. Filesystem metadata is not
// substituted for supervisor authority. Root is released once on any failure.
func New(ctx context.Context, sources Sources) (*Lifetime, error) {
	if absent(sources.Root) {
		return nil, ErrUnavailable
	}
	valid := false
	defer func() {
		if !valid {
			sources.Root.Release()
		}
	}()
	rootCtx := sources.Root.Context()
	if ctx == nil || ctx.Err() != nil || rootCtx == nil || rootCtx.Err() != nil ||
		sources.Root.Anchor() == nil || sources.Root.Directory() == "" || sources.Root.CheckRoot(ctx) != nil {
		return nil, ErrUnavailable
	}
	life, cancel := context.WithCancel(ctx)
	m := &Lifetime{sources: sources, ctx: life, rootCtx: rootCtx, cancel: cancel, drained: make(chan struct{}), released: make(chan struct{})}
	m.stopRoot = context.AfterFunc(rootCtx, m.Revoke)
	m.stopParent = context.AfterFunc(life, m.Revoke)
	if ctx.Err() != nil || rootCtx.Err() != nil {
		m.Revoke()
		m.stopRoot()
		m.stopParent()
		return nil, ErrUnavailable
	}
	valid = true
	return m, nil
}

// Revoke closes admission and requests cancellation without waiting or calling
// external providers under the gate mutex. It does not undo an uncertain commit.
func (m *Lifetime) Revoke() {
	if m == nil || m.cancel == nil {
		return
	}
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		if m.active == 0 {
			close(m.drained)
		}
	}
	m.mu.Unlock()
	m.cancel()
}

// Close must run outside SQL/Publisher/engine locks. Timeout retains ownership;
// it never force-releases a live root. Call again after operations/children drain.
func (m *Lifetime) Close(ctx context.Context) error {
	if m == nil || ctx == nil || m.ctx == nil || m.cancel == nil {
		return ErrUnavailable
	}
	m.Revoke()
	select {
	case <-m.drained:
	case <-ctx.Done():
		return ctx.Err()
	}
	m.once.Do(func() { m.stopRoot(); m.stopParent(); m.sources.Root.Release(); close(m.released) })
	select {
	case <-m.released:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Lifetime) live() bool {
	return m != nil && m.ctx != nil && m.rootCtx != nil && m.ctx.Err() == nil && m.rootCtx.Err() == nil
}

func linked(parent, authority context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(authority, cancel)
	if authority.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

func (m *Lifetime) enter(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil || ctx.Err() != nil || !m.live() {
		return nil, nil, ErrUnavailable
	}
	m.mu.Lock()
	if m.closed || !m.live() {
		m.mu.Unlock()
		return nil, nil, ErrUnavailable
	}
	m.active++
	m.mu.Unlock()
	call, unlink := linked(ctx, m.ctx)
	var once sync.Once
	finish := func() {
		once.Do(func() {
			unlink()
			m.mu.Lock()
			m.active--
			if m.closed && m.active == 0 {
				close(m.drained)
			}
			m.mu.Unlock()
		})
	}
	if call.Err() != nil || !m.live() {
		finish()
		return nil, nil, ErrUnavailable
	}
	return call, finish, nil
}

type boundContextKey struct{}

// Bind must be used for original auditkeys.Store operation contexts: its
// callback API has no context parameter. The marker checks assembly discipline,
// never authenticates a caller. C must not strip cancellation with WithoutCancel.
func (m *Lifetime) Bind(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if ctx == nil || ctx.Err() != nil || !m.live() {
		return nil, nil, ErrUnavailable
	}
	call, unlink := linked(ctx, m.ctx)
	return context.WithValue(call, boundContextKey{}, m), unlink, nil
}

// These borrowed values allow C to fill server.ControlledTaskModeHost without
// importing server here. C keeps Lifetime open through the server's whole life.
func (m *Lifetime) RootAnchor() *os.File {
	if !m.live() {
		return nil
	}
	return m.sources.Root.Anchor()
}
func (m *Lifetime) RootPath() string {
	if !m.live() {
		return ""
	}
	return m.sources.Root.Directory()
}
func (m *Lifetime) CheckRoot(ctx context.Context) error {
	call, finish, err := m.enter(ctx)
	if err != nil {
		return err
	}
	defer finish()
	if m.sources.Root.CheckRoot(call) != nil || call.Err() != nil || !m.live() {
		return ErrUnavailable
	}
	return nil
}
func (m *Lifetime) CheckTaskQuiescent(ctx context.Context, taskID int64) error {
	if taskID <= 0 {
		return ErrUnavailable
	}
	call, finish, err := m.enter(ctx)
	if err != nil {
		return err
	}
	defer finish()
	// The supervisor's tracked planner/worker/chat/child evidence decides this;
	// the resource gate's own active count is deliberately not consulted.
	if m.sources.Root.CheckTaskQuiescent(call, taskID) != nil || call.Err() != nil || !m.live() {
		return ErrUnavailable
	}
	return nil
}
