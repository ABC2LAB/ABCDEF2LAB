package e2proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// authTarget은 로그인 경로에 Set-Cookie로 답하고, 받은 요청(메서드·경로·본문)을 센다.
type authTarget struct {
	mu   sync.Mutex
	seen []string
}

func newAuthTarget(t *testing.T) (*authTarget, *httptest.Server) {
	t.Helper()
	a := &authTarget{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		a.seen = append(a.seen, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		a.mu.Unlock()
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "seeded-session-value-55cc"})
			io.WriteString(w, `{"ok":true}`)
			return
		}
		io.WriteString(w, "page")
	}))
	t.Cleanup(srv.Close)
	return a, srv
}

func (a *authTarget) requests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.seen...)
}

func authConfig(t *testing.T, origin string) Config {
	t.Helper()
	cfg := coreConfig(t, origin, &coreRecorder{}, 10)
	cfg.Rules = append(cfg.Rules, Rule{Ref: "auth-login", Origin: origin, ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/login", PathScopeRef: "auth-login-path", Methods: []string{"POST"}, Purpose: PurposeAuthPrep})
	return cfg
}

func loginRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	r, err := http.NewRequest("POST", url, strings.NewReader(`{"email":"tester","password":"seeded-pass"}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	return r
}

func cookieExtractor(name string) AuthExtractor {
	return func(resp *http.Response, _ []byte) (http.Header, error) {
		for _, c := range resp.Cookies() {
			if c.Name == name && c.Value != "" {
				return http.Header{"Cookie": {c.Name + "=" + c.Value}}, nil
			}
		}
		return nil, errors.New("no session cookie")
	}
}

// 인증 준비 규칙은 POST 하나·정확한 경로·보호 자격 없음만 받는다.
func TestAuthPrepRuleValidation(t *testing.T) {
	base := func() Rule {
		return Rule{Ref: "auth-login", Origin: "http://127.0.0.1:1", ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/login", PathScopeRef: "auth", Methods: []string{"POST"}, Purpose: PurposeAuthPrep}
	}
	if _, err := compileRules([]Rule{base()}); err != nil {
		t.Fatal("valid auth rule rejected:", err)
	}
	for name, change := range map[string]func(*Rule){
		"get":            func(r *Rule) { r.Methods = []string{"GET"} },
		"two methods":    func(r *Rule) { r.Methods = []string{"POST", "PUT"} },
		"root path":      func(r *Rule) { r.PathPrefix = "/" },
		"trailing slash": func(r *Rule) { r.PathPrefix = "/login/" },
		"credentials":    func(r *Rule) { r.CredentialHeaders = http.Header{"Authorization": {"Bearer x"}} },
		"headers":        func(r *Rule) { r.Headers = http.Header{"X-Fixed": {"1"}} },
		"unknown":        func(r *Rule) { r.Purpose = "admin" },
	} {
		r := base()
		change(&r)
		if _, err := compileRules([]Rule{r}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// 모델 요청(Do)은 인증 준비 규칙을 쓰지 못한다: 쓰기 승인이 없는 실행에서 로그인 경로 POST는 정책 거부이고 대상에 닿지 않는다.
func TestModelRequestsCannotUseAuthPrepRule(t *testing.T) {
	target, srv := newAuthTarget(t)
	_, call := coreOpen(t, authConfig(t, srv.URL))
	if _, r, err := call.Do(context.Background(), loginRequest(t, srv.URL+"/login")); err == nil || r.Reason != PolicyDenied || r.ForwardAttempted {
		t.Fatalf("model POST used the auth rule: %v %+v", err, r)
	}
	if got := target.requests(); len(got) != 0 {
		t.Fatalf("target reached: %v", got)
	}
}

// 인증 준비는 로그인 경로 하나에만 보내고, 응답(Set-Cookie 포함)은 extract만 보며, 돌려주는 것은 보낼 세션 헤더다.
// 진단 요청 경계는 거치지 않고(알리지도 않음), 예산은 센다.
func TestAuthPrepSendsOnlyToLoginPathAndReturnsSession(t *testing.T) {
	target, srv := newAuthTarget(t)
	cfg := authConfig(t, srv.URL)
	boundary := &denyAllBoundary{}
	cfg.RequestBoundary = boundary
	_, call := coreOpen(t, cfg)
	before, _ := cfg.Budget.Ledger.Remaining(cfg.Budget.Key, cfg.Budget.Binding, time.Now())
	session, r, err := call.AuthPrep(context.Background(), loginRequest(t, srv.URL+"/login"), cookieExtractor("session"))
	if err != nil || r.RuleRef != "auth-login" || r.Reason != Complete || !r.ResponseComplete || session.Get("Cookie") != "session=seeded-session-value-55cc" {
		t.Fatalf("auth prep: %v %+v %v", err, r, session)
	}
	after, _ := cfg.Budget.Ledger.Remaining(cfg.Budget.Key, cfg.Budget.Binding, time.Now())
	if before-after != 1 {
		t.Fatalf("auth prep not counted in the request budget: %d -> %d", before, after)
	}
	if boundary.before != 0 || boundary.after != 0 {
		t.Fatalf("auth prep went through the diagnosis request boundary: %+v", boundary)
	}
	if got := target.requests(); len(got) != 1 || got[0] != `POST /login {"email":"tester","password":"seeded-pass"}` {
		t.Fatalf("target saw %v", got)
	}
	// 형제 경로·하위 경로·query·GET은 규칙에 맞지 않아 보내지 않는다.
	for _, bad := range []*http.Request{loginRequest(t, srv.URL+"/login2"), loginRequest(t, srv.URL+"/login/x"), loginRequest(t, srv.URL+"/login?next=/")} {
		if _, r, err := call.AuthPrep(context.Background(), bad, cookieExtractor("session")); err == nil || r.Reason != PolicyDenied {
			t.Errorf("%s allowed: %v %+v", bad.URL, err, r)
		}
	}
	get, _ := http.NewRequest("GET", srv.URL+"/login", nil)
	if _, r, err := call.AuthPrep(context.Background(), get, cookieExtractor("session")); err == nil || r.Reason != PolicyDenied {
		t.Errorf("GET login allowed: %v %+v", err, r)
	}
	if got := target.requests(); len(got) != 1 {
		t.Fatalf("refused auth requests reached the target: %v", got)
	}
}

// 세션을 찾지 못하면 ErrAuthSession이고(로그인 실패), 응답은 받았으니 결과 불명이 아니다.
func TestAuthPrepSessionNotFound(t *testing.T) {
	_, srv := newAuthTarget(t)
	_, call := coreOpen(t, authConfig(t, srv.URL))
	session, r, err := call.AuthPrep(context.Background(), loginRequest(t, srv.URL+"/login"), cookieExtractor("other"))
	if !errors.Is(err, ErrAuthSession) || session != nil || r.Reason != Complete || r.RuleRef != "auth-login" {
		t.Fatalf("missing session: %v %+v %v", err, r, session)
	}
}

// 응답이 상한을 넘으면 끝까지 보지 못했으니 세션을 뽑지 않는다(extract를 부르지 않음, 보류).
func TestAuthPrepOversizedResponseIsNotExtracted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "seeded-session-value-66dd"})
		io.WriteString(w, strings.Repeat("x", 4096))
	}))
	defer srv.Close()
	cfg := authConfig(t, srv.URL)
	cfg.Limits.MaxBodyBytes = 1024
	_, call := coreOpen(t, cfg)
	called := false
	extract := func(*http.Response, []byte) (http.Header, error) {
		called = true
		return http.Header{"Cookie": {"x"}}, nil
	}
	if _, r, err := call.AuthPrep(context.Background(), loginRequest(t, srv.URL+"/login"), extract); !errors.Is(err, ErrWithheld) || called || r.Reason != Withheld {
		t.Fatalf("oversized auth response: %v called=%v %+v", err, called, r)
	}
}

// 출력 가림 전용 값은 도구 출력에서 가리지만 응답 표현 보류에는 쓰지 않는다(모델 요청 응답에 그 값이 있어도 전달).
func TestRedactOnlyValuesDoNotWithholdResponses(t *testing.T) {
	const session = "seeded-session-value-77ee"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "token="+session)
	}))
	defer srv.Close()
	e, call := coreOpen(t, coreConfig(t, srv.URL, &coreRecorder{}, 3))
	if err := e.AddRedactValues([]byte(session)); err != nil {
		t.Fatal(err)
	}
	resp, r, err := call.Do(context.Background(), coreRequest(t, "GET", srv.URL+"/allowed"))
	if err != nil || resp == nil || r.Reason != Complete {
		t.Fatalf("response with a redact-only value was withheld: %v %+v", err, r)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), session) {
		t.Fatal("transport changed the response body")
	}
	if out := string(e.RedactOutput(body)); strings.Contains(out, session) || !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("redact-only value not masked in output: %q", out)
	}
	for name, v := range map[string][]byte{"short": []byte("short"), "huge": make([]byte, maxRedactValueBytes+1)} {
		if e.AddRedactValues(v) == nil {
			t.Errorf("%s redact value accepted", name)
		}
	}
}

type denyAllBoundary struct{ before, after int }

func (b *denyAllBoundary) Before(context.Context, RequestView) error {
	b.before++
	return errors.New("denied by workflow")
}
func (b *denyAllBoundary) PreapprovedRead(RequestView) bool { return false }
func (b *denyAllBoundary) After(context.Context, RequestView, Receipt) error {
	b.after++
	return nil
}

type refuseApproval struct{ asked int }

func (g *refuseApproval) AwaitRequestApproval(context.Context, RequestApprovalProposal) (bool, error) {
	g.asked++
	return false, nil
}

// 요청마다 확인하는 실행에서도 인증 준비는 확인을 기다리지 않는다(대상 등록 때 승인한 로그인 경로 하나). 모델 요청은 그대로 확인을 거친다.
func TestAuthPrepSkipsPerRequestApproval(t *testing.T) {
	target, srv := newAuthTarget(t)
	cfg := authConfig(t, srv.URL)
	gate := &refuseApproval{}
	cfg.ApprovalMode, cfg.RequestApproval = ApprovalConfirmEveryRequest, gate
	_, call := coreOpen(t, cfg)
	if _, r, err := call.AuthPrep(context.Background(), loginRequest(t, srv.URL+"/login"), cookieExtractor("session")); err != nil || r.Reason != Complete || gate.asked != 0 {
		t.Fatalf("auth prep waited on approval: %v %+v asked=%d", err, r, gate.asked)
	}
	if _, r, err := call.Do(context.Background(), coreRequest(t, "GET", srv.URL+"/allowed")); err == nil || gate.asked != 1 || r.Reason != PolicyDenied {
		t.Fatalf("model request skipped approval: %v %+v asked=%d", err, r, gate.asked)
	}
	if got := target.requests(); len(got) != 1 {
		t.Fatalf("target saw %v", got)
	}
}

// 실행 뒤에 더한 보호 값(인증 준비 비밀번호)은 대상 응답에 그대로 있으면 그 응답을 통째로 보류하고 출력에서도 가린다.
func TestAddProtectedValuesWithholdReflections(t *testing.T) {
	const password = "seeded-password-33dd"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/allowed/echo" {
			io.WriteString(w, "your password is "+password)
			return
		}
		io.WriteString(w, "plain page")
	}))
	defer srv.Close()
	e, call := coreOpen(t, coreConfig(t, srv.URL, &coreRecorder{}, 5))
	if err := e.AddProtectedValues([]byte(password)); err != nil {
		t.Fatal(err)
	}
	if resp, r, err := call.Do(context.Background(), coreRequest(t, "GET", srv.URL+"/allowed/echo")); !errors.Is(err, ErrWithheld) || resp != nil || r.Reason != Withheld {
		t.Fatalf("reflected protected value delivered: %v %+v", err, r)
	}
	if resp, _, err := call.Do(context.Background(), coreRequest(t, "GET", srv.URL+"/allowed/page")); err != nil || resp == nil {
		t.Fatalf("unrelated response withheld: %v", err)
	}
	if out := string(e.RedactOutput([]byte("x " + password))); strings.Contains(out, password) {
		t.Fatal("protected value not redacted")
	}
	for name, v := range map[string][]byte{"short": []byte("12345"), "huge": make([]byte, maxRedactValueBytes+1)} {
		if e.AddProtectedValues(v) == nil {
			t.Errorf("%s protected value accepted", name)
		}
	}
	if e.AddProtectedValues() == nil {
		t.Error("empty protected value list accepted")
	}
}

// 검사 상한을 설정으로 고정한 실행에서는 상한 사이 조건을 깨는 보호 값을 받지 않는다(반사 검사가 전달 구간 끝을 놓치지 않게).
func TestAddProtectedValuesKeepsScanCondition(t *testing.T) {
	cfg := coreConfig(t, "http://127.0.0.1:1", &coreRecorder{}, 1)
	cfg.Limits.MaxResponseBodyBytes = 4096
	cfg.Limits.MaxResponseScanBytes = 4096 + 9
	e, _ := coreOpen(t, cfg)
	if err := e.AddProtectedValues([]byte("ten-bytes!")); err != nil {
		t.Fatalf("value within the scan condition rejected: %v", err)
	}
	if err := e.AddProtectedValues([]byte("eleven-byte")); err == nil {
		t.Fatal("value breaking the scan condition accepted")
	}
}
