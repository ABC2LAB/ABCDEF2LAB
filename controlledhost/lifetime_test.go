package controlledhost

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/auditkeys"
	"github.com/Autumn-27/artex/auditpublisher"
	"github.com/Autumn-27/artex/evidencerevision"
	"github.com/Autumn-27/artex/taskmode"
	"github.com/Autumn-27/artex/transmit"
)

// These are synthetic authorities. OS checks below exercise only FD identity
// and ownership mechanics, never deployment/restore/secret or child authority.
type testRoot struct {
	ctx      context.Context
	anchor   *os.File
	dir      string
	releases atomic.Int32
	quiet    atomic.Bool
	task     atomic.Int64
}

func (r *testRoot) Context() context.Context { return r.ctx }
func (r *testRoot) Anchor() *os.File         { return r.anchor }
func (r *testRoot) Directory() string        { return r.dir }
func (r *testRoot) CheckRoot(ctx context.Context) error {
	if ctx.Err() != nil || r.ctx.Err() != nil {
		return ErrUnavailable
	}
	a, e := r.anchor.Stat()
	b, f := os.Lstat(r.dir)
	if e != nil || f != nil || !os.SameFile(a, b) || b.Mode() != os.ModeDir|0700 {
		return ErrUnavailable
	}
	return nil
}
func (r *testRoot) CheckTaskQuiescent(ctx context.Context, id int64) error {
	r.task.Store(id)
	if ctx.Err() != nil || !r.quiet.Load() {
		return ErrUnavailable
	}
	return nil
}
func (r *testRoot) Release() { r.releases.Add(1); _ = r.anchor.Close() }
func rootFixture(t *testing.T, ctx context.Context) *testRoot {
	t.Helper()
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	fd, e := os.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	r := &testRoot{ctx: ctx, anchor: fd, dir: dir}
	t.Cleanup(func() {
		if r.releases.Load() == 0 {
			r.Release()
		}
	})
	return r
}
func managerFixture(t *testing.T, r *testRoot, s Sources) *Lifetime {
	t.Helper()
	s.Root = r
	m, e := New(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if e := m.Close(ctx); e != nil {
			t.Error(e)
		}
	})
	return m
}

type checkpointSource struct {
	fn func(context.Context, auditcheckpoint.Access) (auditcheckpoint.Lease, error)
}

func (s checkpointSource) Acquire(c context.Context, a auditcheckpoint.Access) (auditcheckpoint.Lease, error) {
	return s.fn(c, a)
}

type checkpointFixture struct {
	ctx      context.Context
	epoch    auditcheckpoint.Epoch
	releases atomic.Int32
	resolves atomic.Int32
}

func (l *checkpointFixture) Context() context.Context     { return l.ctx }
func (l *checkpointFixture) Epoch() auditcheckpoint.Epoch { return l.epoch }
func (l *checkpointFixture) ResolveKey(context.Context, auditcheckpoint.Binding) ([]byte, error) {
	l.resolves.Add(1)
	return []byte("synthetic-private-key-not-operational"), nil
}
func (l *checkpointFixture) Release() { l.releases.Add(1) }
func accessFixture() auditcheckpoint.Access {
	return auditcheckpoint.Access{Purpose: auditcheckpoint.PublishAccess, Epoch: auditcheckpoint.Epoch{StoreRef: "store:test", EpochRef: "epoch:test", Generation: 1, AnchorDigest: [32]byte{1}}, Binding: auditcheckpoint.Binding{PublisherRef: "publisher:test", Scope: transmit.Scope{RunRef: "run:test"}, JournalID: "journal:test", KeyRef: "key:test"}, Publication: auditcheckpoint.Request{Kind: auditcheckpoint.Register}}
}

type singleContextRoot struct {
	*testRoot
	calls atomic.Int32
}

func (r *singleContextRoot) Context() context.Context {
	if r.calls.Add(1) != 1 {
		panic("external root context queried again")
	}
	return r.testRoot.ctx
}
func TestRootContextCapturedOnceOutsideGate(t *testing.T) {
	r := &singleContextRoot{testRoot: rootFixture(t, context.Background())}
	m, e := New(context.Background(), Sources{Root: r})
	if e != nil {
		t.Fatal(e)
	}
	if e = m.CheckRoot(context.Background()); e != nil {
		t.Fatal(e)
	}
	ctx, stop, e := m.Bind(context.Background())
	if e != nil || ctx.Err() != nil {
		t.Fatal(e)
	}
	stop()
	if e = m.Close(context.Background()); e != nil || r.calls.Load() != 1 {
		t.Fatal("context not captured once")
	}
}

func TestCloseCompletedDrainWinsCancellation(t *testing.T) {
	// Both channels are already ready. Repetition exercises the old select's
	// random choice while each call asserts the observable ownership result.
	for i := 0; i < 64; i++ {
		r := rootFixture(t, context.Background())
		m := managerFixture(t, r, Sources{})
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if err := m.Close(cancelled); err != nil || r.releases.Load() != 1 {
			t.Fatal("completed drain reported cancellation or retained root")
		}
		if err := m.Close(cancelled); err != nil || r.releases.Load() != 1 {
			t.Fatal("completed release was not idempotent under cancellation")
		}
	}
}

func TestCloseCancellationRetainsRootUntilLeaseRelease(t *testing.T) {
	a := accessFixture()
	l := &checkpointFixture{ctx: context.Background(), epoch: a.Epoch}
	r := rootFixture(t, context.Background())
	m := managerFixture(t, r, Sources{Checkpoints: checkpointSource{func(context.Context, auditcheckpoint.Access) (auditcheckpoint.Lease, error) { return l, nil }}})
	lease, err := m.Checkpoints().Acquire(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lease.Release)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = m.Close(cancelled); !errors.Is(err, context.Canceled) || r.releases.Load() != 0 || l.releases.Load() != 0 {
		t.Fatal("cancelled wait released a live lease/root")
	}
	lease.Release()
	if err = m.Close(cancelled); err != nil || r.releases.Load() != 1 || l.releases.Load() != 1 {
		t.Fatal("completed lease did not allow exactly one root release")
	}
}

func TestUnconfiguredAndExpiredSourcesDeny(t *testing.T) {
	for _, zero := range []*Lifetime{nil, {}} {
		zero.Revoke()
		if zero.Close(context.Background()) != ErrUnavailable || zero.CheckRoot(context.Background()) != ErrUnavailable || zero.RootAnchor() != nil || zero.RootPath() != "" {
			t.Fatal("zero lifetime did not deny")
		}
	}
	if _, e := New(context.Background(), Sources{}); e != ErrUnavailable {
		t.Fatal(e)
	}
	var typed *testRoot
	if _, e := New(context.Background(), Sources{Root: typed}); e != ErrUnavailable {
		t.Fatal(e)
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	r := rootFixture(t, expired)
	if _, e := New(context.Background(), Sources{Root: r}); e != ErrUnavailable || r.releases.Load() != 1 {
		t.Fatal("expired root admitted or leaked")
	}
	r = rootFixture(t, context.Background())
	m := managerFixture(t, r, Sources{})
	if _, e := m.Checkpoints().Acquire(context.Background(), accessFixture()); e != ErrUnavailable {
		t.Fatal(e)
	}
	if _, e := m.Revisions().Acquire(context.Background(), evidencerevision.Access{}); e != ErrUnavailable {
		t.Fatal(e)
	}
	if _, e := m.DescriptorRoots().AcquireDescriptorRoot(context.Background(), evidencerevision.DescriptorFileAccess{}); e != ErrUnavailable {
		t.Fatal(e)
	}
	if _, e := m.PublisherRoot(context.Background(), nil); e != ErrUnavailable {
		t.Fatal(e)
	}
	ctx, stop, e := m.Bind(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	if e := m.Keys().WithTrust(ctx, auditkeys.TrustRequest{}, func(auditkeys.Attestation) error { t.Fatal("missing source consumed"); return nil }); e != ErrUnavailable {
		t.Fatal(e)
	}
}

func TestCheckpointExactForwardingCancellationAndRelease(t *testing.T) {
	a := accessFixture()
	sourceCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &checkpointFixture{ctx: sourceCtx, epoch: a.Epoch}
	r := rootFixture(t, context.Background())
	m := managerFixture(t, r, Sources{Checkpoints: checkpointSource{func(ctx context.Context, got auditcheckpoint.Access) (auditcheckpoint.Lease, error) {
		if got != a {
			t.Error("access was changed")
		}
		return l, nil
	}}})
	lease, e := m.Checkpoints().Acquire(context.Background(), a)
	if e != nil {
		t.Fatal(e)
	}
	if lease.Epoch() != a.Epoch {
		t.Fatal("epoch changed")
	}
	wrong := a.Binding
	wrong.KeyRef = "key:other"
	if _, e := lease.ResolveKey(context.Background(), wrong); e != ErrUnavailable || l.resolves.Load() != 0 {
		t.Fatal("key binding bypass")
	}
	if _, e := lease.ResolveKey(context.Background(), a.Binding); e != nil || l.resolves.Load() != 1 {
		t.Fatal(e)
	}
	bound, stop, e := m.Bind(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	m.Revoke()
	select {
	case <-bound.Done():
	case <-time.After(time.Second):
		t.Fatal("bound work not canceled")
	}
	select {
	case <-lease.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("lease not canceled")
	}
	closedCtx, closedCancel := context.WithCancel(context.Background())
	closedCancel()
	if e := m.Close(closedCtx); e == nil || r.releases.Load() != 0 {
		t.Fatal("live root force-released")
	}
	if _, e := lease.ResolveKey(context.Background(), a.Binding); e != ErrUnavailable {
		t.Fatal("revoked key use")
	}
	lease.Release()
	lease.Release()
	if l.releases.Load() != 1 {
		t.Fatal("source double release")
	}
	if e := m.Close(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := m.Close(context.Background()); e != nil || r.releases.Load() != 1 {
		t.Fatal("root double release")
	}
}

func TestRevocationWhileAcquiringKeepsRootUntilProviderReturns(t *testing.T) {
	a := accessFixture()
	entered, proceed := make(chan struct{}), make(chan struct{})
	l := &checkpointFixture{ctx: context.Background(), epoch: a.Epoch}
	r := rootFixture(t, context.Background())
	m := managerFixture(t, r, Sources{Checkpoints: checkpointSource{func(context.Context, auditcheckpoint.Access) (auditcheckpoint.Lease, error) {
		close(entered)
		<-proceed
		return l, nil
	}}})
	done := make(chan error, 1)
	go func() { _, e := m.Checkpoints().Acquire(context.Background(), a); done <- e }()
	<-entered
	m.Revoke()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := m.Close(ctx); e == nil || r.releases.Load() != 0 {
		t.Fatal("pending provider lost root")
	}
	close(proceed)
	if e := <-done; e != ErrUnavailable || l.releases.Load() != 1 {
		t.Fatal("late lease accepted/leaked")
	}
	if e := m.Close(context.Background()); e != nil {
		t.Fatal(e)
	}
}

func TestRootLossAndSupervisorEvidence(t *testing.T) {
	rctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := rootFixture(t, rctx)
	m := managerFixture(t, r, Sources{})
	if m.RootAnchor() != r.anchor || m.RootPath() != r.dir {
		t.Fatal("root reopened/replaced")
	}
	if m.CheckTaskQuiescent(context.Background(), 7) != ErrUnavailable {
		t.Fatal("resource count became child proof")
	}
	r.quiet.Store(true)
	if m.CheckTaskQuiescent(context.Background(), 7) != nil || r.task.Load() != 7 {
		t.Fatal("supervisor task mismatch")
	}
	if m.CheckTaskQuiescent(context.Background(), 0) != ErrUnavailable {
		t.Fatal("invalid task")
	}
	old := r.dir + "-old"
	if e := os.Rename(r.dir, old); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = os.RemoveAll(old) })
	if e := os.Mkdir(r.dir, 0700); e != nil {
		t.Fatal(e)
	}
	if m.CheckRoot(context.Background()) != ErrUnavailable {
		t.Fatal("new path manufactured continuity")
	}
	cancel()
	if m.CheckRoot(context.Background()) != ErrUnavailable || m.RootAnchor() != nil {
		t.Fatal("lost root still admitted")
	}
}

type revisionSource struct {
	access evidencerevision.Access
	lease  *revisionFixture
}

func (s *revisionSource) Acquire(_ context.Context, a evidencerevision.Access) (evidencerevision.Lease, error) {
	s.access = a
	return s.lease, nil
}

type revisionFixture struct {
	ctx      context.Context
	marker   taskmode.Marker
	releases atomic.Int32
}

func (l *revisionFixture) Context() context.Context        { return l.ctx }
func (l *revisionFixture) MarkerSnapshot() taskmode.Marker { return l.marker }
func (l *revisionFixture) Release()                        { l.releases.Add(1) }

type descriptorSource struct {
	access evidencerevision.DescriptorFileAccess
	root   *testRoot
}

func (s *descriptorSource) AcquireDescriptorRoot(_ context.Context, a evidencerevision.DescriptorFileAccess) (evidencerevision.DescriptorRootScope, error) {
	s.access = a
	return s.root, nil
}

func TestRevisionDescriptorAndPublisherOwnership(t *testing.T) {
	r := rootFixture(t, context.Background())
	foreign := rootFixture(t, context.Background())
	rv := &revisionSource{lease: &revisionFixture{ctx: context.Background()}}
	ds := &descriptorSource{root: foreign}
	m := managerFixture(t, r, Sources{Revisions: rv, DescriptorRoots: ds})
	a := evidencerevision.Access{Purpose: evidencerevision.ReadReceipt, TaskID: 3, IntentID: 4, OperationRef: "operation:test"}
	l, e := m.Revisions().Acquire(context.Background(), a)
	if e != nil || rv.access != a || l.MarkerSnapshot() != rv.lease.marker {
		t.Fatal("revision access changed")
	}
	l.Release()
	l.Release()
	if rv.lease.releases.Load() != 1 {
		t.Fatal("revision release")
	}
	op := evidencerevision.DescriptorFileAccess{Purpose: evidencerevision.ReadDescriptorFile, OperationRef: "operation:test"}
	d, e := m.DescriptorRoots().AcquireDescriptorRoot(context.Background(), op)
	if e != nil || ds.access != op || d.Anchor() != foreign.anchor || d.Directory() != foreign.dir {
		t.Fatal("descriptor rebound")
	}
	d.Release()
	d.Release()
	if foreign.releases.Load() != 1 {
		t.Fatal("descriptor release")
	}
	pub := rootFixture(t, context.Background())
	p, e := m.PublisherRoot(context.Background(), func(context.Context) (auditpublisher.RootScope, error) { return pub, nil })
	if e != nil {
		t.Fatal(e)
	}
	p.Release()
	p.Release()
	if pub.releases.Load() != 1 {
		t.Fatal("publisher release")
	}
}

type keySource func(context.Context, auditkeys.TrustRequest, func(auditkeys.Attestation) error) error

func (s keySource) WithTrust(c context.Context, r auditkeys.TrustRequest, f func(auditkeys.Attestation) error) error {
	return s(c, r, f)
}

func TestKeyAuthorityBoundContextExactRequestAndDuplicates(t *testing.T) {
	req := auditkeys.TrustRequest{Namespace: "namespace:test", PolicyRef: "policy:test", Inspect: true, CurrentState: auditkeys.Retired}
	r := rootFixture(t, context.Background())
	var calls atomic.Int32
	backend := keySource(func(_ context.Context, got auditkeys.TrustRequest, f func(auditkeys.Attestation) error) error {
		if got != req {
			t.Error("request changed")
		}
		_ = f(auditkeys.Attestation{Request: got})
		_ = f(auditkeys.Attestation{Request: got})
		return nil
	})
	m := managerFixture(t, r, Sources{Keys: backend})
	consume := func(auditkeys.Attestation) error { calls.Add(1); return nil }
	if m.Keys().WithTrust(context.Background(), req, consume) != ErrUnavailable || calls.Load() != 0 {
		t.Fatal("unbound context admitted")
	}
	ctx, stop, e := m.Bind(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	if m.Keys().WithTrust(ctx, req, consume) != ErrUnavailable || calls.Load() != 1 {
		t.Fatal("duplicate callback accepted")
	}
}

func TestKeyCallbackErrorExpiryAndLateCallback(t *testing.T) {
	for _, kind := range []string{"normal", "error", "provider_error", "expired", "late"} {
		t.Run(kind, func(t *testing.T) {
			r := rootFixture(t, context.Background())
			req := auditkeys.TrustRequest{Inspect: true}
			var saved func(auditkeys.Attestation) error
			wanted := errors.New("store rejected synthetic request")
			m := managerFixture(t, r, Sources{Keys: keySource(func(ctx context.Context, got auditkeys.TrustRequest, f func(auditkeys.Attestation) error) error {
				if kind == "late" {
					saved = f
					return nil
				}
				result := f(auditkeys.Attestation{Request: got})
				if kind == "provider_error" {
					return wanted
				}
				return result
			})})
			ctx, stop, e := m.Bind(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			defer stop()
			if kind == "expired" {
				stop()
			}
			calls := 0
			e = m.Keys().WithTrust(ctx, req, func(auditkeys.Attestation) error {
				calls++
				if kind == "error" {
					return wanted
				}
				return nil
			})
			switch kind {
			case "normal":
				if e != nil || calls != 1 {
					t.Fatal(e)
				}
			case "error":
				if !errors.Is(e, wanted) || calls != 1 {
					t.Fatal(e)
				}
			case "provider_error":
				if e != ErrUnavailable || calls != 1 {
					t.Fatal("provider error after successful callback was accepted")
				}
			case "expired":
				if e != ErrUnavailable || calls != 0 {
					t.Fatal("expired callback")
				}
			case "late":
				if e != ErrUnavailable || calls != 0 || saved(auditkeys.Attestation{Request: req}) != ErrUnavailable {
					t.Fatal("late callback")
				}
			}
		})
	}
}

func TestEarlyReturningKeyProviderStillHoldsRunningCallback(t *testing.T) {
	started, proceed, callbackDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	r := rootFixture(t, context.Background())
	req := auditkeys.TrustRequest{Inspect: true}
	m := managerFixture(t, r, Sources{Keys: keySource(func(_ context.Context, _ auditkeys.TrustRequest, f func(auditkeys.Attestation) error) error {
		go func() { defer close(callbackDone); _ = f(auditkeys.Attestation{Request: req}) }()
		<-started
		return nil
	})})
	var unblockOnce sync.Once
	unblock := func() { unblockOnce.Do(func() { close(proceed) }) }
	t.Cleanup(func() {
		unblock()
		select {
		case <-started:
			<-callbackDone
		default:
		}
	})
	ctx, stop, e := m.Bind(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	if e = m.Keys().WithTrust(ctx, req, func(auditkeys.Attestation) error { close(started); <-proceed; return ctx.Err() }); e != ErrUnavailable {
		t.Fatal("asynchronous provider accepted")
	}
	m.Revoke()
	select {
	case <-m.drained:
		t.Fatal("live callback was counted as drained")
	default:
	}
	deadline, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.Close(deadline); !errors.Is(err, context.DeadlineExceeded) || r.releases.Load() != 0 {
		t.Fatal("callback lost root or deadline result")
	}
	unblock()
	<-callbackDone
	if e = m.Close(context.Background()); e != nil || r.releases.Load() != 1 {
		t.Fatal("callback completion did not release root exactly once")
	}
}

func TestConcurrentReleaseAndClose(t *testing.T) {
	a := accessFixture()
	l := &checkpointFixture{ctx: context.Background(), epoch: a.Epoch}
	r := rootFixture(t, context.Background())
	m := managerFixture(t, r, Sources{Checkpoints: checkpointSource{func(context.Context, auditcheckpoint.Access) (auditcheckpoint.Lease, error) { return l, nil }}})
	lease, e := m.Checkpoints().Acquire(context.Background(), a)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease.Release()
			if e := m.Close(context.Background()); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if l.releases.Load() != 1 || r.releases.Load() != 1 {
		t.Fatal("concurrent duplicate release")
	}
}

func TestNilAndMismatchedAcquisitionsRelease(t *testing.T) {
	for _, kind := range []string{"typed-nil", "epoch", "nil-context", "canceled-context", "partial-error"} {
		t.Run(kind, func(t *testing.T) {
			a := accessFixture()
			l := &checkpointFixture{ctx: context.Background(), epoch: a.Epoch}
			if kind == "epoch" {
				l.epoch.Generation++
			}
			if kind == "nil-context" {
				l.ctx = nil
			}
			if kind == "canceled-context" {
				c, cancel := context.WithCancel(context.Background())
				cancel()
				l.ctx = c
			}
			r := rootFixture(t, context.Background())
			m := managerFixture(t, r, Sources{Checkpoints: checkpointSource{func(context.Context, auditcheckpoint.Access) (auditcheckpoint.Lease, error) {
				if kind == "typed-nil" {
					var nilLease *checkpointFixture
					return nilLease, nil
				}
				if kind == "partial-error" {
					return l, errors.New("private backend detail")
				}
				return l, nil
			}}})
			got, e := m.Checkpoints().Acquire(context.Background(), a)
			if e != ErrUnavailable || got != nil {
				t.Fatal("invalid acquisition accepted")
			}
			want := int32(1)
			if kind == "typed-nil" {
				want = 0
			}
			if l.releases.Load() != want {
				t.Fatal("invalid lease leaked")
			}
		})
	}
}
