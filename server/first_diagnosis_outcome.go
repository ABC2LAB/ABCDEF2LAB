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
	// 아직 진행 중(open·running). 마감 판정은 이것이 없을 때만 한다.
	intentOutcomeActive = "active"
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
	case "open", "running":
		return intentOutcomeActive
	case "deleted":
		return intentOutcomeIgnored
	}
	return intentOutcomeUnknown
}

type firstDiagnosisQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// firstDiagnosisIntentOutcomes는 탐색 하나의 intent를 결과 묶음별로 센다. active는 진행 중인 수다.
func firstDiagnosisIntentOutcomes(ctx context.Context, q firstDiagnosisQuerier, explorationID int64) (diagnosis.IntentOutcomes, int64, error) {
	var out diagnosis.IntentOutcomes
	var active int64
	rows, err := q.QueryContext(ctx, `SELECT state, COALESCE(blocked_reason, ''), count(*) FROM exploration_nodes
 WHERE exploration_id=$1 AND kind='intent' GROUP BY 1, 2`, explorationID)
	if err != nil {
		return out, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var state, reason string
		var n int64
		if err := rows.Scan(&state, &reason, &n); err != nil {
			return diagnosis.IntentOutcomes{}, 0, err
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
		case intentOutcomeActive:
			active += n
		case intentOutcomeIgnored:
		default:
			out.Unknown += n
		}
	}
	if err := rows.Err(); err != nil {
		return diagnosis.IntentOutcomes{}, 0, err
	}
	return out, active, nil
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

func (s *FirstDiagnosisSupervisor) setRunResult(outcomes diagnosis.IntentOutcomes) {
	result := diagnosis.RunResult{Outcome: outcomes.Outcome(), IntentOutcomes: outcomes}
	s.mu.Lock()
	s.result = &result
	s.mu.Unlock()
}
