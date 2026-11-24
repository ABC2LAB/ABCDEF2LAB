//go:build linux

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/internal/diagnosticlog"
)

func TestE2HTTPAuthPrepLogsFixedPerAccountFailureReason(t *testing.T) {
	for _, tc := range []struct {
		name, token, reason, password, authState, logStatus string
		noToken                                             bool
	}{
		{name: "extract_missing", noToken: true, reason: "auth_session_unavailable", authState: E2HTTPAuthSessionNotFound, logStatus: "failed"},
		{name: "redact_failed", token: "abc", reason: "auth_session_redact_failed", authState: E2HTTPAuthSessionNotFound, logStatus: "failed"},
		{name: "login_rejected", password: "synthetic-invalid-pass", reason: "auth_login_rejected", authState: E2HTTPAuthLoginFailed, logStatus: "failed"},
		{name: "authenticated", token: "synthetic-session-value-123", reason: "", authState: E2HTTPAuthAuthenticated, logStatus: "succeeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := &authLoginTarget{token: tc.token, noToken: tc.noToken}
			srv := httptest.NewServer(target.handler(t))
			defer srv.Close()
			dir := authTestDir(t)
			password := "seeded-pass-88ff"
			if tc.password != "" {
				password = tc.password
			}
			pin := authCredentialPin(t, dir, "tester-a.json", "tester@owned.test", password)
			logger, err := diagnosticlog.Create(t.TempDir(), "run", 1, "synthetic-run")
			if err != nil {
				t.Fatal(err)
			}
			ctx := diagnosticlog.With(t.Context(), logger)
			result, err := e2HTTPAuthPrep(ctx, authTestExecution(t, srv.URL, &authEventCounter{}), dir, srv.URL,
				cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin}))
			if err != nil || result.AccountAuth["tester-a"] != tc.authState || logger.Close() != nil {
				t.Fatal("authentication observation failed", err)
			}
			raw, err := os.ReadFile(logger.Path())
			if err != nil || strings.Contains(string(raw), password) || len(tc.token) >= 8 && strings.Contains(string(raw), tc.token) {
				t.Fatal("authentication observation leaked a protected value", err)
			}
			found := 0
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				var event diagnosticlog.Event
				if json.Unmarshal([]byte(line), &event) != nil {
					t.Fatal("invalid diagnostic event")
				}
				if event.Event == "account_auth_checked" {
					found++
					if event.AccountLabel != "tester-a" || event.ReasonCode != tc.reason || event.Status != tc.logStatus {
						t.Fatal("per-account fixed reason missing")
					}
				}
			}
			if found != 1 {
				t.Fatalf("account auth events=%d", found)
			}
		})
	}
}

func TestE2HTTPAuthMissingTokenStaysUnauthenticatedAcrossRefreshes(t *testing.T) {
	const label = "tester-b"
	const run = "synthetic-run"
	target := &authLoginTarget{noToken: true}
	srv := httptest.NewServer(target.handler(t))
	defer srv.Close()
	dir := authTestDir(t)
	auth := cookieAuth(E2HTTPFileAuthAccount{
		Label: label, Credential: authCredentialPin(t, dir, "tester-b.json", "tester@owned.test", "seeded-pass-88ff"),
	})
	logger, err := diagnosticlog.Create(t.TempDir(), "run", 1, run)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	ctx := diagnosticlog.With(t.Context(), logger)
	execution := authTestExecution(t, srv.URL, &authEventCounter{})
	initial, err := e2HTTPAuthPrep(ctx, execution, dir, srv.URL, auth)
	if err != nil || initial.AccountAuth[label] != E2HTTPAuthSessionNotFound {
		t.Fatalf("initial token-free response: %v", err)
	}

	store := newE2HTTPSessionStore(initial.Sessions, run, nil)
	runtime := &E2HTTPRuntime{
		options:     E2HTTPRuntimeOptions{Expected: E2HTTPExpectedBinding{RunRef: run}},
		accountAuth: initial.AccountAuth,
		task:        &e2HTTPRuntimeTask{lease: &E2HTTPRunOptions{sessionStore: store}, validLease: true},
	}
	clock := time.Now()
	counter := &e2HTTPRefreshCounter{now: func() time.Time { return clock }}
	for range maxSessionRefreshFailuresPerAccount {
		header, state := store.refresh(ctx, label, func() (http.Header, string) {
			return e2HTTPRefreshSession(ctx, execution, dir, srv.URL, auth, counter, nil, label, false)
		})
		if len(header) != 0 || state != E2HTTPAuthSessionNotFound || runtime.AccountAuthForE3()[label] != E2HTTPAuthSessionNotFound {
			t.Fatal("token-free reply became a usable session")
		}
		clock = clock.Add(minSessionRefreshInterval)
	}
	if _, state := e2HTTPRefreshSession(ctx, execution, dir, srv.URL, auth, counter, nil, label, false); state != E2HTTPRefreshLimit {
		t.Fatal("refresh attempt limit was not enforced")
	}
	if err := execution.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, state := e2HTTPRefreshSession(ctx, execution, dir, srv.URL, auth, &e2HTTPRefreshCounter{}, nil, label, false); state != E2HTTPAuthNotSent {
		t.Fatal("closed execution tried to refresh")
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logger.Path())
	if err != nil {
		t.Fatal(err)
	}
	failed, notSent := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var event diagnosticlog.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Event != "account_auth_checked" {
			continue
		}
		if event.AccountLabel != label {
			t.Fatal("unexpected account label")
		}
		switch {
		case event.Status == "failed" && event.ReasonCode == "auth_session_unavailable":
			failed++
		case event.Status == "not_sent" && event.ReasonCode == "auth_not_sent":
			notSent++
		default:
			t.Fatal("unexpected authentication status or reason")
		}
	}
	wantFailures := 1 + maxSessionRefreshFailuresPerAccount
	if failed != wantFailures || notSent != 2 || target.posts.Load() != int64(wantFailures) {
		t.Fatalf("failure logs=%d not sent logs=%d requests=%d", failed, notSent, target.posts.Load())
	}
}
