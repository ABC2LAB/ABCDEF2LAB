//go:build linux

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
				// 대상이 세션을 새로 발급(회전)한다. 모델에는 가지만 기록에서는 가려져야 한다(#522 이동찬 리뷰).
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "factory-rotated-session-55ee"})
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
			var dump []byte
			if response != nil {
				dump, _ = httputil.DumpResponse(response, true)
				response.Body.Close()
			}
			if tc.sent {
				if err != nil || cookieHits.Load() != 1 || !strings.Contains(string(dump), "factory-rotated-session-55ee") {
					t.Fatalf("인증된 시험 계정 실행이 모델 Cookie를 보내지 않거나 새 세션을 모델에 주지 않음: %+v %v", receipt, err)
				}
				if out := string(lease.Execution.RedactOutput(dump)); strings.Contains(out, "factory-rotated-session-55ee") || strings.Contains(out, "seeded-session-cookie-99aa") {
					t.Fatal("새로 발급된 세션이 기록용 출력에 남음")
				}
				return
			}
			if err == nil || receipt.Reason != e2proxy.Unsupported || cookieHits.Load() != 0 {
				t.Fatalf("인증된 계정 없는 실행이 모델 Cookie를 보냄: 수신=%d 결과=%+v 오류=%v", cookieHits.Load(), receipt, err)
			}
		})
	}
}

// 로그인을 한 뒤 실행 획득이 실패해도(OnAcquire 실패) 계정별 인증 결과는 실패 반환에 남는다(계약 §3.4, #532 리뷰).
func TestE2HTTPFactoryKeepsAccountAuthWhenAcquireFails(t *testing.T) {
	login := &authLoginTarget{token: "seeded-factory-session-33dd"}
	target := httptest.NewServer(login.handler(t))
	defer target.Close()
	cfg, _, policy := e2HostPolicyFixture(t)
	policy.Rules[0].Origin = target.URL
	policy.Rules[0].PathPrefix = "/read"
	auth := policy.Rules[0]
	auth.Ref, auth.PathScopeRef, auth.PathPrefix, auth.Methods, auth.Purpose, auth.Credentials = "auth-login", "auth-login-path", "/login", []string{"POST"}, e2proxy.PurposeAuthPrep, nil
	policy.Rules = append(policy.Rules, auth)
	pin := authCredentialPin(t, filepath.Dir(cfg.PolicyFile), "account-tester-a.json", "tester@owned.test", "seeded-pass-88ff")
	a := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin})
	policy.Auth = &a
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0o600) != nil {
		t.Fatal("policy write")
	}
	input := E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{},
		OnAcquire: func(context.Context, E2HTTPAcquired) error { return ErrE2HTTPHost }}
	options, err := LoadE2HTTPRuntimeOptions(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	run, cancel := context.WithCancel(t.Context())
	defer cancel()
	acquired, err := options.Acquire(run, E2HTTPRequest{TaskID: "17", TaskIDNumber: 17, ExplorationID: 1, IntentID: 1})
	if acquired.Release != nil {
		defer acquired.Release(context.Background())
	}
	if err == nil || login.posts.Load() != 1 || acquired.AccountAuth["tester-a"] != E2HTTPAuthAuthenticated {
		t.Fatalf("acquire failure lost account auth: err=%v posts=%d auth=%v", err, login.posts.Load(), acquired.AccountAuth)
	}
	if len(acquired.Sessions) != 0 {
		t.Fatal("failed acquire returned session headers")
	}
}

// 시험 계정 실행은 Worker의 refresh_session 경로를 받는다. 같은 인증 준비 경로로 다시 로그인하고, 실행 전체에서 계정당
// 상한(실패도 셈)을 지키며, 정책에 없는 라벨은 보내지 않는다. 인증된 계정이 없으면 경로가 없다.
func TestE2HTTPFactoryRefreshSession(t *testing.T) {
	var logins atomic.Int64
	login := (&authLoginTarget{token: "refresh-session-cookie-77bb"}).handler(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			logins.Add(1)
		}
		login.ServeHTTP(w, r)
	}))
	defer target.Close()
	cfg, _, policy := e2HostPolicyFixture(t)
	policy.Rules[0].Origin = target.URL
	policy.Rules = append(policy.Rules, E2HTTPFileRule{Ref: "auth-login", Origin: target.URL, ConnectIP: "127.0.0.1", PathPrefix: "/login", PathScopeRef: "auth-login", Methods: []string{"POST"}, Purpose: e2proxy.PurposeAuthPrep})
	policy.HeaderMode = e2proxy.HeaderTestAccountPassthrough
	policy.Limits.MaxAttempts, policy.Limits.MaxCalls, policy.MaxRequests = 64, 64, 64
	// tester-b는 비밀번호가 틀려 로그인에 실패한다. 실패한 다시 로그인도 상한에 센다.
	auth := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: authCredentialPin(t, cfg.AuthorityRoot, "tester-a.json", "tester@owned.test", "seeded-pass-88ff")},
		E2HTTPFileAuthAccount{Label: "tester-b", Credential: authCredentialPin(t, cfg.AuthorityRoot, "tester-b.json", "tester@owned.test", "wrong-pass-value")})
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
	acquire := func(intent int64) E2HTTPRunOptions {
		lease, err := options.Acquire(ctx, E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 27, IntentID: intent})
		if err != nil || lease.RefreshSession == nil {
			t.Fatal("refresh path missing for an authenticated test account run", err)
		}
		return lease
	}
	// intent는 하나씩 돈다. 다음 intent 전에 앞 실행 판을 닫는다.
	release := func(lease E2HTTPRunOptions) {
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
	}
	first := acquire(31)
	before := logins.Load()
	if header, result := first.RefreshSession(ctx, "tester-a"); result != E2HTTPAuthAuthenticated || header.Get("Cookie") != "session=refresh-session-cookie-77bb" || logins.Load() != before+1 {
		t.Fatalf("refresh: %s %v logins=%d", result, header, logins.Load()-before)
	}
	if out := string(first.Execution.RedactOutput([]byte("refresh-session-cookie-77bb"))); strings.Contains(out, "77bb") {
		t.Fatal("refreshed session not registered for output redaction")
	}
	if _, result := first.RefreshSession(ctx, "someone-else"); result != E2HTTPAuthNotSent || logins.Load() != before+1 {
		t.Fatal("unknown label reached the target")
	}
	defer release(first)
	// 상한은 실행 전체에서 계정당이고 실패도 센다. 처음 인증 준비는 세지 않는다.
	sent := logins.Load()
	for i := 1; i < maxSessionRefreshesPerAccount; i++ {
		if _, result := first.RefreshSession(ctx, "tester-a"); result != E2HTTPAuthAuthenticated {
			t.Fatalf("refresh %d: %s", i, result)
		}
	}
	if _, result := first.RefreshSession(ctx, "tester-a"); result != E2HTTPRefreshLimit || logins.Load() != sent+int64(maxSessionRefreshesPerAccount-1) {
		t.Fatalf("refresh limit: %s logins=%d", result, logins.Load()-sent)
	}
	failed := logins.Load()
	for i := 0; i < maxSessionRefreshesPerAccount; i++ {
		if _, result := first.RefreshSession(ctx, "tester-b"); result != E2HTTPAuthLoginFailed {
			t.Fatalf("failed refresh %d: %s", i, result)
		}
	}
	if _, result := first.RefreshSession(ctx, "tester-b"); result != E2HTTPRefreshLimit || logins.Load() != failed+int64(maxSessionRefreshesPerAccount) {
		t.Fatalf("failed logins not counted: %s logins=%d", result, logins.Load()-failed)
	}
}

// 인증된 계정이 없거나 시험 계정 실행이 아니면 Worker에게 다시 로그인 경로를 주지 않는다.
func TestE2HTTPRefreshForOnlyAuthenticatedTestAccountRuns(t *testing.T) {
	refresh := func(context.Context, string) (http.Header, string) { return nil, "" }
	with := &E2HTTPRunOptions{Sessions: E2HTTPSessions{"tester-a": http.Header{"Cookie": {"session=x"}}}, RefreshSession: refresh}
	if e2HTTPRefreshFor(e2proxy.HeaderTestAccountPassthrough, with) == nil {
		t.Fatal("authenticated test account run lost its refresh path")
	}
	if e2HTTPRefreshFor(e2proxy.HeaderPreserve, with) != nil || e2HTTPRefreshFor(e2proxy.HeaderTestAccountPassthrough, &E2HTTPRunOptions{RefreshSession: refresh}) != nil || e2HTTPRefreshFor(e2proxy.HeaderTestAccountPassthrough, nil) != nil {
		t.Fatal("refresh path given to a run without test account sessions")
	}
}

// 로그인할 때마다 이전 세션을 무효로 하는 대상에서, 다시 로그인한 세션이 실행 세션 저장소에 반영돼 다음 Worker가 새 세션으로
// 시작한다(#550 이동찬 리뷰). Worker 시작(읽기)과 다시 로그인(쓰기)이 겹쳐도 경쟁하지 않는다(-race). 다시 로그인한 세션 값도
// 실행 가림에 들어가 표시용 경로 패턴에서 가려진다(#550 윤재상 리뷰, #548).
func TestE2HTTPFactoryRefreshedSessionReachesNextWorker(t *testing.T) {
	var mu sync.Mutex
	current, issued := "", 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/login" {
			_ = r.ParseForm()
			if r.PostForm.Get("email") != "tester@owned.test" || r.PostForm.Get("password") != "seeded-pass-88ff" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			issued++
			current = fmt.Sprintf("rotatingsession%04dvalue", issued) // 새로 발급하면 이전 세션은 무효
			http.SetCookie(w, &http.Cookie{Name: "session", Value: current})
			io.WriteString(w, "ok")
			return
		}
		if c, err := r.Cookie("session"); err != nil || c.Value != current {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, "owned")
	}))
	defer target.Close()
	cfg, _, policy := e2HostPolicyFixture(t)
	policy.Rules[0].Origin = target.URL
	policy.Rules = append(policy.Rules, E2HTTPFileRule{Ref: "auth-login", Origin: target.URL, ConnectIP: "127.0.0.1", PathPrefix: "/login", PathScopeRef: "auth-login", Methods: []string{"POST"}, Purpose: e2proxy.PurposeAuthPrep})
	policy.HeaderMode = e2proxy.HeaderTestAccountPassthrough
	policy.Limits.MaxAttempts, policy.Limits.MaxCalls, policy.MaxRequests = 64, 64, 64
	auth := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: authCredentialPin(t, cfg.AuthorityRoot, "tester-a.json", "tester@owned.test", "seeded-pass-88ff")})
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
	if err != nil || lease.RefreshSession == nil {
		t.Fatal("실행 획득 실패", err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = lease.Runner.Close(cleanup)
		_ = lease.Execution.Stop(cleanup)
		_ = lease.Release(cleanup)
	}()
	// Worker 시작(읽기)과 다시 로그인(쓰기)을 겹친다.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = e2HTTPSessionsFor(e2proxy.HeaderTestAccountPassthrough, lease.CurrentSessions())
		}()
	}
	refreshed, result := lease.RefreshSession(ctx, "tester-a")
	wg.Wait()
	if result != E2HTTPAuthAuthenticated {
		t.Fatal("refresh", result)
	}
	next := e2HTTPWorkerSessions(e2proxy.HeaderTestAccountPassthrough, &lease)
	if got := next["tester-a"].Get("Cookie"); got != refreshed.Get("Cookie") || got != "session=rotatingsession0002value" {
		t.Fatalf("next worker got a stale session: %q", got)
	}
	// 다음 Worker가 받은 세션으로 보낸 요청이 대상에서 통한다. 그 세션 값을 경로에 넣으면 표시용 패턴에서 가려진다.
	call, err := lease.Execution.BeginCall(ctx, "next-worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/allowed/me", "/allowed/rotatingsession0002value/items"} {
		request, _ := http.NewRequest("GET", target.URL+path, nil)
		request.Header.Set("Cookie", next["tester-a"].Get("Cookie"))
		response, receipt, err := call.Do(ctx, request)
		if err != nil || response == nil || receipt.StatusCode != http.StatusOK {
			t.Fatalf("next worker request %s: status=%d err=%v", path, receipt.StatusCode, err)
		}
		response.Body.Close()
	}
	closeCtx, done := context.WithTimeout(context.Background(), time.Second)
	defer done()
	_, _ = call.Close(closeCtx)
	data, err := os.ReadFile(filepath.Join(policy.StateDir, E2AttemptPatternsFile))
	if err != nil || strings.Contains(string(data), "rotatingsession0002value") || !strings.Contains(string(data), `"/allowed/{id}/items"`) {
		t.Fatalf("refreshed session not masked in display patterns: %q %v", data, err)
	}
}

// 두 Worker가 같은 계정을 동시에 다시 로그인시켜도, 늦게 돌아온 옛 세션이 더 새 세션을 덮지 않는다(#550 이동찬 리뷰).
// 첫 다시 로그인은 대상이 세션을 발급한 뒤 응답을 붙잡고, 둘째 다시 로그인이 끝날 기회를 준 다음 풀어 준다.
func TestE2HTTPFactoryConcurrentRefreshKeepsNewestSession(t *testing.T) {
	var mu sync.Mutex
	current, issued := "", 0
	held, release := make(chan struct{}), make(chan struct{})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			_ = r.ParseForm()
			if r.PostForm.Get("email") != "tester@owned.test" || r.PostForm.Get("password") != "seeded-pass-88ff" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			mu.Lock()
			issued++
			mine, n := fmt.Sprintf("rotatingsession%04dvalue", issued), issued
			current = mine // 새로 발급하면 이전 세션은 무효
			mu.Unlock()
			if n == 2 { // 첫 다시 로그인(1은 인증 준비)
				close(held)
				<-release
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: mine})
			io.WriteString(w, "ok")
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if c, err := r.Cookie("session"); err != nil || c.Value != current {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, "owned")
	}))
	defer target.Close()
	cfg, _, policy := e2HostPolicyFixture(t)
	policy.Rules[0].Origin = target.URL
	policy.Rules = append(policy.Rules, E2HTTPFileRule{Ref: "auth-login", Origin: target.URL, ConnectIP: "127.0.0.1", PathPrefix: "/login", PathScopeRef: "auth-login", Methods: []string{"POST"}, Purpose: e2proxy.PurposeAuthPrep})
	policy.HeaderMode = e2proxy.HeaderTestAccountPassthrough
	policy.Limits.MaxAttempts, policy.Limits.MaxCalls, policy.MaxRequests = 64, 64, 64
	policy.Limits.MaxConcurrent = 2
	auth := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: authCredentialPin(t, cfg.AuthorityRoot, "tester-a.json", "tester@owned.test", "seeded-pass-88ff")})
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
	if err != nil || lease.RefreshSession == nil {
		t.Fatal("실행 획득 실패", err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = lease.Runner.Close(cleanup)
		_ = lease.Execution.Stop(cleanup)
		_ = lease.Release(cleanup)
	}()
	results := make(chan string, 2)
	go func() { _, result := lease.RefreshSession(ctx, "tester-a"); results <- result }()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("first refresh never reached the target")
	}
	second := make(chan string, 1)
	go func() { _, result := lease.RefreshSession(ctx, "tester-a"); second <- result }()
	// 줄을 세우지 않으면 둘째가 여기서 끝난다. 줄을 세우면 첫째를 풀 때까지 기다린다.
	select {
	case result := <-second:
		results <- result
	case <-time.After(300 * time.Millisecond): // 요청 시간 상한(1초) 안에서 첫째를 푼다
	}
	close(release)
	if len(results) < 2 {
		results <- <-second
	}
	for range 2 {
		if result := <-results; result != E2HTTPAuthAuthenticated {
			t.Fatal("refresh", result)
		}
	}
	next := e2HTTPWorkerSessions(e2proxy.HeaderTestAccountPassthrough, &lease)
	mu.Lock()
	want := "session=" + current
	mu.Unlock()
	if got := next["tester-a"].Get("Cookie"); got != want {
		t.Fatalf("next worker got a stale session: %q want %q", got, want)
	}
	call, err := lease.Execution.BeginCall(ctx, "next-worker")
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest("GET", target.URL+"/allowed/me", nil)
	request.Header.Set("Cookie", next["tester-a"].Get("Cookie"))
	response, receipt, err := call.Do(ctx, request)
	if err != nil || response == nil || receipt.StatusCode != http.StatusOK {
		t.Fatalf("next worker request: status=%d err=%v", receipt.StatusCode, err)
	}
	response.Body.Close()
	closeCtx, done := context.WithTimeout(context.Background(), time.Second)
	defer done()
	_, _ = call.Close(closeCtx)
}

// 같은 계정 다시 로그인을 기다리는 동안 문맥이 끝나면 보내지 않는다. 다른 계정은 기다리지 않는다.
func TestE2HTTPSessionStoreRefreshQueuesPerAccount(t *testing.T) {
	store := newE2HTTPSessionStore(E2HTTPSessions{"a": http.Header{"Cookie": {"s=1"}}})
	inside, leave := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		store.refresh(t.Context(), "a", func() (http.Header, string) {
			close(inside)
			<-leave
			return http.Header{"Cookie": {"s=2"}}, E2HTTPAuthAuthenticated
		})
	}()
	<-inside
	waitCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	sent := false
	if _, result := store.refresh(waitCtx, "a", func() (http.Header, string) { sent = true; return nil, E2HTTPAuthAuthenticated }); result != E2HTTPAuthNotSent || sent {
		t.Fatal("waiting refresh must not send after its context ends", result, sent)
	}
	if _, result := store.refresh(t.Context(), "b", func() (http.Header, string) { return http.Header{"Cookie": {"t=1"}}, E2HTTPAuthAuthenticated }); result != E2HTTPAuthAuthenticated {
		t.Fatal("other account must not wait", result)
	}
	close(leave)
	<-done
	if got := store.snapshot()["a"].Get("Cookie"); got != "s=2" {
		t.Fatal("session not replaced", got)
	}
	if _, result := store.refresh(nil, "a", func() (http.Header, string) { return nil, E2HTTPAuthAuthenticated }); result != E2HTTPAuthNotSent { //nolint:staticcheck // nil 문맥 방어
		t.Fatal("nil context", result)
	}
}
