package learnedlogin

// ReplayFailureCounts는 기록 순서대로 결과가 바뀐 행을 세고, 자동 재로그인 차단에 쓸 수를 함께 돌려준다.
// 이미 상한에 닿은 아이디는 뒤늦게 확정된 성공이 집계를 초기화해도 같은 회차에 다시 허용하지 않는다.
func ReplayFailureCounts(rows []Row, runRef string, limit int) (counts, guarded map[string]int) {
	counts = make(map[string]int)
	// 기존 시도 순서에 따른 제한도 유지해 집계 변경으로 다시 로그인이 더 허용되지 않게 한다.
	guarded = FailureCounts(rows, runRef)
	last := make(map[string]string)
	for _, row := range rows {
		if row.Schema != RowSchema || row.RunRef != runRef || !validRef(row.AttemptRef) || len(row.Identifier) > maxIdentifier {
			continue
		}
		id := row.Identifier
		if limit > 0 && (row.ConsecutiveFailures >= limit ||
			(row.Transport == TransportNotSent && row.Refusal == string(ReasonAttemptsExhausted))) {
			guarded[id] = max(guarded[id], limit)
		}
		if last[row.AttemptRef] == row.Outcome {
			continue
		}
		last[row.AttemptRef] = row.Outcome
		switch row.Outcome {
		case OutcomeFailed:
			counts[id]++
			if limit > 0 && counts[id] >= limit {
				guarded[id] = max(guarded[id], limit)
			}
		case OutcomeSucceeded:
			counts[id] = 0
		}
	}
	for id, count := range counts {
		guarded[id] = max(guarded[id], count)
	}
	return counts, guarded
}
