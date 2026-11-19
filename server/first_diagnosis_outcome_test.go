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

// 마감 판정 앞부분: 실행 중이면 대기, 실행 상한·목표 달성 전에는 계획이 비어도 대기, 상한이면 open을 incomplete로 세고 마감.
func TestFirstDiagnosisClosingOutcomes(t *testing.T) {
	base := diagnosis.IntentOutcomes{Done: 1}
	for _, tc := range []struct {
		name          string
		open, running int64
		forced        bool
		closing       bool
		incomplete    int64
	}{
		// 작업이 다 끝나고 계획이 비어도(남은 open·running 0) 실행 상한·목표 달성 전에는 마감하지 않는다(원본과 같은 끝 규칙).
		{"idle-empty-plan-waits", 0, 0, false, false, 0},
		{"running-waits", 0, 1, false, false, 0},
		{"running-waits-even-at-limit", 0, 1, true, false, 0},
		{"open-waits", 1, 0, false, false, 0},
		{"limit-counts-open-incomplete", 2, 0, true, true, 2},
		{"limit-without-open", 0, 0, true, true, 0},
	} {
		out, closing := firstDiagnosisClosingOutcomes(firstDiagnosisIntentCounts{outcomes: base, open: tc.open, running: tc.running}, tc.forced)
		if closing != tc.closing || closing && (out.Incomplete != tc.incomplete || out.Done != 1) {
			t.Fatalf("%s: closing=%v out=%+v", tc.name, closing, out)
		}
	}
}

// 중지 실행의 결과: 시작하지 않은 open은 incomplete, 실행 중 intent가 남았으면 쓰지 않는다(C08 2-2).
func TestFirstDiagnosisStoppedOutcomes(t *testing.T) {
	base := diagnosis.IntentOutcomes{Done: 1, Stopped: 1, Unknown: 1}
	out, ok := firstDiagnosisStoppedOutcomes(firstDiagnosisIntentCounts{outcomes: base, open: 2})
	if !ok || out != (diagnosis.IntentOutcomes{Done: 1, Stopped: 1, Unknown: 1, Incomplete: 2}) {
		t.Fatalf("stopped outcomes: %+v %v", out, ok)
	}
	if _, ok := firstDiagnosisStoppedOutcomes(firstDiagnosisIntentCounts{outcomes: base, running: 1}); ok {
		t.Fatal("running intent counted as stopped result")
	}
}

// 정상 마감 결과가 있으면 그것을, 없으면 중지 개수만, 둘 다 없으면 nil을 마감 결과로 낸다.
func TestSupervisorClosureResult(t *testing.T) {
	s := &FirstDiagnosisSupervisor{}
	if s.ClosureResult() != nil {
		t.Fatal("empty supervisor has a closure result")
	}
	s.stopped = &diagnosis.IntentOutcomes{Stopped: 1}
	if got := s.ClosureResult(); got == nil || !got.StoppedValid() || got.IntentOutcomes.Stopped != 1 {
		t.Fatalf("stopped closure result: %+v", got)
	}
	s.completed = true
	s.setRunResult(diagnosis.RunResult{Outcome: diagnosis.OutcomeComplete, IntentOutcomes: diagnosis.IntentOutcomes{Done: 1}})
	if got := s.ClosureResult(); got == nil || got.Outcome != diagnosis.OutcomeComplete {
		t.Fatalf("completed closure result: %+v", got)
	}
}

// 목표 달성(작업 done)이나 실행 상한이면 Planner를 기다리지 않고 마감하고, 남은 open intent는 incomplete다(C26).
func TestFirstDiagnosisForcedCloseOnGoalMet(t *testing.T) {
	task := &Task{}
	if firstDiagnosisForcedClose(false, task) || !firstDiagnosisForcedClose(true, task) || firstDiagnosisForcedClose(false, nil) {
		t.Fatal("forced close without goal or limit")
	}
	task.updateLifecycle(func(state *taskLifecycleState) { state.Status = "done" })
	if !firstDiagnosisGoalMet(task) || !firstDiagnosisForcedClose(false, task) {
		t.Fatal("goal met not forcing the close")
	}
	out, closing := firstDiagnosisClosingOutcomes(firstDiagnosisIntentCounts{outcomes: diagnosis.IntentOutcomes{Done: 1}, open: 2}, firstDiagnosisForcedClose(false, task))
	if !closing || out != (diagnosis.IntentOutcomes{Done: 1, Incomplete: 2}) || out.Outcome() != diagnosis.OutcomePartial {
		t.Fatalf("goal-met close: %+v %v", out, closing)
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
