//go:build linux

package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
)

// 정책에 인증 준비 칸이 있으면 factory가 실행을 잡을 때 계정마다 로그인 요청을 E② 인증 준비 규칙으로 보내고, 결과와
// 세션을 실행 옵션으로 돌려준다. 칸이 정책 규칙과 맞지 않으면 정책 적재를 거부한다.
func TestE2HTTPFactoryRunsAuthPrepOnAcquire(t *testing.T) {
	login := &authLoginTarget{token: "seeded-factory-session-22cc"}
	target := httptest.NewServer(login.handler(t))
	defer target.Close()
	cfg, _, policy := e2HostPolicyFixture(t)
	policy.Rules[0].Origin = target.URL
	policy.Rules[0].PathPrefix = "/read"
	auth := policy.Rules[0]
	auth.Ref, auth.PathScopeRef, auth.PathPrefix, auth.Methods, auth.Purpose, auth.Credentials = "auth-login", "auth-login-path", "/login", []string{"POST"}, e2proxy.PurposeAuthPrep, nil
	policy.Rules = append(policy.Rules, auth)
	dir := filepath.Dir(cfg.PolicyFile)
	pin := authCredentialPin(t, dir, "account-tester-a.json", "tester@owned.test", "seeded-pass-88ff")
	a := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin})
	policy.Auth = &a
	write := func() {
		raw, err := json.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(cfg.PolicyFile, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	input := E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}}
	options, err := LoadE2HTTPRuntimeOptions(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	run, cancel := context.WithCancel(t.Context())
	defer cancel()
	acquired, err := options.Acquire(run, E2HTTPRequest{TaskID: "17", TaskIDNumber: 17, ExplorationID: 1, IntentID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer acquired.Release(context.Background())
	if acquired.AccountAuth["tester-a"] != E2HTTPAuthAuthenticated || acquired.Sessions["tester-a"].Get("Cookie") != "session="+login.token || login.posts.Load() != 1 {
		t.Fatalf("auth prep on acquire: %v posts=%d", acquired.AccountAuth, login.posts.Load())
	}
	if out := string(acquired.Execution.RedactOutput([]byte(login.token))); strings.Contains(out, login.token) {
		t.Fatal("session not registered for output redaction")
	}
	// 인증 칸이 규칙과 맞지 않으면 적재를 거부한다.
	policy.Auth.Login.Path = "/other"
	write()
	if _, err := LoadE2HTTPRuntimeOptions(t.Context(), input); err == nil {
		t.Fatal("auth section that does not match the auth rule accepted")
	}
}
