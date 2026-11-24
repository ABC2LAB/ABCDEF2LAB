//go:build linux

package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

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
