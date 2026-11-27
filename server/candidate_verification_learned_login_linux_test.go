package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/learnedlogin"
)

// selectedLoginTarget은 소유 합성 대상이다. 로그인 방식마다 새 CSRF·로그인 전 쿠키를 주고, 짝이 맞는 로그인만 새 세션을
// 준다. /read는 지금 세션(쿠키 또는 Bearer)이 있을 때만 200이다. 받은 요청을 순서대로 남긴다.
type selectedLoginTarget struct {
	mu       sync.Mutex
	seen     []string
	csrf     map[string]string
	sessions map[string]bool
	n        int
	drop     atomic.Bool
	noField  atomic.Bool
}

func newSelectedLoginTarget() *selectedLoginTarget {
	return &selectedLoginTarget{csrf: map[string]string{}, sessions: map[string]bool{}}
}

func (c *selectedLoginTarget) issue(w http.ResponseWriter) (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	pre, token := fmt.Sprintf("pre-login-%04d", c.n), fmt.Sprintf("csrf-token-%04d", c.n)
	c.csrf[pre] = token
	http.SetCookie(w, &http.Cookie{Name: "pre", Value: pre, Path: "/"})
	return pre, token
}

func (c *selectedLoginTarget) accept(r *http.Request, token, user, pass string) bool {
	pre, _ := r.Cookie("pre")
	c.mu.Lock()
	defer c.mu.Unlock()
	ok := pre != nil && c.csrf[pre.Value] != "" && token == c.csrf[pre.Value] && user == "tester@owned.test" && pass == "seeded-pass-88ff"
	if pre != nil {
		delete(c.csrf, pre.Value)
	}
	return ok
}

func (c *selectedLoginTarget) newSession() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	value := fmt.Sprintf("selected-session-%04d-value", c.n)
	c.sessions[value] = true
	return value
}

func (c *selectedLoginTarget) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.seen = append(c.seen, r.Method+" "+r.URL.Path)
		c.mu.Unlock()
		switch {
		case r.URL.Path == "/api/csrf" && r.Method == http.MethodGet:
			_, token := c.issue(w)
			w.Header().Set("Content-Type", "application/json")
			if c.noField.Load() {
				_, _ = io.WriteString(w, `{"other":"x"}`)
				return
			}
			fmt.Fprintf(w, `{"csrf":%q}`, token)
		case r.URL.Path == "/session/new" && r.Method == http.MethodGet:
			_, token := c.issue(w)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<div><input type="hidden" name="authenticity_token" value="%s"></div>`, token)
		case r.URL.Path == "/signin" && r.Method == http.MethodGet:
			_, token := c.issue(w)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<form method="post"><input type="hidden" name="_csrf" value="%s"><input name="email"><input type="password" name="password"></form>`, token)
		case r.URL.Path == "/signin" && r.Method == http.MethodPost:
			form, _ := parseFormBody(body)
			if !c.accept(r, form["_csrf"], form["email"], form["password"]) {
				w.Header().Set("Content-Type", "text/html")
				_, _ = io.WriteString(w, `<form><input type="password" name="password"></form>`)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: c.newSession(), Path: "/"})
			w.Header().Set("Location", "/home")
			w.WriteHeader(http.StatusFound)
		case r.URL.Path == "/api/login" && r.Method == http.MethodPost:
			if c.drop.Load() {
				hj, _ := w.(http.Hijacker)
				conn, _, _ := hj.Hijack()
				_ = conn.Close()
				return
			}
			var in map[string]string
			if json.Unmarshal(body, &in) != nil || !c.accept(r, in["csrf"], in["email"], in["password"]) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: c.newSession(), Path: "/"})
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/token" && r.Method == http.MethodPost:
			var in map[string]string
			if json.Unmarshal(body, &in) != nil || in["email"] != "tester@owned.test" || in["password"] != "seeded-pass-88ff" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"auth":{"token":%q}}`, c.newSession())
		case r.URL.Path == "/read":
			value := ""
			if sid, err := r.Cookie("sid"); err == nil {
				value = sid.Value
			}
			if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
				value = bearer
			}
			c.mu.Lock()
			valid := c.sessions[value]
			c.mu.Unlock()
			if !valid {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"items":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func parseFormBody(body []byte) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(string(body), "&") {
		key, value, _ := strings.Cut(part, "=")
		out[key] = strings.ReplaceAll(strings.ReplaceAll(value, "%40", "@"), "+", " ")
	}
	return out, nil
}

func (c *selectedLoginTarget) requests() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

// 선택 후보 학습 방법 네 가지: 별도 경로 JSON CSRF(v2), 별도 경로 HTML hidden CSRF(v2), 같은 경로 Form CSRF(v1), 준비
// 없는 JSON 토큰(v1).
func selectedJSONCSRFMethod() learnedlogin.Method {
	return learnedlogin.Method{Method: "POST", Path: "/api/login", Format: learnedlogin.FormatJSON, IdentifierField: "email", PasswordField: "password",
		OtherFields: []string{"csrf"}, FieldsFromResponse: []string{"csrf"}, SessionSource: learnedlogin.SourceSetCookie, SessionNames: []string{"sid"},
		SessionSentAs: learnedlogin.SentAsCookie, Schema: learnedlogin.MethodSchemaV2,
		FieldSources: []learnedlogin.FieldSource{{Destination: "csrf", SourceMethod: "GET", SourcePath: "/api/csrf", SourceType: learnedlogin.SourceJSONField, SourceField: "csrf"}}}
}

func selectedHiddenCSRFMethod() learnedlogin.Method {
	m := selectedJSONCSRFMethod()
	m.FieldSources = []learnedlogin.FieldSource{{Destination: "csrf", SourceMethod: "GET", SourcePath: "/session/new", SourceType: learnedlogin.SourceHTMLHidden, SourceField: "authenticity_token"}}
	return m
}

func selectedFormCSRFMethod() learnedlogin.Method {
	return learnedlogin.Method{Method: "POST", Path: "/signin", Format: learnedlogin.FormatForm, IdentifierField: "email", PasswordField: "password",
		OtherFields: []string{"_csrf"}, FieldsFromResponse: []string{"_csrf"}, SessionSource: learnedlogin.SourceSetCookie, SessionNames: []string{"sid"},
		SessionSentAs: learnedlogin.SentAsCookie}
}

func selectedTokenMethod() learnedlogin.Method {
	return learnedlogin.Method{Method: "POST", Path: "/api/token", Format: learnedlogin.FormatJSON, IdentifierField: "email", PasswordField: "password",
		OtherFields: []string{}, FieldsFromResponse: []string{}, SessionSource: learnedlogin.SourceJSONField, SessionNames: []string{"auth.token"},
		SessionSentAs: "Authorization:Bearer "}
}

// selectedLearnedPolicy는 선택 후보 학습 로그인 실행 정책이다: 계획 행동 /read GET 하나, 로그인 POST 규칙, (준비가 필요하면)
// 준비 GET 규칙. 실행 예산은 행동 1 + 준비 요청 수다.
func selectedLearnedPolicy(t *testing.T, target *httptest.Server, m learnedlogin.Method, password string) (FirstDiagnosisHTTPHostConfig, E2HTTPFilePolicy) {
	t.Helper()
	cfg, _, policy := e2HostPolicyFixture(t)
	policy.Rules[0].Origin, policy.Rules[0].PathPrefix, policy.Rules[0].PathMatch, policy.Rules[0].Methods = target.URL, "/read", "exact", []string{"GET"}
	policy.Rules = append(policy.Rules, E2HTTPFileRule{Ref: "auth-login", Origin: target.URL, ConnectIP: "127.0.0.1",
		PathPrefix: m.Path, PathScopeRef: "auth-login-path", Methods: []string{"POST"}, Purpose: e2proxy.PurposeAuthPrep})
	requests := 1
	auth := E2HTTPFileAuth{RuleRef: "auth-login", LearnedMethod: &m}
	if m.NeedsPrep() {
		policy.Rules = append(policy.Rules, E2HTTPFileRule{Ref: "auth-login-prep", Origin: target.URL, ConnectIP: "127.0.0.1",
			PathPrefix: m.PrepPath(), PathScopeRef: "auth-login-prep-path", Methods: []string{"GET"}, Purpose: e2proxy.PurposeLoginPrep})
		auth.PrepRuleRef = "auth-login-prep"
		requests++
	}
	pin := authCredentialPin(t, cfg.AuthorityRoot, "selected-account.json", "tester@owned.test", password)
	auth.Accounts = []E2HTTPFileAuthAccount{{Label: "tester-a", Credential: pin}}
	policy.Auth, policy.HeaderMode = &auth, e2proxy.HeaderTestAccountPassthrough
	policy.SelectedAccountRef = CandidateAccountRevision("tester-a", pin.SHA256)
	policy.MaxRequests = uint64(1 + requests)
	policy.Limits.MaxAttempts, policy.Limits.MaxCalls = uint64(1+requests), 1+requests
	return cfg, policy
}

func acquireSelectedLearned(t *testing.T, cfg FirstDiagnosisHTTPHostConfig, policy E2HTTPFilePolicy) E2HTTPRunOptions {
	t.Helper()
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0o600) != nil {
		t.Fatal("합성 선택 정책 준비 실패")
	}
	if _, _, err := ValidateE2HTTPRunPolicyDocument(raw); err != nil {
		t.Fatal("선택 학습 로그인 정책을 거부함", err)
	}
	options, err := LoadE2HTTPRuntimeOptions(t.Context(), E2HTTPFileOptions{PolicyFile: cfg.PolicyFile,
		Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := options.Acquire(t.Context(), E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 27, IntentID: 31})
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
	return lease
}

func selectedRead(t *testing.T, lease E2HTTPRunOptions, target *httptest.Server) (e2proxy.Receipt, error) {
	t.Helper()
	call, err := lease.Execution.BeginCall(t.Context(), "selected-read")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = call.Close(t.Context()) }()
	request, _ := http.NewRequest(http.MethodGet, target.URL+"/read", nil)
	response, receipt, err := call.Do(t.Context(), request)
	if response != nil {
		response.Body.Close()
	}
	if err == nil {
		observed := call.Result().Observations
		if len(observed) != 1 || observed[len(observed)-1].RequestAccountRef == "" {
			t.Fatalf("재현 요청의 계정 귀속이 없음: %+v", observed)
		}
	}
	return receipt, err
}

// AL03~AL06·AL08: 학습 방법으로 새 CSRF·로그인 전 쿠키를 받아 로그인하고, 받은 새 세션만 보호 세션 저장소에 결속해 재현
// 요청에 싣는다. 준비 GET·로그인 POST는 전용 규칙의 인증 준비 호출로 나가 감사·예산 집계가 대상 수신 수와 같다.
func TestSelectedCandidateFactoryLearnedLogin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method learnedlogin.Method
		want   []string
		auth   string
	}{
		{"separate JSON CSRF", selectedJSONCSRFMethod(), []string{"GET /api/csrf", "POST /api/login", "GET /read"}, "cookie"},
		{"separate HTML hidden CSRF", selectedHiddenCSRFMethod(), []string{"GET /session/new", "POST /api/login", "GET /read"}, "cookie"},
		{"same path form CSRF", selectedFormCSRFMethod(), []string{"GET /signin", "POST /signin", "GET /read"}, "cookie"},
		{"JSON token", selectedTokenMethod(), []string{"POST /api/token", "GET /read"}, "authorization"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := newSelectedLoginTarget()
			srv := httptest.NewServer(target.handler())
			defer srv.Close()
			cfg, policy := selectedLearnedPolicy(t, srv, tc.method, "seeded-pass-88ff")
			lease := acquireSelectedLearned(t, cfg, policy)
			if lease.AccountAuth["tester-a"] != E2HTTPAuthAuthenticated || len(lease.Sessions["tester-a"]) == 0 || lease.RefreshSession != nil {
				t.Fatalf("학습 로그인 인증 준비: %v", lease.AccountAuth)
			}
			receipt, err := selectedRead(t, lease, srv)
			if err != nil || receipt.StatusCode != http.StatusOK {
				t.Fatalf("새 세션으로 재현하지 못함: %+v %v", receipt, err)
			}
			if got := target.requests(); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("대상 요청 %v", got)
			}
			program, written := 0, 0
			for _, ev := range lease.Execution.Events(0, 0) {
				if ev.Stage != "result" || !ev.RequestWritten || !ev.AuditComplete {
					continue
				}
				written++
				if ev.Program == e2proxy.ProgramAuthPrep {
					program++
				}
			}
			if program != len(tc.want)-1 || written != len(tc.want) {
				t.Fatalf("감사 집계: 인증 준비 %d, 전체 %d, 대상 수신 %d", program, written, len(tc.want))
			}
			// 결속 계정 하나의 새 세션만 있다(다른 계정 세션이 섞이지 않음).
			if len(lease.Sessions) != 1 {
				t.Fatalf("세션 결속 %d개", len(lease.Sessions))
			}
		})
	}
}

// AN10·AN12·AN09: 로그인 실패·결과 불명·준비 칸 없음은 세션을 만들지 않고 다시 보내지 않으며, 그 뒤 재현 요청은 E②가 대상에
// 보내기 전에 거부한다(세션 없음).
func TestSelectedCandidateFactoryLearnedLoginFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		password string
		setup    func(*selectedLoginTarget)
		result   string
		want     []string
	}{
		{"rejected", "wrong-pass-value", nil, E2HTTPAuthLoginFailed, []string{"GET /api/csrf", "POST /api/login"}},
		{"result unknown", "seeded-pass-88ff", func(c *selectedLoginTarget) { c.drop.Store(true) }, E2HTTPAuthResultUnknown, []string{"GET /api/csrf", "POST /api/login"}},
		{"csrf field missing", "seeded-pass-88ff", func(c *selectedLoginTarget) { c.noField.Store(true) }, E2HTTPAuthNotSent, []string{"GET /api/csrf"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := newSelectedLoginTarget()
			if tc.setup != nil {
				tc.setup(target)
			}
			srv := httptest.NewServer(target.handler())
			defer srv.Close()
			cfg, policy := selectedLearnedPolicy(t, srv, selectedJSONCSRFMethod(), tc.password)
			lease := acquireSelectedLearned(t, cfg, policy)
			if lease.AccountAuth["tester-a"] != tc.result || len(lease.Sessions) != 0 {
				t.Fatalf("결과 %v 세션 %d", lease.AccountAuth, len(lease.Sessions))
			}
			if receipt, err := selectedRead(t, lease, srv); err == nil || receipt.ForwardAttempted {
				t.Fatalf("인증 실패 뒤 재현 요청이 나감: %+v", receipt)
			}
			if got := target.requests(); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("대상 요청(재시도 없어야 함) %v", got)
			}
		})
	}
}

// AN07·AN08·AN13·AN14: 정책 검증. 학습 로그인 칸은 선택 실행·계정 하나·9칸 비움에서만, 준비 GET 규칙은 방법의 정확한 준비
// 경로 하나, 로그인 규칙은 방법의 정확한 로그인 경로만 받는다.
func TestSelectedCandidateLearnedPolicyValidation(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, base := selectedLearnedPolicy(t, srv, selectedJSONCSRFMethod(), "seeded-pass-88ff")
	for name, change := range map[string]func(*E2HTTPFilePolicy){
		"not selected run": func(p *E2HTTPFilePolicy) { p.SelectedAccountRef = "" },
		"configured login too": func(p *E2HTTPFilePolicy) {
			p.Auth.Login = diagnosisprep.TargetLogin{Method: "POST", Path: "/api/login"}
		},
		"model login":           func(p *E2HTTPFilePolicy) { p.Auth.ModelLogin = true },
		"prep rule other path":  func(p *E2HTTPFilePolicy) { p.Rules[2].PathPrefix = "/api/other" },
		"prep rule missing":     func(p *E2HTTPFilePolicy) { p.Rules = p.Rules[:2]; p.Auth.PrepRuleRef = "" },
		"login rule other path": func(p *E2HTTPFilePolicy) { p.Rules[1].PathPrefix = "/api/other" },
		"extra prep rule": func(p *E2HTTPFilePolicy) {
			extra := p.Rules[2]
			extra.Ref, extra.PathPrefix = "auth-login-prep-2", "/api/extra"
			p.Rules = append(p.Rules, extra)
		},
		"invalid method": func(p *E2HTTPFilePolicy) {
			m := p.Auth.LearnedMethod.Clone()
			m.Method = "PUT"
			p.Auth.LearnedMethod = &m
		},
		"prep rule without learned login": func(p *E2HTTPFilePolicy) {
			auth := cookieAuth(p.Auth.Accounts...)
			p.Auth = &auth
		},
	} {
		policy := base
		auth := *base.Auth
		policy.Auth = &auth
		policy.Rules = append([]E2HTTPFileRule(nil), base.Rules...)
		change(&policy)
		raw, _ := json.Marshal(policy)
		if _, _, err := ValidateE2HTTPRunPolicyDocument(raw); err == nil {
			t.Errorf("%s: 정책을 받음", name)
		}
	}
}
