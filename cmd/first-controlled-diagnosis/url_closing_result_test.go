package main

import (
	"context"
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

// 결속 계정 변경으로 멈춘 실행은 cancelled + account_binding_changed로 저장되고, 그 실행의 계정별 인증 결과가 같이
// 남는다(#532 재상 지적). 마감을 확인하지 못하면 결과 불명이 우선하고 계정 결과는 빠진다.
func TestURLAccountBindingStopCarriesAccountAuth(t *testing.T) {
	stopped, stop := context.WithCancelCause(context.Background())
	stop(errAccountBindingChanged)
	auth := map[string]string{"tester-a": "authenticated", "tester-b": "login_failed"}
	closed := &diagnosis.Closure{DrainComplete: true, ResultReady: true, AccountAuth: auth}
	known := diagnosisprep.ModelUsage{UsageKnown: true}
	var u diagnosisprep.Update
	urlStoppedUpdate(stopped, closed, known, false)(&u)
	u = urlClosingResult(u)
	if u.State != diagnosisprep.Cancelled || u.ReasonCode != "account_binding_changed" || u.AccountAuth["tester-a"] != "authenticated" || u.AccountAuth["tester-b"] != "login_failed" {
		t.Fatalf("binding stop state=%s reason=%q auth=%v", u.State, u.ReasonCode, u.AccountAuth)
	}
	for name, tc := range map[string]struct {
		closure *diagnosis.Closure
		usage   diagnosisprep.ModelUsage
		reason  string
	}{
		"closure-unconfirmed": {&diagnosis.Closure{DrainComplete: false, ResultReady: true, AccountAuth: auth}, known, "execution_closure_unconfirmed"},
		"usage-unknown":       {closed, diagnosisprep.ModelUsage{}, "model_usage_unknown"},
	} {
		var v diagnosisprep.Update
		urlStoppedUpdate(stopped, tc.closure, tc.usage, false)(&v)
		v = urlClosingResult(v)
		if v.State != diagnosisprep.Unknown || v.ReasonCode != tc.reason || v.AccountAuth != nil {
			t.Fatalf("%s: state=%s reason=%q auth=%v", name, v.State, v.ReasonCode, v.AccountAuth)
		}
	}
}
