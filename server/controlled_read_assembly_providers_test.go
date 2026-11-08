package server

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/auditkeys"
	"github.com/Autumn-27/artex/controlledhost"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/evidencerevision"
	"github.com/Autumn-27/artex/transmit"
)

type assemblyProviderRoot struct {
	ctx      context.Context
	anchor   *os.File
	dir      string
	releases atomic.Int32
	deny     bool
}

func (r *assemblyProviderRoot) Context() context.Context { return r.ctx }
func (r *assemblyProviderRoot) Anchor() *os.File         { return r.anchor }
func (r *assemblyProviderRoot) Directory() string        { return r.dir }
func (r *assemblyProviderRoot) CheckRoot(ctx context.Context) error {
	if r.deny {
		return errors.New("synthetic private root error")
	}
	return ctx.Err()
}
func (r *assemblyProviderRoot) CheckTaskQuiescent(ctx context.Context, _ int64) error {
	return ctx.Err()
}
func (r *assemblyProviderRoot) Release() { r.releases.Add(1) }

type assemblyProviderKeys struct{}

func (*assemblyProviderKeys) WithTrust(ctx context.Context, request auditkeys.TrustRequest, consume func(auditkeys.Attestation) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return consume(auditkeys.Attestation{Request: request})
}

type assemblyProviderCheckpoint struct{ id int }

func (*assemblyProviderCheckpoint) Acquire(context.Context, auditcheckpoint.Access) (auditcheckpoint.Lease, error) {
	return nil, errors.New("synthetic checkpoint acquisition not requested")
}

type assemblyProviderRevision struct{}

func (*assemblyProviderRevision) Acquire(context.Context, evidencerevision.Access) (evidencerevision.Lease, error) {
	return nil, errors.New("synthetic revision acquisition not requested")
}

type assemblyProviderDescriptor struct{}

func (*assemblyProviderDescriptor) AcquireDescriptorRoot(context.Context, evidencerevision.DescriptorFileAccess) (evidencerevision.DescriptorRootScope, error) {
	return nil, errors.New("synthetic descriptor acquisition not requested")
}

type assemblyProviderScopeFactory func(context.Context, ControlledReadVerificationRequest, evidencebind.Context) (ControlledReadFinalizationScope, error)

func (f assemblyProviderScopeFactory) Acquire(ctx context.Context, r ControlledReadVerificationRequest, e evidencebind.Context) (ControlledReadFinalizationScope, error) {
	return f(ctx, r, e)
}

type assemblyProviderScope struct {
	ctx      context.Context
	releases atomic.Int32
	release  func()
	deny     bool
}

func (s *assemblyProviderScope) Context() context.Context { return s.ctx }
func (s *assemblyProviderScope) Check(ctx context.Context) error {
	if s.deny {
		return errors.New("synthetic private scope denial")
	}
	return ctx.Err()
}
func (s *assemblyProviderScope) Release() {
	s.releases.Add(1)
	if s.release != nil {
		s.release()
	}
}

type assemblyProviderPublisherRoot struct{ *assemblyProviderRoot }

func assemblyProvidersFixture(t *testing.T) (ControlledReadAssemblyHostConfig, *assemblyProviderRoot) {
	t.Helper()
	dir := t.TempDir()
	anchor, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = anchor.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	root := &assemblyProviderRoot{ctx: ctx, anchor: anchor, dir: dir}
	checkpoint := &assemblyProviderCheckpoint{id: 1}
	cfg := ControlledReadAssemblyHostConfig{
		Sources: controlledhost.Sources{Root: root, Keys: &assemblyProviderKeys{}, Checkpoints: checkpoint, Revisions: &assemblyProviderRevision{}, DescriptorRoots: &assemblyProviderDescriptor{}},
		Inputs: func(context.Context, ControlledReadVerificationRequest) (ControlledReadAssemblyInput, error) {
			return ControlledReadAssemblyInput{}, nil
		},
		FinalizationScope: assemblyProviderScopeFactory(func(ctx context.Context, _ ControlledReadVerificationRequest, _ evidencebind.Context) (ControlledReadFinalizationScope, error) {
			return &assemblyProviderScope{ctx: ctx}, nil
		}),
		AuditSource: ControlledReadAuditSourceOptions{MaxJournalBytes: 1024, MaxEvents: 4, Timeout: time.Second,
			LatestCheckpoint: func(context.Context, transmit.Scope) (ControlledReadAuditAnchor, error) {
				return ControlledReadAuditAnchor{}, nil
			},
			ResolveKey: func(context.Context, ControlledReadAuditKeyRequest) ([]byte, error) { return nil, nil }},
	}
	cfg.Publisher = func(ctx context.Context, _ ControlledReadPublisherRequest) (ControlledReadPublisherPlan, error) {
		return ControlledReadPublisherPlan{Host: checkpoint, RootScope: assemblyProviderPublisherRoot{&assemblyProviderRoot{ctx: ctx, anchor: anchor, dir: dir}}}, nil
	}
	return cfg, root
}

func TestAssemblyProvidersRejectMissingReleasesEnteredRoot(t *testing.T) {
	for _, name := range []string{"root", "keys", "checkpoint", "revision", "descriptor", "publisher", "inputs", "scope", "typed-root", "typed-scope", "audit-head", "audit-key", "bytes", "events", "timeout", "cancelled", "nil-context", "revoked-root"} {
		t.Run(name, func(t *testing.T) {
			cfg, root := assemblyProvidersFixture(t)
			ctx := context.Background()
			switch name {
			case "root":
				cfg.Sources.Root = nil
			case "keys":
				cfg.Sources.Keys = nil
			case "checkpoint":
				cfg.Sources.Checkpoints = nil
			case "revision":
				cfg.Sources.Revisions = nil
			case "descriptor":
				cfg.Sources.DescriptorRoots = nil
			case "publisher":
				cfg.Publisher = nil
			case "inputs":
				cfg.Inputs = nil
			case "scope":
				cfg.FinalizationScope = nil
			case "typed-root":
				cfg.Sources.Root = (*assemblyProviderRoot)(nil)
			case "typed-scope":
				cfg.FinalizationScope = assemblyProviderScopeFactory(nil)
			case "audit-head":
				cfg.AuditSource.LatestCheckpoint = nil
			case "audit-key":
				cfg.AuditSource.ResolveKey = nil
			case "bytes":
				cfg.AuditSource.MaxJournalBytes = maxControlledAuditSourceBytes + 1
			case "events":
				cfg.AuditSource.MaxEvents = maxControlledAuditSourceEvents + 1
			case "timeout":
				cfg.AuditSource.Timeout = maxControlledAuditSourceTimeout + 1
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil-context":
				ctx = nil
			case "revoked-root":
				rootContext, cancel := context.WithCancel(context.Background())
				cancel()
				root.ctx = rootContext
			}
			h, err := NewControlledReadAssemblyHost(ctx, cfg)
			wantReleases := int32(1)
			if name == "root" || name == "typed-root" {
				wantReleases = 0
			}
			if h != nil || !errors.Is(err, ErrControlledReadHostConfiguration) || root.releases.Load() != wantReleases {
				t.Fatal("failed host construction lost entered Root ownership")
			}
		})
	}
}

func TestAssemblyProvidersRootFailureAndSuccessfulClose(t *testing.T) {
	cfg, root := assemblyProvidersFixture(t)
	root.deny = true
	if h, err := NewControlledReadAssemblyHost(context.Background(), cfg); h != nil || !errors.Is(err, ErrControlledReadHostConfiguration) || root.releases.Load() != 1 {
		t.Fatal("failed lifetime lost Root ownership")
	}
	cfg, root = assemblyProvidersFixture(t)
	var sourceCalls atomic.Int32
	cfg.AuditSource.ResolveKey = func(context.Context, ControlledReadAuditKeyRequest) ([]byte, error) {
		sourceCalls.Add(1)
		return nil, nil
	}
	h, err := NewControlledReadAssemblyHost(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if h.Options.Durable.Host.Mode.RootAnchor != root.anchor || h.Options.Durable.Host.Mode.RootPath != root.dir || sourceCalls.Load() != 0 {
		t.Fatal("host changed Root or eagerly read Source")
	}
	if err = h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = h.Close(context.Background()); err != nil || root.releases.Load() != 1 {
		t.Fatal("Root released more than once")
	}
	if _, err = h.Options.Durable.Host.AuditSource.ResolveKey(context.Background(), ControlledReadAuditKeyRequest{}); err == nil || sourceCalls.Load() != 0 {
		t.Fatal("revoked audit callback ran")
	}
}

func TestAssemblyProvidersPublisherRequestAndHostBinding(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "matching", true: "foreign-host"}[mismatch], func(t *testing.T) {
			cfg, root := assemblyProvidersFixture(t)
			writer := &assemblyProviderRoot{ctx: root.ctx, anchor: root.anchor, dir: root.dir}
			want := ControlledReadPublisherRequest{Scope: transmit.Scope{TaskRef: "7", RunRef: "synthetic-run"}, IntentID: 8, KeyRef: "synthetic-key", Filename: "synthetic.jsonl"}
			cfg.Publisher = func(ctx context.Context, actual ControlledReadPublisherRequest) (ControlledReadPublisherPlan, error) {
				if actual != want {
					t.Fatal("publisher request changed")
				}
				host := cfg.Sources.Checkpoints
				if mismatch {
					host = &assemblyProviderCheckpoint{id: 2}
				}
				return ControlledReadPublisherPlan{Host: host, RootScope: assemblyProviderPublisherRoot{writer}, PublisherRef: "synthetic-publisher", Epoch: auditcheckpoint.Epoch{StoreRef: "synthetic-store", Generation: 3}}, nil
			}
			h, err := NewControlledReadAssemblyHost(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := h.Options.Durable.Publisher(context.Background(), want)
			if mismatch {
				if err == nil || writer.releases.Load() != 1 {
					t.Fatal("foreign publisher host accepted or leaked")
				}
			} else {
				if err != nil || plan.PublisherRef != "synthetic-publisher" || plan.Epoch.Generation != 3 {
					t.Fatalf("publisher metadata altered: %v", err)
				}
				plan.RootScope.Release()
				plan.RootScope.Release()
				if writer.releases.Load() != 1 {
					t.Fatal("writer not released exactly once")
				}
			}
			if err = h.Close(context.Background()); err != nil || root.releases.Load() != 1 {
				t.Fatal("host close lost Root")
			}
		})
	}
}

func TestAssemblyProvidersScopeHeldUntilActualRelease(t *testing.T) {
	cfg, root := assemblyProvidersFixture(t)
	var h *ControlledReadAssemblyHost
	var entered, released atomic.Bool
	actual := &assemblyProviderScope{ctx: root.ctx}
	actual.release = func() {
		gate := h.resources.(*controlledReadAssemblyResources)
		if !gate.mu.TryLock() {
			t.Error("external release under gate lock")
		} else {
			gate.mu.Unlock()
		}
		if root.releases.Load() != 0 {
			t.Error("Root released before authority")
		}
		released.Store(true)
	}
	want := ControlledReadVerificationRequest{TaskID: 7, IntentID: 8}
	expected := evidencebind.Context{IntentID: 8}
	cfg.FinalizationScope = assemblyProviderScopeFactory(func(ctx context.Context, r ControlledReadVerificationRequest, e evidencebind.Context) (ControlledReadFinalizationScope, error) {
		if r != want || e != expected {
			t.Error("scope request changed")
		}
		gate := h.resources.(*controlledReadAssemblyResources)
		if !gate.mu.TryLock() {
			t.Error("provider under gate lock")
		} else {
			gate.mu.Unlock()
		}
		if err := h.Lifetime.Keys().WithTrust(ctx, auditkeys.TrustRequest{}, func(auditkeys.Attestation) error { return nil }); err != nil {
			t.Error("key operation context was not bound")
		}
		entered.Store(true)
		return actual, nil
	})
	var err error
	h, err = NewControlledReadAssemblyHost(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	provider := assemblyFinalizationProvider{resources: h.resources.(*controlledReadAssemblyResources), actual: cfg.FinalizationScope}
	lease, err := provider.Acquire(context.Background(), want, expected)
	if err != nil || !entered.Load() {
		t.Fatalf("scope acquisition failed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if err = h.Close(ctx); !errors.Is(err, context.DeadlineExceeded) || root.releases.Load() != 0 || released.Load() {
		t.Fatal("Close freed an outstanding authority")
	}
	if lease.Context().Err() == nil {
		t.Fatal("Revoke did not cancel held scope")
	}
	lease.Release()
	lease.Release()
	if actual.releases.Load() != 1 {
		t.Fatal("scope released twice")
	}
	if err = h.Close(context.Background()); err != nil || root.releases.Load() != 1 {
		t.Fatal("drained scope did not release Root")
	}
}

func TestAssemblyProvidersPendingAcquireAndPartialFailure(t *testing.T) {
	for _, mode := range []string{"pending", "partial-error", "nil-scope", "uncancellable", "scope-check"} {
		t.Run(mode, func(t *testing.T) {
			cfg, root := assemblyProvidersFixture(t)
			entered, unblock := make(chan struct{}), make(chan struct{})
			actual := &assemblyProviderScope{ctx: root.ctx, deny: mode == "scope-check"}
			if mode == "uncancellable" {
				actual.ctx = context.Background()
			}
			cfg.FinalizationScope = assemblyProviderScopeFactory(func(context.Context, ControlledReadVerificationRequest, evidencebind.Context) (ControlledReadFinalizationScope, error) {
				if mode == "pending" {
					close(entered)
					<-unblock
				}
				if mode == "partial-error" {
					return actual, errors.New("synthetic private acquisition error")
				}
				if mode == "nil-scope" {
					return (*assemblyProviderScope)(nil), nil
				}
				return actual, nil
			})
			h, err := NewControlledReadAssemblyHost(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			provider := assemblyFinalizationProvider{resources: h.resources.(*controlledReadAssemblyResources), actual: cfg.FinalizationScope}
			done := make(chan error, 1)
			go func() {
				lease, err := provider.Acquire(context.Background(), ControlledReadVerificationRequest{}, evidencebind.Context{})
				if lease != nil {
					lease.Release()
				}
				done <- err
			}()
			if mode == "pending" {
				<-entered
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
				if err = h.Close(ctx); !errors.Is(err, context.DeadlineExceeded) || root.releases.Load() != 0 {
					t.Error("pending acquisition lost Root")
				}
				cancel()
				close(unblock)
			}
			select {
			case err = <-done:
			case <-time.After(time.Second):
				t.Fatal("acquisition stuck")
			}
			if err == nil {
				t.Fatal("invalid scope acquired")
			}
			want := int32(1)
			if mode == "nil-scope" {
				want = 0
			}
			if actual.releases.Load() != want {
				t.Fatal("partial scope cleanup incorrect")
			}
			if err = h.Close(context.Background()); err != nil || root.releases.Load() != 1 {
				t.Fatal("failed acquisition leaked Root")
			}
		})
	}
}

func TestAssemblyProvidersAuditCallbackAndIndependentCallerCancellation(t *testing.T) {
	for _, mode := range []string{"blocked-audit", "independent-caller"} {
		t.Run(mode, func(t *testing.T) {
			cfg, root := assemblyProvidersFixture(t)
			entered, unblock := make(chan struct{}), make(chan struct{})
			var h *ControlledReadAssemblyHost
			cfg.AuditSource.ResolveKey = func(ctx context.Context, _ ControlledReadAuditKeyRequest) ([]byte, error) {
				if err := h.Lifetime.Keys().WithTrust(ctx, auditkeys.TrustRequest{}, func(auditkeys.Attestation) error { return nil }); err != nil {
					t.Error("audit key context unbound")
				}
				close(entered)
				<-unblock
				return []byte("synthetic borrowed key"), nil
			}
			var err error
			h, err = NewControlledReadAssemblyHost(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "blocked-audit" {
				done := make(chan error, 1)
				go func() {
					key, err := h.Options.Durable.Host.AuditSource.ResolveKey(context.Background(), ControlledReadAuditKeyRequest{})
					if len(key) != 0 {
						t.Error("revoked callback exposed key")
					}
					done <- err
				}()
				<-entered
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
				if err = h.Close(ctx); !errors.Is(err, context.DeadlineExceeded) || root.releases.Load() != 0 {
					t.Error("pending audit read lost Root")
				}
				cancel()
				close(unblock)
				if err = <-done; !errors.Is(err, ErrControlledReadAuditSource) {
					t.Fatal("revoked audit succeeded")
				}
			} else {
				ctx, cancel := context.WithCancel(context.Background())
				provider := assemblyFinalizationProvider{resources: h.resources.(*controlledReadAssemblyResources), actual: assemblyProviderScopeFactory(func(context.Context, ControlledReadVerificationRequest, evidencebind.Context) (ControlledReadFinalizationScope, error) {
					return &assemblyProviderScope{ctx: root.ctx}, nil
				})}
				lease, err := provider.Acquire(ctx, ControlledReadVerificationRequest{}, evidencebind.Context{})
				if err != nil {
					t.Fatal(err)
				}
				cancel()
				select {
				case <-lease.Context().Done():
				case <-time.After(time.Second):
					t.Fatal("caller cancellation not propagated")
				}
				if lease.Check(context.Background()) == nil || root.releases.Load() != 0 {
					t.Error("cancelled scope remained usable or freed Root")
				}
				lease.Release()
			}
			if err = h.Close(context.Background()); err != nil || root.releases.Load() != 1 {
				t.Fatal("cancelled work leaked Root")
			}
		})
	}
}

func TestAssemblyProvidersPublisherScopeOutlivesFactory(t *testing.T) {
	cfg, root := assemblyProvidersFixture(t)
	var writer *assemblyProviderRoot
	cfg.Publisher = func(ctx context.Context, _ ControlledReadPublisherRequest) (ControlledReadPublisherPlan, error) {
		writer = &assemblyProviderRoot{ctx: ctx, anchor: root.anchor, dir: root.dir}
		return ControlledReadPublisherPlan{Host: cfg.Sources.Checkpoints, RootScope: assemblyProviderPublisherRoot{writer}}, nil
	}
	h, err := NewControlledReadAssemblyHost(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := h.Options.Durable.Publisher(context.Background(), ControlledReadPublisherRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.RootScope.Context().Err() != nil || writer.ctx.Err() != nil {
		t.Fatal("factory return cancelled the publisher's owned root context")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	if err = h.Close(ctx); !errors.Is(err, context.DeadlineExceeded) || root.releases.Load() != 0 || writer.releases.Load() != 0 {
		t.Error("Close freed a publisher root before its owner released it")
	}
	cancel()
	plan.RootScope.Release()
	plan.RootScope.Release()
	if writer.releases.Load() != 1 {
		t.Error("publisher root released more than once")
	}
	if err = h.Close(context.Background()); err != nil || root.releases.Load() != 1 {
		t.Fatal("released publisher root did not unblock host cleanup")
	}
}

type assemblyCancelOnRootContext struct {
	*assemblyProviderRoot
	cancel context.CancelFunc
}

func (r *assemblyCancelOnRootContext) Context() context.Context {
	r.cancel()
	return r.assemblyProviderRoot.Context()
}

func TestAssemblyProvidersCancellationDuringRootTransfer(t *testing.T) {
	cfg, root := assemblyProvidersFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg.Sources.Root = &assemblyCancelOnRootContext{assemblyProviderRoot: root, cancel: cancel}
	host, err := NewControlledReadAssemblyHost(ctx, cfg)
	if host != nil || !errors.Is(err, ErrControlledReadHostConfiguration) || root.releases.Load() != 1 {
		t.Fatal("cancellation during transfer did not release Root exactly once")
	}
}
