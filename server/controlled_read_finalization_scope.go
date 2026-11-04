package server

import (
	"context"
	"reflect"
	"sync"

	"github.com/Autumn-27/artex/evidencebind"
)

// ControlledReadFinalizationScopeProvider acquires an existing host authority;
// it must authenticate the exact request and expected evidence scope. It must
// honor cancellation and hold the authority and all subordinate resources until
// Release, including while a database operation is returning an uncertain result.
// Acquisition grants no new key, epoch, approval, or root continuity.
type ControlledReadFinalizationScopeProvider interface {
	Acquire(context.Context, ControlledReadVerificationRequest, evidencebind.Context) (ControlledReadFinalizationScope, error)
}

// ControlledReadFinalizationScope is an owned lease, not an authority assertion
// derived from matching strings. Context must be cancellable and be cancelled on
// revocation/expiry. Check validates current authority without releasing it.
// Source and its key/checkpoint/revision providers must use this same lease's
// authority. A context alone cannot establish that relationship. Release must
// release only this acquisition, safely after all its synchronous calls return.
// Providers must not require a collector/Publisher/SQL lock to release or drain.
type ControlledReadFinalizationScope interface {
	Context() context.Context
	Check(context.Context) error
	Release()
}

// RequireControlledReadFinalizationScope wraps only a resolver; it does not
// configure a Server. C must apply it to the strict constructor's Findings
// option before admission, preserving that constructor's revision services.
// Each returned plan still needs an actual host-supplied scope provider.
func RequireControlledReadFinalizationScope(resolve ControlledReadFindingResolver) ControlledReadFindingResolver {
	if resolve == nil {
		return nil
	}
	return func(ctx context.Context, request ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
		plan, err := resolve(ctx, request)
		plan.RequireFinalizationScope = true
		return plan, err
	}
}

// ConfigureControlledReadFindingsWithFinalizationScope is the initialization-only
// strict non-revision entry point. The older entry point remains an explicit
// compatibility path without a finalization-wide host lease guarantee.
// For revision-aware strict bootstrap, wrap options.Durable.Host.Findings with
// RequireControlledReadFinalizationScope before constructing the server instead
// of reconfiguring findings after construction.
func (s *Server) ConfigureControlledReadFindingsWithFinalizationScope(resolve ControlledReadFindingResolver) error {
	if resolve == nil {
		return ErrControlledFindingUnavailable
	}
	return s.ConfigureControlledReadFindings(RequireControlledReadFinalizationScope(resolve))
}

func nilFinalizationValue(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

// Called only after the collector has claimed finalization and unlocked. The
// returned release is idempotent and must be deferred until save has returned.
func (c *controlledFindingCollector) acquireFinalizationScope(ctx context.Context) (context.Context, func() error, func(), error) {
	provider := c.plan.FinalizationScope
	if nilFinalizationValue(provider) {
		if c.plan.RequireFinalizationScope || provider != nil {
			return nil, nil, nil, ErrControlledFindingUnavailable
		}
		return ctx, ctx.Err, func() {}, nil
	}
	scope, err := provider.Acquire(ctx, c.request, c.plan.Expected)
	if nilFinalizationValue(scope) {
		return nil, nil, nil, ErrControlledFindingUnavailable
	}
	var once sync.Once
	release := func() { once.Do(scope.Release) }
	if err != nil {
		release()
		return nil, nil, nil, ErrControlledFindingUnavailable
	}
	leaseContext := scope.Context()
	if nilFinalizationValue(leaseContext) || leaseContext.Done() == nil {
		release()
		return nil, nil, nil, ErrControlledFindingUnavailable
	}
	// Use the provider's context (including its bound values); independently
	// propagate caller cancellation. The provider should derive it from ctx.
	operation, cancel := context.WithCancel(leaseContext)
	if deadline, ok := ctx.Deadline(); ok {
		cancel()
		operation, cancel = context.WithDeadline(leaseContext, deadline)
	}
	stop := context.AfterFunc(ctx, cancel)
	cleanup := func() { stop(); cancel(); release() }
	check := func() error {
		if ctx.Err() != nil || leaseContext.Err() != nil || operation.Err() != nil {
			return ErrControlledFindingUnavailable
		}
		if scope.Check(operation) != nil || ctx.Err() != nil || leaseContext.Err() != nil || operation.Err() != nil {
			return ErrControlledFindingUnavailable
		}
		return nil
	}
	if check() != nil {
		cleanup()
		return nil, nil, nil, ErrControlledFindingUnavailable
	}
	return operation, check, cleanup, nil
}
