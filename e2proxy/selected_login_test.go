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

// selectedLoginFixture는 선택 후보 검증 실행이다: 계획 행동 규칙(/allowed GET), 승인 계획에 결속한 로그인 POST 규칙
// (/login, auth_prep)과 준비 GET 규칙(/csrf, login_prep). max는 실행 요청 예산이다.
func selectedLoginFixture(t *testing.T, max uint64, excluded ...string) (*Execution, *httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Cookie")+" "+string(body))
		mu.Unlock()
		switch {
		case r.URL.Path == "/csrf" && r.Method == http.MethodGet:
			http.SetCookie(w, &http.Cookie{Name: "pre", Value: "prelogin-cookie", Path: "/"})
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"csrf":"synthetic-csrf-token"}`)
		case r.URL.Path == "/login" && r.Method == http.MethodPost:
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "selected-login-session", Path: "/"})
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(server.Close)
	cfg := coreConfig(t, server.URL, &coreRecorder{}, max)
	cfg.ExcludedPaths = excluded
	cfg.HeaderMode = HeaderTestAccountPassthrough
	cfg.SelectedAccountRef = strings.Repeat("a", 64)
	cfg.ResolveSentAccount = func(http.Header) string { return "" }
	cfg.SelectedSessionHeaders = func() (http.Header, bool) { return nil, false }
	cfg.Rules = append(cfg.Rules,
		Rule{Ref: "auth-login", Purpose: PurposeAuthPrep, Origin: server.URL, ConnectIP: netip.MustParseAddr("127.0.0.1"),
			PathPrefix: "/login", PathScopeRef: "auth-login-path", Methods: []string{"POST"}},
		Rule{Ref: "auth-login-prep", Purpose: PurposeLoginPrep, Origin: server.URL, ConnectIP: netip.MustParseAddr("127.0.0.1"),
			PathPrefix: "/csrf", PathScopeRef: "auth-login-prep-path", Methods: []string{"GET"}})
	e, err := NewExecution(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e, server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

func selectedLoginCall(t *testing.T, e *Execution) *Call {
	t.Helper()
	c, err := e.BeginSelectedLoginCall(t.Context(), "selected-learned-login")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = c.Close(t.Context()) })
	return c
}

// 선택 실행의 학습 로그인은 계획에 결속한 준비 GET·로그인 POST 규칙으로만 나가고, 인증 준비 귀속으로 실행 예산을 쓴다.
func TestSelectedLoginUsesPlanBoundRulesAndRunBudget(t *testing.T) {
	e, server, seen := selectedLoginFixture(t, 2)
	c := selectedLoginCall(t, e)
	prep, _ := http.NewRequest(http.MethodGet, server.URL+"/csrf", nil)
	var page string
	got, r, err := c.LearnedLogin(t.Context(), prep, PurposeLoginPrep, func(resp *http.Response, body []byte) (http.Header, error) {
		page = string(body)
		return http.Header{"Cookie": {"pre=" + resp.Cookies()[0].Value}}, nil
	})
	if err != nil || r.Reason != Complete || r.RuleRef != "auth-login-prep" || r.Program != ProgramAuthPrep || !strings.Contains(page, "synthetic-csrf-token") ||
		got.Get("Cookie") != "pre=prelogin-cookie" {
		t.Fatalf("준비 GET: %v %+v %q %v", err, r, page, got)
	}
	login, _ := http.NewRequest(http.MethodPost, server.URL+"/login", strings.NewReader(`{"csrf":"synthetic-csrf-token"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Cookie", "pre=prelogin-cookie")
	session, r, err := c.LearnedLogin(t.Context(), login, PurposeLearnedLogin, func(resp *http.Response, _ []byte) (http.Header, error) {
		return http.Header{"Cookie": {"sid=" + resp.Cookies()[0].Value}}, nil
	})
	if err != nil || r.Reason != Complete || r.RuleRef != "auth-login" || session.Get("Cookie") != "sid=selected-login-session" {
		t.Fatalf("로그인 POST: %v %+v %v", err, r, session)
	}
	// 예산 2를 다 썼다. 계획에 없는 세 번째 요청은 나가지 않는다.
	again, _ := http.NewRequest(http.MethodGet, server.URL+"/csrf", nil)
	if _, r, _ := c.LearnedLogin(t.Context(), again, PurposeLoginPrep, passHeader(http.Header{})); r.Reason != BudgetExhausted || r.ForwardAttempted {
		t.Fatalf("예산을 넘겨 보냄: %+v", r)
	}
	if got := seen(); len(got) != 2 || got[1] != `POST /login pre=prelogin-cookie {"csrf":"synthetic-csrf-token"}` {
		t.Fatalf("대상이 받은 요청: %v", got)
	}
}

// 전용 호출도 계획 밖 경로·메서드·질의·제외 경로·Worker 행동 규칙으로는 보내지 않는다. 다른 목적은 쓰지 못한다.
func TestSelectedLoginRefusesOutsidePlan(t *testing.T) {
	e, server, seen := selectedLoginFixture(t, 20, "/csrf/private")
	c := selectedLoginCall(t, e)
	get := func(path string) *http.Request {
		r, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		return r
	}
	post := func(path string) *http.Request {
		r, _ := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader("u=a"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r
	}
	for name, tc := range map[string]struct {
		req     *http.Request
		purpose string
	}{
		"worker rule as prep":  {get("/allowed"), PurposeLoginPrep},
		"worker rule as login": {post("/allowed"), PurposeLearnedLogin},
		"prep query":           {get("/csrf?next=/"), PurposeLoginPrep},
		"prep other path":      {get("/csrf/other"), PurposeLoginPrep},
		"login as prep":        {get("/login"), PurposeLoginPrep},
		"prep as login":        {post("/csrf"), PurposeLearnedLogin},
		"login query":          {post("/login?x=1"), PurposeLearnedLogin},
		"excluded":             {get("/csrf/private"), PurposeLoginPrep},
	} {
		if _, r, err := c.LearnedLogin(t.Context(), tc.req, tc.purpose, passHeader(http.Header{})); err == nil || r.ForwardAttempted ||
			r.Reason != PolicyDenied && r.Reason != Unsupported {
			t.Errorf("%s 보냄: %v %+v", name, err, r)
		}
	}
	if _, _, err := c.AuthPrep(t.Context(), post("/login"), passHeader(http.Header{})); !errors.Is(err, ErrConfig) {
		t.Errorf("선택 로그인 호출이 설정 로그인(auth_prep)을 보냄: %v", err)
	}
	if got := seen(); len(got) != 0 {
		t.Fatalf("계획 밖 요청이 대상에 닿음: %v", got)
	}
}

// 전용 호출·준비 GET 규칙은 선택 실행에서만 있다. 다른 호출(Worker·인증 준비·Worker refresh_session·확정 단계 다시 로그인)은
// 전용 규칙으로 학습 로그인을 보내지 못한다.
func TestSelectedLoginOnlyForSelectedRun(t *testing.T) {
	plain := coreConfig(t, "https://selected.test", &coreRecorder{}, 5)
	open, err := NewExecution(t.Context(), plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := open.BeginSelectedLoginCall(t.Context(), "selected-learned-login"); !errors.Is(err, ErrConfig) {
		t.Fatalf("선택 실행이 아닌데 전용 호출을 엶: %v", err)
	}
	plain.Rules = append(plain.Rules, Rule{Ref: "auth-login-prep", Purpose: PurposeLoginPrep, Origin: "https://selected.test",
		ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/csrf", PathScopeRef: "auth-login-prep-path", Methods: []string{"GET"}})
	if _, err := NewExecution(t.Context(), plain); !errors.Is(err, ErrConfig) {
		t.Fatalf("선택 실행이 아닌 실행이 준비 GET 규칙을 받음: %v", err)
	}
	e, server, seen := selectedLoginFixture(t, 20)
	prep, _ := http.NewRequest(http.MethodGet, server.URL+"/csrf", nil)
	for _, kind := range []ProgramCallKind{ProgramAuthPrep, ProgramRefreshSession} {
		call, err := e.BeginProgramCall(t.Context(), kind, "not-selected-login")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := call.LearnedLogin(t.Context(), prep, PurposeLoginPrep, passHeader(http.Header{})); !errors.Is(err, ErrConfig) {
			t.Errorf("%s 호출이 학습 로그인 목적을 씀: %v", kind, err)
		}
		_, _ = call.Close(t.Context())
	}
	model, err := e.BeginCall(t.Context(), "model")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := model.LearnedLogin(t.Context(), prep, PurposeLoginPrep, passHeader(http.Header{})); !errors.Is(err, ErrConfig) {
		t.Errorf("모델 호출이 학습 로그인 목적을 씀: %v", err)
	}
	_, _ = model.Close(t.Context())
	relogin, err := e.BeginE3ReloginCall(t.Context(), "e3-relogin")
	if err != nil {
		t.Fatal(err)
	}
	if _, r, err := relogin.LearnedLogin(t.Context(), prep, PurposeLoginPrep, passHeader(http.Header{})); err == nil || r.ForwardAttempted {
		t.Errorf("확정 단계 다시 로그인이 선택 실행 전용 규칙으로 보냄: %v %+v", err, r)
	}
	_, _ = relogin.Close(t.Context())
	if got := seen(); len(got) != 0 {
		t.Fatalf("대상에 닿음: %v", got)
	}
}
