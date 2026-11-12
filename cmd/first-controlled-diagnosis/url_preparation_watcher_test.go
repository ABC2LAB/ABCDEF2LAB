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
			stopped := cancelURLStatusFailure(target, cancel)
			if (hard.Err() == nil) != softStop || stopped == softStop {
				t.Fatal("status failure escalated a normal target drain or ignored live failure")
			}
		})
	}
}

// 상태 조회(C10): 정한 횟수만큼 연속 실패는 다시 묻고, 그보다 많으면 포기한다. 한 번 성공하면 다시 센다.
func TestURLStatusReadsRetryConsecutiveFailures(t *testing.T) {
	none := urlStatusReads{}
	if !none.failed() {
		t.Fatal("재시도 0인데 첫 실패에서 멈추지 않음(지금 동작과 다름)")
	}
	reads := urlStatusReads{retries: 2}
	if reads.failed() || reads.failed() {
		t.Fatal("정한 횟수 안의 실패에서 멈춤")
	}
	reads.succeeded()
	if reads.failed() || reads.failed() || !reads.failed() {
		t.Fatal("성공 뒤 다시 세지 않거나 정한 횟수를 넘은 실패에서 멈추지 않음")
	}
}

// 정한 횟수 안의 실패는 실행을 멈추지 않고, 넘으면 멈추고 결과 불명으로 표시한다. 대상 철회로 이미 정리 중이면
// 넘어도 멈추거나 불명으로 바꾸지 않는다.
func TestURLStatusReadFailedStopsOnlyAfterRetries(t *testing.T) {
	for _, draining := range []bool{false, true} {
		hard, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		target, stop := context.WithCancelCause(hard)
		if draining {
			stop(server.ErrFirstDiagnosisTargetEnded)
		}
		reads := urlStatusReads{retries: 1}
		first := urlStatusReadFailed(target, cancel, &reads)
		if first || hard.Err() != nil {
			t.Fatalf("draining=%v: 첫 실패에서 멈춤", draining)
		}
		second := urlStatusReadFailed(target, cancel, &reads)
		if second == draining || (hard.Err() == nil) != draining {
			t.Fatalf("draining=%v: 횟수를 넘은 실패 처리 틀림 lost=%v", draining, second)
		}
		stop(context.Canceled)
		cancel()
	}
}

// 상태 조회를 다 못 해 멈춘 실행은 취소가 아니라 결과 불명이다. 다른 사유로 이미 불명이면 그 사유를 둔다.
func TestMarkURLStatusLost(t *testing.T) {
	for _, tc := range []struct {
		in         diagnosisprep.Update
		wantState  diagnosisprep.State
		wantReason string
	}{
		{diagnosisprep.Update{State: diagnosisprep.Cancelled}, diagnosisprep.Unknown, "status_read_failed"},
		{diagnosisprep.Update{State: diagnosisprep.Unknown, ReasonCode: "execution_closure_unconfirmed"}, diagnosisprep.Unknown, "execution_closure_unconfirmed"},
		{diagnosisprep.Update{State: diagnosisprep.Completed}, diagnosisprep.Completed, ""},
	} {
		u := tc.in
		markURLStatusLost(&u)
		if u.State != tc.wantState || u.ReasonCode != tc.wantReason {
			t.Fatalf("%+v -> %s/%s", tc.in, u.State, u.ReasonCode)
		}
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
