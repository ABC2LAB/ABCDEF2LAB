package main

import (
	"testing"

	"github.com/Autumn-27/artex/diagnosis"
	"github.com/Autumn-27/artex/diagnosisprep"
)

// 마감 갱신의 결과 칸: completed는 그대로, cancelled는 intent 결과 개수만(outcome·closing_reason 버림), 나머지는
// 지운다(계약 §3.2, C08 2-2).
func TestURLClosingResultByState(t *testing.T) {
	counts := diagnosis.IntentOutcomes{Done: 1, Stopped: 2}
	full := &diagnosis.RunResult{Outcome: diagnosis.OutcomePartial, ClosingReason: diagnosis.ClosingBudgetLimited, IntentOutcomes: counts}
	for _, tc := range []struct {
		state    diagnosisprep.State
		want     *diagnosis.RunResult
		logFlag  bool
		wantFlag bool
	}{
		{diagnosisprep.Completed, full, true, true},
		{diagnosisprep.Cancelled, &diagnosis.RunResult{IntentOutcomes: counts}, true, false},
		{diagnosisprep.Failed, nil, true, false},
		{diagnosisprep.Unknown, nil, true, false},
	} {
		result := *full
		got := urlClosingResult(diagnosisprep.Update{State: tc.state, Result: &result, DiagnosticLogIncomplete: tc.logFlag})
		if (got.Result == nil) != (tc.want == nil) || got.Result != nil && *got.Result != *tc.want || got.DiagnosticLogIncomplete != tc.wantFlag {
			t.Fatalf("%s: result=%+v flag=%v", tc.state, got.Result, got.DiagnosticLogIncomplete)
		}
	}
	if got := urlClosingResult(diagnosisprep.Update{State: diagnosisprep.Cancelled}); got.Result != nil {
		t.Fatal("cancelled without counts gained a result")
	}
}

// 마감 갱신의 계정별 인증 결과는 completed·cancelled·failed에만 남는다(계약 §3.4). runtime이 아는 결과는 형식을
// 확인한 사본으로 마감 결과에 실린다.
func TestURLClosingAccountAuth(t *testing.T) {
	auth := map[string]string{"tester-a": "authenticated"}
	for state, kept := range map[diagnosisprep.State]bool{diagnosisprep.Completed: true, diagnosisprep.Cancelled: true, diagnosisprep.Failed: true, diagnosisprep.Unknown: false, diagnosisprep.Running: false} {
		got := urlClosingResult(diagnosisprep.Update{State: state, AccountAuth: auth})
		if (got.AccountAuth != nil) != kept {
			t.Errorf("%s: account_auth kept=%v", state, got.AccountAuth != nil)
		}
	}
	if got := launchAccountAuth(fakeAuthRuntime{auth: auth}); got["tester-a"] != "authenticated" {
		t.Fatal("runtime account auth not carried", got)
	} else if got["tester-a"] = "not_sent"; auth["tester-a"] != "authenticated" {
		t.Fatal("closure shares the runtime map")
	}
	if launchAccountAuth(fakeAuthRuntime{auth: map[string]string{"Tester": "authenticated"}}) != nil || launchAccountAuth(struct{}{}) != nil {
		t.Fatal("malformed or missing account auth carried")
	}
}

type fakeAuthRuntime struct{ auth map[string]string }

func (f fakeAuthRuntime) AccountAuth() map[string]string { return f.auth }
