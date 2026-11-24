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
