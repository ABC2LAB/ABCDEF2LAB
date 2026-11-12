package server

import (
	"testing"

	"github.com/Autumn-27/artex/diagnosis"
)

// 그래프 상태 → intent 결과 묶음(실행 결과 상태 값 계약 §2).
func TestFirstDiagnosisIntentOutcomeProjection(t *testing.T) {
	for _, tc := range []struct{ state, reason, want string }{
		{"done", "", intentOutcomeDone},
		{"blocked", "e2_http_policy_limited", intentOutcomePolicyLimited},
		{"blocked", "e2_http_unsupported", intentOutcomePolicyLimited},
		{"blocked", "e2_http_result_unknown", intentOutcomeUnknown},
		{"blocked", "", intentOutcomeIncomplete},
		{"blocked", "llm_quota", intentOutcomeIncomplete},
		{"exhausted", "", intentOutcomeIncomplete},
		// Planner kill_work·task 마감 뒤 취소는 운영자 중지가 아니다.
		{"stopped", "", intentOutcomeIncomplete},
		// 운영자 중지는 paused로 남는다.
		{"paused", "", intentOutcomeStopped},
		{"open", "", intentOutcomeOpen},
		{"running", "", intentOutcomeRunning},
		{"deleted", "", intentOutcomeIgnored},
		// 모르는 상태는 완료로 보이지 않게 unknown이다.
		{"vulnerable", "", intentOutcomeUnknown},
	} {
		if got := firstDiagnosisIntentOutcome(tc.state, tc.reason); got != tc.want {
			t.Fatalf("%s/%s: %s, want %s", tc.state, tc.reason, got, tc.want)
		}
	}
}

// 대조식(계약 §3.2): no_intents ⇔ 모두 0, complete ⇔ done+policy_limited ≥ 1이고 나머지 0, partial ⇔ 나머지 ≥ 1.
func TestRunOutcomeFromIntentCounts(t *testing.T) {
	for _, tc := range []struct {
		counts diagnosis.IntentOutcomes
		want   string
	}{
		{diagnosis.IntentOutcomes{}, diagnosis.OutcomeNoIntents},
		{diagnosis.IntentOutcomes{Done: 2}, diagnosis.OutcomeComplete},
		{diagnosis.IntentOutcomes{PolicyLimited: 1}, diagnosis.OutcomeComplete},
		{diagnosis.IntentOutcomes{Done: 1, Incomplete: 1}, diagnosis.OutcomePartial},
		{diagnosis.IntentOutcomes{Incomplete: 3}, diagnosis.OutcomePartial},
		{diagnosis.IntentOutcomes{Done: 1, Stopped: 1}, diagnosis.OutcomePartial},
		{diagnosis.IntentOutcomes{Done: 1, Unknown: 1}, diagnosis.OutcomePartial},
	} {
		if got := tc.counts.Outcome(); got != tc.want {
			t.Fatalf("%+v: %s, want %s", tc.counts, got, tc.want)
		}
		if !(diagnosis.RunResult{Outcome: tc.want, IntentOutcomes: tc.counts}).Valid() {
			t.Fatalf("%+v rejected", tc.counts)
		}
	}
}

// 마감 판정 앞부분: 실행 중이면 대기, 상한 전에는 open·미계획이면 대기, 상한이면 open을 incomplete로 세고 마감.
func TestFirstDiagnosisClosingOutcomes(t *testing.T) {
	base := diagnosis.IntentOutcomes{Done: 1}
	for _, tc := range []struct {
		name                   string
		open, running          int64
		budgetLimited, planned bool
		closing                bool
		incomplete             int64
	}{
		{"planned", 0, 0, false, true, true, 0},
		{"running-waits", 0, 1, false, true, false, 0},
		{"running-waits-even-at-limit", 0, 1, true, true, false, 0},
		{"open-waits", 1, 0, false, true, false, 0},
		{"unplanned-waits", 0, 0, false, false, false, 0},
		{"limit-counts-open-incomplete", 2, 0, true, false, true, 2},
		{"limit-without-open", 0, 0, true, false, true, 0},
	} {
		out, closing := firstDiagnosisClosingOutcomes(firstDiagnosisIntentCounts{outcomes: base, open: tc.open, running: tc.running}, tc.budgetLimited, tc.planned)
		if closing != tc.closing || closing && (out.Incomplete != tc.incomplete || out.Done != 1) {
			t.Fatalf("%s: closing=%v out=%+v", tc.name, closing, out)
		}
	}
}

// 결과는 정상 마감(completed)일 때만 밖에 보인다.
func TestSupervisorRunResultOnlyAfterCompletion(t *testing.T) {
	s := &FirstDiagnosisSupervisor{}
	s.setRunResult(diagnosis.RunResult{Outcome: diagnosis.OutcomePartial, IntentOutcomes: diagnosis.IntentOutcomes{Done: 1, Incomplete: 1}})
	if s.RunResult() != nil {
		t.Fatal("result exposed before completion")
	}
	s.completed = true
	got := s.RunResult()
	if got == nil || got.Outcome != diagnosis.OutcomePartial || got.IntentOutcomes.Incomplete != 1 || !got.Valid() {
		t.Fatalf("result: %+v", got)
	}
	got.IntentOutcomes.Done = 99
	if s.RunResult().IntentOutcomes.Done != 1 {
		t.Fatal("result shared by reference")
	}
}
