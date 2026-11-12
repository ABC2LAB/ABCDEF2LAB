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
