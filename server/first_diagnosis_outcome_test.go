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
		{"open", "", intentOutcomeActive},
		{"running", "", intentOutcomeActive},
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

// 결과는 정상 마감(completed)일 때만 밖에 보인다.
func TestSupervisorRunResultOnlyAfterCompletion(t *testing.T) {
	s := &FirstDiagnosisSupervisor{}
	s.setRunResult(diagnosis.IntentOutcomes{Done: 1, Incomplete: 1})
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
