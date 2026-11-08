// 승인된 기존 공급자의 수명과 실제 검증 경계만 조립하며 권한을 발급하지 않는다.
package server

import (
	"context"
	"reflect"
	"sync"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/auditpublisher"
	"github.com/Autumn-27/artex/controlledhost"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/transmit"
)

func NewControlledReadAssemblyHost(ctx context.Context, cfg ControlledReadAssemblyHostConfig) (*ControlledReadAssemblyHost, error) {
	rootTransferred := false
	if !nilFinalizationValue(cfg.Sources.Root) {
		defer func() {
			if !rootTransferred {
				cfg.Sources.Root.Release()
			}
		}()
	}
	a := cfg.AuditSource
	if ctx == nil || ctx.Err() != nil || nilFinalizationValue(cfg.Sources.Root) ||
		nilFinalizationValue(cfg.Sources.Keys) || nilFinalizationValue(cfg.Sources.Checkpoints) ||
		nilFinalizationValue(cfg.Sources.Revisions) || nilFinalizationValue(cfg.Sources.DescriptorRoots) ||
		cfg.Publisher == nil || cfg.Inputs == nil || nilFinalizationValue(cfg.FinalizationScope) ||
		a.LatestCheckpoint == nil || a.ResolveKey == nil ||
		a.MaxJournalBytes <= 0 || a.MaxJournalBytes > maxControlledAuditSourceBytes ||
		a.MaxEvents == 0 || a.MaxEvents > maxControlledAuditSourceEvents ||
		a.Timeout <= 0 || a.Timeout > maxControlledAuditSourceTimeout {
		return nil, ErrControlledReadHostConfiguration
	}
	rootTransferred = true
	life, err := controlledhost.New(ctx, cfg.Sources)
	if err != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	resources := &controlledReadAssemblyResources{life: life, drained: make(chan struct{})}
	host := &ControlledReadAssemblyHost{Lifetime: life, resources: resources}
	root, path := life.RootAnchor(), life.RootPath()
	if root == nil || path == "" {
		_ = host.Close(context.Background())
		return nil, ErrControlledReadHostConfiguration
	}
	a.LatestCheckpoint = func(ctx context.Context, scope transmit.Scope) (ControlledReadAuditAnchor, error) {
		bound, finish, err := resources.enter(ctx)
		if err != nil {
			return ControlledReadAuditAnchor{}, ErrControlledReadAuditSource
		}
		defer finish()
		anchor, err := cfg.AuditSource.LatestCheckpoint(bound, scope)
		if err != nil || bound.Err() != nil {
			return ControlledReadAuditAnchor{}, ErrControlledReadAuditSource
		}
		return anchor, nil
	}
	a.ResolveKey = func(ctx context.Context, request ControlledReadAuditKeyRequest) ([]byte, error) {
		bound, finish, err := resources.enter(ctx)
		if err != nil {
			return nil, ErrControlledReadAuditSource
		}
		defer finish()
		key, err := cfg.AuditSource.ResolveKey(bound, request)
		if err != nil || bound.Err() != nil {
			return nil, ErrControlledReadAuditSource
		}
		return key, nil
	}
	scope := assemblyFinalizationProvider{resources: resources, actual: cfg.FinalizationScope}
	host.Options = ControlledReadRevisionHostOptions{
		Durable: ControlledReadDurableHostOptions{
			Host: ControlledReadHostOptions{
				Mode:        ControlledTaskModeHost{RootPath: path, RootAnchor: root, CheckRoot: life.CheckRoot, CheckTaskQuiescent: life.CheckTaskQuiescent},
				Findings:    NewControlledReadAssemblyResolver(cfg.Inputs, scope),
				AuditSource: &a, LocalLLMManifestPath: cfg.LocalLLMManifestPath, FixtureRegistries: cfg.FixtureRegistries,
			},
			Publisher: func(ctx context.Context, request ControlledReadPublisherRequest) (ControlledReadPublisherPlan, error) {
				bound, finish, err := resources.enter(ctx)
				if err != nil {
					return ControlledReadPublisherPlan{}, ErrControlledReadPublisher
				}
				transferred := false
				defer func() {
					if !transferred {
						finish()
					}
				}()
				var plan ControlledReadPublisherPlan
				root, err := life.PublisherRoot(bound, func(call context.Context) (auditpublisher.RootScope, error) {
					var factoryErr error
					plan, factoryErr = cfg.Publisher(call, request)
					if factoryErr != nil {
						return nil, factoryErr
					}
					if !sameAssemblyCheckpointHost(plan.Host, cfg.Sources.Checkpoints) {
						return plan.RootScope, ErrControlledReadPublisher
					}
					return plan.RootScope, nil
				})
				if err != nil {
					return ControlledReadPublisherPlan{}, ErrControlledReadPublisher
				}
				plan.Host = life.Checkpoints()
				plan.RootScope = &assemblyPublisherRoot{RootScope: root, finish: finish}
				transferred = true
				return plan, nil
			},
		},
		Scope: life.Revisions(), DescriptorRoot: life.DescriptorRoots(),
	}
	return host, nil
}

type assemblyPublisherRoot struct {
	auditpublisher.RootScope
	once   sync.Once
	finish func()
}

func (r *assemblyPublisherRoot) Release() {
	r.once.Do(func() { r.RootScope.Release(); r.finish() })
}

func sameAssemblyCheckpointHost(a, b auditcheckpoint.Host) bool {
	if nilFinalizationValue(a) || nilFinalizationValue(b) {
		return false
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	return ta == tb && ta.Comparable() && a == b
}

type controlledReadAssemblyResources struct {
	life    *controlledhost.Lifetime
	mu      sync.Mutex
	closed  bool
	active  int
	drained chan struct{}
}

func (r *controlledReadAssemblyResources) enter(ctx context.Context) (context.Context, func(), error) {
	if r == nil || ctx == nil || ctx.Err() != nil {
		return nil, nil, ErrControlledReadHostConfiguration
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, nil, ErrControlledReadHostConfiguration
	}
	r.active++
	r.mu.Unlock()
	var once sync.Once
	finish := func() {
		once.Do(func() {
			r.mu.Lock()
			r.active--
			if r.closed && r.active == 0 {
				close(r.drained)
			}
			r.mu.Unlock()
		})
	}
	bound, cancel, err := r.life.Bind(ctx)
	if err != nil {
		finish()
		return nil, nil, ErrControlledReadHostConfiguration
	}
	return bound, func() { cancel(); finish() }, nil
}

func (r *controlledReadAssemblyResources) revoke() {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		if r.active == 0 {
			close(r.drained)
		}
	}
	r.mu.Unlock()
	r.life.Revoke()
}

func (r *controlledReadAssemblyResources) close(ctx context.Context) error {
	if ctx == nil {
		return ErrControlledReadHostConfiguration
	}
	r.revoke()
	select {
	case <-r.drained:
	case <-ctx.Done():
		return ctx.Err()
	}
	return r.life.Close(ctx)
}

func (h *ControlledReadAssemblyHost) Revoke() {
	if h != nil && h.resources != nil {
		h.resources.revoke()
	}
}

func (h *ControlledReadAssemblyHost) Close(ctx context.Context) error {
	if h == nil || h.resources == nil {
		return ErrControlledReadHostConfiguration
	}
	return h.resources.close(ctx)
}

type assemblyFinalizationProvider struct {
	resources *controlledReadAssemblyResources
	actual    ControlledReadFinalizationScopeProvider
}

func (p assemblyFinalizationProvider) Acquire(ctx context.Context, request ControlledReadVerificationRequest, expected evidencebind.Context) (ControlledReadFinalizationScope, error) {
	bound, finish, err := p.resources.enter(ctx)
	if err != nil {
		return nil, ErrControlledFindingUnavailable
	}
	actual, err := p.actual.Acquire(bound, request, expected)
	if nilFinalizationValue(actual) {
		finish()
		return nil, ErrControlledFindingUnavailable
	}
	if err != nil {
		actual.Release()
		finish()
		return nil, ErrControlledFindingUnavailable
	}
	actualContext := actual.Context()
	if nilFinalizationValue(actualContext) || actualContext.Done() == nil || actualContext.Err() != nil || bound.Err() != nil {
		actual.Release()
		finish()
		return nil, ErrControlledFindingUnavailable
	}
	operation, cancel, err := p.resources.life.Bind(actualContext)
	if err != nil {
		actual.Release()
		finish()
		return nil, ErrControlledFindingUnavailable
	}
	stop := context.AfterFunc(bound, cancel)
	scope := &assemblyFinalizationScope{ctx: operation, bound: bound, actual: actual,
		release: func() { stop(); cancel(); actual.Release(); finish() }}
	if scope.Check(operation) != nil {
		scope.Release()
		return nil, ErrControlledFindingUnavailable
	}
	return scope, nil
}

type assemblyFinalizationScope struct {
	ctx, bound context.Context
	actual     ControlledReadFinalizationScope
	once       sync.Once
	release    func()
}

func (s *assemblyFinalizationScope) Context() context.Context { return s.ctx }
func (s *assemblyFinalizationScope) Check(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil || s.ctx.Err() != nil || s.bound.Err() != nil {
		return ErrControlledFindingUnavailable
	}
	if s.actual.Check(ctx) != nil || ctx.Err() != nil || s.ctx.Err() != nil || s.bound.Err() != nil {
		return ErrControlledFindingUnavailable
	}
	return nil
}
func (s *assemblyFinalizationScope) Release() { s.once.Do(s.release) }
