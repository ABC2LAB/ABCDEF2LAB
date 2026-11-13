package diagnosis

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// 계정별 인증 결과 맵의 형식(계약 §3.4): 1…16개, 등록 라벨 규칙, 결과 값 5개뿐.
func TestValidAccountAuth(t *testing.T) {
	many := map[string]string{}
	for i := range MaxAccountAuth + 1 {
		many[fmt.Sprintf("tester-%02d", i)] = "authenticated"
	}
	for name, m := range map[string]map[string]string{
		"empty":       {},
		"too many":    many,
		"upper label": {"Tester": "authenticated"},
		"proto label": {"__proto__": "authenticated"},
		"email label": {"a@b.test": "authenticated"},
		"bad result":  {"tester-a": "logged_in"},
		"value":       {"tester-a": "session=abc"},
	} {
		if ValidAccountAuth(m) || CloneAccountAuth(m) != nil {
			t.Errorf("%s accepted", name)
		}
	}
	good := map[string]string{"tester-a": "authenticated", "tester_b": "login_failed", "c": "session_not_found", "d1": "result_unknown", "e": "not_sent"}
	clone := CloneAccountAuth(good)
	if !ValidAccountAuth(good) || len(clone) != len(good) {
		t.Fatal("valid account auth rejected")
	}
	clone["tester-a"] = "not_sent"
	if good["tester-a"] != "authenticated" {
		t.Fatal("clone shares the source map")
	}
}

// 상태 응답은 completed·cancelled·failed에만 계정별 결과를 싣는다.
func TestSnapshotAccountAuthStates(t *testing.T) {
	auth := map[string]string{"tester-a": "authenticated"}
	for _, tc := range []struct {
		snapshot Snapshot
		ok       bool
	}{
		{Snapshot{State: Completed, Started: true, DrainComplete: true, ResultReady: true, Result: completeResult()}, true},
		{Snapshot{State: Cancelled, Started: true, DrainComplete: true, ResultReady: true}, true},
		{Snapshot{State: Failed, Started: true, DrainComplete: true, ResultReady: true}, true},
		{Snapshot{State: Unknown, Started: true}, false},
		{Snapshot{State: Running, Started: true}, false},
	} {
		s := tc.snapshot
		s.TaskID, s.PreparationRef, s.AccountAuth = "23", "operator-prepared", auth
		if validSnapshot(s) != tc.ok {
			t.Errorf("%s: account_auth valid=%v", s.State, !tc.ok)
		}
		s.AccountAuth = map[string]string{"tester-a": "logged_in"}
		if validSnapshot(s) {
			t.Errorf("%s: bad account_auth accepted", s.State)
		}
	}
}

// 실행 서비스는 마감에서 받은 계정별 결과를 completed·failed·cancelled 상태에 싣고, 마감을 확인하지 못한
// 결과 불명에는 싣지 않는다. 형식이 틀리면 싣지 않고 상태도 바꾸지 않는다.
func TestServiceCarriesAccountAuth(t *testing.T) {
	auth := map[string]string{"tester-a": "authenticated", "tester-b": "login_failed"}
	for _, tc := range []struct {
		name     string
		runErr   error
		closeErr error
		auth     map[string]string
		state    State
		want     bool
	}{
		{"completed", nil, nil, auth, Completed, true},
		{"failed", errors.New("private run failure"), nil, auth, Failed, true},
		{"unknown closure", nil, errors.New("private close failure"), auth, Unknown, false},
		{"malformed", nil, nil, map[string]string{"Tester": "authenticated"}, Completed, false},
		{"no accounts", nil, nil, nil, Completed, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok := tc.closeErr == nil
			runner := &testRunner{finish: make(chan struct{}), runErr: tc.runErr, closeErr: tc.closeErr,
				closure: &Closure{DrainComplete: ok, ResultReady: ok, Result: completeResult(), AccountAuth: tc.auth}}
			s := newTestService(t, runner)
			if r, e := s.Call(context.Background(), request(Start)); e != nil || r.Error != "" {
				t.Fatal(r, e)
			}
			close(runner.finish)
			awaitState(t, s, tc.state)
			response, _ := s.Call(context.Background(), request(Status))
			got := response.Snapshot.AccountAuth
			if tc.want != (got != nil) || tc.want && got["tester-b"] != "login_failed" {
				t.Fatalf("account_auth=%v", got)
			}
		})
	}
}
