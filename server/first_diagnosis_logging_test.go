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

// 감독자 검사가 실은 사유 코드가 실패 행에 남고, 사유가 없으면 단계의 일반 사유로 돌아간다. 호출자가 보는 오류 종류는 그대로다.
func TestDiagnosisFailureReasonKeepsCheckReason(t *testing.T) {
	failed := firstDiagnosisCheckFailed(reasonClosureToolOutcomeInvalid, ErrFirstDiagnosisSupervisor)
	if got := diagnosisFailureReason("execution", failed); got != reasonClosureToolOutcomeInvalid {
		t.Fatalf("검사 사유가 실패 행에 남지 않음: %s", got)
	}
	if status, reason := diagnosisLogOutcome(context.Background(), failed, diagnosisFailureReason("execution", failed)); status != "failed" || reason != reasonClosureToolOutcomeInvalid {
		t.Fatalf("검사 사유가 실행 마감 행에 남지 않음: %s %s", status, reason)
	}
	if got := diagnosisFailureReason("execution", ErrFirstDiagnosisSupervisor); got != "execution_failed" {
		t.Fatalf("사유 없는 실패가 일반 사유로 돌아가지 않음: %s", got)
	}
	if got := diagnosisFailureReason("drain", firstDiagnosisCheckFailed(reasonClosureNotReady, ErrFirstDiagnosisSupervisor)); got != reasonClosureNotReady {
		t.Fatalf("드레인 뒤 마감 판정 사유가 단계 사유로 덮임: %s", got)
	}
	if !errors.Is(failed, ErrFirstDiagnosisSupervisor) || errors.Is(failed, ErrFirstDiagnosisOutcome) ||
		!errors.Is(firstDiagnosisCheckFailed(reasonClosureWorkflowFailed, ErrFirstDiagnosisOutcome), ErrFirstDiagnosisOutcome) {
		t.Fatal("검사 사유가 원래 오류 종류를 바꿈")
	}
	// 저장 timeout은 검사 사유보다 앞선다.
	if status, reason := diagnosisLogOutcome(context.Background(), diagnosisRunObservedError(firstDiagnosisCheckFailed(reasonEngineTaskFailed, ErrFirstDiagnosisSupervisor), "timeout", ""), "execution_failed"); status != "timeout" || reason != "timeout" {
		t.Fatalf("저장 timeout이 검사 사유로 바뀜: %s %s", status, reason)
	}
}
