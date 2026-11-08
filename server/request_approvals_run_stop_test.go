package server

import (
	"context"
	"errors"
	"github.com/Autumn-27/artex/db"
	"testing"
)

func TestRequestApprovalRunAdmissionStopBeforeAcquire(t *testing.T) {
	service := newRequestApprovalService(nil)
	runtime, task := e2HTTPRuntimeFixture(t, func(options *E2HTTPRuntimeOptions) { options.RequestApprovals = service })
	if err := runtime.AdmitTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	if err := service.stop(t.Context(), 7, "foreign"); err == nil {
		t.Fatal("foreign owner admitted")
	}
	if err := service.stop(t.Context(), 8, "ARTEX"); err == nil {
		t.Fatal("foreign task admitted")
	}
	if _, err := runtime.PreparePlanning(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	if err := service.stop(t.Context(), 7, "ARTEX"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.PreparePlanning(t.Context(), task); err == nil {
		t.Fatal("planning after stop")
	}
	if err := service.stop(t.Context(), 7, "ARTEX"); err != nil {
		t.Fatal("duplicate stop", err)
	}
	if err := runtime.AdmitTask(t.Context(), task); err == nil {
		t.Fatal("readmission after stop")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := service.admitRun(ctx, 7, "other-run", cancel); err == nil {
		t.Fatal("rebound another run")
	}
	state := service.runState(7)
	if !state.StopRequested || !state.ContextCancelled || state.Execution != nil || state.DrainComplete {
		t.Fatal("unsupported status", state)
	}
	service.finishStop(7, errors.New("synthetic closure failure"))
	if state := service.runState(7); state.DrainComplete || !state.ClosureFailed {
		t.Fatal("closure failure promoted")
	}
	service.finishStop(7, nil)
	if err := service.stop(t.Context(), 7, "ARTEX"); err != nil {
		t.Fatal(err)
	}
	if state := service.runState(7); !state.DrainComplete || state.ClosureFailed || state.Phase != "closed" || state.Outcome != "cancelled" {
		t.Fatal("closed state not reflected")
	}
}

func TestRequestApprovalRunLifecycleIsSupervisorOwned(t *testing.T) {
	service := newRequestApprovalService(nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := service.admitRun(ctx, 7, "run", cancel); err != nil {
		t.Fatal(err)
	}
	if state := service.runState(7); state.Phase != "prepared" || state.DrainComplete {
		t.Fatal(state)
	}
	service.runStarted(7)
	if state := service.runState(7); state.Phase != "running" || state.Outcome != "" {
		t.Fatal(state)
	}
	service.runFinished(7, errors.New("synthetic failure"), false)
	if state := service.runState(7); state.Phase != "failed" || state.Outcome != "failed" || state.DrainComplete || state.ClosureFailed {
		t.Fatal(state)
	}
}

func TestRequestApprovalFailedRunRejectsLateStop(t *testing.T) {
	service := newRequestApprovalService(nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := service.admitRun(ctx, 7, "run", cancel); err != nil {
		t.Fatal(err)
	}
	service.runStarted(7)
	if service.runFinished(7, errors.New("early failure"), false) {
		t.Fatal("invented stop")
	}
	if err := service.stop(t.Context(), 7, "ARTEX"); !errors.Is(err, db.ErrRequestApprovalConflict) {
		t.Fatal("late stop accepted", err)
	}
	state := service.runState(7)
	if state.Phase != "failed" || state.Outcome != "failed" || state.StopRequested || !state.ContextCancelled {
		t.Fatal("terminal failure reopened", state)
	}
	service.runStarted(7)
	if service.runState(7).Phase != "failed" {
		t.Fatal("terminal run restarted")
	}
}

func TestRequestApprovalStopAndFinishLinearize(t *testing.T) {
	for i := 0; i < 500; i++ {
		service := newRequestApprovalService(nil)
		ctx, cancel := context.WithCancel(t.Context())
		if err := service.admitRun(ctx, 7, "run", cancel); err != nil {
			t.Fatal(err)
		}
		service.runStarted(7)
		start := make(chan struct{})
		stopped := make(chan error, 1)
		finished := make(chan bool, 1)
		go func() { <-start; stopped <- service.stop(t.Context(), 7, "ARTEX") }()
		go func() { <-start; finished <- service.runFinished(7, errors.New("early failure"), false) }()
		close(start)
		stopErr, needsCleanup := <-stopped, <-finished
		if stopErr == nil {
			if !needsCleanup {
				t.Fatal("accepted stop lost its supervisor cleanup")
			}
			service.finishStop(7, nil)
			state := service.runState(7)
			if state.Phase != "closed" || state.Outcome != "cancelled" || !state.DrainComplete {
				t.Fatal(state)
			}
		} else {
			if !errors.Is(stopErr, db.ErrRequestApprovalConflict) || needsCleanup {
				t.Fatal("inconsistent terminal decision", stopErr, needsCleanup)
			}
			state := service.runState(7)
			if state.Phase != "failed" || state.StopRequested {
				t.Fatal("failed run reopened", state)
			}
		}
		cancel()
	}
}
