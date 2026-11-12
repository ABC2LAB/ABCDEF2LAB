package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/server"
)

// 대상 철회는 drain(소프트 중지)이고 hard context는 남는다. 진단 로그 기록 실패는 감사가 아니라
// 이 watcher가 실행을 취소하지 않는다(P3 결정안 C10). watcher는 done으로만 끝난다.
func TestURLPreparationWatcherTargetDrainKeepsHardContext(t *testing.T) {
	hard, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	target, stopTarget := context.WithCancelCause(hard)
	defer stopTarget(context.Canceled)
	done := make(chan struct{})
	ticks := make(chan time.Time)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		watchURLPreparation(hard, done, ticks, func() error { return server.ErrFirstDiagnosisTargetEnded }, cancel, stopTarget)
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
	// 소프트 중지 뒤의 다음 tick도 hard 취소로 바꾸지 않는다.
	ticks <- time.Now()
	if hard.Err() != nil {
		t.Fatal("repeated target end cancelled the drain")
	}
	close(done)
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("watcher leaked")
	}
	if hard.Err() != nil {
		t.Fatal("watcher exit cancelled the run")
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
	watchURLPreparation(hard, done, ticks, func() error { return errPreparation }, cancel, stopTarget)
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

func TestDiagnosticLogWarningNeverChangesRunState(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		in                     diagnosisprep.Update
		logFailed, observation bool
		wantState              diagnosisprep.State
		wantReason             string
		wantWarning            bool
	}{
		{"completed-log-failed", diagnosisprep.Update{State: diagnosisprep.Completed}, true, false, diagnosisprep.Completed, "", true},
		{"completed-observation", diagnosisprep.Update{State: diagnosisprep.Completed}, false, true, diagnosisprep.Completed, "", true},
		{"completed-clean", diagnosisprep.Update{State: diagnosisprep.Completed}, false, false, diagnosisprep.Completed, "", false},
		// 다른 사유가 있어도 경고가 따로 남는다(#418 이경준 지적).
		{"completed-keeps-reason-and-warning", diagnosisprep.Update{State: diagnosisprep.Completed, ReasonCode: "budget_limited"}, true, true, diagnosisprep.Completed, "budget_limited", true},
		{"failed-log-failed", diagnosisprep.Update{State: diagnosisprep.Failed, ReasonCode: "planner_failed"}, true, true, diagnosisprep.Failed, "planner_failed", false},
		{"cancelled-log-failed", diagnosisprep.Update{State: diagnosisprep.Cancelled}, true, false, diagnosisprep.Cancelled, "", false},
		{"unknown-stays", diagnosisprep.Update{State: diagnosisprep.Unknown, ReasonCode: "model_usage_unknown"}, true, false, diagnosisprep.Unknown, "model_usage_unknown", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := tc.in
			markDiagnosticLogWarning(&u, tc.logFailed, tc.observation)
			if u.State != tc.wantState || u.ReasonCode != tc.wantReason || u.DiagnosticLogIncomplete != tc.wantWarning {
				t.Fatalf("state=%s reason=%q warning=%v", u.State, u.ReasonCode, u.DiagnosticLogIncomplete)
			}
		})
	}
}
