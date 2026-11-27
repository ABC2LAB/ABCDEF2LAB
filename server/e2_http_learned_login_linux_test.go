package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"encoding/json"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/learnedlogin"
)

// csrfTarget은 로그인 화면 GET마다 새 CSRF 값과 로그인 전 쿠키를 주고, 로그인 POST는 그 둘이 짝이 맞고 자격이 맞을 때만
// 새 세션 쿠키를 준다. 신원 경로는 세션이 있을 때만 200, 없으면 401이다. 받은 요청을 순서대로 남긴다.
type csrfTarget struct {
	mu       sync.Mutex
	seen     []string
	csrf     map[string]string // 로그인 전 쿠키 → 그 화면의 CSRF
	sessions map[string]bool
	n        int
	drop     atomic.Bool
}

func (c *csrfTarget) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.seen = append(c.seen, r.Method+" "+r.URL.Path)
		c.mu.Unlock()
		switch {
		case r.URL.Path == "/login":
			// 처음 인증 준비(로그인 설정 9칸)가 쓰는 경로다.
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "initial-prep-session-value"})
			c.mu.Lock()
			c.sessions["initial-prep-session-value"] = true
			c.mu.Unlock()
		case r.URL.Path == "/allowed/signin" && r.Method == http.MethodGet:
			c.mu.Lock()
			c.n++
			pre, token := fmt.Sprintf("pre-login-%04d", c.n), fmt.Sprintf("csrf-token-%04d", c.n)
			c.csrf[pre] = token
			c.mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "pre", Value: pre, Path: "/"})
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<form method="post"><input type="hidden" name="_csrf" value="%s"><input name="userName"><input type="password" name="password"><input type="submit" name="commit" value="Sign in"></form>`, token)
		case r.URL.Path == "/allowed/signin":
			if c.drop.Load() {
				// 응답 도중 연결을 끊는다(결과 불명).
				hj, _ := w.(http.Hijacker)
				conn, _, _ := hj.Hijack()
				_ = conn.Close()
				return
			}
			form, _ := url.ParseQuery(string(body))
			pre, _ := r.Cookie("pre")
			c.mu.Lock()
			ok := pre != nil && c.csrf[pre.Value] != "" && form.Get("_csrf") == c.csrf[pre.Value] && form.Get("commit") == "Sign in" &&
				form.Get("userName") == "tester@owned.test" && form.Get("password") == "seeded-pass-88ff"
			if pre != nil {
				delete(c.csrf, pre.Value) // 한 번 쓴 CSRF는 다시 받지 않는다.
			}
			session := fmt.Sprintf("learned-session-%04d-value", c.n)
			if ok {
				c.sessions[session] = true
			}
			c.mu.Unlock()
			if !ok {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusOK)
				io.WriteString(w, `<form><input type="password" name="password"></form>`)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: session, Path: "/"})
			w.Header().Set("Location", "/allowed/home")
			w.WriteHeader(http.StatusFound)
		case r.URL.Path == "/allowed/me":
			sid, _ := r.Cookie("sid")
			c.mu.Lock()
			valid := sid != nil && c.sessions[sid.Value]
			c.mu.Unlock()
			if !valid {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"id":"subject-a"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (c *csrfTarget) requests() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

func csrfMethod() learnedlogin.Method {
	return learnedlogin.Method{Method: "POST", Path: "/allowed/signin", Format: learnedlogin.FormatForm, IdentifierField: "userName", PasswordField: "password",
		OtherFields: []string{"_csrf", "commit"}, FieldsFromResponse: []string{"_csrf"}, SessionSource: learnedlogin.SourceSetCookie,
		SessionNames: []string{"sid"}, SessionSentAs: learnedlogin.SentAsCookie}
}

// learnedLease는 Worker 규칙 /allowed(GET·POST = 쓰기 승인), 인증 준비 규칙 /login, 확정 몫(e3Requests)이 있는 실제 E②
// 실행을 잡는다. 처음 인증 준비로 tester-a가 인증돼 다시 로그인 경로가 열린다.
func learnedLease(t *testing.T, target *httptest.Server, e3Requests uint64, methods ...string) (E2HTTPRunOptions, *e2RefreshClock, context.Context) {
	t.Helper()
	cfg, _, policy := e2HostPolicyFixture(t)
	policy.Rules[0].Origin, policy.Rules[0].PathPrefix, policy.Rules[0].Methods = target.URL, "/allowed", methods
	policy.Rules = append(policy.Rules, E2HTTPFileRule{Ref: "auth-login", Origin: target.URL, ConnectIP: "127.0.0.1", PathPrefix: "/login", PathScopeRef: "auth-login", Methods: []string{"POST"}, Purpose: e2proxy.PurposeAuthPrep})
	policy.HeaderMode = e2proxy.HeaderTestAccountPassthrough
	policy.Limits.MaxAttempts, policy.Limits.MaxCalls, policy.Limits.MaxUnknown, policy.MaxRequests = 64, 64, 8, 64
	e3TimeoutMS := int64(e3Requests) * 1000
	policy.Limits.E3ConfirmationRequests, policy.Limits.E3ConfirmationTimeoutMS = &e3Requests, &e3TimeoutMS
	auth := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: authCredentialPin(t, cfg.AuthorityRoot, "tester-a.json", "tester@owned.test", "seeded-pass-88ff")},
		E2HTTPFileAuthAccount{Label: "tester-b", Credential: authCredentialPin(t, cfg.AuthorityRoot, "tester-b.json", "tester@owned.test", "wrong-pass-value")})
	policy.Auth = &auth
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0o600) != nil {
		t.Fatal("합성 정책 준비 실패")
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	options, err := LoadE2HTTPRuntimeOptions(ctx, E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: ctx}, Supervisor: &e2HTTPSupervisorFixture{}})
	if err != nil {
		t.Fatal("호스트 준비 실패", err)
	}
	lease, err := options.Acquire(ctx, E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 27, IntentID: 31})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = lease.Runner.Close(cleanup)
		_ = lease.Execution.Stop(cleanup)
		_ = lease.Release(cleanup)
	})
	clock := &e2RefreshClock{at: time.Now()}
	lease.sessionStore.refreshes.now = clock.Now
	return lease, clock, ctx
}

func TestLearnedSessionValuesMultipleCookies(t *testing.T) {
	session := http.Header{"Cookie": {"sid=synthetic-session-a; csrf=synthetic-session-b"}}
	values := learnedSessionValues(csrfMethod(), session)
	if len(values) != 2 || string(values[0]) != "synthetic-session-a" || string(values[1]) != "synthetic-session-b" {
		t.Fatalf("다중 쿠키 세션 값 추출 개수 %d", len(values))
	}
}

// 확정 단계 학습 로그인 재사용은 로그인 화면을 먼저 받아 새 CSRF·로그인 전 쿠키로 로그인하고, 배운 자리의 새 세션을 계정
// 결속에 넣는다. 두 요청 모두 Worker 승인 범위 안 확정 몫으로 나가고 모델 단계 예산을 쓰지 않는다.
// 세션 없는 대조 요청은 인증 헤더 없이 나가 막힘을 본다.
func TestE2HTTPFactoryLearnedReloginWithCSRF(t *testing.T) {
	target := &csrfTarget{csrf: map[string]string{}, sessions: map[string]bool{}}
	srv := httptest.NewServer(target.handler(t))
	defer srv.Close()
	lease, clock, ctx := learnedLease(t, srv, 8, "GET", "POST")
	bindings, runRef := lease.AccountBindings(), lease.Execution.Snapshot().Scope.RunRef
	learned, ok := bindings.(E2HTTPLearnedRelogin)
	if !ok {
		t.Fatal("세션 저장소가 학습 로그인 재사용을 주지 않음")
	}
	prevRef, _ := bindings.AccountBindingRef(runRef, "tester-a")
	modelBudget, _ := lease.Execution.BudgetRemaining()
	before := len(target.requests())
	clock.Advance(minSessionRefreshInterval)
	res := learned.LearnedRelogin(ctx, E2HTTPLearnedLoginRequest{Label: "tester-a", Method: csrfMethod(), MaxFailures: 3})
	if res.Result != E2HTTPAuthAuthenticated || res.Reason != "" || res.Requests != 2 || res.Transport != learnedlogin.TransportSent ||
		res.Status != http.StatusFound || res.Identifier != "tester@owned.test" || res.BindingRef == "" || res.BindingRef == prevRef {
		t.Fatalf("학습 로그인 %+v", res)
	}
	if got := target.requests()[before:]; len(got) != 2 || got[0] != "GET /allowed/signin" || got[1] != "POST /allowed/signin" {
		t.Fatalf("대상 요청 %v", got)
	}
	session, err := bindings.BoundSession(runRef, "tester-a", res.BindingRef)
	if err != nil || !strings.HasPrefix(session.Get("Cookie"), "sid=learned-session-") {
		t.Fatalf("새 세션 결속 %v %v", session, err)
	}
	value := strings.TrimPrefix(session.Get("Cookie"), "sid=")
	values := learnedSessionValues(csrfMethod(), session)
	if len(values) != 1 || string(values[0]) != value {
		t.Fatal("결속된 새 세션 값을 판정기에 전달하지 않음")
	}
	if after, err := lease.Execution.BudgetRemaining(); err != nil || after != modelBudget {
		t.Fatalf("학습 로그인이 모델 단계 예산을 씀: %d → %d %v", modelBudget, after, err)
	}
	contrast := learned.LoginContrast(ctx, "/allowed/me")
	if !contrast.Sent || contrast.Status != http.StatusUnauthorized || contrast.Reason != "" ||
		!learnedlogin.Blocked(contrast.Status, contrast.RedirectPath, res.LoginPaths) {
		t.Fatalf("대조 %+v", contrast)
	}
	// 집계 일치: 학습 로그인이 보낸 요청(화면 GET·로그인 POST)과 대조 GET은 모두 감사 완료된 다시 로그인(refresh_session)
	// 결과 사건이고(화면 target_requests의 login 몫), 확정 몫에서 나가 모델 단계 예산을 쓰지 않는다. 대상이 받은 수와 같다.
	written := 0
	for _, ev := range lease.Execution.Events(0, 0) {
		if ev.Stage == "result" && ev.RequestWritten && ev.Program == e2proxy.ProgramRefreshSession && ev.AuditComplete {
			written++
		}
	}
	if written != res.Requests+1 || len(target.requests())-before != res.Requests+1 {
		t.Fatalf("감사 사건 %d, 학습 로그인 %d + 대조 1, 대상 수신 %d", written, res.Requests, len(target.requests())-before)
	}
	if after, err := lease.Execution.BudgetRemaining(); err != nil || after != modelBudget {
		t.Fatalf("대조 요청이 모델 단계 예산을 씀: %d → %d %v", modelBudget, after, err)
	}
	// 간격 상한 안의 같은 계정 다시 로그인은 보내지 않는다.
	before = len(target.requests())
	if again := learned.LearnedRelogin(ctx, E2HTTPLearnedLoginRequest{Label: "tester-a", Method: csrfMethod(), MaxFailures: 3}); again.Result != E2HTTPRefreshCooldown || again.Requests != 0 {
		t.Fatalf("간격 안 다시 로그인 %+v", again)
	}
	if len(target.requests()) != before {
		t.Fatal("거부한 다시 로그인이 대상에 닿음")
	}
}

func TestE2HTTPFactoryLearnedReloginRejectsRegistration(t *testing.T) {
	target := &csrfTarget{csrf: map[string]string{}, sessions: map[string]bool{}}
	srv := httptest.NewServer(target.handler(t))
	defer srv.Close()
	lease, clock, ctx := learnedLease(t, srv, 8, "GET", "POST")
	learned := lease.AccountBindings().(E2HTTPLearnedRelogin)
	method := csrfMethod()
	method.Path = "/allowed/%73ignup"
	before := len(target.requests())
	clock.Advance(minSessionRefreshInterval)
	res := learned.LearnedRelogin(ctx, E2HTTPLearnedLoginRequest{Label: "tester-a", Method: method, MaxFailures: 3})
	if res.Result != E2HTTPAuthNotSent || res.Reason != learnedlogin.ReasonScopeDenied ||
		res.Transport != learnedlogin.TransportNotSent || res.Requests != 0 || len(target.requests()) != before {
		t.Fatalf("가입 경로를 학습 로그인으로 보내면 안 됨: %+v", res)
	}
}

// 학습 로그인은 값을 지어내지 않고 범위 밖으로 보내지 않는다: 쓰기 승인이 없는 실행, Worker 범위 밖 경로, 위험 단어 경로,
// 연속 실패 상한, 기록되지 않은 칸 값은 대상에 아무것도 보내지 않는다. 비밀번호가 틀리면 로그인 실패이고, 결과 불명 로그인은
// 다시 보내지 않는다.
func TestE2HTTPFactoryLearnedReloginRefusals(t *testing.T) {
	target := &csrfTarget{csrf: map[string]string{}, sessions: map[string]bool{}}
	srv := httptest.NewServer(target.handler(t))
	defer srv.Close()

	readOnly, clockRO, ctxRO := learnedLease(t, srv, 8, "GET")
	clockRO.Advance(minSessionRefreshInterval)
	before := len(target.requests())
	res := readOnly.AccountBindings().(E2HTTPLearnedRelogin).LearnedRelogin(ctxRO, E2HTTPLearnedLoginRequest{Label: "tester-a", Method: csrfMethod(), MaxFailures: 3})
	// 로그인 화면 GET은 읽기 범위라 나가지만 로그인 POST는 쓰기 승인이 없어 나가지 않는다.
	if res.Result != E2HTTPAuthNotSent || res.Reason != learnedlogin.ReasonScopeDenied || res.Transport != learnedlogin.TransportNotSent {
		t.Fatalf("읽기 전용 실행 %+v", res)
	}
	if got := target.requests()[before:]; len(got) != 1 || got[0] != "GET /allowed/signin" {
		t.Fatalf("읽기 전용 실행 대상 요청 %v", got)
	}

	lease, clock, ctx := learnedLease(t, srv, 16, "GET", "POST")
	learned := lease.AccountBindings().(E2HTTPLearnedRelogin)
	try := func(label string, m learnedlogin.Method, failures map[string]int) E2HTTPLearnedLoginResult {
		t.Helper()
		clock.Advance(minSessionRefreshInterval)
		return learned.LearnedRelogin(ctx, E2HTTPLearnedLoginRequest{Label: label, Method: m, PriorFailures: failures, MaxFailures: 3})
	}
	outside := csrfMethod()
	outside.Path = "/login"
	risky := csrfMethod()
	risky.Path = "/allowed/account/delete"
	encodedRisky := csrfMethod()
	encodedRisky.Path = "/allowed/account/%64elete"
	unknownField := csrfMethod()
	unknownField.OtherFields = []string{"_csrf", "remember"}
	before = len(target.requests())
	for name, tc := range map[string]struct {
		m        learnedlogin.Method
		failures map[string]int
		reason   learnedlogin.Reason
	}{
		"outside worker scope": {outside, nil, learnedlogin.ReasonScopeDenied},
		"risky path":           {risky, nil, learnedlogin.ReasonScopeDenied},
		"encoded risky path":   {encodedRisky, nil, learnedlogin.ReasonScopeDenied},
		"exhausted":            {csrfMethod(), map[string]int{"tester@owned.test": 3}, learnedlogin.ReasonAttemptsExhausted},
	} {
		if res := try("tester-a", tc.m, tc.failures); res.Result != E2HTTPAuthNotSent || res.Reason != tc.reason || res.Requests != 0 {
			t.Errorf("%s: %+v", name, res)
		}
	}
	if got := target.requests()[before:]; len(got) != 0 {
		t.Fatalf("거부한 학습 로그인이 대상에 닿음 %v", got)
	}
	// 기록되지 않은 체크 칸 값은 로그인 화면에도 없으므로 로그인 POST를 보내지 않는다(화면 GET 하나만).
	if res := try("tester-a", unknownField, nil); res.Result != E2HTTPAuthNotSent || res.Reason != learnedlogin.ReasonInputUnavailable || res.Requests != 1 {
		t.Fatalf("모르는 칸 %+v", res)
	}
	// 비밀번호가 틀린 계정: 로그인 화면을 다시 주므로 로그인 실패다.
	if res := try("tester-b", csrfMethod(), nil); res.Result != E2HTTPAuthLoginFailed || res.Reason != learnedlogin.ReasonRejected || !res.LoginFormReturned || res.Requests != 2 {
		t.Fatalf("틀린 비밀번호 %+v", res)
	}
	// 보내기 전 거부도 기존 다시 로그인처럼 계정별 연속 실패에 센다(입장한 다시 로그인이 세션을 주지 못함). 세 번째부터는
	// 그 계정 다시 로그인을 더 하지 않는다.
	if res := try("tester-a", csrfMethod(), nil); res.Result != E2HTTPRefreshLimit || res.Reason != learnedlogin.ReasonAttemptsExhausted || res.Requests != 0 {
		t.Fatalf("연속 실패 뒤 %+v", res)
	}
	// 결과 불명: 한 번만 보내고 다시 보내지 않는다(새 실행).
	lease, clock, ctx = learnedLease(t, srv, 16, "GET", "POST")
	learned = lease.AccountBindings().(E2HTTPLearnedRelogin)
	target.drop.Store(true)
	before = len(target.requests())
	if res := try("tester-a", csrfMethod(), nil); res.Result != E2HTTPAuthResultUnknown || res.Reason != learnedlogin.ReasonResultUnknown || res.Transport != learnedlogin.TransportUnknown {
		t.Fatalf("결과 불명 %+v", res)
	}
	posts := 0
	for _, r := range target.requests()[before:] {
		if r == "POST /allowed/signin" {
			posts++
		}
	}
	if posts != 1 {
		t.Fatalf("결과 불명 로그인 POST %d번", posts)
	}
}
