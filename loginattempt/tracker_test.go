package loginattempt

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type recorder struct{ rows []Row }

func (r *recorder) sink(row Row) { r.rows = append(r.rows, row) }

func (r *recorder) last(ref string) Row {
	for i := len(r.rows) - 1; i >= 0; i-- {
		if r.rows[i].AttemptRef == ref {
			return r.rows[i]
		}
	}
	return Row{}
}

func newTracker(t *testing.T, exception bool) (*Tracker, *recorder) {
	t.Helper()
	origin, _ := url.Parse("http://target.test:4000")
	rec := &recorder{}
	tr, err := New(Config{RunRef: "run-1", Stage: StageExploration, Origin: origin, Accounts: testAccounts, MaxConsecutiveFailures: 3,
		MaxContrastProbes: 3, TokenMinBytes: 16, Exception: exception, Now: func() time.Time { return time.Unix(1700000000, 0) }, Sink: rec.sink})
	if err != nil {
		t.Fatal(err)
	}
	return tr, rec
}

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

func get(path string, kv ...string) Request {
	p, q, _ := strings.Cut(path, "?")
	return Request{Method: "GET", Path: p, RawQuery: q, Header: hdr(kv...)}
}

func formLogin(user, pass string, kv ...string) Request {
	return Request{Method: "POST", Path: "/login", ContentType: "application/x-www-form-urlencoded",
		Body: []byte("userName=" + url.QueryEscape(user) + "&password=" + url.QueryEscape(pass)), Header: hdr(kv...)}
}

const loginPage = `<html><form><input name="userName"><input type="password" name="password"></form></html>`

func html(status int, body string, kv ...string) Response {
	h := hdr(append([]string{"Content-Type", "text/html; charset=utf-8"}, kv...)...)
	return Response{Status: status, Header: h, Body: []byte(body)}
}

func redirect(to string, kv ...string) Response {
	return Response{Status: 302, Header: hdr(append([]string{"Location", to}, kv...)...)}
}

// NodeGoat 모양: 세션 없는 GET /는 로그인 화면으로 302(막힘), 틀린 비밀번호도 200+쿠키지만 비밀번호 칸이 다시 나와 실패,
// 맞으면 302 /dashboard. 로그인 뒤 같은 URL이 세션으로 열리면 대조가 선다(observed).
func TestNodeGoatShapeObservedContrast(t *testing.T) {
	tr, rec := newTracker(t, false)
	tr.Observe(get("/"), redirect("/login", "Set-Cookie", "connect.sid=anon-cookie-0001; Path=/"))
	tr.Observe(get("/login"), html(200, loginPage, "Set-Cookie", "connect.sid=anon-cookie-0002; Path=/"))
	wrong := formLogin("user2", "nope", "Cookie", "connect.sid=anon-cookie-0002")
	p := tr.Prepare(wrong, true)
	tr.Complete(p, "seq-3", wrong, html(200, loginPage, "Set-Cookie", "connect.sid=anon-cookie-0003; Path=/"))
	if r := rec.last("exploration-1"); r.Outcome != OutcomeFailed || !r.LoginFormReturned || r.ConsecutiveFailures != 1 || r.AccountLabel != "tester-b" || r.PasswordRegistered {
		t.Fatalf("틀린 비밀번호: %+v", r)
	}
	right := formLogin("user2", "bravo-pass-2", "Cookie", "connect.sid=anon-cookie-0003")
	p = tr.Prepare(right, true)
	tr.Complete(p, "seq-4", right, redirect("/dashboard", "Set-Cookie", "connect.sid=user-session-0004; Path=/"))
	r := rec.last("exploration-2")
	if r.Outcome != OutcomeUnconfirmed || r.SessionSource != SourceSetCookie || strings.Join(r.SessionSeenAnonymous, ",") != "connect.sid" {
		t.Fatalf("맞는 비밀번호 응답: %+v", r)
	}
	if tr.AccountAuth()["tester-b"] != AuthUnconfirmed {
		t.Fatal("대조 전 계정 상태")
	}
	tr.Observe(get("/", "Cookie", "connect.sid=user-session-0004"), redirect("/dashboard"))
	r = rec.last("exploration-2")
	if r.Outcome != OutcomeSucceeded || r.Followup != FollowupContrast || r.ContrastSource != ContrastObserved || r.SessionSentAs != "cookie" || r.ConsecutiveFailures != 0 {
		t.Fatalf("대조 뒤: %+v", r)
	}
	if got := tr.AccountAuth(); got["tester-b"] != AuthAuthenticated || got["tester-a"] != AuthNotAttempted {
		t.Fatalf("계정 상태: %v", got)
	}
}

// 모델이 먼저 로그인해 대조할 세션 없는 응답이 없으면 그 URL로 세션 없는 GET을 보낸다(probe). 막힘이면 성공이다.
func TestProbeEstablishesContrast(t *testing.T) {
	tr, rec := newTracker(t, false)
	login := formLogin("user2", "bravo-pass-2")
	p := tr.Prepare(login, true)
	tr.Complete(p, "1", login, redirect("/dashboard", "Set-Cookie", "sid=user-session-0004"))
	tr.Observe(get("/dashboard?tab=1", "Cookie", "sid=user-session-0004"), html(200, "<p>hi</p>"))
	if r := rec.last("exploration-1"); r.Followup != FollowupNoLoginForm || r.Outcome != OutcomeUnconfirmed {
		t.Fatalf("대조 전: %+v", r)
	}
	probe, ok := tr.NextProbe()
	if !ok || probe.URLKey() != "/dashboard?tab=1" {
		t.Fatalf("대조 요청: %v %q", ok, probe.URLKey())
	}
	if _, again := tr.NextProbe(); again {
		t.Fatal("같은 URL 대조 요청을 두 번 냄")
	}
	resp := redirect("/login")
	tr.ProbeDone(probe, &resp)
	r := rec.last("exploration-1")
	if r.Outcome != OutcomeSucceeded || r.ContrastSource != ContrastProbe || r.ContrastProbes != 1 {
		t.Fatalf("대조 요청 뒤: %+v", r)
	}
}

// 대조 요청은 시도마다 서로 다른 URL 상한까지만 내고, 세션 없는 응답이 이미 있는 URL은 내지 않는다. 열림이면 공개 화면이다.
func TestProbeLimitAndPublicPages(t *testing.T) {
	tr, rec := newTracker(t, false)
	login := formLogin("user2", "bravo-pass-2")
	p := tr.Prepare(login, true)
	tr.Complete(p, "1", login, redirect("/home", "Set-Cookie", "sid=user-session-0004"))
	tr.Observe(get("/public"), html(200, "<p>open</p>"))
	for _, u := range []string{"/public", "/a", "/b", "/c", "/d"} {
		tr.Observe(get(u, "Cookie", "sid=user-session-0004"), html(200, "<p>x</p>"))
	}
	var sent []string
	for {
		probe, ok := tr.NextProbe()
		if !ok {
			break
		}
		sent = append(sent, probe.URLKey())
		resp := html(200, "<p>public</p>")
		tr.ProbeDone(probe, &resp)
	}
	if strings.Join(sent, ",") != "/a,/b,/c" {
		t.Fatalf("대조 요청: %v", sent)
	}
	if r := rec.last("exploration-1"); r.Outcome != OutcomeUnconfirmed || r.ContrastProbes != 3 {
		t.Fatalf("공개 화면뿐인 시도: %+v", r)
	}
}

// VAmPI 모양: JSON 토큰을 받고 Authorization Bearer로 싣는다. 메시지 문장은 토큰 후보가 아니라서 실패 응답은 세션 없음이다.
func TestJSONTokenLoginAndFailureMessage(t *testing.T) {
	tr, rec := newTracker(t, false)
	body := func(u, p string) Request {
		return Request{Method: "POST", Path: "/users/v1/login", ContentType: "application/json", Body: []byte(`{"username":"` + u + `","password":"` + p + `"}`), Header: http.Header{}}
	}
	jsonResp := func(s string) Response {
		return Response{Status: 200, Header: hdr("Content-Type", "application/json"), Body: []byte(s)}
	}
	bad := body("user2", "nope")
	p := tr.Prepare(bad, true)
	tr.Complete(p, "1", bad, jsonResp(`{"status":"fail","message":"Password is not correct for the given username."}`))
	if r := rec.last("exploration-1"); r.Outcome != OutcomeFailed || r.SessionSource != SourceNone {
		t.Fatalf("실패 응답: %+v", r)
	}
	good := body("user2", "bravo-pass-2")
	p = tr.Prepare(good, true)
	tok := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyMiJ9.c2lnbmF0dXJlLXZhbHVl"
	tr.Complete(p, "2", good, jsonResp(`{"auth_token":"`+tok+`","message":"Successfully logged in.","status":"success"}`))
	if r := rec.last("exploration-2"); r.SessionSource != SourceJSONField || strings.Join(r.SessionNames, ",") != "auth_token" {
		t.Fatalf("토큰 자리: %+v", r)
	}
	tr.Observe(get("/users/v1/me", "Authorization", "Bearer "+tok), jsonResp(`{"data":{}}`))
	tr.Observe(get("/users/v1/me"), Response{Status: 401, Header: http.Header{}})
	r := rec.last("exploration-2")
	if r.Outcome != OutcomeSucceeded || r.SessionSentAs != "Authorization:Bearer " || r.ConsecutiveFailures != 0 {
		t.Fatalf("토큰 대조: %+v", r)
	}
}

// 세션 고정 앱: 로그인 응답이 새 세션을 주지 않아도 로그인 요청에 실린 쿠키가 있으면 이어진 세션이다. 대조의 세션 없는
// 응답은 그 쿠키를 싣지 않은 것만 센다.
func TestContinuedSessionNeedsCookielessContrast(t *testing.T) {
	tr, rec := newTracker(t, false)
	tr.Observe(get("/account", "Cookie", "sid=fixed-session-0001"), redirect("/login"))
	login := formLogin("user2", "bravo-pass-2", "Cookie", "sid=fixed-session-0001")
	p := tr.Prepare(login, true)
	tr.Complete(p, "1", login, redirect("/account"))
	if r := rec.last("exploration-1"); r.SessionSource != SourceContinued || r.Outcome != OutcomeUnconfirmed {
		t.Fatalf("이어진 세션: %+v", r)
	}
	tr.Observe(get("/account", "Cookie", "sid=fixed-session-0001"), html(200, "<p>me</p>"))
	if r := rec.last("exploration-1"); r.Outcome == OutcomeSucceeded {
		t.Fatal("같은 쿠키를 실은 막힘을 세션 없는 응답으로 셈")
	}
	tr.Observe(get("/account"), redirect("/login"))
	if r := rec.last("exploration-1"); r.Outcome != OutcomeSucceeded {
		t.Fatalf("쿠키 없는 막힘 뒤: %+v", r)
	}
}

// 다른 계정 세션으로 받은 403은 세션 없는 응답이 아니다. 질의 값이 다른 URL은 대조가 아니다.
func TestContrastNeedsAnonymousSameURL(t *testing.T) {
	tr, rec := newTracker(t, false)
	login := formLogin("user2", "bravo-pass-2")
	p := tr.Prepare(login, true)
	tr.Complete(p, "1", login, redirect("/home", "Set-Cookie", "sid=user-session-0004"))
	tr.Observe(get("/orders?id=1", "Cookie", "sid=other-account-session"), Response{Status: 403, Header: http.Header{}})
	tr.Observe(get("/orders?id=2"), Response{Status: 403, Header: http.Header{}})
	tr.Observe(get("/orders?id=1", "Cookie", "sid=user-session-0004"), html(200, "<p>order</p>"))
	if r := rec.last("exploration-1"); r.Outcome != OutcomeUnconfirmed {
		t.Fatalf("다른 인증·다른 질의로 대조함: %+v", r)
	}
}

// 200이고 쿠키·토큰·리다이렉트가 하나도 없으면 실패다. 400 이상도 실패다. 로그인 화면으로 돌아가는 뒤 요청도 실패로 센다.
func TestFailureSignalsAndLoginFormFollowup(t *testing.T) {
	tr, rec := newTracker(t, false)
	login := formLogin("user2", "x1")
	p := tr.Prepare(login, true)
	tr.Complete(p, "1", login, html(200, "<p>welcome</p>"))
	if r := rec.last("exploration-1"); r.Outcome != OutcomeFailed || r.SessionSource != SourceNone || r.ConsecutiveFailures != 1 {
		t.Fatalf("세션 없음: %+v", r)
	}
	login = formLogin("user2", "x2")
	p = tr.Prepare(login, true)
	tr.Complete(p, "2", login, Response{Status: 401, Header: hdr("Set-Cookie", "sid=whatever-session-1")})
	if r := rec.last("exploration-2"); r.Outcome != OutcomeFailed || r.ConsecutiveFailures != 2 {
		t.Fatalf("401: %+v", r)
	}
	login = formLogin("user2", "bravo-pass-2")
	p = tr.Prepare(login, true)
	tr.Complete(p, "3", login, redirect("/home", "Set-Cookie", "sid=user-session-0004"))
	tr.Observe(get("/home", "Cookie", "sid=user-session-0004"), redirect("/login"))
	if r := rec.last("exploration-3"); r.Outcome != OutcomeFailed || r.Followup != FollowupLoginForm || r.ConsecutiveFailures != 3 {
		t.Fatalf("뒤 요청 로그인 화면: %+v", r)
	}
}

// 아이디별 연속 실패 3에 닿으면 보내기 전에 거부하고 거부도 한 행으로 남긴다. 다른 아이디는 영향 없다. 성공하면 0이다.
func TestConsecutiveFailureLimit(t *testing.T) {
	tr, rec := newTracker(t, false)
	for i := 0; i < 3; i++ {
		login := formLogin("user2", "nope")
		p := tr.Prepare(login, true)
		if p.Refusal != "" {
			t.Fatalf("%d번째에 거부", i+1)
		}
		tr.Complete(p, "x", login, Response{Status: 401, Header: http.Header{}})
	}
	p := tr.Prepare(formLogin("user2", "bravo-pass-2"), true)
	if p.Refusal != RefusalExhausted {
		t.Fatal("상한 뒤에도 보냄")
	}
	if r := rec.last("exploration-4"); r.Transport != TransportNotSent || r.Refusal != RefusalExhausted || r.Outcome != OutcomeNotSent || r.ConsecutiveFailures != 3 {
		t.Fatalf("거부 행: %+v", r)
	}
	other := formLogin("user1@owned.test", "alpha-pass-1")
	if p := tr.Prepare(other, true); p.Refusal != "" {
		t.Fatal("다른 아이디까지 막음")
	}
	if got := tr.AccountAuth()["tester-b"]; got != AuthLoginFailed {
		t.Fatalf("상한 계정 상태: %s", got)
	}
}

// 탐색 쓰기 예외(§2-2): 로그인 안 됨이면 등록 쌍 POST를 예외로 보내고 확인 중이 된다. 확인 중에는 그 계정의 다른 예외
// POST를 보류한다. 분명한 실패면 로그인 안 됨, 아니면(대조 전이라도) 로그인됨으로 예외를 끈다. 결과 불명도 끈다.
func TestExplorationWriteExceptionStates(t *testing.T) {
	tr, rec := newTracker(t, true)
	if tr.ExceptionState("tester-b") != StateLoggedOut {
		t.Fatal("처음 상태")
	}
	first := formLogin("user2", "bravo-pass-2")
	p := tr.Prepare(first, false)
	if !p.Exception || tr.ExceptionState("tester-b") != StateVerifying {
		t.Fatal("예외로 안 보냄")
	}
	if q := tr.Prepare(formLogin("user2", "bravo-pass-2"), false); q.Refusal != RefusalPending || q.Exception {
		t.Fatal("확인 중에 같은 계정 예외 POST를 또 보냄")
	}
	if q := tr.Prepare(formLogin("user1@owned.test", "alpha-pass-1"), false); !q.Exception {
		t.Fatal("다른 계정 예외까지 막음")
	} else {
		tr.NotSent(q, "seq-9", "out_of_scope_path")
		if tr.ExceptionState("tester-a") != StateLoggedOut {
			t.Fatal("다른 규칙으로 못 보낸 예외 상태를 되돌리지 않음")
		}
	}
	tr.Complete(p, "1", first, html(200, loginPage))
	if tr.ExceptionState("tester-b") != StateLoggedOut {
		t.Fatal("실패 뒤 예외가 다시 안 켜짐")
	}
	if r := rec.last("exploration-1"); !r.WriteException {
		t.Fatal("예외 표시 없음")
	}
	second := formLogin("user2", "bravo-pass-2")
	p = tr.Prepare(second, false)
	tr.Complete(p, "2", second, redirect("/home", "Set-Cookie", "sid=user-session-0004"))
	if tr.ExceptionState("tester-b") != StateLoggedIn {
		t.Fatal("대조 전 unconfirmed인데 예외를 안 끔")
	}
	if q := tr.Prepare(formLogin("user2", "bravo-pass-2"), false); q.Exception || q.Refusal != "" || !q.Login {
		t.Fatalf("로그인됨인데 예외로 보냄: %+v", q)
	}
	if q := tr.Prepare(formLogin("user2", "wrong-pass"), false); q.Exception {
		t.Fatal("등록 쌍이 아닌 POST를 예외로 보냄")
	}
	third := formLogin("user1@owned.test", "alpha-pass-1")
	p = tr.Prepare(third, false)
	tr.Unknown(p, "3")
	if tr.ExceptionState("tester-a") != StateLoggedIn {
		t.Fatal("결과 불명인데 예외를 안 끔")
	}
	// 쓰기 승인이 있으면 예외가 필요 없다.
	if q := tr.Prepare(formLogin("user2", "bravo-pass-2"), true); q.Exception {
		t.Fatal("쓰기 승인 범위인데 예외로 표시")
	}
}

// 세션 끊김: 성공 판정에 쓴 대조 URL이 그 세션으로 다시 막히면 끊김이고 예외가 다시 켜진다. 다른 URL 막힘은 보지 않는다.
func TestSessionEndedOnlyOnContrastURL(t *testing.T) {
	tr, rec := newTracker(t, true)
	tr.Observe(get("/home"), redirect("/login"))
	login := formLogin("user2", "bravo-pass-2")
	p := tr.Prepare(login, false)
	tr.Complete(p, "1", login, redirect("/home", "Set-Cookie", "sid=user-session-0004"))
	tr.Observe(get("/home", "Cookie", "sid=user-session-0004"), html(200, "<p>home</p>"))
	if rec.last("exploration-1").Outcome != OutcomeSucceeded {
		t.Fatal("성공 안 함")
	}
	tr.Observe(get("/admin", "Cookie", "sid=user-session-0004"), Response{Status: 403, Header: http.Header{}})
	if rec.last("exploration-1").SessionEnded || tr.ExceptionState("tester-b") != StateLoggedIn {
		t.Fatal("다른 URL 막힘을 끊김으로 봄")
	}
	tr.Observe(get("/home", "Cookie", "sid=user-session-0004"), redirect("/login"))
	r := rec.last("exploration-1")
	if !r.SessionEnded || r.Outcome != OutcomeSucceeded || r.ConsecutiveFailures != 0 || tr.ExceptionState("tester-b") != StateLoggedOut {
		t.Fatalf("끊김: %+v state=%s", r, tr.ExceptionState("tester-b"))
	}
}

// 계정 상태 표(§4): 위에서부터 처음 맞는 행 하나. 결과 불명 뒤 미전송은 결과 불명이고, 미전송만 있으면 not_sent다.
func TestAccountAuthTableOrder(t *testing.T) {
	tr, _ := newTracker(t, false)
	unknown := formLogin("user2", "bravo-pass-2")
	p := tr.Prepare(unknown, true)
	tr.Unknown(p, "1")
	tr.NotSent(tr.Prepare(formLogin("user2", "bravo-pass-2"), true), "", "method_not_approved")
	if got := tr.AccountAuth()["tester-b"]; got != AuthResultUnknown {
		t.Fatalf("결과 불명 뒤 미전송: %s", got)
	}
	tr.NotSent(tr.Prepare(formLogin("user1@owned.test", "alpha-pass-1"), true), "", "method_not_approved")
	if got := tr.AccountAuth()["tester-a"]; got != AuthNotSent {
		t.Fatalf("미전송만: %s", got)
	}
	// 보내는 중인 시도는 보지 않는다.
	tr2, _ := newTracker(t, false)
	tr2.Prepare(formLogin("user2", "bravo-pass-2"), true)
	if got := tr2.AccountAuth()["tester-b"]; got != AuthNotAttempted {
		t.Fatalf("보내는 중: %s", got)
	}
}

// 한 번 성공한 계정은 뒤 실패가 덮지 않는다.
func TestAuthenticatedNotOverwrittenByLaterFailure(t *testing.T) {
	tr, _ := newTracker(t, false)
	tr.Observe(get("/home"), redirect("/login"))
	ok := formLogin("user2", "bravo-pass-2")
	p := tr.Prepare(ok, true)
	tr.Complete(p, "1", ok, redirect("/home", "Set-Cookie", "sid=user-session-0004"))
	tr.Observe(get("/home", "Cookie", "sid=user-session-0004"), html(200, "<p>home</p>"))
	bad := formLogin("user2", "nope")
	p = tr.Prepare(bad, true)
	tr.Complete(p, "2", bad, Response{Status: 401, Header: http.Header{}})
	if got := tr.AccountAuth()["tester-b"]; got != AuthAuthenticated {
		t.Fatalf("성공 뒤 실패: %s", got)
	}
}

// 진단 단계는 탐색 기록으로 연속 실패를 이어 센다(§5 회차당).
func TestSeedCarriesFailuresAcrossStages(t *testing.T) {
	explore, rec := newTracker(t, false)
	for i := 0; i < 2; i++ {
		login := formLogin("user2", "nope")
		p := explore.Prepare(login, true)
		explore.Complete(p, "x", login, Response{Status: 401, Header: http.Header{}})
	}
	origin, _ := url.Parse("http://target.test:4000")
	diag, err := New(Config{RunRef: "run-1", Stage: StageDiagnosis, Origin: origin, Accounts: testAccounts, MaxConsecutiveFailures: 3, MaxContrastProbes: 3, TokenMinBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	diag.Seed(rec.rows)
	login := formLogin("user2", "nope")
	p := diag.Prepare(login, true)
	diag.Complete(p, "y", login, Response{Status: 401, Header: http.Header{}})
	if p := diag.Prepare(formLogin("user2", "bravo-pass-2"), true); p.Refusal != RefusalExhausted {
		t.Fatal("탐색 실패를 잇지 않음")
	}
	if got := diag.AccountAuth()["tester-b"]; got != AuthLoginFailed {
		t.Fatalf("이은 계정 상태: %s", got)
	}
}

// 기록 행에는 비밀번호·세션 값이 없다.
func TestRowsCarryNoSecrets(t *testing.T) {
	tr, rec := newTracker(t, false)
	tr.Observe(get("/home"), redirect("/login"))
	login := formLogin("user2", "bravo-pass-2")
	p := tr.Prepare(login, true)
	tr.Complete(p, "1", login, redirect("/home", "Set-Cookie", "sid=user-session-0004"))
	tr.Observe(get("/home?k=secret-query-value", "Cookie", "sid=user-session-0004"), html(200, "<p>home</p>"))
	raw, _ := json.Marshal(rec.rows)
	for _, v := range []string{"bravo-pass-2", "user-session-0004", "secret-query-value"} {
		if strings.Contains(string(raw), v) {
			t.Fatalf("기록에 값이 남음: %s", v)
		}
	}
	if len(rec.rows) == 0 || rec.rows[0].Schema != Schema || rec.rows[0].Identifier != "user2" {
		t.Fatal("행 모양")
	}
}

func TestConfigValidation(t *testing.T) {
	origin, _ := url.Parse("http://t.test")
	for _, cfg := range []Config{{}, {RunRef: "r", Stage: StageDiagnosis, Origin: origin, TokenMinBytes: 16}, {RunRef: "r", Stage: StageDiagnosis, MaxConsecutiveFailures: 3, TokenMinBytes: 16}} {
		if _, err := New(cfg); err == nil {
			t.Fatal("잘못된 설정을 받음")
		}
	}
}

// 로그인 응답이 로그인 시도 경로로 리다이렉트하면 로그인 화면을 다시 준 것이라 실패다(새 쿠키가 있어도).
func TestLoginRedirectBackToLoginPathFails(t *testing.T) {
	tr, rec := newTracker(t, false)
	login := formLogin("user2", "nope")
	p := tr.Prepare(login, true)
	tr.Complete(p, "1", login, redirect("http://target.test:4000/login?error=1", "Set-Cookie", "sid=flash-session-0001"))
	r := rec.last("exploration-1")
	if r.Outcome != OutcomeFailed || !r.LoginFormReturned || r.RedirectPath != "/login" || r.ConsecutiveFailures != 1 {
		t.Fatalf("로그인 경로로 되돌림: %+v", r)
	}
	other := formLogin("user2", "nope2")
	p = tr.Prepare(other, true)
	tr.Complete(p, "2", other, redirect("https://elsewhere.test/login", "Set-Cookie", "sid=flash-session-0002"))
	if r := rec.last("exploration-2"); r.RedirectPath != "" || r.LoginFormReturned {
		t.Fatalf("다른 origin 리다이렉트를 같은 경로로 봄: %+v", r)
	}
}

// 성공한 등록 계정 로그인은 뒤 요청이 실어 보낸 방식 그대로 세션 헤더를 넘기고(세션 저장소·확정 단계용), 보낸 요청의
// 계정은 성공한 세션만 귀속한다. 두 계정 세션이 같이 실리면 귀속하지 않는다.
func TestSucceededSessionHandoffAndAccountOf(t *testing.T) {
	origin, _ := url.Parse("http://target.test:4000")
	got := map[string]http.Header{}
	tr, err := New(Config{RunRef: "run-1", Stage: StageDiagnosis, Origin: origin, Accounts: testAccounts, MaxConsecutiveFailures: 3, MaxContrastProbes: 3, TokenMinBytes: 16,
		OnSucceeded: func(label string, h http.Header) { got[label] = h.Clone() }})
	if err != nil {
		t.Fatal(err)
	}
	tr.Observe(get("/home"), redirect("/login"))
	login := formLogin("user2", "bravo-pass-2")
	p := tr.Prepare(login, true)
	tr.Complete(p, "1", login, redirect("/home", "Set-Cookie", "sid=user-session-0004", "Set-Cookie", "csrf=csrf-cookie-value-1"))
	if tr.AccountOf(hdr("Cookie", "sid=user-session-0004")) != "" {
		t.Fatal("성공 전 세션을 귀속함")
	}
	tr.Observe(get("/home", "Cookie", "sid=user-session-0004; theme=dark"), html(200, "<p>home</p>"))
	if h := got["tester-b"]; h.Get("Cookie") != "csrf=csrf-cookie-value-1; sid=user-session-0004" {
		t.Fatalf("세션 헤더: %v", h)
	}
	if tr.AccountOf(hdr("Cookie", "theme=dark; sid=user-session-0004")) != "tester-b" || tr.AccountOf(hdr("Cookie", "sid=other")) != "" {
		t.Fatal("계정 귀속")
	}
	tok := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyMSJ9.c2lnbmF0dXJlLWE"
	tr.Observe(get("/api/me"), Response{Status: 401, Header: http.Header{}})
	jl := Request{Method: "POST", Path: "/api/login", ContentType: "application/json", Body: []byte(`{"email":"user1@owned.test","password":"alpha-pass-1"}`), Header: http.Header{}}
	p = tr.Prepare(jl, true)
	tr.Complete(p, "2", jl, Response{Status: 200, Header: hdr("Content-Type", "application/json"), Body: []byte(`{"token":"` + tok + `"}`)})
	tr.Observe(get("/api/me", "Authorization", "Bearer "+tok), Response{Status: 200, Header: http.Header{}})
	if h := got["tester-a"]; h.Get("Authorization") != "Bearer "+tok {
		t.Fatalf("토큰 세션 헤더: %v", h)
	}
	if tr.AccountOf(hdr("Authorization", "Bearer "+tok, "Cookie", "sid=user-session-0004")) != "" {
		t.Fatal("두 계정 세션이 같이 실렸는데 귀속함")
	}
}

// 사용자 지정 헤더(X-Auth-Token)로만 세션을 싣는 앱: 그 헤더 이름을 처음 보는 요청도 무인증이 아니라 세션 요청이다.
// 같은 URL의 무인증 401과 세션 200이 대조된다(#969 동찬 리뷰 P2).
func TestCustomHeaderSessionIsNotAnonymous(t *testing.T) {
	tr, rec := newTracker(t, false)
	tok := "c3VwZXItc2VjcmV0LWN1c3RvbS1oZWFkZXItdG9rZW4"
	tr.Observe(get("/api/me"), Response{Status: 401, Header: http.Header{}})
	jl := Request{Method: "POST", Path: "/api/login", ContentType: "application/json", Body: []byte(`{"username":"user2","password":"bravo-pass-2"}`), Header: http.Header{}}
	p := tr.Prepare(jl, true)
	tr.Complete(p, "1", jl, Response{Status: 200, Header: hdr("Content-Type", "application/json"), Body: []byte(`{"auth":{"token":"` + tok + `"}}`)})
	tr.Observe(get("/api/me", "X-Auth-Token", tok), Response{Status: 200, Header: http.Header{}})
	r := rec.last("exploration-1")
	if r.Outcome != OutcomeSucceeded || r.SessionSentAs != "X-Auth-Token:" || strings.Join(r.SessionNames, ",") != "auth.token" {
		t.Fatalf("사용자 지정 헤더 세션: %+v", r)
	}
	// 그 뒤 같은 헤더를 실은 요청은 무인증 대조 근거가 아니다.
	tr2, rec2 := newTracker(t, false)
	p = tr2.Prepare(jl, true)
	tr2.Complete(p, "1", jl, Response{Status: 200, Header: hdr("Content-Type", "application/json"), Body: []byte(`{"auth":{"token":"` + tok + `"}}`)})
	tr2.Observe(get("/api/me", "X-Auth-Token", tok), Response{Status: 403, Header: http.Header{}})
	tr2.Observe(get("/api/me", "X-Auth-Token", tok), Response{Status: 200, Header: http.Header{}})
	if r := rec2.last("exploration-1"); r.Outcome == OutcomeSucceeded {
		t.Fatal("세션을 실은 403을 무인증 막힘으로 셈")
	}
}

// 보내지 않은 시도도 경계·E② 기록 참조를 남긴다: 상한 거부는 Prepare의 요청 참조, 다른 규칙 거부는 NotSent의 참조다.
func TestNotSentRowsKeepTransportRef(t *testing.T) {
	tr, rec := newTracker(t, false)
	for i := 0; i < 3; i++ {
		login := formLogin("user2", "nope")
		p := tr.Prepare(login, true)
		tr.Complete(p, "x", login, Response{Status: 401, Header: http.Header{}})
	}
	refused := formLogin("user2", "nope")
	refused.TransportRef = "call_a/attempt_4"
	if p := tr.Prepare(refused, true); p.Refusal != RefusalExhausted {
		t.Fatal("상한 거부 아님")
	}
	if r := rec.last("exploration-4"); r.TransportRef != "call_a/attempt_4" {
		t.Fatalf("상한 거부 행 참조: %q", r.TransportRef)
	}
	other := formLogin("user1@owned.test", "alpha-pass-1")
	other.TransportRef = "call_a/attempt_5"
	tr.NotSent(tr.Prepare(other, true), "call_a/attempt_5b", "budget_exhausted")
	if r := rec.last("exploration-5"); r.TransportRef != "call_a/attempt_5b" || r.Refusal != "budget_exhausted" {
		t.Fatalf("다른 거부 행: %+v", r)
	}
}

// 성공 뒤 계정 귀속은 성공 대조에서 확인한 헤더 이름·접두와 정확한 토큰 값으로만 한다. 관계없는 메타데이터 헤더에 토큰
// 문자열이 들어 있거나 값이 덧붙은 요청은 귀속하지 않는다(#969 동찬 리뷰 P2).
func TestAccountOfUsesConfirmedHeaderOnly(t *testing.T) {
	tr, _ := newTracker(t, false)
	tok := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyMiJ9.c2lnbmF0dXJlLWI"
	tr.Observe(get("/api/me"), Response{Status: 401, Header: http.Header{}})
	jl := Request{Method: "POST", Path: "/api/login", ContentType: "application/json", Body: []byte(`{"username":"user2","password":"bravo-pass-2"}`), Header: http.Header{}}
	p := tr.Prepare(jl, true)
	tr.Complete(p, "1", jl, Response{Status: 200, Header: hdr("Content-Type", "application/json"), Body: []byte(`{"token":"` + tok + `"}`)})
	tr.Observe(get("/api/me", "Authorization", "Bearer "+tok), Response{Status: 200, Header: http.Header{}})
	if got := tr.AccountOf(hdr("Authorization", "Bearer "+tok)); got != "tester-b" {
		t.Fatalf("확인한 헤더 귀속: %q", got)
	}
	for name, h := range map[string]http.Header{
		"metadata header": hdr("X-Trace-Context", "Bearer "+tok),
		"other prefix":    hdr("Authorization", "Token "+tok),
		"extra suffix":    hdr("Authorization", "Bearer "+tok+"x"),
		"no session":      hdr("Accept", "application/json"),
	} {
		if got := tr.AccountOf(h); got != "" {
			t.Fatalf("%s: 확인 안 한 방식을 귀속함 %q", name, got)
		}
	}
}

// 한 브라우저 문맥에는 한 등록 계정만 로그인한다. 다른 계정이 그 문맥에서 로그인 중이거나 로그인해 있으면 막고(실패한
// 계정은 세지 않음), 문맥을 닫으면(ResetContext) 그 문맥의 계정은 '로그인 안 됨'으로 돌아가 다시 로그인할 수 있다.
// 직접 요청(문맥 없음)에는 이 규칙이 없다(팀 합의 2026-11-26 (나)).
func TestBrowserContextHoldsOneAccount(t *testing.T) {
	tr, rec := newTracker(t, true)
	inBrowser := func(r Request) Request { r.Context = "browser"; return r }
	a := inBrowser(formLogin("user1@owned.test", "alpha-pass-1"))
	p := tr.Prepare(a, false)
	if !p.Exception {
		t.Fatal("첫 계정 예외 아님")
	}
	if q := tr.Prepare(inBrowser(formLogin("user2", "bravo-pass-2")), false); q.Refusal != RefusalContextInUse {
		t.Fatalf("응답 대기 중인 다른 계정이 있는 문맥에서 로그인함: %+v", q)
	}
	tr.Complete(p, "1", a, redirect("/home", "Set-Cookie", "sid=user-session-a1"))
	if q := tr.Prepare(inBrowser(formLogin("user2", "bravo-pass-2")), false); q.Refusal != RefusalContextInUse {
		t.Fatalf("로그인된 다른 계정이 있는 문맥에서 로그인함: %+v", q)
	}
	if r := rec.rows[len(rec.rows)-1]; r.Refusal != RefusalContextInUse || r.Transport != TransportNotSent {
		t.Fatalf("거부 행: %+v", r)
	}
	if q := tr.Prepare(formLogin("user2", "bravo-pass-2"), true); q.Refusal != "" {
		t.Fatal("직접 요청에도 문맥 규칙을 적용함")
	}
	tr.ResetContext("browser")
	if tr.ExceptionState("tester-a") != StateLoggedOut {
		t.Fatal("닫힌 문맥의 계정이 로그인 안 됨으로 안 돌아감")
	}
	q := tr.Prepare(inBrowser(formLogin("user2", "bravo-pass-2")), false)
	if q.Refusal != "" || !q.Exception {
		t.Fatalf("새 문맥에서 다른 계정 로그인을 막음: %+v", q)
	}
	// 실패한 계정은 문맥을 잡지 않는다.
	tr.Complete(q, "2", inBrowser(formLogin("user2", "bravo-pass-2")), Response{Status: 401, Header: http.Header{}})
	if r := tr.Prepare(inBrowser(formLogin("user1@owned.test", "alpha-pass-1")), false); r.Refusal != "" {
		t.Fatalf("실패한 계정이 문맥을 잡음: %+v", r)
	}
}

// 브라우저 문맥을 바꿔 계정이 '로그인 안 됨'이 돼도, 그 계정의 확인된 세션 값을 실은 요청은 로그인됨으로 보고 쓰기
// 예외를 주지 않는다(사용자 결정 2026-11-26). 세션을 싣지 않은 요청(새 문맥의 로그인)은 예외를 받는다.
func TestExceptionNotForRequestsCarryingConfirmedSession(t *testing.T) {
	tr, _ := newTracker(t, true)
	tr.Observe(get("/home"), redirect("/login"))
	inBrowser := func(r Request) Request { r.Context = "browser"; return r }
	login := inBrowser(formLogin("user2", "bravo-pass-2"))
	p := tr.Prepare(login, false)
	tr.Complete(p, "1", login, redirect("/home", "Set-Cookie", "sid=user-session-b1"))
	tr.Observe(get("/home", "Cookie", "sid=user-session-b1"), html(200, "<p>home</p>"))
	tr.ResetContext("browser")
	if tr.ExceptionState("tester-b") != StateLoggedOut {
		t.Fatal("문맥 바꾼 뒤 상태")
	}
	carrying := formLogin("user2", "bravo-pass-2", "Cookie", "sid=user-session-b1")
	if q := tr.Prepare(carrying, false); !q.Login || q.Exception || q.Refusal != "" {
		t.Fatalf("확인된 세션을 실은 요청에 예외를 줌: %+v", q)
	}
	if tr.ExceptionState("tester-b") != StateLoggedOut {
		t.Fatal("세션 실은 요청이 상태를 바꿈")
	}
	if q := tr.Prepare(inBrowser(formLogin("user2", "bravo-pass-2")), false); !q.Exception {
		t.Fatal("세션 없는 새 문맥 로그인에 예외를 안 줌")
	}
}
