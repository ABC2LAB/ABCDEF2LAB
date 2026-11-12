package server

import (
	"context"

	"github.com/Autumn-27/artex/diagnosis"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/exploration"
)

// 이 값은 실제 모델 원장·탐색 마감이 검증된 pipeline만 만든다. 모델/HTTP의 오류 문자열로 복원하지 않는다.
// 진단을 실행했다는 증명이 아니라, 진단 권위를 아직 획득하지 않은 종료의 증명이다.
type explorationBudgetClosure struct {
	budget       *FirstDiagnosisModelBudget
	workflow     *diagnosisflow.Controller
	binding      exploration.Binding
	sourceDigest string
	flowRevision uint64
}

func (*explorationBudgetClosure) Error() string { return ErrE2HTTPBudgetLimited.Error() }
func (*explorationBudgetClosure) Unwrap() error { return ErrE2HTTPBudgetLimited }
func (p *explorationBudgetClosure) validFlow() bool {
	if p == nil || p.budget == nil || p.workflow == nil {
		return false
	}
	p.budget.mu.Lock()
	known := p.budget.limitReached && !p.budget.blocked && p.budget.inflight == 0 && p.budget.usage.UsageKnown
	p.budget.mu.Unlock()
	if !known {
		return false
	}
	// revision과 private 권위 상태를 한 Controller lock 안에서 함께 확인한다.
	return p.workflow.CheckExplorationClosedBeforeDiagnosis(context.Background(), explorationFlowBinding(p.binding, p.sourceDigest), p.flowRevision) == nil
}
func (s *Server) closedExplorationBudget(o ExplorationPipelineOptions, session *ExplorationSession) (*explorationBudgetClosure, error) {
	if s == nil || session == nil || session.Binding() != o.Binding {
		return nil, ErrFirstDiagnosisModelBudget
	}
	if err := session.Close(context.Background(), exploration.Completed); err != nil {
		return nil, err
	}
	snapshot, err := o.Workflow.Snapshot()
	if err != nil {
		return nil, ErrFirstDiagnosisModelBudget
	}
	proof := &explorationBudgetClosure{budget: s.firstModelBudget, workflow: o.Workflow, binding: o.Binding, sourceDigest: o.SourceDigest, flowRevision: snapshot.Revision}
	if !proof.validFlow() {
		return nil, ErrFirstDiagnosisModelBudget
	}
	return proof, nil
}

// 검증된 탐색 예산 종료만 인정한다. nil Execution·일반 ErrBudget·진단/판정 완료를 대신하는 우회는 허용하지 않는다.
func (r *E2HTTPRuntime) hasClosedExplorationBudget(task *Task, budget *FirstDiagnosisModelBudget) bool {
	if r == nil || task == nil || budget == nil {
		return false
	}

	r.mu.Lock()
	t := r.task
	r.mu.Unlock()
	if t == nil || t.id != task.ID || t.exploration != task.ExpID || t.store != task.Store {
		return false
	}
	t.mu.Lock()
	proof := t.explorationBudget
	clean := t.explorationDone && proof != nil && proof.budget == budget && proof.workflow == r.options.Workflow && proof.binding.TaskID == t.id && proof.binding.RunRef == r.options.Expected.RunRef && t.lease == nil && !t.validLease && len(t.active) == 0 && len(t.runs) == 0 && len(t.results) == 0 && !t.resultsFailed && !t.releaseCalled && t.control.Snapshot().Calls == 0
	t.mu.Unlock()
	if !clean || !proof.validFlow() {
		return false
	}
	return true
}

func (r *E2HTTPRuntime) explorationBudgetOutcome(task *Task, budget *FirstDiagnosisModelBudget, out diagnosis.IntentOutcomes) (diagnosis.RunResult, bool) {
	if out.Done != 0 || out.PolicyLimited != 0 || out.Unknown != 0 || out.Stopped != 0 || out.Incomplete < 0 || !r.hasClosedExplorationBudget(task, budget) {
		return diagnosis.RunResult{}, false
	}

	outcome := diagnosis.OutcomePartial
	if out.Incomplete == 0 {
		outcome = diagnosis.OutcomeNoIntents
	}
	return diagnosis.RunResult{Outcome: outcome, ClosingReason: diagnosis.ClosingBudgetLimited, IntentOutcomes: out}, true
}
