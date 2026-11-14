package server

import (
	"fmt"
	"testing"

	"github.com/Autumn-27/artex/diagnosisprep"
)

// 제품의 모델 호출은 첫 진단 모델 예산을 거치므로, 예산 한도가 아닌 실패(429·5xx·보내기 전 거부)도 오류에
// ErrFirstDiagnosisModelBudget이 붙어 돌아온다. 그런 실패는 예산 종료가 아니라 탐색 모델 오류라서, 남긴 발견으로
// 진단을 이어야 한다(2026-11-14 실제 회차: 탐색 모델 호출 실패 뒤 실행 전체가 failed).
func TestExplorationPipelineModelCallFailureThroughBudgetIsPartialNotFailure(t *testing.T) {
	server, task, o, _, _, _, p := pipelineFixture(t)
	budget, err := NewFirstDiagnosisModelBudget(diagnosisprep.Budget{ModelRequests: 4, InputTokens: 4 * 65536, OutputTokens: 512}, 65536, 128, func(diagnosisprep.ModelUsage) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	server.firstModelBudget = budget
	p.failure = fmt.Errorf("owned synthetic provider failure: %w", ErrFirstDiagnosisModelBudget)
	result, err := server.RunExplorationPipeline(t.Context(), task, o)
	if err != nil || result.Status != "PARTIAL" || result.Reason != "model_error" || !result.Charge.Known {
		t.Fatalf("model call failure ended exploration as a failure: err=%v status=%s reason=%s charge=%+v", err, result.Status, result.Reason, result.Charge)
	}
}

// 회계가 막힌 예산(사용량을 믿을 수 없음)의 실패는 지금처럼 탐색 실패로 남는다. 모델 오류로 낮추지 않는다.
func TestExplorationPipelineBlockedBudgetFailureStillFails(t *testing.T) {
	server, task, o, _, _, _, p := pipelineFixture(t)
	budget, err := NewFirstDiagnosisModelBudget(diagnosisprep.Budget{ModelRequests: 4, InputTokens: 4 * 65536, OutputTokens: 512}, 65536, 128, func(diagnosisprep.ModelUsage) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	budget.blocked = true
	server.firstModelBudget = budget
	p.failure = fmt.Errorf("owned synthetic provider failure: %w", ErrFirstDiagnosisModelBudget)
	if _, err := server.RunExplorationPipeline(t.Context(), task, o); err == nil {
		t.Fatal("blocked budget failure continued as an ordinary model error")
	}
}
