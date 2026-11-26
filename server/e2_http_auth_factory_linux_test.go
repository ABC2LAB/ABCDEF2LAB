//go:build linux

package server

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/Autumn-27/artex/loginattempt"
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
	// 인증 준비가 연 E② 호출은 서버 호출로 기록돼 마감 대조가 Worker 결과 없이 센다(C14).
	program := acquired.programCalls.snapshot()
	calls := acquired.Execution.Calls()
	if len(program) != 1 || len(calls) != 1 || !program[calls[0].CallRef] {
		t.Fatalf("auth prep call not recorded as a program call: program=%d calls=%d", len(program), len(calls))
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

func TestSelectedCandidateFactoryReissuesOneProtectedSession(t *testing.T) {
	var logins, reads atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" && r.Method == "POST" {
			if r.ParseForm() != nil || r.Form.Get("email") != "tester@owned.test" || r.Form.Get("password") != "seeded-pass-88ff" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: fmt.Sprintf("synthetic-session-%d", logins.Add(1))})
			_, _ = io.WriteString(w, "signed in")
			return
		}
		if r.URL.Path != "/read" || r.URL.RawQuery != "mode=review" || r.Header.Get("Accept") != "application/json" ||
			r.Header.Get("Cookie") != fmt.Sprintf("session=synthetic-session-%d", logins.Load()) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		reads.Add(1)
		_, _ = io.WriteString(w, "synthetic document")
	}))
	defer target.Close()
	cfg, _, policy := e2HostPolicyFixture(t)
	policy.Rules[0].Origin, policy.Rules[0].PathPrefix, policy.Rules[0].PathMatch = target.URL, "/read", "exact"
	policy.Rules[0].ExactQuery = "mode=review"
	policy.Rules[0].RequestHeaders = map[string]string{"Accept": "application/json"}
	policy.Rules[0].StrictHeaders = true
	policy.Rules = append(policy.Rules, E2HTTPFileRule{Ref: "auth-login", Origin: target.URL, ConnectIP: "127.0.0.1",
		PathPrefix: "/login", PathScopeRef: "auth-login", Methods: []string{"POST"}, Purpose: e2proxy.PurposeAuthPrep})
	pin := authCredentialPin(t, cfg.AuthorityRoot, "selected-account.json", "tester@owned.test", "seeded-pass-88ff")
	auth := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin})
	policy.Auth, policy.HeaderMode = &auth, e2proxy.HeaderTestAccountPassthrough
	policy.SelectedAccountRef = CandidateAccountRevision("tester-a", pin.SHA256)
	policy.MaxRequests = 4
	policy.Limits.MaxAttempts, policy.Limits.MaxCalls = 4, 4
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0600) != nil {
		t.Fatal("synthetic selected policy unavailable")
	}
	if _, _, err := ValidateE2HTTPRunPolicyDocument(raw); err != nil {
		t.Fatal("structured selected policy rejected", err)
	}
	options, err := LoadE2HTTPRuntimeOptions(t.Context(), E2HTTPFileOptions{PolicyFile: cfg.PolicyFile,
		Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := options.Acquire(t.Context(), E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 27, IntentID: 31})
	if err != nil || lease.AccountAuth["tester-a"] != E2HTTPAuthAuthenticated || logins.Load() != 1 {
		t.Fatalf("selected account preparation unavailable: %v", err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = lease.Runner.Close(cleanup)
		_ = lease.Execution.Stop(cleanup)
		_ = lease.Release(cleanup)
	}()
	for i := range 2 {
		if i == 1 {
			if _, result := lease.RefreshSession(t.Context(), "tester-a"); result != E2HTTPAuthAuthenticated || logins.Load() != 2 {
				t.Fatalf("same account session reissue unavailable: %s", result)
			}
		}
		call, err := lease.Execution.BeginCall(t.Context(), fmt.Sprintf("selected-read-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		request, _ := http.NewRequest("GET", target.URL+"/read?mode=review", nil)
		request.Header.Set("Accept", "application/json")
		response, receipt, err := call.Do(t.Context(), request)
		if err != nil || response == nil || receipt.StatusCode != 200 || !receipt.AuditComplete {
			t.Fatalf("selected protected read failed: %+v %v", receipt, err)
		}
		response.Body.Close()
		observed := call.Result().Observations
		if len(observed) != 1 || observed[0].RequestAccountRef != policy.SelectedAccountRef ||
			observed[0].RequestAuthContext != "cookie" || len(observed[0].RequestQueryDigest) != 64 || len(observed[0].RequestHeaderDigest) != 64 {
			t.Fatalf("same account was not bound to actual send: %+v", observed)
		}
		_, _ = call.Close(t.Context())
	}
	if reads.Load() != 2 {
		t.Fatalf("selected read count=%d", reads.Load())
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
// 60초 세션이 여러 번 만료돼도 성공한 갱신은 막지 않는다. 실패 상한·간격은 계정별이며 새 HTTP/call을 쓰지 않는다.
func TestE2HTTPFactoryRefreshSession(t *testing.T) {
	clock := &e2RefreshClock{at: time.Now()}
	var logins atomic.Int64
	var rejectA atomic.Bool
	var mu sync.Mutex
	var token string
	var expires time.Time
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			n := logins.Add(1)
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["email"] != "tester@owned.test" || body["password"] != "seeded-pass-88ff" || rejectA.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			mu.Lock()
			token, expires = fmt.Sprintf("ownedtoken%04dvalue", n), clock.Now().Add(time.Minute)
			fresh := token
			mu.Unlock()
			fmt.Fprintf(w, `{"data":{"token":%q}}`, fresh)
			return
		}
		mu.Lock()
		valid := r.Header.Get("Authorization") == "Bearer "+token && clock.Now().Before(expires)
		mu.Unlock()
		if !valid {
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
	// 확정 몫 4건(다시 로그인 2 + 신원 2): 확정 단계 직전 다시 로그인은 이 몫을 쓰는 호출로 나간다.
	e3Requests, e3TimeoutMS := uint64(4), int64(4000)
	policy.Limits.E3ConfirmationRequests, policy.Limits.E3ConfirmationTimeoutMS = &e3Requests, &e3TimeoutMS
	// tester-b는 비밀번호가 틀린다. 첫 인증 준비 실패는 재로그인 연속 실패에 포함하지 않는다.
	auth := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: authCredentialPin(t, cfg.AuthorityRoot, "tester-a.json", "tester@owned.test", "seeded-pass-88ff")},
		E2HTTPFileAuthAccount{Label: "tester-b", Credential: authCredentialPin(t, cfg.AuthorityRoot, "tester-b.json", "tester@owned.test", "wrong-pass-value")})
	auth.Login.Cookies, auth.Login.Format, auth.Login.JSONField, auth.Login.Header, auth.Login.Prefix = nil, "json", "data.token", "Authorization", "Bearer "
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
		t.Fatal("refresh path missing", err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = lease.Runner.Close(cleanup)
		_ = lease.Execution.Stop(cleanup)
		_ = lease.Release(cleanup)
	}()
	lease.sessionStore.refreshes.now = clock.Now
	read := func(header http.Header, want int) {
		t.Helper()
		call, err := lease.Execution.BeginCall(ctx, "owned-read")
		if err != nil {
			t.Fatal(err)
		}
		request, _ := http.NewRequest("GET", target.URL+"/allowed/me", nil)
		request.Header = header.Clone()
		response, receipt, err := call.Do(ctx, request)
		if err != nil || response == nil || receipt.StatusCode != want {
			t.Fatalf("owned read: status=%d want=%d err=%v", receipt.StatusCode, want, err)
		}
		response.Body.Close()
		if _, err := call.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	unsent := func(label, want string) {
		t.Helper()
		before, calls := logins.Load(), len(lease.Execution.Calls())
		counts := lease.Execution.Snapshot().Counts
		if header, result := lease.RefreshSession(ctx, label); result != want || len(header) != 0 {
			t.Fatalf("refresh %s: %s want %s", label, result, want)
		}
		if logins.Load() != before || len(lease.Execution.Calls()) != calls || lease.Execution.Snapshot().Counts != counts {
			t.Fatal("refused refresh spent a call/request")
		}
	}
	// 계정 결속 참조(v3 계약 §2.2): 인증된 tester-a만 참조가 있고(tester-b는 비밀번호가 틀려 없음), 다시 로그인에 성공할
	// 때마다 바뀌며 옛 참조로는 세션을 얻을 수 없다. 실패하면 그대로다.
	bindings, runRef := lease.AccountBindings(), lease.Execution.Snapshot().Scope.RunRef
	if _, ok := bindings.AccountBindingRef(runRef, "tester-a"); !ok {
		t.Fatal("인증된 계정의 결속 참조가 없음")
	}
	if _, ok := bindings.AccountBindingRef(runRef, "tester-b"); ok {
		t.Fatal("로그인 실패 계정에 결속 참조가 있음")
	}
	refresh := func(label, want string) {
		t.Helper()
		before := logins.Load()
		prevRef, _ := bindings.AccountBindingRef(runRef, label)
		header, result := lease.RefreshSession(ctx, label)
		if result != want || logins.Load() != before+1 {
			t.Fatalf("refresh %s: %s want %s logins=%d", label, result, want, logins.Load()-before)
		}
		nowRef, bound := bindings.AccountBindingRef(runRef, label)
		if result != E2HTTPAuthAuthenticated && nowRef != prevRef {
			t.Fatal("실패한 다시 로그인이 결속 참조를 바꿈")
		}
		if result == E2HTTPAuthAuthenticated {
			if !bound || nowRef == prevRef {
				t.Fatal("다시 로그인 뒤 결속 참조가 그대로", label)
			}
			if _, err := bindings.BoundSession(runRef, label, prevRef); prevRef != "" && !errors.Is(err, ErrE2HTTPAccountBinding) {
				t.Fatal("옛 결속 참조로 세션을 얻음")
			}
			if got, err := bindings.BoundSession(runRef, label, nowRef); err != nil || got.Get("Authorization") != header.Get("Authorization") {
				t.Fatal("지금 결속 참조가 새 세션을 가리키지 않음", err)
			}
			if value := strings.TrimPrefix(header.Get("Authorization"), "Bearer "); value == "" || strings.Contains(string(lease.Execution.RedactOutput([]byte(value))), value) {
				t.Fatal("new session missing or not redacted")
			}
			read(e2HTTPWorkerSessions(e2proxy.HeaderTestAccountPassthrough, &lease)[label], http.StatusOK)
		}
	}
	// 초기 인증 뒤 60초 만료를 8번 진행한다. 가상 시계만 앞당기고 HTTP·E②는 실제 경로를 쓴다.
	for range 8 {
		old := e2HTTPWorkerSessions(e2proxy.HeaderTestAccountPassthrough, &lease)["tester-a"]
		clock.Advance(time.Minute)
		read(old, http.StatusUnauthorized)
		refresh("tester-a", E2HTTPAuthAuthenticated)
		unsent("tester-a", E2HTTPRefreshCooldown)
	}
	unsent("someone-else", E2HTTPAuthNotSent)
	// 확정 단계 직전 다시 로그인(사용자 결정 2026-11-20 (가))도 같은 경로(인증 준비 규칙)·같은 간격 상한이고, 확정 몫을
	// 쓰는 호출로 나간다.
	relogin, ok := bindings.(E2HTTPAccountRelogin)
	if !ok {
		t.Fatal("세션 저장소가 확정 단계 다시 로그인을 주지 않음")
	}
	clock.Advance(minSessionRefreshInterval)
	prevRef, _ := bindings.AccountBindingRef(runRef, "tester-a")
	before := logins.Load()
	modelBudget, err := lease.Execution.BudgetRemaining()
	if err != nil {
		t.Fatal(err)
	}
	if result := relogin.Relogin(ctx, "tester-a"); result != E2HTTPAuthAuthenticated || logins.Load() != before+1 {
		t.Fatalf("확정 전 다시 로그인: %s logins=%d", result, logins.Load()-before)
	}
	// 확정 몫에서 나갔으므로 모델 단계가 쓸 남은 예산은 그대로다(Worker refresh_session은 1 줄인다).
	if after, err := lease.Execution.BudgetRemaining(); err != nil || after != modelBudget {
		t.Fatalf("확정 전 다시 로그인이 모델 단계 예산을 씀: %d → %d %v", modelBudget, after, err)
	}
	if nowRef, _ := bindings.AccountBindingRef(runRef, "tester-a"); nowRef == prevRef {
		t.Fatal("확정 전 다시 로그인 뒤 결속 참조가 그대로")
	}
	if result := relogin.Relogin(ctx, "tester-a"); result != E2HTTPRefreshCooldown || logins.Load() != before+1 {
		t.Fatalf("간격 상한 안 확정 전 다시 로그인: %s", result)
	}
	// A의 2회 실패와 B의 3회 실패는 합산하지 않는다. B의 상한 뒤에도 A는 성공할 수 있다.
	rejectA.Store(true)
	for range 2 {
		clock.Advance(minSessionRefreshInterval)
		refresh("tester-a", E2HTTPAuthLoginFailed)
	}
	for range maxSessionRefreshFailuresPerAccount {
		clock.Advance(minSessionRefreshInterval)
		refresh("tester-b", E2HTTPAuthLoginFailed)
	}
	unsent("tester-b", E2HTTPRefreshLimit)
	rejectA.Store(false)
	refresh("tester-a", E2HTTPAuthAuthenticated)
	// 성공으로 실패 수가 초기화된다. 다시 2회 실패한 뒤에도 세 번째 실제 로그인을 허용한다.
	rejectA.Store(true)
	for i := range maxSessionRefreshFailuresPerAccount {
		clock.Advance(minSessionRefreshInterval)
		refresh("tester-a", E2HTTPAuthLoginFailed)
		want := E2HTTPRefreshCooldown
		if i == maxSessionRefreshFailuresPerAccount-1 {
			want = E2HTTPRefreshLimit
		}
		unsent("tester-a", want)
	}
	clock.Advance(time.Minute)
	unsent("tester-a", E2HTTPRefreshLimit)
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
	before := len(lease.programCalls.snapshot())
	refreshed, result := lease.RefreshSession(ctx, "tester-a")
	wg.Wait()
	if result != E2HTTPAuthAuthenticated {
		t.Fatal("refresh", result)
	}
	// 다시 로그인 호출도 Worker 결과가 없는 서버 호출이라 마감 대조에서 따로 센다(#555).
	if before != 1 || len(lease.programCalls.snapshot()) != 2 {
		t.Fatal("refresh call must be recorded as a program call", before, len(lease.programCalls.snapshot()))
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
	if err != nil || strings.Contains(string(data), "rotatingsession0002value") || !strings.Contains(string(data), `"/{id}/{id}/{id}"`) {
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
	secondDone := false
	select {
	case result := <-second:
		results <- result
		secondDone = true
	case <-time.After(300 * time.Millisecond): // 요청 시간 상한(1초) 안에서 첫째를 푼다
	}
	close(release)
	if !secondDone {
		results <- <-second
	}
	seen := map[string]int{}
	for range 2 {
		seen[<-results]++
	}
	mu.Lock()
	loginCount := issued
	mu.Unlock()
	if secondDone || seen[E2HTTPAuthAuthenticated] != 1 || seen[E2HTTPRefreshCooldown] != 1 || loginCount != 2 {
		t.Fatalf("concurrent refresh: results=%v logins=%d bypassed queue=%v", seen, loginCount, secondDone)
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
	store := newE2HTTPSessionStore(E2HTTPSessions{"a": http.Header{"Cookie": {"s=1"}}}, "run-1", nil)
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

// 모델 로그인 회차(인증 칸 model_login)는 실행을 잡을 때 프로그램이 로그인하지 않는다. 결속 계정의 자격을 읽어 Worker
// 지침용으로 돌려주고, 모델이 대상에서 직접 로그인한다(사용자 결정 2026-11-26, docs/contracts/model-login-attempts.md).
// 비밀번호는 저장 기록 가림 값으로 등록한다.
func TestE2HTTPFactoryGivesCredentialsToModelWithoutLogin(t *testing.T) {
	login := &authLoginTarget{token: "seeded-factory-session-44ee"}
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
	a.ModelLogin = true
	policy.LoginAttempts = &E2HTTPLoginAttemptLimits{MaxConsecutiveFailures: 3, MaxContrastProbes: 3, TokenMinBytes: 16}
	policy.Auth, policy.HeaderMode = &a, e2proxy.HeaderTestAccountPassthrough
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0o600) != nil {
		t.Fatal("policy write")
	}
	options, err := LoadE2HTTPRuntimeOptions(t.Context(), E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}})
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
	if login.posts.Load() != 0 || len(acquired.Sessions) != 0 || len(acquired.Execution.Calls()) != 0 || acquired.AccountAuth != nil {
		t.Fatalf("모델 회차에서 프로그램이 로그인함: posts=%d sessions=%d calls=%d auth=%v", login.posts.Load(), len(acquired.Sessions), len(acquired.Execution.Calls()), acquired.AccountAuth)
	}
	if len(acquired.Credentials) != 1 || acquired.Credentials[0].Label != "tester-a" || acquired.Credentials[0].Username != "tester@owned.test" || acquired.Credentials[0].Password != "seeded-pass-88ff" {
		t.Fatal("Worker 지침용 자격이 없음")
	}
	if got := e2HTTPWorkerCredentials(e2proxy.HeaderTestAccountPassthrough, &acquired); len(got) != 1 || got[0].Label != "tester-a" {
		t.Fatal("시험 계정 실행 Worker에 자격이 안 감")
	}
	if got := e2HTTPWorkerCredentials(e2proxy.HeaderPreserve, &acquired); len(got) != 0 {
		t.Fatal("자격 헤더 통과가 아닌 실행에 자격이 감")
	}
	if out := string(acquired.Execution.RedactRecord([]byte("pw seeded-pass-88ff"))); strings.Contains(out, "seeded-pass-88ff") {
		t.Fatal("비밀번호가 저장 기록 가림 값으로 등록되지 않음")
	}
}

// 저장 기록 가림 값으로 등록할 수 없는 짧은 비밀번호(1~5바이트, 제품 등록은 받음)의 계정은 모델 로그인 회차에서도 Worker에
// 주지 않고 not_sent로 남긴다. 모델이 그 값을 다시 쓰면 저장 기록에 원문이 남기 때문이다(#967 동찬 리뷰 P1).
func TestE2HTTPFactoryWithholdsUnmaskablePasswordFromModel(t *testing.T) {
	login := &authLoginTarget{token: "seeded-factory-session-55ff"}
	target := httptest.NewServer(login.handler(t))
	defer target.Close()
	cfg, _, policy := e2HostPolicyFixture(t)
	policy.Rules[0].Origin = target.URL
	policy.Rules[0].PathPrefix = "/read"
	auth := policy.Rules[0]
	auth.Ref, auth.PathScopeRef, auth.PathPrefix, auth.Methods, auth.Purpose, auth.Credentials = "auth-login", "auth-login-path", "/login", []string{"POST"}, e2proxy.PurposeAuthPrep, nil
	policy.Rules = append(policy.Rules, auth)
	dir := filepath.Dir(cfg.PolicyFile)
	long := authCredentialPin(t, dir, "account-tester-a.json", "tester@owned.test", "seeded-pass-88ff")
	short := authCredentialPin(t, dir, "account-tester-b.json", "short@owned.test", "ab12")
	a := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: long}, E2HTTPFileAuthAccount{Label: "tester-b", Credential: short})
	a.ModelLogin = true
	policy.LoginAttempts = &E2HTTPLoginAttemptLimits{MaxConsecutiveFailures: 3, MaxContrastProbes: 3, TokenMinBytes: 16}
	policy.Auth, policy.HeaderMode = &a, e2proxy.HeaderTestAccountPassthrough
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0o600) != nil {
		t.Fatal("policy write")
	}
	options, err := LoadE2HTTPRuntimeOptions(t.Context(), E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}})
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
	if len(acquired.Credentials) != 1 || acquired.Credentials[0].Label != "tester-a" {
		t.Fatalf("짧은 비밀번호 계정이 Worker 자격에 감: %d", len(acquired.Credentials))
	}
	if acquired.AccountAuth["tester-b"] != E2HTTPAuthNotSent || acquired.AccountAuth["tester-a"] != "" {
		t.Fatalf("계정 상태: %v", acquired.AccountAuth)
	}
	if login.posts.Load() != 0 {
		t.Fatal("모델 로그인 회차에서 프로그램이 로그인함")
	}
}

// 모델 직접 로그인 회차의 진단 단계: E②가 모델 요청마다 로그인 시도를 판정한다(계약 docs/contracts/model-login-attempts.md).
// 틀린 로그인은 실패로 세고, 맞는 로그인은 세션 없는 막힘과 세션 열림이 같은 URL에서 대조될 때만 성공이다. 성공한 세션은
// 세션 저장소에 묶여(확정 단계 계정 결속) 계정 귀속에 쓰이고, 같은 아이디로 세 번 연달아 실패하면 네 번째는 대상에 나가지
// 않는다. 기록 파일에는 비밀번호·세션 값이 없다.
func TestE2HTTPModelLoginAttemptsThroughE2(t *testing.T) {
	const token = "model-login-session-77aa"
	var posts atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/read/login":
			posts.Add(1)
			_ = r.ParseForm()
			if r.PostForm.Get("email") != "tester@owned.test" || r.PostForm.Get("password") != "seeded-pass-88ff" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: token, Path: "/"})
			http.Redirect(w, r, "/read/home", http.StatusFound)
		case r.Method == "GET" && r.URL.Path == "/read/home":
			if c, err := r.Cookie("session"); err == nil && c.Value == token {
				io.WriteString(w, "home")
				return
			}
			http.Redirect(w, r, "/read/login", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer target.Close()
	cfg, _, policy := e2HostPolicyFixture(t)
	policy.Rules[0].Origin = target.URL
	policy.Rules[0].PathPrefix = "/read"
	policy.Rules[0].Methods = []string{"GET", "POST"}
	policy.MaxRequests = 64
	// 대상의 401은 이 call의 거부 수로 세지 않지만, 상한 거부(policy_denied)는 센다. 호출 상한이 판정기 상한보다 먼저 닿지 않게 둔다.
	policy.Limits.MaxDenials, policy.Limits.MaxAttempts = 16, 64
	auth := policy.Rules[0]
	auth.Ref, auth.PathScopeRef, auth.PathPrefix, auth.Methods, auth.Purpose, auth.Credentials = "auth-login", "auth-login-path", "/login", []string{"POST"}, e2proxy.PurposeAuthPrep, nil
	policy.Rules = append(policy.Rules, auth)
	pin := authCredentialPin(t, filepath.Dir(cfg.PolicyFile), "account-tester-a.json", "tester@owned.test", "seeded-pass-88ff")
	a := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin})
	a.ModelLogin = true
	policy.LoginAttempts = &E2HTTPLoginAttemptLimits{MaxConsecutiveFailures: 3, MaxContrastProbes: 3, TokenMinBytes: 16}
	policy.Auth, policy.HeaderMode = &a, e2proxy.HeaderTestAccountPassthrough
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0o600) != nil {
		t.Fatal("policy write")
	}
	options, err := LoadE2HTTPRuntimeOptions(t.Context(), E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}})
	if err != nil {
		t.Fatal(err)
	}
	run, cancel := context.WithCancel(t.Context())
	defer cancel()
	lease, err := options.Acquire(run, E2HTTPRequest{TaskID: "17", TaskIDNumber: 17, ExplorationID: 1, IntentID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release(context.Background())
	if lease.LoginAttempts == nil {
		t.Fatal("모델 로그인 회차에 판정기가 없음")
	}
	call, err := lease.Execution.BeginCall(run, "model-login")
	if err != nil {
		t.Fatal(err)
	}
	defer call.Close(context.Background())
	send := func(method, path, body, cookie string) (int, e2proxy.Receipt, error) {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, target.URL+path, reader)
		if body != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		resp, receipt, err := call.Do(run, req)
		status := 0
		if resp != nil {
			status = resp.StatusCode
			resp.Body.Close()
		}
		return status, receipt, err
	}
	wrong := "email=tester%40owned.test&password=wrong-pass-1"
	right := "email=tester%40owned.test&password=seeded-pass-88ff"
	if status, _, err := send("GET", "/read/home", "", ""); err != nil || status != http.StatusFound {
		t.Fatalf("세션 없는 GET: %d %v", status, err)
	}
	for i := 0; i < 2; i++ {
		if status, receipt, err := send("POST", "/read/login", wrong, ""); err != nil || status != http.StatusUnauthorized {
			t.Fatalf("틀린 로그인 %d: %d %v %+v", i, status, err, receipt)
		}
	}
	if status, _, err := send("POST", "/read/login", right, ""); err != nil || status != http.StatusFound {
		t.Fatalf("맞는 로그인: %d %v", status, err)
	}
	if got := lease.LoginAttempts.AccountAuth()["tester-a"]; got != loginattempt.AuthUnconfirmed {
		t.Fatalf("대조 전 계정 상태: %s", got)
	}
	if status, _, err := send("GET", "/read/home", "", "theme=dark; session="+token); err != nil || status != http.StatusOK {
		t.Fatalf("세션 GET: %d %v", status, err)
	}
	// 성공을 세운 요청부터 그 계정으로 귀속한다(다른 쿠키가 같이 실려도). 세션 없는 요청은 귀속하지 않는다.
	observed := call.Result().Observations
	wantRef := CandidateAccountRevision("tester-a", pin.SHA256)
	if len(observed) < 2 || observed[len(observed)-1].RequestAccountRef != wantRef || observed[0].RequestAccountRef != "" {
		t.Fatalf("모델 로그인 세션 요청의 계정 귀속: %+v", observed)
	}
	if got := lease.LoginAttempts.AccountAuth()["tester-a"]; got != loginattempt.AuthAuthenticated {
		t.Fatalf("대조 뒤 계정 상태: %s", got)
	}
	store := lease.sessionStore
	ref, ok := store.AccountBindingRef(lease.Execution.Snapshot().Scope.RunRef, "tester-a")
	if !ok {
		t.Fatal("성공한 모델 로그인 세션이 확정 단계 결속에 없음")
	}
	if h, err := store.BoundSession(lease.Execution.Snapshot().Scope.RunRef, "tester-a", ref); err != nil || h.Get("Cookie") != "session="+token {
		t.Fatalf("결속 세션: %v %v", h, err)
	}
	for i := 0; i < 3; i++ {
		send("POST", "/read/login", wrong, "")
	}
	before := posts.Load()
	_, refusedReceipt, err := send("POST", "/read/login", right, "")
	if err == nil || refusedReceipt.Reason != e2proxy.PolicyDenied || posts.Load() != before {
		t.Fatalf("연속 실패 상한 뒤에도 보냄: %+v %v posts=%d→%d", refusedReceipt, err, before, posts.Load())
	}
	path := filepath.Join(policy.StateDir, E2HTTPLoginAttemptsFile)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("기록 파일: %v %v", info, err)
	}
	rows, _, err := readLoginAttemptRows(path)
	if err != nil || len(rows) == 0 {
		t.Fatal("기록 행 없음")
	}
	last := rows[len(rows)-1]
	if last.Refusal != loginattempt.RefusalExhausted || last.Transport != loginattempt.TransportNotSent || last.Stage != loginattempt.StageDiagnosis ||
		last.TransportRef != refusedReceipt.CallRef+"/"+refusedReceipt.AttemptID || refusedReceipt.AttemptID == "" {
		t.Fatalf("마지막 행(상한 거부는 E② 거부 영수증과 이어져야 함): %+v", last)
	}
	content, _ := os.ReadFile(path)
	for _, v := range []string{"seeded-pass-88ff", "wrong-pass-1", token} {
		if strings.Contains(string(content), v) {
			t.Fatalf("기록 파일에 값이 남음: %s", v)
		}
	}
}

// 판정기가 로그인으로 본 뒤 다른 E② 규칙(송신 시도 상한)으로 보내지 못한 로그인도 미전송 행으로 남고, 그 거부 영수증의
// call_ref·attempt_id로 E② 기록과 이어진다(#969 동찬 리뷰 P2).
func TestE2HTTPModelLoginNotSentRowLinksReceipt(t *testing.T) {
	var posts atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts.Add(1)
		}
		io.WriteString(w, "ok")
	}))
	defer target.Close()
	cfg, _, policy := e2HostPolicyFixture(t)
	policy.Rules[0].Origin = target.URL
	policy.Rules[0].PathPrefix = "/read"
	policy.Rules[0].Methods = []string{"GET", "POST"}
	policy.MaxRequests = 64
	policy.Limits.MaxDenials, policy.Limits.MaxAttempts = 16, 1
	auth := policy.Rules[0]
	auth.Ref, auth.PathScopeRef, auth.PathPrefix, auth.Methods, auth.Purpose, auth.Credentials = "auth-login", "auth-login-path", "/login", []string{"POST"}, e2proxy.PurposeAuthPrep, nil
	policy.Rules = append(policy.Rules, auth)
	pin := authCredentialPin(t, filepath.Dir(cfg.PolicyFile), "account-tester-a.json", "tester@owned.test", "seeded-pass-88ff")
	a := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin})
	a.ModelLogin = true
	policy.LoginAttempts = &E2HTTPLoginAttemptLimits{MaxConsecutiveFailures: 3, MaxContrastProbes: 3, TokenMinBytes: 16}
	policy.Auth, policy.HeaderMode = &a, e2proxy.HeaderTestAccountPassthrough
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0o600) != nil {
		t.Fatal("policy write")
	}
	options, err := LoadE2HTTPRuntimeOptions(t.Context(), E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}})
	if err != nil {
		t.Fatal(err)
	}
	run, cancel := context.WithCancel(t.Context())
	defer cancel()
	lease, err := options.Acquire(run, E2HTTPRequest{TaskID: "17", TaskIDNumber: 17, ExplorationID: 1, IntentID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release(context.Background())
	call, err := lease.Execution.BeginCall(run, "model-login-budget")
	if err != nil {
		t.Fatal(err)
	}
	defer call.Close(context.Background())
	first, _ := http.NewRequest("GET", target.URL+"/read/home", nil)
	if resp, _, err := call.Do(run, first); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}
	login, _ := http.NewRequest("POST", target.URL+"/read/login", strings.NewReader("email=tester%40owned.test&password=seeded-pass-88ff"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, receipt, err := call.Do(run, login)
	if err == nil || receipt.Reason != e2proxy.AttemptLimit || posts.Load() != 0 {
		t.Fatalf("시도 상한인데 보냄: %+v %v", receipt, err)
	}
	rows, _, err := readLoginAttemptRows(filepath.Join(policy.StateDir, E2HTTPLoginAttemptsFile))
	if err != nil || len(rows) != 1 {
		t.Fatalf("미전송 행: %d %v", len(rows), err)
	}
	if r := rows[0]; r.Transport != loginattempt.TransportNotSent || r.Refusal != string(e2proxy.AttemptLimit) || r.TransportRef != receipt.CallRef+"/"+receipt.AttemptID || receipt.AttemptID == "" {
		t.Fatalf("미전송 행이 E② 거부 영수증과 이어지지 않음: %+v / %s/%s", r, receipt.CallRef, receipt.AttemptID)
	}
}

// 프로그램 대조 요청(계약 §4): 모델이 로그인 세션으로 연 URL에 세션 없는 응답이 없으면, E² 프로그램 호출(login_contrast)로
// 그 정확한 URL을 세션 없이 한 번 다시 연다. 막힘이면 성공이 서고(contrast_source probe), 대상은 쿠키 없는 GET을 받는다.
// 대조 요청은 GET·본문 없음·인증 헤더 없음만 보낸다.
func TestE2HTTPModelLoginContrastProbe(t *testing.T) {
	const token = "model-login-session-88bb"
	var anonymous atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/read/login":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: token, Path: "/"})
			http.Redirect(w, r, "/read/dashboard", http.StatusFound)
		case r.Method == "GET" && r.URL.Path == "/read/dashboard":
			if c, err := r.Cookie("session"); err == nil && c.Value == token {
				io.WriteString(w, "dashboard")
				return
			}
			if r.Header.Get("Cookie") == "" && r.Header.Get("Authorization") == "" {
				anonymous.Add(1)
			}
			http.Redirect(w, r, "/read/login", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer target.Close()
	cfg, _, policy := e2HostPolicyFixture(t)
	policy.Rules[0].Origin = target.URL
	policy.Rules[0].PathPrefix = "/read"
	policy.Rules[0].Methods = []string{"GET", "POST"}
	policy.MaxRequests = 64
	policy.Limits.MaxDenials, policy.Limits.MaxAttempts = 16, 64
	auth := policy.Rules[0]
	auth.Ref, auth.PathScopeRef, auth.PathPrefix, auth.Methods, auth.Purpose, auth.Credentials = "auth-login", "auth-login-path", "/login", []string{"POST"}, e2proxy.PurposeAuthPrep, nil
	policy.Rules = append(policy.Rules, auth)
	pin := authCredentialPin(t, filepath.Dir(cfg.PolicyFile), "account-tester-a.json", "tester@owned.test", "seeded-pass-88ff")
	a := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin})
	a.ModelLogin = true
	policy.LoginAttempts = &E2HTTPLoginAttemptLimits{MaxConsecutiveFailures: 3, MaxContrastProbes: 3, TokenMinBytes: 16}
	policy.Auth, policy.HeaderMode = &a, e2proxy.HeaderTestAccountPassthrough
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0o600) != nil {
		t.Fatal("policy write")
	}
	options, err := LoadE2HTTPRuntimeOptions(t.Context(), E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}})
	if err != nil {
		t.Fatal(err)
	}
	run, cancel := context.WithCancel(t.Context())
	defer cancel()
	lease, err := options.Acquire(run, E2HTTPRequest{TaskID: "17", TaskIDNumber: 17, ExplorationID: 1, IntentID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release(context.Background())
	call, err := lease.Execution.BeginCall(run, "model-login")
	if err != nil {
		t.Fatal(err)
	}
	defer call.Close(context.Background())
	login, _ := http.NewRequest("POST", target.URL+"/read/login", strings.NewReader("email=tester%40owned.test&password=seeded-pass-88ff"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if resp, _, err := call.Do(run, login); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}
	dash, _ := http.NewRequest("GET", target.URL+"/read/dashboard", nil)
	dash.Header.Set("Cookie", "session="+token)
	if resp, _, err := call.Do(run, dash); err != nil || resp.StatusCode != 200 {
		t.Fatalf("세션 GET: %v", err)
	} else {
		resp.Body.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for lease.LoginAttempts.AccountAuth()["tester-a"] != loginattempt.AuthAuthenticated && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := lease.LoginAttempts.AccountAuth()["tester-a"]; got != loginattempt.AuthAuthenticated || anonymous.Load() != 1 {
		t.Fatalf("대조 요청으로 성공 안 함: %s 세션 없는 GET=%d", got, anonymous.Load())
	}
	rows, _, err := readLoginAttemptRows(filepath.Join(policy.StateDir, E2HTTPLoginAttemptsFile))
	if err != nil || len(rows) == 0 {
		t.Fatal("기록 없음")
	}
	last := rows[len(rows)-1]
	if last.ContrastSource != loginattempt.ContrastProbe || last.ContrastProbes != 1 || last.Outcome != loginattempt.OutcomeSucceeded {
		t.Fatalf("대조 요청 행: %+v", last)
	}
	contrast := 0
	for _, c := range lease.Execution.Calls() {
		if c.Program == e2proxy.ProgramLoginContrast {
			contrast++
		}
	}
	if contrast != 1 {
		t.Fatalf("프로그램 대조 호출 수: %d", contrast)
	}
	// 대조 요청 호출은 세션·본문·쓰기 메서드를 싣지 못한다.
	probeCall, err := lease.Execution.BeginProgramCall(run, e2proxy.ProgramLoginContrast, "login-contrast-check")
	if err != nil {
		t.Fatal(err)
	}
	defer probeCall.Close(context.Background())
	for name, req := range map[string]*http.Request{
		"cookie": func() *http.Request {
			r, _ := http.NewRequest("GET", target.URL+"/read/dashboard", nil)
			r.Header.Set("Cookie", "session="+token)
			return r
		}(),
		"post": func() *http.Request {
			r, _ := http.NewRequest("POST", target.URL+"/read/login", strings.NewReader("x=1"))
			return r
		}(),
		"empty post": func() *http.Request {
			r, _ := http.NewRequest("POST", target.URL+"/read/dashboard", nil)
			return r
		}(),
	} {
		if _, receipt, err := probeCall.Do(run, req); err == nil || receipt.Reason != e2proxy.Unsupported {
			t.Fatalf("%s: 대조 요청이 막아야 할 요청을 보냄: %+v %v", name, receipt, err)
		}
	}
}

// 계정을 등록하지 않은 제품 회차도 판정기가 붙는다(사용자 결정 2026-11-26). 등록 밖 아이디(기본 계정 시험 등)로 같은 아이디
// 세 번 연달아 실패하면 네 번째 로그인은 대상에 나가지 않는다(절대규칙 1·3). 이 회차는 세션을 싣지 못하는 것은 그대로다.
func TestE2HTTPLoginAttemptsWithoutRegisteredAccounts(t *testing.T) {
	var posts atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/read/login" {
			posts.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	defer target.Close()
	cfg, _, policy := e2HostPolicyFixture(t)
	policy.Rules[0].Origin = target.URL
	policy.Rules[0].PathPrefix = "/read"
	policy.Rules[0].Methods = []string{"GET", "POST"}
	policy.MaxRequests = 64
	policy.Limits.MaxDenials, policy.Limits.MaxAttempts = 16, 64
	policy.LoginAttempts = &E2HTTPLoginAttemptLimits{MaxConsecutiveFailures: 3, MaxContrastProbes: 3, TokenMinBytes: 16}
	if policy.Auth != nil || policy.HeaderMode == e2proxy.HeaderTestAccountPassthrough {
		t.Fatal("시험 정책이 계정 없는 회차가 아님")
	}
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0o600) != nil {
		t.Fatal("policy write")
	}
	options, err := LoadE2HTTPRuntimeOptions(t.Context(), E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}})
	if err != nil {
		t.Fatal(err)
	}
	run, cancel := context.WithCancel(t.Context())
	defer cancel()
	lease, err := options.Acquire(run, E2HTTPRequest{TaskID: "17", TaskIDNumber: 17, ExplorationID: 1, IntentID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release(context.Background())
	if lease.LoginAttempts == nil {
		t.Fatal("계정 없는 제품 회차에 판정기가 없음")
	}
	call, err := lease.Execution.BeginCall(run, "default-creds")
	if err != nil {
		t.Fatal(err)
	}
	defer call.Close(context.Background())
	send := func() (e2proxy.Receipt, error) {
		req, _ := http.NewRequest("POST", target.URL+"/read/login", strings.NewReader("username=admin&password=admin123"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, receipt, err := call.Do(run, req)
		if resp != nil {
			resp.Body.Close()
		}
		return receipt, err
	}
	for i := 0; i < 3; i++ {
		if _, err := send(); err != nil {
			t.Fatalf("%d번째 기본 계정 로그인: %v", i+1, err)
		}
	}
	if receipt, err := send(); err == nil || receipt.Reason != e2proxy.PolicyDenied || posts.Load() != 3 {
		t.Fatalf("계정 없는 회차에서 연속 실패 상한 뒤에도 보냄: %+v %v posts=%d", receipt, err, posts.Load())
	}
	rows, _, err := readLoginAttemptRows(filepath.Join(policy.StateDir, E2HTTPLoginAttemptsFile))
	if err != nil || len(rows) == 0 || rows[len(rows)-1].Refusal != loginattempt.RefusalExhausted || rows[len(rows)-1].AccountLabel != "" || rows[len(rows)-1].Identifier != "admin" {
		t.Fatalf("기록: %v %+v", err, rows)
	}
}

// 모델 로그인 인증 칸은 판정 설정 없이 받지 않고, 판정 설정은 있으면 맞는 값이어야 한다(상한 없이 로그인이 나가지 않게).
func TestE2HTTPPolicyRequiresLoginAttemptLimits(t *testing.T) {
	for name, change := range map[string]func(*E2HTTPFilePolicy, string){
		"model login without limits": func(p *E2HTTPFilePolicy, dir string) {
			pin := authCredentialPin(t, dir, "account-tester-a.json", "tester@owned.test", "seeded-pass-88ff")
			a := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin})
			a.ModelLogin = true
			auth := p.Rules[0]
			auth.Ref, auth.PathScopeRef, auth.PathPrefix, auth.Methods, auth.Purpose, auth.Credentials = "auth-login", "auth-login-path", "/login", []string{"POST"}, e2proxy.PurposeAuthPrep, nil
			p.Rules = append(p.Rules, auth)
			p.Auth, p.HeaderMode = &a, e2proxy.HeaderTestAccountPassthrough
		},
		"zero failure limit": func(p *E2HTTPFilePolicy, _ string) {
			p.LoginAttempts = &E2HTTPLoginAttemptLimits{MaxConsecutiveFailures: 0, MaxContrastProbes: 3, TokenMinBytes: 16}
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, _, policy := e2HostPolicyFixture(t)
			change(&policy, filepath.Dir(cfg.PolicyFile))
			raw, err := json.Marshal(policy)
			if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0o600) != nil {
				t.Fatal("policy write")
			}
			if _, err := LoadE2HTTPRuntimeOptions(t.Context(), E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}}); err == nil {
				t.Fatal("판정 설정이 틀린 정책을 받음")
			}
		})
	}
}
