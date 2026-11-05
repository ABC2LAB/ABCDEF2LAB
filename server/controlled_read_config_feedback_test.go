package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestConfigHandleRejectsFirstInvalidRequestWithoutPinning(t *testing.T) {
	for _, field := range []string{"task", "intent", "run", "reset", "case", "policy", "taskref", "account", "resource"} {
		t.Run(field, func(t *testing.T) {
			r, _, cfg := realInputsTestFixture()
			h, closeHandle, err := newControlledReadConfigHandle(t.Context(), r.TaskID, r.IntentID, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer closeHandle()
			other := r
			switch field {
			case "task":
				other.TaskID++
			case "intent":
				other.IntentID++
			case "run":
				other.Scope.RunRef = "other-run"
			case "reset":
				other.Scope.ResetRef = "other-reset"
			case "case":
				other.Scope.CaseRef = "other-case"
			case "policy":
				other.Scope.PolicyRef = "other-policy"
			case "taskref":
				other.Scope.TaskRef = "other-task"
			case "account":
				other.AccountRef = "unregistered-account"
			case "resource":
				other.ResourceRef = "unregistered-resource"
			}
			if view, err := h.View(t.Context(), other); !errors.Is(err, ErrControlledFindingUnavailable) || len(view.Sessions) != 0 {
				t.Fatal("first invalid request returned metadata")
			}
			if _, err := h.View(t.Context(), r); err != nil {
				t.Fatal("refused request pinned the fresh handle")
			}
		})
	}
}

func TestConfigHandleFactoryRefusesInvalidRegistration(t *testing.T) {
	for _, mode := range []string{"nil-life", "cancelled-life", "task-zero", "intent-zero", "taskref", "run-empty", "sessions-empty"} {
		t.Run(mode, func(t *testing.T) {
			r, _, cfg := realInputsTestFixture()
			life := t.Context()
			switch mode {
			case "nil-life":
				life = nil
			case "cancelled-life":
				ctx, cancel := context.WithCancel(life)
				cancel()
				life = ctx
			case "task-zero":
				r.TaskID = 0
				// Keep the independent TaskRef check satisfied.
				cfg.Scope.TaskRef = "0"
			case "intent-zero":
				r.IntentID = 0
			case "taskref":
				cfg.Scope.TaskRef = "other-task"
			case "run-empty":
				cfg.Scope.RunRef = ""
			case "sessions-empty":
				cfg.Sessions = nil
			}
			h, closeHandle, err := newControlledReadConfigHandle(life, r.TaskID, r.IntentID, cfg)
			if closeHandle != nil {
				defer closeHandle()
			}
			if !errors.Is(err, ErrControlledFindingUnavailable) || h != nil || closeHandle != nil {
				t.Fatal("invalid registration minted a runtime handle")
			}
		})
	}
}

func TestConfigHandlePreservesReadOnlyFalseAndRefusesBind(t *testing.T) {
	r, expected, cfg := realInputsTestFixture()
	cfg.Resources[0].ReadOnly = false
	h, closeHandle, err := newControlledReadConfigHandle(t.Context(), r.TaskID, r.IntentID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeHandle()
	view, err := h.View(t.Context(), r)
	if err != nil || len(view.Resources) != 1 || view.Resources[0].ReadOnly {
		t.Fatal("metadata promoted a non-read-only registration")
	}
	plan, err := h.Bind(t.Context(), r, expected, &realInputsTestKG{}, configRuntimeSource)
	if !errors.Is(err, ErrControlledFindingUnavailable) || plan.KG != nil || plan.Source != nil {
		t.Fatal("Bind promoted a non-read-only registration")
	}
}

// End the runtime after View releases its lock, at the adapter's first check.
// This makes close/cancellation/expiry races reproducible without scheduler waits.
type configBindBoundaryContext struct {
	context.Context
	checks atomic.Int32
	end    func()
}

func (c *configBindBoundaryContext) Err() error {
	if c.checks.Add(1) == 3 {
		c.end()
	}
	return c.Context.Err()
}

func TestConfigHandleBindRechecksRuntimeBeforeReturn(t *testing.T) {
	for _, mode := range []string{"close", "cancel-life", "expire"} {
		t.Run(mode, func(t *testing.T) {
			r, expected, cfg := realInputsTestFixture()
			life, cancel := context.WithCancel(t.Context())
			defer cancel()
			h, closeHandle, err := newControlledReadConfigHandle(life, r.TaskID, r.IntentID, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer closeHandle()
			ctx := &configBindBoundaryContext{Context: t.Context(), end: func() {
				switch mode {
				case "close":
					closeHandle()
				case "cancel-life":
					cancel()
				case "expire":
					h.state.mu.Lock()
					h.state.until = time.Now().Add(-time.Second)
					h.state.mu.Unlock()
				}
			}}
			plan, err := h.Bind(ctx, r, expected, &realInputsTestKG{}, configRuntimeSource)
			if ctx.checks.Load() < 3 {
				t.Fatal("test did not reach the adapter boundary")
			}
			if !errors.Is(err, ErrControlledFindingUnavailable) || plan.KG != nil || plan.Source != nil {
				t.Fatal("Bind returned a plan after the runtime ended")
			}
		})
	}
}
