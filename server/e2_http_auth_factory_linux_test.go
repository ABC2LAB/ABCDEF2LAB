//go:build linux

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	// 시험 계정 모드는 인증 준비 칸 없이 적재하지 않는다(#522 재상 지적 2). 같은 정책을 preserve로는 적재한다.
	policy.Auth, policy.HeaderMode = nil, e2proxy.HeaderTestAccountPassthrough
	write()
	if _, err := LoadE2HTTPRuntimeOptions(t.Context(), input); err == nil {
		t.Fatal("test account mode without auth loaded")
	}
	policy.HeaderMode = e2proxy.HeaderPreserve
	write()
	if _, err := LoadE2HTTPRuntimeOptions(t.Context(), input); err != nil {
		t.Fatal("preserve policy without auth rejected:", err)
	}
}

// 실제 factory에서 인증 준비 결과가 E② 헤더 모드를 정한다. 인증된 시험 계정이 하나도 없으면 첫 모델 요청 전에 preserve로
// 묶여 모델이 쓴 Cookie를 대상에 보내지 않는다. 하나라도 인증되면 시험 계정 모드 그대로 보낸다(C14 결정 5·6, #522 재상 지적 1).
func TestE2HTTPFactoryTestAccountModeFollowsAuthResult(t *testing.T) {
	for _, tc := range []struct {
		name     string
		password string
		want     string
		sent     bool
	}{
		{"no authenticated account", "wrong-pass-value", E2HTTPAuthLoginFailed, false},
		{"authenticated account", "seeded-pass-88ff", E2HTTPAuthAuthenticated, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cookieHits, logins atomic.Int64
			login := (&authLoginTarget{token: "seeded-session-cookie-99aa"}).handler(t)
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/login" {
					logins.Add(1)
					login.ServeHTTP(w, r)
					return
				}
				if r.Header.Get("Cookie") != "" {
					cookieHits.Add(1)
				}
				io.WriteString(w, "owned")
			}))
			defer target.Close()
			cfg, _, policy := e2HostPolicyFixture(t)
			policy.Rules[0].Origin = target.URL
			policy.Rules = append(policy.Rules, E2HTTPFileRule{Ref: "auth-login", Origin: target.URL, ConnectIP: "127.0.0.1", PathPrefix: "/login", PathScopeRef: "auth-login", Methods: []string{"POST"}, Purpose: e2proxy.PurposeAuthPrep})
			policy.HeaderMode = e2proxy.HeaderTestAccountPassthrough
			auth := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: authCredentialPin(t, cfg.AuthorityRoot, "tester-a.json", "tester@owned.test", tc.password)})
			policy.Auth = &auth
			raw, err := json.Marshal(policy)
			if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0o600) != nil {
				t.Fatal("합성 정책 준비 실패")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			options, err := LoadE2HTTPRuntimeOptions(ctx, E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: ctx}, Supervisor: &e2HTTPSupervisorFixture{}})
			if err != nil {
				t.Fatal("호스트 준비 실패", err)
			}
			lease, err := options.Acquire(ctx, E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 27, IntentID: 31})
			if err != nil {
				t.Fatal("실제 실행 획득 실패", err)
			}
			t.Cleanup(func() {
				cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
				defer done()
				if lease.Runner != nil {
					_ = lease.Runner.Close(cleanup)
				}
				if lease.Execution != nil {
					_ = lease.Execution.Stop(cleanup)
				}
				if lease.Release != nil {
					_ = lease.Release(cleanup)
				}
			})
			if lease.AccountAuth["tester-a"] != tc.want || logins.Load() != 1 || (len(lease.Sessions) == 1) != tc.sent {
				t.Fatalf("인증 준비 결과=%v 로그인=%d 세션=%d", lease.AccountAuth, logins.Load(), len(lease.Sessions))
			}
			call, err := lease.Execution.BeginCall(ctx, "model-cookie")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanup, done := context.WithTimeout(context.Background(), time.Second)
				defer done()
				_, _ = call.Close(cleanup)
			})
			request, _ := http.NewRequest("GET", target.URL+"/me", nil)
			request.Header.Set("Cookie", "session=model-written-cookie")
			response, receipt, err := call.Do(ctx, request)
			if response != nil {
				response.Body.Close()
			}
			if tc.sent {
				if err != nil || cookieHits.Load() != 1 {
					t.Fatalf("인증된 시험 계정 실행이 모델 Cookie를 보내지 않음: %+v %v", receipt, err)
				}
				return
			}
			if err == nil || receipt.Reason != e2proxy.Unsupported || cookieHits.Load() != 0 {
				t.Fatalf("인증된 계정 없는 실행이 모델 Cookie를 보냄: 수신=%d 결과=%+v 오류=%v", cookieHits.Load(), receipt, err)
			}
		})
	}
}
