package e2proxy

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

// learnedTarget은 로그인 화면(GET)과 로그인(POST)에 답하고 받은 요청을 센다.
type learnedTarget struct {
	mu   sync.Mutex
	seen []string
}

func (l *learnedTarget) requests() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.seen...)
}

// learnedLoginFixture는 Worker 규칙 /allowed(methods)와 확정 몫(share)이 있는 v3 실행이다. excluded는 운영자 제외 경로다.
func learnedLoginFixture(t *testing.T, methods []string, share uint64, excluded ...string) (*Execution, *httptest.Server, *learnedTarget) {
	t.Helper()
	target := &learnedTarget{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		target.mu.Lock()
		target.seen = append(target.seen, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Cookie")+" "+string(body))
		target.mu.Unlock()
		switch {
		case r.URL.Path == "/allowed/login" && r.Method == "GET":
			http.SetCookie(w, &http.Cookie{Name: "pre", Value: "prelogin-cookie"})
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<input type="hidden" name="_csrf" value="t1"><input type="password" name="password">`)
		case r.URL.Path == "/allowed/login":
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "learned-login-session"})
			w.WriteHeader(http.StatusFound)
		case r.URL.Path == "/allowed/gz":
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = io.WriteString(w, "x")
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(server.Close)
	_, in := e3IdentityFixture(t, server.URL, &coreRecorder{})
	cfg := coreConfig(t, server.URL, &coreRecorder{}, 20)
	cfg.AuditVersion = JournalVersionV3
	cfg.E3Verifier = e3IdentityVerifier{}
	cfg.E3ConfirmationRequests = share
	cfg.ExcludedPaths = excluded
	cfg.Rules[0].Methods = methods
	// 제품은 시험 계정을 결속한 실행을 이 모드로 둔다(로그인 전 쿠키를 로그인 요청에 싣는다).
	cfg.HeaderMode = HeaderTestAccountPassthrough
	cfg.Rules = append(cfg.Rules,
		Rule{Ref: "identity-rule", Purpose: PurposeE3Identity, Origin: server.URL, ConnectIP: netip.MustParseAddr("127.0.0.1"),
			PathPrefix: "/allowed/identity", PathScopeRef: "identity-path", Methods: []string{"GET"}},
		Rule{Ref: "auth-login", Purpose: PurposeAuthPrep, Origin: server.URL, ConnectIP: netip.MustParseAddr("127.0.0.1"),
			PathPrefix: "/login", PathScopeRef: "auth-login-path", Methods: []string{"POST"}})
	e, err := NewExecution(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	value := in.Marker.Reveal()
	if err := e.RegisterProgramMarker(value); err != nil {
		t.Fatal(err)
	}
	clear(value)
	return e, server, target
}

func learnedCall(t *testing.T, e *Execution, e3 bool) *Call {
	t.Helper()
	open := func() (*Call, error) { return e.BeginProgramCall(t.Context(), ProgramRefreshSession, "auth-refresh") }
	if e3 {
		open = func() (*Call, error) { return e.BeginE3ReloginCall(t.Context(), "e3-relogin") }
	}
	c, err := open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = c.Close(t.Context()) })
	return c
}

func passHeader(h http.Header) AuthExtractor {
	return func(*http.Response, []byte) (http.Header, error) { return h, nil }
}

// 확정 단계 다시 로그인 호출은 학습 로그인 요청을 Worker 승인 규칙 안에서만 보내고, 응답은 판정 함수만 본다. 로그인 화면
// GET과 로그인 POST 모두 확정 몫으로 센다.
func TestLearnedLoginUsesWorkerScopeAndConfirmationShare(t *testing.T) {
	e, server, target := learnedLoginFixture(t, []string{"GET", "POST"}, 3)
	c := learnedCall(t, e, true)
	var page string
	prep, _ := http.NewRequest("GET", server.URL+"/allowed/login", nil)
	got, r, err := c.LearnedLogin(t.Context(), prep, PurposeLoginPrep, func(resp *http.Response, body []byte) (http.Header, error) {
		page = string(body)
		return http.Header{"Cookie": {resp.Cookies()[0].Name + "=" + resp.Cookies()[0].Value}}, nil
	})
	if err != nil || r.Reason != Complete || r.RuleRef != "rule" || !strings.Contains(page, `value="t1"`) || got.Get("Cookie") != "pre=prelogin-cookie" {
		t.Fatalf("로그인 화면 GET: %v %+v %q %v", err, r, page, got)
	}
	login, _ := http.NewRequest("POST", server.URL+"/allowed/login", strings.NewReader("u=a&p=b&_csrf=t1"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login.Header.Set("Cookie", "pre=prelogin-cookie")
	session, r, err := c.LearnedLogin(t.Context(), login, PurposeLearnedLogin, func(resp *http.Response, _ []byte) (http.Header, error) {
		if resp.StatusCode != http.StatusFound {
			return nil, errors.New("status")
		}
		return http.Header{"Cookie": {"sid=" + resp.Cookies()[0].Value}}, nil
	})
	if err != nil || session.Get("Cookie") != "sid=learned-login-session" || r.Reason != Complete {
		t.Fatalf("로그인 POST: %v %+v %v", err, r, session)
	}
	if seen := target.requests(); len(seen) != 2 || seen[1] != "POST /allowed/login pre=prelogin-cookie u=a&p=b&_csrf=t1" {
		t.Fatalf("대상이 받은 요청 %v", seen)
	}
	// 몫 3 가운데 둘을 썼다. 하나 남는다.
	anon, _ := http.NewRequest("GET", server.URL+"/allowed/identity", nil)
	if _, r, err := c.LearnedLogin(t.Context(), anon, PurposeLoginPrep, passHeader(http.Header{})); err != nil || r.Reason != Complete {
		t.Fatalf("세션 없는 대조 GET: %v %+v", err, r)
	}
	if _, r, _ := c.LearnedLogin(t.Context(), anon, PurposeLoginPrep, passHeader(http.Header{})); r.Reason != BudgetExhausted {
		t.Fatalf("확정 몫을 넘겨 보냄: %+v", r)
	}
}

// 쓰기 승인이 없는 실행(Worker 규칙이 GET뿐)은 학습 로그인 POST를 보내지 않는다. 질의·목적과 다른 메서드·본문 있는 GET·
// 제외 경로·Worker 승인 밖 경로도 보내지 않는다.
func TestLearnedLoginRefusesOutsideScope(t *testing.T) {
	e, server, target := learnedLoginFixture(t, []string{"GET"}, 10, "/allowed/private/*")
	c := learnedCall(t, e, true)
	post := func(path string) *http.Request {
		r, _ := http.NewRequest("POST", server.URL+path, strings.NewReader("u=a"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r
	}
	get := func(path string) *http.Request {
		r, _ := http.NewRequest("GET", server.URL+path, nil)
		return r
	}
	for name, tc := range map[string]struct {
		req     *http.Request
		purpose string
	}{
		"read-only POST":    {post("/allowed/login"), PurposeLearnedLogin},
		"outside scope":     {get("/other"), PurposeLoginPrep},
		"query":             {get("/allowed/login?next=/"), PurposeLoginPrep},
		"wrong method":      {get("/allowed/login"), PurposeLearnedLogin},
		"post as prep":      {post("/allowed/login"), PurposeLoginPrep},
		"excluded":          {get("/allowed/private/x"), PurposeLoginPrep},
		"auth rule not hit": {post("/login"), PurposeLearnedLogin},
	} {
		if _, r, err := c.LearnedLogin(t.Context(), tc.req, tc.purpose, passHeader(http.Header{})); err == nil || r.Reason != PolicyDenied || r.ForwardAttempted {
			t.Errorf("%s 보냄: %v %+v", name, err, r)
		}
	}
	body, _ := http.NewRequest("GET", server.URL+"/allowed/login", strings.NewReader("x"))
	if _, r, err := c.LearnedLogin(t.Context(), body, PurposeLoginPrep, passHeader(http.Header{})); err == nil || r.Reason != Unsupported {
		t.Errorf("본문 있는 GET 보냄: %v %+v", err, r)
	}
	if seen := target.requests(); len(seen) != 0 {
		t.Fatalf("범위 밖 요청이 대상에 닿음: %v", seen)
	}
}

// 학습 로그인 목적은 확정 단계 다시 로그인 호출만 쓴다. Worker refresh_session·모델 호출·처음 인증 준비는 쓰지 못한다.
func TestLearnedLoginOnlyOnConfirmationRelogin(t *testing.T) {
	e, server, target := learnedLoginFixture(t, []string{"GET", "POST"}, 10)
	req, _ := http.NewRequest("GET", server.URL+"/allowed/login", nil)
	if _, _, err := learnedCall(t, e, false).LearnedLogin(t.Context(), req, PurposeLoginPrep, passHeader(http.Header{})); !errors.Is(err, ErrConfig) {
		t.Fatalf("Worker refresh_session이 학습 로그인을 씀: %v", err)
	}
	model, err := e.BeginCall(t.Context(), "model")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = model.Close(t.Context()) }()
	if _, _, err := model.LearnedLogin(t.Context(), req, PurposeLoginPrep, passHeader(http.Header{})); !errors.Is(err, ErrConfig) {
		t.Fatalf("모델 호출이 학습 로그인을 씀: %v", err)
	}
	if _, _, err := learnedCall(t, e, true).LearnedLogin(t.Context(), req, PurposeAuthPrep, passHeader(http.Header{})); !errors.Is(err, ErrConfig) {
		t.Fatalf("다른 목적을 받음: %v", err)
	}
	if seen := target.requests(); len(seen) != 0 {
		t.Fatalf("대상에 닿음: %v", seen)
	}
}

// 판정 함수가 실패하면 세션을 돌려주지 않는다(응답은 받았으니 결과 불명이 아님). 압축 응답은 판정하지 않고 보류한다.
// 보호 비밀번호가 든 헤더는 돌려주지 않는다.
func TestLearnedLoginInspectFailuresAndWithheld(t *testing.T) {
	e, server, _ := learnedLoginFixture(t, []string{"GET", "POST"}, 10)
	c := learnedCall(t, e, true)
	req := func(path string) *http.Request {
		r, _ := http.NewRequest("GET", server.URL+path, nil)
		return r
	}
	if h, r, err := c.LearnedLogin(t.Context(), req("/allowed/login"), PurposeLoginPrep, func(*http.Response, []byte) (http.Header, error) {
		return nil, errors.New("no field")
	}); !errors.Is(err, ErrAuthSession) || h != nil || r.Reason != Complete {
		t.Fatalf("판정 실패: %v %+v %v", err, r, h)
	}
	if _, r, err := c.LearnedLogin(t.Context(), req("/allowed/gz"), PurposeLoginPrep, passHeader(http.Header{})); !errors.Is(err, ErrWithheld) || r.Reason != Withheld {
		t.Fatalf("압축 응답: %v %+v", err, r)
	}
	if err := e.AddProtectedPasswords([]byte("registered-password-1")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.LearnedLogin(t.Context(), req("/allowed/login"), PurposeLoginPrep, passHeader(http.Header{"Cookie": {"x=registered-password-1"}})); !errors.Is(err, ErrAuthSession) {
		t.Fatalf("비밀번호가 든 세션을 돌려줌: %v", err)
	}
}

// 건별 승인을 받는 실행(수동·사용자 승인)에서는 학습 로그인 POST와 로그인 화면 GET도 Worker 요청처럼 승인을 받는다. 승인이
// 거절되면 대상에 보내지 않는다. 처음 인증 준비(로그인 설정 9칸)는 지금처럼 승인 대기가 없다.
func TestLearnedLoginFollowsPerRequestApproval(t *testing.T) {
	e, server, target := learnedLoginFixture(t, []string{"GET", "POST"}, 10)
	gate := &refuseApproval{}
	e.cfg.ApprovalMode, e.cfg.RequestApproval = ApprovalConfirmEveryRequest, gate
	c := learnedCall(t, e, true)
	prep, _ := http.NewRequest("GET", server.URL+"/allowed/login", nil)
	if _, r, err := c.LearnedLogin(t.Context(), prep, PurposeLoginPrep, passHeader(http.Header{})); err == nil || gate.asked != 1 || r.ForwardAttempted {
		t.Fatalf("로그인 화면 GET이 승인 없이 나감: %v %+v asked=%d", err, r, gate.asked)
	}
	login, _ := http.NewRequest("POST", server.URL+"/allowed/login", strings.NewReader("u=a"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, r, err := c.LearnedLogin(t.Context(), login, PurposeLearnedLogin, passHeader(http.Header{})); err == nil || gate.asked != 2 || r.ForwardAttempted {
		t.Fatalf("로그인 POST가 승인 없이 나감: %v %+v asked=%d", err, r, gate.asked)
	}
	if seen := target.requests(); len(seen) != 0 {
		t.Fatalf("승인 거절된 요청이 대상에 닿음: %v", seen)
	}
}
