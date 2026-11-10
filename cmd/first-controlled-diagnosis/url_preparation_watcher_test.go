package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Autumn-27/artex/server"
)

func TestURLPreparationWatcherTargetDrainKeepsHardContextUntilAuditFailure(t *testing.T) {
	hard, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	target, stopTarget := context.WithCancelCause(hard)
	defer stopTarget(context.Canceled)
	done := make(chan struct{})
	defer close(done)
	ticks := make(chan time.Time)
	auditFailed := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		watchURLPreparation(hard, done, auditFailed, ticks, func() error { return server.ErrFirstDiagnosisTargetEnded }, cancel, stopTarget)
	}()
	ticks <- time.Now()
	select {
	case <-target.Done():
	case <-time.After(time.Second):
		t.Fatal("watcher did not stop target admission")
	}
	if !errors.Is(context.Cause(target), server.ErrFirstDiagnosisTargetEnded) || hard.Err() != nil {
		t.Fatal("target watcher used hard cancellation instead of drain")
	}
	// The watcher must still process a later audit failure after the soft stop.
	close(auditFailed)
	select {
	case <-hard.Done():
	case <-time.After(time.Second):
		t.Fatal("audit failed during drain without hard cancellation")
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("watcher leaked")
	}
}

func TestURLPreparationWatcherInstallationFailureCancelsImmediately(t *testing.T) {
	hard, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	target, stopTarget := context.WithCancelCause(hard)
	defer stopTarget(context.Canceled)
	done := make(chan struct{})
	defer close(done)
	ticks := make(chan time.Time, 1)
	ticks <- time.Now()
	watchURLPreparation(hard, done, make(chan struct{}), ticks, func() error { return errPreparation }, cancel, stopTarget)
	if hard.Err() == nil || errors.Is(context.Cause(target), server.ErrFirstDiagnosisTargetEnded) {
		t.Fatal("installation failure treated as normal target drain")
	}
}

func TestURLStatusFailurePreservesTargetDrainButStopsLiveExecution(t *testing.T) {
	for _, softStop := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "draining"}[softStop], func(t *testing.T) {
			hard, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			target, stop := context.WithCancelCause(hard)
			defer stop(context.Canceled)
			if softStop {
				stop(server.ErrFirstDiagnosisTargetEnded)
			}
			cancelURLStatusFailure(target, cancel)
			if (hard.Err() == nil) != softStop {
				t.Fatal("status failure escalated a normal target drain or ignored live failure")
			}
		})
	}
}
