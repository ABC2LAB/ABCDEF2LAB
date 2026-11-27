package learnedlogin

import "testing"

func TestReplayFailureCounts(t *testing.T) {
	const identifier = "user-a"
	row := func(ref, outcome string) Row {
		return Row{Schema: RowSchema, RunRef: run, AttemptRef: ref, Identifier: identifier, Outcome: outcome}
	}
	pending := row("exploration-1", OutcomeUnconfirmed)
	lateSuccess := row("exploration-1", OutcomeSucceeded)
	lateFailure := row("exploration-1", OutcomeFailed)
	failed := []Row{row("diagnosis-2", OutcomeFailed), row("diagnosis-3", OutcomeFailed), row("diagnosis-4", OutcomeFailed)}
	laterSuccess := row("diagnosis-5", OutcomeSucceeded)
	refused := row("diagnosis-6", OutcomeNotSent)
	refused.Transport, refused.Refusal = TransportNotSent, string(ReasonAttemptsExhausted)
	recordedLimit := row("diagnosis-7", OutcomeUnknown)
	recordedLimit.ConsecutiveFailures = 3
	otherRun := refused
	otherRun.RunRef = "run-other"

	for _, tc := range []struct {
		name           string
		rows           []Row
		count, guarded int
	}{
		{"늦게 확정된 실패를 한 번만 집계", []Row{pending, laterSuccess, lateFailure, lateFailure}, 1, 1},
		{"늦게 확정된 성공 뒤에도 기존 상한 유지", []Row{pending, failed[0], failed[1], failed[2], lateSuccess}, 0, 3},
		{"중간에 닿은 상한 유지", []Row{failed[0], failed[1], failed[2], laterSuccess}, 0, 3},
		{"기존 거부 기록 유지", []Row{refused, laterSuccess}, 0, 3},
		{"행에 저장된 상한 유지", []Row{recordedLimit, laterSuccess}, 0, 3},
		{"상한 미만에서도 전송 제한을 완화하지 않음", []Row{pending, failed[0], failed[1], lateSuccess}, 0, 2},
		{"다른 회차의 차단은 제외", []Row{otherRun, failed[0]}, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counts, guard := ReplayFailureCounts(tc.rows, run, 3)
			if counts[identifier] != tc.count || guard[identifier] != tc.guarded {
				t.Fatalf("기록 집계 %d, 차단 판단용 집계 %d, 기대 %d·%d", counts[identifier], guard[identifier], tc.count, tc.guarded)
			}
		})
	}
}
