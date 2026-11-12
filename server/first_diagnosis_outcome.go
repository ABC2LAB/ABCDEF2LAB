package server

import (
	"context"
	"database/sql"

	"github.com/Autumn-27/artex/diagnosis"
)

// intent 결과 묶음(docs/contracts/run-outcome-states.md §2). 그래프 상태를 새로 만들지 않고 지금 상태에서 계산한다.
const (
	intentOutcomeDone          = "done"
	intentOutcomePolicyLimited = "policy_limited"
	intentOutcomeIncomplete    = "incomplete"
	intentOutcomeUnknown       = "unknown"
	intentOutcomeStopped       = "stopped"
	// 아직 시작하지 않음(open). 보통 마감 판정은 이것이 없을 때만 하고, 실행 상한 마감(budget_limited)에서는
	// 시작하지 못한 intent로 incomplete에 센다(계약 §2).
	intentOutcomeOpen = "open"
	// 실행 중(running). 마감 판정은 이것이 없을 때만 한다.
	intentOutcomeRunning = "running"
	// 세지 않는다(deleted).
	intentOutcomeIgnored = "ignored"
)

// firstDiagnosisIntentOutcome은 intent 하나의 그래프 상태를 결과 묶음으로 옮긴다.
//   - 그래프 stopped(Planner kill_work·task 마감 뒤 취소)는 운영자 중지가 아니라 incomplete다.
//   - 운영자 중지는 지금 엔진에서 paused로 남으므로 그것을 stopped로 센다.
//   - 모르는 상태는 결과를 확인할 수 없으므로 unknown으로 센다(완료로 보이지 않게).
func firstDiagnosisIntentOutcome(state, blockedReason string) string {
	switch state {
	case "done":
		return intentOutcomeDone
	case "blocked":
		switch blockedReason {
		case "e2_http_policy_limited", "e2_http_unsupported":
			return intentOutcomePolicyLimited
		case "e2_http_result_unknown":
			return intentOutcomeUnknown
		}
		return intentOutcomeIncomplete
	case "exhausted", "stopped":
		return intentOutcomeIncomplete
	case "paused":
		return intentOutcomeStopped
	case "open":
		return intentOutcomeOpen
	case "running":
		return intentOutcomeRunning
	case "deleted":
		return intentOutcomeIgnored
	}
	return intentOutcomeUnknown
}

type firstDiagnosisQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// firstDiagnosisIntentCounts는 결과 묶음별 개수와 아직 시작하지 않은(open)·실행 중(running) 수다.
type firstDiagnosisIntentCounts struct {
	outcomes      diagnosis.IntentOutcomes
	open, running int64
}

// firstDiagnosisIntentOutcomes는 탐색 하나의 intent를 결과 묶음별로 센다.
func firstDiagnosisIntentOutcomes(ctx context.Context, q firstDiagnosisQuerier, explorationID int64) (firstDiagnosisIntentCounts, error) {
	var counts firstDiagnosisIntentCounts
	out := &counts.outcomes
	rows, err := q.QueryContext(ctx, `SELECT state, COALESCE(blocked_reason, ''), count(*) FROM exploration_nodes
 WHERE exploration_id=$1 AND kind='intent' GROUP BY 1, 2`, explorationID)
	if err != nil {
		return firstDiagnosisIntentCounts{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var state, reason string
		var n int64
		if err := rows.Scan(&state, &reason, &n); err != nil {
			return firstDiagnosisIntentCounts{}, err
		}
		switch firstDiagnosisIntentOutcome(state, reason) {
		case intentOutcomeDone:
			out.Done += n
		case intentOutcomePolicyLimited:
			out.PolicyLimited += n
		case intentOutcomeIncomplete:
			out.Incomplete += n
		case intentOutcomeStopped:
			out.Stopped += n
		case intentOutcomeOpen:
			counts.open += n
		case intentOutcomeRunning:
			counts.running += n
		case intentOutcomeIgnored:
		default:
			out.Unknown += n
		}
	}
	if err := rows.Err(); err != nil {
		return firstDiagnosisIntentCounts{}, err
	}
	return counts, nil
}

// firstDiagnosisBudgetLimited는 실행 상한(E② 런타임의 Planner 라운드·도구 호출, 승인 모델 예산)에 닿았는지 본다.
func firstDiagnosisBudgetLimited(task *Task, budget *FirstDiagnosisModelBudget) bool {
	return task != nil && task.firstDiagnosisBudgetLimited.Load() || budget.LimitReached()
}

// firstDiagnosisPlanned는 마지막 작업 뒤 Planner가 계획했는지 본다. 작업이 하나도 없으면 Planner가 오류 없이 한 번
// 이상 계획했는지 본다(그래도 intent가 없으면 no_intents로 마감한다).
func firstDiagnosisPlanned(task *Task) bool {
	work := task.firstDiagnosisWorkSeq.Load()
	if work == 0 {
		return task.firstDiagnosisPlanRounds.Load() > 0
	}
	return task.firstDiagnosisPlannedSeq.Load() >= work
}

// firstDiagnosisClosingOutcomes는 마감 판정의 앞부분이다. 실행 중 intent가 있으면 기다린다. 실행 상한에 닿지
// 않았으면 시작하지 않은 intent가 없고 마지막 작업 뒤 Planner가 계획했을 때만 마감한다. 실행 상한에 닿았으면
// Planner를 기다리지 않고, 시작하지 못한 intent를 incomplete로 센다(계약 §2·§3.2).
func firstDiagnosisClosingOutcomes(c firstDiagnosisIntentCounts, budgetLimited, planned bool) (diagnosis.IntentOutcomes, bool) {
	if c.running > 0 || !budgetLimited && (c.open > 0 || !planned) {
		return diagnosis.IntentOutcomes{}, false
	}
	out := c.outcomes
	out.Incomplete += c.open
	return out, true
}

// RunResult는 감사가 닫힌 실행의 결과다. 실행이 정상 마감되지 않았으면 nil이다.
func (s *FirstDiagnosisSupervisor) RunResult() *diagnosis.RunResult {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.completed || s.result == nil {
		return nil
	}
	result := *s.result
	return &result
}

func (s *FirstDiagnosisSupervisor) setRunResult(result diagnosis.RunResult) {
	s.mu.Lock()
	s.result = &result
	s.mu.Unlock()
}
