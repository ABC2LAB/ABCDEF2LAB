package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
)

type authEventCounter struct {
	mu     sync.Mutex
	events []e2proxy.Event
}

func (r *authEventCounter) Record(_ context.Context, e e2proxy.Event) error {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
	return nil
}

// authLoginTarget은 소유 로그인 fixture다. 올바른 자격이면 쿠키(또는 JSON 토큰)를 주고 아니면 401이다.
type authLoginTarget struct {
	// posts는 handler goroutine이 쓰고 시험 goroutine이 읽으므로 atomic이다(-race).
	posts   atomic.Int64
	token   string
	noToken bool
}

func (a *authLoginTarget) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/read/echo" {
			// 취약한 앱처럼 비밀번호를 평문으로 되돌려 주는 페이지.
			io.WriteString(w, "password=seeded-pass-88ff")
			return
		}
		if r.Method != "POST" || r.URL.Path != "/login" {
			http.NotFound(w, r)
			return
		}
		a.posts.Add(1)
		var user, pass string
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			user, pass = body["email"], body["password"]
		} else {
			_ = r.ParseForm()
			user, pass = r.PostForm.Get("email"), r.PostForm.Get("password")
		}
		if user != "tester@owned.test" || pass != "seeded-pass-88ff" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"bad"}`)
			return
		}
		if a.noToken {
			io.WriteString(w, `{"ok":true}`)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: a.token})
		io.WriteString(w, `{"data":{"token":"`+a.token+`"}}`)
	})
}

func authTestExecution(t *testing.T, origin string, rec *authEventCounter) *e2proxy.Execution {
	t.Helper()
	now := time.Now()
	ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 20})
	if err != nil {
		t.Fatal(err)
	}
	grant := budget.Grant{Key: budget.Key{TaskRef: "task", BudgetRef: "budget"}, Binding: budget.Binding{PolicyRef: "policy", ResetRef: "reset"}, MaxRequests: 20, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour)}
	if err := ledger.Register(grant, now); err != nil {
		t.Fatal(err)
	}
	ip := netip.MustParseAddr("127.0.0.1")
	e, err := e2proxy.NewExecution(t.Context(), e2proxy.Config{ApprovalMode: e2proxy.ApprovalPreauthorizedScope, Scope: transmit.Scope{TaskRef: "task", RunRef: "run", CaseRef: "case", ResetRef: "reset", PolicyRef: "policy"},
		IntentID: 1, OwnerRef: "ARTEX", PolicyRevision: "revision1", AccountRef: "anonymous", SessionRef: "anonymous-session", ExpiresAt: now.Add(time.Hour),
		Rules: []e2proxy.Rule{{Ref: "read", Origin: origin, ConnectIP: ip, PathPrefix: "/read", PathScopeRef: "read", Methods: []string{"GET"}},
			{Ref: "auth-login", Origin: origin, ConnectIP: ip, PathPrefix: "/login", PathScopeRef: "auth-login", Methods: []string{"POST"}, Purpose: e2proxy.PurposeAuthPrep}},
		Budget: transmit.CumulativeBudget{Ledger: ledger, Key: grant.Key, Binding: grant.Binding}, Recorder: rec,
		Limits: e2proxy.Limits{MaxAttempts: 100, MaxCalls: 10, MaxConcurrent: 2, MaxBodyBytes: 1 << 20, RequestTimeout: 2 * time.Second, CallTimeout: time.Minute, MaxDenials: 10, MaxTransient: 10, MaxUnknown: 10}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// authCredentialPin은 실행 결속 디렉터리에 자격 보호 파일(0600)을 쓰고 pin을 돌려준다.
func authCredentialPin(t *testing.T, dir, name, user, pass string) E2HTTPFilePin {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"username": user, "password": pass, "nonce": strings.Repeat("ab", 32)})
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return E2HTTPFilePin{File: name, SHA256: hex.EncodeToString(sum[:])}
}

func authTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func cookieAuth(accounts ...E2HTTPFileAuthAccount) E2HTTPFileAuth {
	return E2HTTPFileAuth{RuleRef: "auth-login", Login: diagnosisprep.TargetLogin{Method: "POST", Path: "/login", Format: "form", UsernameField: "email", PasswordField: "password", Cookies: []string{"session"}}, Accounts: accounts}
}

// 계정마다 로그인 요청을 한 번씩 보내 결과를 계정별로 남기고, 인증된 계정의 세션만 돌려주며, 세션 값은 출력에서 가린다.
// 자격 파일이 pin과 다르면 보내지 않는다.
func TestE2HTTPAuthPrepCookieAccounts(t *testing.T) {
	target := &authLoginTarget{token: "seeded-session-cookie-99aa"}
	srv := httptest.NewServer(target.handler(t))
	defer srv.Close()
	dir := authTestDir(t)
	good := authCredentialPin(t, dir, "tester-a.json", "tester@owned.test", "seeded-pass-88ff")
	bad := authCredentialPin(t, dir, "tester-b.json", "tester@owned.test", "wrong-pass")
	tampered := authCredentialPin(t, dir, "tester-c.json", "tester@owned.test", "seeded-pass-88ff")
	tampered.SHA256 = strings.Repeat("0", 64)
	rec := &authEventCounter{}
	e := authTestExecution(t, srv.URL, rec)
	result, err := e2HTTPAuthPrep(t.Context(), e, dir, srv.URL, cookieAuth(
		E2HTTPFileAuthAccount{Label: "tester-a", Credential: good}, E2HTTPFileAuthAccount{Label: "tester-b", Credential: bad}, E2HTTPFileAuthAccount{Label: "tester-c", Credential: tampered}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"tester-a": E2HTTPAuthAuthenticated, "tester-b": E2HTTPAuthLoginFailed, "tester-c": E2HTTPAuthNotSent}
	for label, state := range want {
		if result.AccountAuth[label] != state {
			t.Errorf("%s: %s, want %s", label, result.AccountAuth[label], state)
		}
	}
	if len(result.Sessions) != 1 || result.Sessions["tester-a"].Get("Cookie") != "session="+target.token {
		t.Fatalf("sessions: %v", map[string]http.Header(result.Sessions))
	}
	if target.posts.Load() != 2 {
		t.Fatalf("login posts %d, want 2 (tampered credential not sent)", target.posts.Load())
	}
	if out := string(e.RedactOutput([]byte("Set-Cookie: session=" + target.token))); strings.Contains(out, target.token) {
		t.Fatalf("session value not redacted: %q", out)
	}
	// 감사 기록에는 자격·세션 값이 없다.
	encoded, _ := json.Marshal(rec.events)
	for _, marker := range []string{"seeded-pass-88ff", "tester@owned.test", target.token} {
		if strings.Contains(string(encoded), marker) {
			t.Fatalf("audit events carry %q", marker)
		}
	}
}

// JSON 토큰 방식: 응답의 필드 경로에서 토큰을 읽어 설정 헤더에 접두와 함께 싣고, 토큰 값을 가린다.
func TestE2HTTPAuthPrepJSONToken(t *testing.T) {
	target := &authLoginTarget{token: "seeded-json-token-11bb"}
	srv := httptest.NewServer(target.handler(t))
	defer srv.Close()
	dir := authTestDir(t)
	pin := authCredentialPin(t, dir, "tester-a.json", "tester@owned.test", "seeded-pass-88ff")
	auth := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin})
	auth.Login.Cookies, auth.Login.Format, auth.Login.JSONField, auth.Login.Header, auth.Login.Prefix = nil, "json", "data.token", "Authorization", "Bearer "
	e := authTestExecution(t, srv.URL, &authEventCounter{})
	result, err := e2HTTPAuthPrep(t.Context(), e, dir, srv.URL, auth)
	if err != nil || result.AccountAuth["tester-a"] != E2HTTPAuthAuthenticated || result.Sessions["tester-a"].Get("Authorization") != "Bearer "+target.token {
		t.Fatalf("json token: %v %v", err, result.AccountAuth)
	}
	if out := string(e.RedactOutput([]byte(target.token))); strings.Contains(out, target.token) {
		t.Fatal("token not redacted")
	}
}

// 로그인은 성공했지만 설정한 세션 위치에 값이 없거나 너무 짧아 가릴 수 없으면 session_not_found이고 세션을 쓰지 않는다.
func TestE2HTTPAuthPrepSessionNotFound(t *testing.T) {
	for name, target := range map[string]*authLoginTarget{"missing": {noToken: true}, "too short": {token: "abc"}} {
		srv := httptest.NewServer(target.handler(t))
		dir := authTestDir(t)
		pin := authCredentialPin(t, dir, "tester-a.json", "tester@owned.test", "seeded-pass-88ff")
		result, err := e2HTTPAuthPrep(t.Context(), authTestExecution(t, srv.URL, &authEventCounter{}), dir, srv.URL, cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin}))
		srv.Close()
		if err != nil || result.AccountAuth["tester-a"] != E2HTTPAuthSessionNotFound || len(result.Sessions) != 0 {
			t.Errorf("%s: %v %v", name, err, result.AccountAuth)
		}
	}
}

func TestE2HTTPFileAuthValidation(t *testing.T) {
	rules := []E2HTTPFileRule{{Ref: "read", PathPrefix: "/read", Methods: []string{"GET"}}, {Ref: "auth-login", PathPrefix: "/login", Methods: []string{"POST"}, Purpose: e2proxy.PurposeAuthPrep},
		{Ref: "plain-login", PathPrefix: "/login", Methods: []string{"POST"}}}
	pin := E2HTTPFilePin{File: "tester-a.json", SHA256: strings.Repeat("a", 64)}
	good := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin})
	if !validE2HTTPFileAuth(&good, rules) || !validE2HTTPFileAuth(nil, rules) {
		t.Fatal("valid auth rejected")
	}
	for name, change := range map[string]func(*E2HTTPFileAuth){
		"missing rule":          func(a *E2HTTPFileAuth) { a.RuleRef = "nope" },
		"plain rule":            func(a *E2HTTPFileAuth) { a.RuleRef = "read" },
		"same path, no purpose": func(a *E2HTTPFileAuth) { a.RuleRef = "plain-login" },
		"path mismatch":         func(a *E2HTTPFileAuth) { a.Login.Path = "/login2" },
		"no accounts":           func(a *E2HTTPFileAuth) { a.Accounts = nil },
		"duplicate":             func(a *E2HTTPFileAuth) { a.Accounts = append(a.Accounts, a.Accounts[0]) },
		"bad label":             func(a *E2HTTPFileAuth) { a.Accounts[0].Label = "User@x" },
		"bad pin":               func(a *E2HTTPFileAuth) { a.Accounts[0].Credential.SHA256 = "x" },
		"bad login":             func(a *E2HTTPFileAuth) { a.Login.Method = "GET" },
	} {
		a := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin})
		change(&a)
		if validE2HTTPFileAuth(&a, rules) {
			t.Errorf("%s accepted", name)
		}
	}
}

// 대상이 연결을 받은 뒤 답 없이 끊으면 결과 불명이다. 다시 보내지 않는다(요청 1번).
func TestE2HTTPAuthPrepResultUnknownIsNotRetried(t *testing.T) {
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer srv.Close()
	dir := authTestDir(t)
	pin := authCredentialPin(t, dir, "tester-a.json", "tester@owned.test", "seeded-pass-88ff")
	result, err := e2HTTPAuthPrep(t.Context(), authTestExecution(t, srv.URL, &authEventCounter{}), dir, srv.URL, cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin}))
	if err != nil || result.AccountAuth["tester-a"] != E2HTTPAuthResultUnknown || posts.Load() != 1 || len(result.Sessions) != 0 {
		t.Fatalf("unknown result: %v %v posts=%d", err, result.AccountAuth, posts.Load())
	}
}

// 쿠키 방식은 설정한 이름이 모두 있어야 세션이다. JSON 방식은 경로 끝이 빈 값 아닌 문자열이어야 한다.
func TestE2HTTPAuthExtractor(t *testing.T) {
	cookies := cookieAuth().Login
	cookies.Cookies = []string{"a", "b"}
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Set-Cookie": {"a=one-value-123"}}}
	if _, err := authExtractor(cookies)(resp, nil); err == nil {
		t.Fatal("partial cookie set accepted")
	}
	resp.Header.Add("Set-Cookie", "b=two-value-456")
	if h, err := authExtractor(cookies)(resp, nil); err != nil || h.Get("Cookie") != "a=one-value-123; b=two-value-456" {
		t.Fatalf("cookies: %v %v", h, err)
	}
	token := cookieAuth().Login
	token.Cookies, token.JSONField, token.Header, token.Prefix = nil, "data.token", "Authorization", "Bearer "
	for body, ok := range map[string]bool{`{"data":{"token":"tok-123456"}}`: true, `{"data":{"token":5}}`: false, `{"data":"x"}`: false, `{"data":{"token":""}}`: false, `not json`: false} {
		_, err := authExtractor(token)(&http.Response{StatusCode: 200, Header: http.Header{}}, []byte(body))
		if (err == nil) != ok {
			t.Errorf("%s: %v", body, err)
		}
	}
	if _, err := authExtractor(token)(&http.Response{StatusCode: 401, Header: http.Header{}}, []byte(`{"data":{"token":"tok-123456"}}`)); err == nil {
		t.Fatal("401 treated as a session")
	}
}

// 로그인에 쓴 비밀번호는 실행의 보호 값이 된다: 같은 실행의 모델 요청 응답이 비밀번호를 되돌려 주면 보류된다(S09).
// 보호할 수 없을 만큼 짧은 비밀번호로는 로그인하지 않는다.
func TestE2HTTPAuthPrepProtectsPasswordReflection(t *testing.T) {
	target := &authLoginTarget{token: "seeded-session-cookie-44ee"}
	srv := httptest.NewServer(target.handler(t))
	defer srv.Close()
	dir := authTestDir(t)
	good := authCredentialPin(t, dir, "tester-a.json", "tester@owned.test", "seeded-pass-88ff")
	short := authCredentialPin(t, dir, "tester-b.json", "tester@owned.test", "12345")
	e := authTestExecution(t, srv.URL, &authEventCounter{})
	result, err := e2HTTPAuthPrep(t.Context(), e, dir, srv.URL, cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: good}, E2HTTPFileAuthAccount{Label: "tester-b", Credential: short}))
	if err != nil || result.AccountAuth["tester-a"] != E2HTTPAuthAuthenticated || result.AccountAuth["tester-b"] != E2HTTPAuthNotSent || target.posts.Load() != 1 {
		t.Fatalf("auth prep: %v %v posts=%d", err, result.AccountAuth, target.posts.Load())
	}
	call, err := e.BeginCall(t.Context(), "model-command")
	if err != nil {
		t.Fatal(err)
	}
	defer call.Close(context.Background())
	req, _ := http.NewRequest("GET", srv.URL+"/read/echo", nil)
	if resp, r, err := call.Do(t.Context(), req); !errors.Is(err, e2proxy.ErrWithheld) || resp != nil || r.Reason != e2proxy.Withheld {
		t.Fatalf("reflected password reached the tool: %v %+v", err, r)
	}
	if out := string(e.RedactOutput([]byte("seeded-pass-88ff"))); strings.Contains(out, "seeded-pass-88ff") {
		t.Fatal("password not redacted from output")
	}
}
