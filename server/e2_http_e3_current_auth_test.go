package server

import (
	"context"
	"net/http"
	"testing"
)

func TestE3AccountAuthUsesBoundSessionAfterRefresh(t *testing.T) {
	run := "synthetic-run"
	store := newE2HTTPSessionStore(E2HTTPSessions{"tester-a": http.Header{"Authorization": {"Bearer synthetic-token-a"}}}, run, nil)
	runtime := &E2HTTPRuntime{
		options:     E2HTTPRuntimeOptions{Expected: E2HTTPExpectedBinding{RunRef: run}},
		accountAuth: map[string]string{"tester-a": E2HTTPAuthAuthenticated, "tester-b": E2HTTPAuthSessionNotFound},
		task:        &e2HTTPRuntimeTask{lease: &E2HTTPRunOptions{sessionStore: store}, validLease: true},
	}
	if runtime.AccountAuthForE3()["tester-b"] == E2HTTPAuthAuthenticated {
		t.Fatal("missing session accepted for E3")
	}
	_, result := store.refresh(context.Background(), "tester-b", func() (http.Header, string) {
		return http.Header{"Authorization": {"Bearer synthetic-token-b"}}, E2HTTPAuthAuthenticated
	})
	if result != E2HTTPAuthAuthenticated || runtime.AccountAuthForE3()["tester-b"] != E2HTTPAuthAuthenticated ||
		runtime.AccountAuth()["tester-b"] != E2HTTPAuthSessionNotFound {
		t.Fatal("current E3 binding and historical preparation result conflated")
	}
	runtime.options.Expected.RunRef = "foreign-run"
	if runtime.AccountAuthForE3()["tester-b"] == E2HTTPAuthAuthenticated {
		t.Fatal("foreign run session accepted for E3")
	}
	runtime.options.Expected.RunRef = run
	runtime.task.mu.Lock()
	runtime.task.closed = true
	runtime.task.mu.Unlock()
	if runtime.AccountAuthForE3()["tester-a"] == E2HTTPAuthAuthenticated || runtime.AccountAuthForE3()["tester-b"] == E2HTTPAuthAuthenticated {
		t.Fatal("closed execution accepted for E3")
	}
	runtime.task.mu.Lock()
	runtime.task.closed = false
	runtime.task.lease.sessionStore = nil
	runtime.task.mu.Unlock()
	if runtime.AccountAuthForE3()["tester-a"] == E2HTTPAuthAuthenticated {
		t.Fatal("historical success accepted without a current session store")
	}
}

// 실행 중 상태 응답용 계정 인증 상태는 확정 단계와 같은 기준이다(인증 준비 결과 + 지금 회차 세션 결속). 지금 세션을 볼 수
// 없으면 모름(ok 거짓)이라 호출자가 앞 값을 유지하고, session_not_found로 바꾸지 않는다(2026-11-25).
func TestAccountAuthNowFollowsCurrentSessionOrUnknown(t *testing.T) {
	run := "synthetic-run"
	if auth, ok := (&E2HTTPRuntime{}).AccountAuthNow(); ok || auth != nil {
		t.Fatal("인증 준비 전에 값을 줌")
	}
	store := newE2HTTPSessionStore(E2HTTPSessions{"tester-a": http.Header{"Authorization": {"Bearer synthetic-token-a"}}}, run, nil)
	runtime := &E2HTTPRuntime{
		options:     E2HTTPRuntimeOptions{Expected: E2HTTPExpectedBinding{RunRef: run}},
		accountAuth: map[string]string{"tester-a": E2HTTPAuthAuthenticated, "tester-b": E2HTTPAuthSessionNotFound},
		task:        &e2HTTPRuntimeTask{lease: &E2HTTPRunOptions{sessionStore: store}, validLease: true},
	}
	auth, ok := runtime.AccountAuthNow()
	if !ok || auth["tester-a"] != E2HTTPAuthAuthenticated || auth["tester-b"] != E2HTTPAuthSessionNotFound {
		t.Fatalf("인증 준비 직후 %v %t", auth, ok)
	}
	// 세션 다시 받기로 b가 살아나면 값이 바뀐다. 최초 결과(AccountAuth)는 그대로다.
	_, _ = store.refresh(context.Background(), "tester-b", func() (http.Header, string) {
		return http.Header{"Authorization": {"Bearer synthetic-token-b"}}, E2HTTPAuthAuthenticated
	})
	if auth, ok := runtime.AccountAuthNow(); !ok || auth["tester-b"] != E2HTTPAuthAuthenticated || runtime.AccountAuth()["tester-b"] != E2HTTPAuthSessionNotFound {
		t.Fatalf("세션 다시 받기 뒤 %v %t", auth, ok)
	}
	// 지금 세션을 볼 수 없으면(실행이 닫힘) 모름이다. 확정 단계용은 같은 순간 쓸 수 없음으로 본다.
	runtime.task.mu.Lock()
	runtime.task.closed = true
	runtime.task.mu.Unlock()
	if auth, ok := runtime.AccountAuthNow(); ok || auth != nil {
		t.Fatalf("닫힌 실행의 지금 값을 줌 %v", auth)
	}
	if runtime.AccountAuthForE3()["tester-a"] != E2HTTPAuthSessionNotFound {
		t.Fatal("확정 단계용 판정이 바뀜")
	}
}
