package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDiagnosisLogOutcomePreservesTypedFailure(t *testing.T) {
	cases := []struct {
		err            error
		status, reason string
	}{
		{nil, "succeeded", ""},
		{errors.New("Authorization: secret DSN private query"), "failed", "result_storage_failed"},
		{firstDiagnosisFailure(context.Canceled), "cancelled", "user_cancelled"},
		{firstDiagnosisOutcomeFailure{cause: context.DeadlineExceeded}, "timeout", "timeout"},
	}
	for _, c := range cases {
		status, reason := diagnosisLogOutcome(context.Background(), c.err, "result_storage_failed")
		if status != c.status || reason != c.reason {
			t.Fatalf("typed outcome lost: %s %s", status, reason)
		}
	}
}
func TestDiagnosisLogStagesRemainDistinct(t *testing.T) {
	for stage, want := range map[string]string{"model": "model_profile_invalid", "task": "task_binding_failed", "drain": "drain_failed", "audit-close": "audit_failed", "registered-result": "result_storage_failed", "execution": "execution_failed"} {
		if got := diagnosisLogStageReason(stage); got != want {
			t.Fatalf("stage %s: %s", stage, got)
		}
	}
	if diagnosisLogStage("untrusted raw stage") != "execution" {
		t.Fatal("untrusted stage escaped")
	}
}

func TestDiagnosisPreparationInnerDeadlineNotCleanupCancellation(t *testing.T) {
	outer := context.Background()
	for _, expired := range []bool{false, true} {
		deadline := time.Now().Add(time.Hour)
		if expired {
			deadline = time.Now().Add(-time.Second)
		}
		inner, cancel := context.WithDeadline(outer, deadline)
		var observed error
		func() {
			defer cancel()
			defer captureDiagnosisContextError(inner, &observed)
		}()
		if inner.Err() == nil {
			t.Fatal("정리 취소 미실행")
		}
		status, reason := diagnosisLogOutcome(outer, observed, "planner_failed")
		if expired && (status != "timeout" || reason != "timeout") {
			t.Fatal("내부 준비 시한이 일반 실패로 바뀜")
		}
		if !expired && (status != "succeeded" || reason != "") {
			t.Fatal("정리 취소를 실제 실패로 기록함")
		}
	}
}

func TestDiagnosisRunStoredTimeoutRemainsTimeout(t *testing.T) {
	for _, state := range [][2]string{{"timeout", ""}, {"running", "timeout"}} {
		observed := diagnosisRunObservedError(ErrFirstDiagnosisSupervisor, state[0], state[1])
		status, reason := diagnosisLogOutcome(context.Background(), observed, "execution_failed")
		if status != "timeout" || reason != "timeout" {
			t.Fatal("저장 timeout이 일반 실행 실패로 바뀜")
		}
	}
	if diagnosisRunObservedError(ErrFirstDiagnosisSupervisor, "failed", "blocked") != ErrFirstDiagnosisSupervisor {
		t.Fatal("시간 초과가 아닌 실패의 의미 변경")
	}
}

func TestDiagnosisLogCompletedStepIgnoresLaterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status, reason := diagnosisLogOutcome(ctx, nil, "execution_failed")
	if status != "succeeded" || reason != "" {
		t.Fatal("완료된 단계가 늦은 취소로 다시 분류됨")
	}
}
