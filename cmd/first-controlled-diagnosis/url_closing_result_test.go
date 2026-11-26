package main

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/diagnosis"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/server"
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
	// 2026-11-25부터 인증 준비 뒤의 실행 중(running·stopping)에도 싣는다. 준비 중·결과 불명에는 싣지 않는다.
	for state, kept := range map[diagnosisprep.State]bool{diagnosisprep.Completed: true, diagnosisprep.Cancelled: true, diagnosisprep.Failed: true, diagnosisprep.Unknown: false,
		diagnosisprep.Running: true, diagnosisprep.Stopping: true, diagnosisprep.Preparing: false} {
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

type fakeAuthNowRuntime struct {
	auth map[string]string
	ok   bool
}

func (f *fakeAuthNowRuntime) AccountAuthNow() (map[string]string, bool) { return f.auth, f.ok }

// 실행 중에는 지금 세션 기준 값이 바뀐 때만 싣고, 모르는 순간에는 앞 값을 둔다. 마감은 마지막으로 실은 값을 잇는다(2026-11-25).
func TestAccountAuthProgressPublishesChangesAndCarriesLast(t *testing.T) {
	var progress accountAuthProgress
	runtime := &fakeAuthNowRuntime{}
	if _, changed := progress.next(runtime); changed {
		t.Fatal("인증 준비 전에 실음")
	}
	if _, changed := progress.next(struct{}{}); changed {
		t.Fatal("지금 값을 모르는 런타임인데 실음")
	}
	runtime.auth, runtime.ok = map[string]string{"tester-a": "authenticated", "tester-b": "session_not_found"}, true
	first, changed := progress.next(runtime)
	if !changed || first["tester-b"] != "session_not_found" {
		t.Fatalf("첫 값 %v %t", first, changed)
	}
	progress.published = first
	if _, changed := progress.next(runtime); changed {
		t.Fatal("같은 값을 다시 실음")
	}
	runtime.ok = false
	if _, changed := progress.next(runtime); changed {
		t.Fatal("모르는 순간에 값을 바꿈")
	}
	runtime.auth, runtime.ok = map[string]string{"tester-a": "authenticated", "tester-b": "authenticated"}, true
	second, changed := progress.next(runtime)
	if !changed || second["tester-b"] != "authenticated" {
		t.Fatalf("세션 다시 받기 뒤 %v %t", second, changed)
	}
	progress.published = second
	runtime.auth = map[string]string{"Tester@x": "authenticated"}
	if _, changed := progress.next(runtime); changed {
		t.Fatal("형식이 틀린 값을 실음")
	}
	closing := map[string]string{"tester-a": "authenticated", "tester-b": "session_not_found"}
	if got := progress.final(closing); got["tester-b"] != "authenticated" {
		t.Fatalf("마감이 최초 결과로 되돌아감 %v", got)
	}
	if got := (&accountAuthProgress{}).final(closing); got["tester-b"] != "session_not_found" {
		t.Fatalf("실행 중에 싣지 못했으면 마감 결과 %v", got)
	}
}

// 진단 실행이 스스로 끝난 상태의 사유(B1): failed·unknown은 실행이 분류한 사유를 그대로 싣고, 중지는 원인 있는 중지만 이름을
// 붙이며(실행 쪽 사유는 쓰지 않음), 완료에는 사유가 없다. 사용량을 모르면 결과 불명 사유가 이긴다.
func TestURLSnapshotUpdateCarriesRunReason(t *testing.T) {
	known := diagnosisprep.ModelUsage{UsageKnown: true}
	plain := context.Background()
	target, stopTarget := context.WithCancelCause(context.Background())
	stopTarget(server.ErrFirstDiagnosisTargetEnded)
	recheck, stopRecheck := context.WithCancelCause(context.Background())
	stopRecheck(errAuthorityRecheckFailed)
	timed, stopTimed := context.WithTimeout(context.Background(), 0)
	defer stopTimed()
	base := diagnosis.Snapshot{TaskID: "23", PreparationRef: "prep-1", Started: true, DrainComplete: true, ResultReady: true}
	for name, tc := range map[string]struct {
		ctx    context.Context
		edit   func(*diagnosis.Snapshot)
		usage  diagnosisprep.ModelUsage
		state  diagnosisprep.State
		reason string
	}{
		"failed exploration": {plain, func(s *diagnosis.Snapshot) { s.State, s.ReasonCode = diagnosis.Failed, "planning_exploration_failed" }, known, diagnosisprep.Failed, "planning_exploration_failed"},
		"failed no reason":   {plain, func(s *diagnosis.Snapshot) { s.State = diagnosis.Failed }, known, diagnosisprep.Failed, ""},
		"unknown closure": {plain, func(s *diagnosis.Snapshot) {
			s.State, s.DrainComplete, s.ReasonCode = diagnosis.Unknown, false, diagnosis.ReasonClosureUnconfirmed
		}, known, diagnosisprep.Unknown, diagnosis.ReasonClosureUnconfirmed},
		"cancelled target":     {target, func(s *diagnosis.Snapshot) { s.State = diagnosis.Cancelled }, known, diagnosisprep.Cancelled, "target_ended"},
		"cancelled plain":      {plain, func(s *diagnosis.Snapshot) { s.State = diagnosis.Cancelled }, known, diagnosisprep.Cancelled, ""},
		"cancelled recheck":    {recheck, func(s *diagnosis.Snapshot) { s.State = diagnosis.Cancelled }, known, diagnosisprep.Cancelled, "authority_recheck_failed"},
		"cancelled time limit": {timed, func(s *diagnosis.Snapshot) { s.State = diagnosis.Cancelled }, known, diagnosisprep.Cancelled, "run_time_limit_reached"},
		"completed": {plain, func(s *diagnosis.Snapshot) {
			s.State, s.Result = diagnosis.Completed, &diagnosis.RunResult{Outcome: diagnosis.OutcomeComplete, IntentOutcomes: diagnosis.IntentOutcomes{Done: 1}}
		}, known, diagnosisprep.Completed, ""},
		"usage unknown wins": {plain, func(s *diagnosis.Snapshot) { s.State, s.ReasonCode = diagnosis.Failed, "planning_exploration_failed" }, diagnosisprep.ModelUsage{}, diagnosisprep.Unknown, "model_usage_unknown"},
	} {
		s := base
		tc.edit(&s)
		var u diagnosisprep.Update
		urlSnapshotUpdate(tc.ctx, s, tc.usage, nil)(&u)
		if u.State != tc.state || u.ReasonCode != tc.reason || u.Phase != diagnosisprep.Report {
			t.Fatalf("%s: state=%s reason=%q", name, u.State, u.ReasonCode)
		}
	}
}
