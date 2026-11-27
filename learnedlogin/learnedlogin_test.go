package learnedlogin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const run = "run-job1"

// succeededRow는 진단 단계 모델 로그인이 인증 대조로 성공한 행이다(#969 판정기가 남기는 모양).
func succeededRow(ref, label string) Row {
	return Row{Schema: RowSchema, AttemptRef: ref, RunRef: run, Stage: StageDiagnosis, Actor: ActorModel, TransportRef: "call_a/attempt_1",
		At: "2026-11-26T10:00:00Z", AccountLabel: label, Identifier: "user-" + label, PasswordRegistered: true, Method: "POST", Path: "/login",
		QueryKeys: []string{}, Format: FormatForm, IdentifierField: "userName", PasswordField: "password", OtherFields: []string{"_csrf"},
		FieldsFromResponse: []string{"_csrf"}, Status: 302, Transport: TransportSent, RedirectPath: "/dashboard", SessionSource: SourceSetCookie,
		SessionNames: []string{"sid"}, SessionSeenAnonymous: []string{}, SessionSentAs: SentAsCookie, Followup: FollowupContrast,
		ContrastSource: "observed", Outcome: OutcomeSucceeded}
}

func TestSelectPicksLatestEligibleSucceededPerAccount(t *testing.T) {
	early := succeededRow("exploration-1", "acct-a")
	later := succeededRow("diagnosis-3", "acct-a")
	later.Path = "/session"
	later.At = "2026-11-26T10:05:00Z"
	other := succeededRow("diagnosis-4", "acct-b")
	other.Format, other.IdentifierField, other.OtherFields, other.FieldsFromResponse = FormatJSON, "user.email", []string{}, []string{}
	other.SessionSource, other.SessionNames, other.SessionSentAs = SourceJSONField, []string{"data.token"}, "Authorization:Bearer "
	failedAfter := succeededRow("diagnosis-5", "acct-a")
	failedAfter.Outcome, failedAfter.Followup, failedAfter.Path = OutcomeFailed, FollowupNone, "/other"
	got := Select([]Row{early, later, other, failedAfter}, run, []string{"acct-a", "acct-b"})
	a, b := got["acct-a"], got["acct-b"]
	if a.Reason != "" || a.Learned.Method.Path != "/session" || a.Learned.Source.AttemptRef != "diagnosis-3" || !a.Learned.Source.At.Equal(time.Date(2026, 11, 26, 10, 5, 0, 0, time.UTC)) {
		t.Fatalf("A: 마지막 성공을 고르지 않음(뒤 실패가 덮으면 안 됨): %+v", a)
	}
	if b.Reason != "" || b.Learned.Method.SessionSentAs != "Authorization:Bearer " || b.Learned.Method.IdentifierField != "user.email" {
		t.Fatalf("B: 계정별로 나뉘지 않음: %+v", b)
	}
}

func TestSelectUsesLastRowOfEachAttempt(t *testing.T) {
	first := succeededRow("diagnosis-1", "acct-a")
	first.Outcome, first.Followup = OutcomeUnconfirmed, "no_login_form"
	// 같은 시도의 뒤 행이 대조로 성공을 세운다(§3 덧붙이기). 시도 순서는 처음 나온 순서다.
	second := succeededRow("diagnosis-2", "acct-a")
	second.Path = "/second"
	firstLater := succeededRow("diagnosis-1", "acct-a")
	got := Select([]Row{first, second, firstLater}, run, []string{"acct-a"})
	if got["acct-a"].Learned.Method.Path != "/second" {
		t.Fatalf("시작 순서가 아니라 파일 마지막 행 순서로 골랐다: %+v", got["acct-a"])
	}
	got = Select([]Row{first}, run, []string{"acct-a"})
	if _, ok := got["acct-a"]; ok {
		t.Fatalf("확인 전(unconfirmed) 시도를 배웠다: %+v", got)
	}
}

func TestSelectExcludesIneligibleRows(t *testing.T) {
	cases := map[string]func(*Row){
		"failed":         func(r *Row) { r.Outcome = OutcomeFailed },
		"unconfirmed":    func(r *Row) { r.Outcome = OutcomeUnconfirmed },
		"not sent":       func(r *Row) { r.Transport, r.Outcome = TransportNotSent, OutcomeNotSent },
		"unknown":        func(r *Row) { r.Transport, r.Outcome = TransportUnknown, OutcomeUnknown },
		"other run":      func(r *Row) { r.RunRef = "run-other" },
		"program":        func(r *Row) { r.Actor = ActorProgram },
		"confirmation":   func(r *Row) { r.Stage = StageConfirmation },
		"unbound label":  func(r *Row) { r.AccountLabel = "acct-z" },
		"no label":       func(r *Row) { r.AccountLabel = "" },
		"wrong password": func(r *Row) { r.PasswordRegistered = false },
		"no contrast":    func(r *Row) { r.Followup = "no_login_form" },
		"4xx":            func(r *Row) { r.Status = 401 },
		"login form":     func(r *Row) { r.LoginFormReturned = true },
		"no session":     func(r *Row) { r.SessionSource = SourceNone },
	}
	for name, change := range cases {
		r := succeededRow("diagnosis-1", "acct-a")
		change(&r)
		raw, _ := json.Marshal(r)
		rows, _, err := ParseRows(append(raw, '\n'))
		if err != nil {
			t.Fatal(err)
		}
		if got := Select(rows, run, []string{"acct-a"}); len(got) != 0 {
			t.Errorf("%s: 적격이 아닌 기록을 배웠다: %+v", name, got)
		}
	}
}

func TestSelectRejectsSuccessWithoutTransportRef(t *testing.T) {
	older := succeededRow("diagnosis-1", "acct-a")
	latest := succeededRow("diagnosis-2", "acct-a")
	for _, ref := range []string{"", "invalid ref"} {
		latest.TransportRef = ref
		for _, rows := range [][]Row{{latest}, {older, latest}} {
			if got := Select(rows, run, []string{"acct-a"}); got["acct-a"].Reason != ReasonTransportUnverified {
				t.Errorf("출처 참조 %q인 마지막 성공을 확인 불가로 처리하지 않음", ref)
			}
		}
	}
}

func TestSelectRejectsTamperedOrUnreproducibleAttempts(t *testing.T) {
	ok := succeededRow("diagnosis-1", "acct-a")
	tampered := ok
	tampered.Path = "/admin/delete"
	if got := Select([]Row{ok, tampered}, run, []string{"acct-a"}); got["acct-a"].Reason != ReasonRecordInvalid {
		t.Fatalf("같은 시도의 고정 칸이 바뀐 기록을 받아들임: %+v", got)
	}
	// 변조된 시도가 있으면 더 앞의 정상 성공으로 되돌아가지 않는다.
	older := succeededRow("diagnosis-0", "acct-a")
	if got := Select([]Row{older, ok, tampered}, run, []string{"acct-a"}); got["acct-a"].Reason != ReasonRecordInvalid {
		t.Fatalf("변조 시도를 건너뛰고 앞 시도를 배웠다: %+v", got)
	}
	query := succeededRow("diagnosis-1", "acct-a")
	query.QueryKeys = []string{"next"}
	if got := Select([]Row{query}, run, []string{"acct-a"}); got["acct-a"].Reason != ReasonInputUnavailable {
		t.Fatalf("질의 값을 모르는 로그인을 배웠다: %+v", got)
	}
	badTime := succeededRow("diagnosis-1", "acct-a")
	badTime.At = "yesterday"
	if got := Select([]Row{badTime}, run, []string{"acct-a"}); got["acct-a"].Reason != ReasonRecordInvalid {
		t.Fatalf("시각을 읽을 수 없는 기록을 배웠다: %+v", got)
	}
	// 마지막 성공이 재현할 수 없으면 앞의 성공으로 되돌아가지 않고 그 사유를 남긴다.
	later := succeededRow("diagnosis-2", "acct-a")
	later.Method = "PUT"
	if got := Select([]Row{ok, later}, run, []string{"acct-a"}); got["acct-a"].Reason != ReasonFormatUnsupported {
		t.Fatalf("재현할 수 없는 마지막 성공을 건너뜀: %+v", got)
	}
}

func TestParseRowsRejectsMalformedAndOversizedLines(t *testing.T) {
	good, _ := json.Marshal(succeededRow("diagnosis-1", "acct-a"))
	raw := strings.Join([]string{"not json", `{"schema":"jindo.login-attempt/v1","attempt_ref":"x"} trailing`, string(good), ""}, "\n")
	rows, skipped, err := ParseRows([]byte(raw))
	if err != ErrRecord || rows != nil || skipped != 2 {
		t.Fatalf("rows=%d skipped=%d err=%v", len(rows), skipped, err)
	}
	if _, _, err := ParseRows([]byte(strings.Repeat("x", maxRowBytes+1) + "\n")); err == nil {
		t.Fatal("상한을 넘는 줄을 건너뛰고 나머지를 읽음(일부만 읽어 최신 값을 잘못 고를 수 있음)")
	}
}

func TestParseRowsRejectsMalformedUpdate(t *testing.T) {
	good, err := json.Marshal(succeededRow("diagnosis-1", "acct-a"))
	if err != nil {
		t.Fatal(err)
	}
	raw := append(good, []byte("\n{\"attempt_ref\":\"diagnosis-1\",\n")...)
	if rows, skipped, err := ParseRows(raw); rows != nil || skipped != 1 || err != ErrRecord {
		t.Fatalf("손상된 갱신 행을 무시하고 이전 기록을 읽음: 행 %d, 손상 %d, 오류 %v", len(rows), skipped, err)
	}
}

func TestParseRowsRejectsAmbiguousOrMalformedJSON(t *testing.T) {
	good, err := json.Marshal(succeededRow("diagnosis-1", "acct-a"))
	if err != nil {
		t.Fatal(err)
	}
	for name, malformed := range map[string]string{
		"duplicate":     `{"path":"/another",` + string(good[1:]),
		"case alias":    `{"PATH":"/another",` + string(good[1:]),
		"escaped alias": `{"p\u0061th":"/another",` + string(good[1:]),
		"unicode alias": `{"ſtage":"confirmation",` + string(good[1:]),
		"extra bracket": string(good) + "]",
		"invalid UTF-8": strings.Replace(string(good), "/login", "/login\xff", 1),
		"null run":      strings.Replace(string(good), `"run_ref":"run-job1"`, `"run_ref":null`, 1),
		"empty stage":   strings.Replace(string(good), `"stage":"diagnosis"`, `"stage":""`, 1),
		"unknown actor": strings.Replace(string(good), `"actor":"model"`, `"actor":"other"`, 1),
		"null outcome":  strings.Replace(string(good), `"outcome":"succeeded"`, `"outcome":null`, 1),
		"bad pair":      strings.Replace(string(good), `"transport":"sent"`, `"transport":"result_unknown"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			raw := []byte(string(good) + "\n" + malformed + "\n")
			if rows, skipped, err := ParseRows(raw); rows != nil || skipped != 1 || err != ErrRecord {
				t.Fatalf("모호하거나 손상된 행을 포함한 기록을 읽음: 행 %d, 손상 %d, 오류 %v", len(rows), skipped, err)
			}
		})
	}
}

func TestSelectRejectsConflictAfterRunOrLabelChanges(t *testing.T) {
	older := succeededRow("diagnosis-1", "acct-a")
	latest := succeededRow("diagnosis-2", "acct-a")
	changedLabel := latest
	changedLabel.AccountLabel = "acct-b"
	changedRun := latest
	changedRun.RunRef = "run-other"
	got := Select([]Row{older, latest, changedLabel, changedRun}, run, []string{"acct-a", "acct-b"})
	for _, label := range []string{"acct-a", "acct-b"} {
		if got[label].Reason != ReasonRecordInvalid {
			t.Errorf("충돌 행에 등장한 계정 %s의 기록을 거부하지 않음: %v", label, got[label].Reason)
		}
	}
}

func TestReadRowsRequiresProtectedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if rows, _, err := ReadRows(dir); err != nil || rows != nil {
		t.Fatalf("파일이 없으면 빈 목록이어야 함: %v %v", rows, err)
	}
	good, _ := json.Marshal(succeededRow("diagnosis-1", "acct-a"))
	path := filepath.Join(dir, AttemptsFile)
	if err := os.WriteFile(path, append(good, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadRows(dir); err == nil {
		t.Fatal("0600이 아닌 기록 파일을 읽음")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	rows, _, err := ReadRows(dir)
	if err != nil || len(rows) != 1 {
		t.Fatalf("보호 파일을 읽지 못함: %v %d", err, len(rows))
	}
	link := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(path, filepath.Join(filepath.Dir(link), AttemptsFile)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadRows(filepath.Dir(link)); err == nil {
		t.Fatal("링크 기록 파일을 읽음")
	}
}

func TestFailureCountsFollowAttemptOrder(t *testing.T) {
	f := func(ref, outcome string) Row {
		r := succeededRow(ref, "acct-a")
		r.Outcome = outcome
		return r
	}
	rows := []Row{f("e-1", OutcomeFailed), f("e-2", OutcomeSucceeded), f("d-3", OutcomeFailed), f("d-4", OutcomeFailed), f("d-5", OutcomeUnknown), f("d-6", OutcomeNotSent)}
	if got := FailureCounts(rows, run)["user-acct-a"]; got != 2 {
		t.Fatalf("연속 실패 %d", got)
	}
	if got := NextAttemptNumber(append(rows, f("d-3", OutcomeFailed))); got != 7 {
		t.Fatalf("다음 시도 번호 %d", got)
	}
}

func formMethod() Method {
	return Method{Method: "POST", Path: "/login", Format: FormatForm, IdentifierField: "userName", PasswordField: "password",
		OtherFields: []string{"_csrf", "commit"}, FieldsFromResponse: []string{"_csrf"}, SessionSource: SourceSetCookie,
		SessionNames: []string{"sid"}, SessionSentAs: SentAsCookie}
}

func TestBuildFormJSONMultipart(t *testing.T) {
	cred := Credential{Username: "alice@example.test", Password: "p&ss=word"}
	req, reason := Build(formMethod(), cred, map[string]string{"_csrf": "tok+/=", "commit": "Log in"})
	if reason != "" || req.ContentType != "application/x-www-form-urlencoded" || req.Path != "/login" || req.Method != "POST" {
		t.Fatalf("form: %v %s", reason, req.ContentType)
	}
	values, err := url.ParseQuery(string(req.Body))
	if err != nil || values.Get("userName") != cred.Username || values.Get("password") != cred.Password || values.Get("_csrf") != "tok+/=" || values.Get("commit") != "Log in" {
		t.Fatalf("form 본문: %q", req.Body)
	}
	if !strings.HasPrefix(string(req.Body), "userName=") {
		t.Fatalf("칸 순서: %q", req.Body)
	}
	if _, reason := Build(formMethod(), cred, map[string]string{"_csrf": "x"}); reason != ReasonInputUnavailable {
		t.Fatalf("값이 없는 칸을 지어냄: %v", reason)
	}

	jm := Method{Method: "POST", Path: "/api/login", Format: FormatJSON, IdentifierField: "user.email", PasswordField: "user.password",
		OtherFields: []string{"meta.csrf"}, FieldsFromResponse: []string{"meta.csrf"}, SessionSource: SourceJSONField,
		SessionNames: []string{"token"}, SessionSentAs: "Authorization:Bearer "}
	req, reason = Build(jm, cred, map[string]string{"meta.csrf": "c<>&"})
	if reason != "" || req.ContentType != "application/json" || string(req.Body) != `{"user":{"email":"alice@example.test","password":"p&ss=word"},"meta":{"csrf":"c<>&"}}` {
		t.Fatalf("json: %v %s", reason, req.Body)
	}

	mm := formMethod()
	mm.Format = FormatMultipart
	req, reason = Build(mm, cred, map[string]string{"_csrf": "t", "commit": "c"})
	if reason != "" {
		t.Fatal(reason)
	}
	media, params, err := mime.ParseMediaType(req.ContentType)
	if err != nil || media != "multipart/form-data" {
		t.Fatalf("multipart 형식 %q", req.ContentType)
	}
	r := multipart.NewReader(strings.NewReader(string(req.Body)), params["boundary"])
	got := map[string]string{}
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil || p.FileName() != "" {
			t.Fatalf("multipart 부분: %v %q", err, p.FileName())
		}
		b, _ := io.ReadAll(p)
		got[p.FormName()] = string(b)
	}
	if got["userName"] != cred.Username || got["password"] != cred.Password || got["_csrf"] != "t" || len(got) != 4 {
		t.Fatalf("multipart 칸 %v", got)
	}
}

func TestValidateRejectsAmbiguousOrUnsupported(t *testing.T) {
	cases := map[string]func(*Method){
		"get":                  func(m *Method) { m.Method = "GET" },
		"xml":                  func(m *Method) { m.Format = "xml" },
		"no password":          func(m *Method) { m.PasswordField = "" },
		"duplicate field":      func(m *Method) { m.OtherFields = []string{"password"}; m.FieldsFromResponse = nil },
		"response not other":   func(m *Method) { m.FieldsFromResponse = []string{"nonce"} },
		"truncated fields":     func(m *Method) { m.OtherFields = make([]string, MaxFieldNames) },
		"query path":           func(m *Method) { m.Path = "/login?next=/" },
		"relative path":        func(m *Method) { m.Path = "login" },
		"dot path":             func(m *Method) { m.Path = "/a/../login" },
		"no session":           func(m *Method) { m.SessionSource = SourceNone },
		"empty sent as":        func(m *Method) { m.SessionSentAs = "" },
		"cookie sent as token": func(m *Method) { m.SessionSentAs = "Authorization:Bearer " },
		"json conflict": func(m *Method) {
			m.Format, m.IdentifierField, m.PasswordField, m.OtherFields, m.FieldsFromResponse = FormatJSON, "user", "user.password", nil, nil
		},
		"two tokens": func(m *Method) {
			m.SessionSource, m.SessionNames, m.SessionSentAs = SourceJSONField, []string{"a", "b"}, "Authorization:Bearer "
		},
		"cookie header token": func(m *Method) {
			m.SessionSource, m.SessionNames, m.SessionSentAs = SourceHeader, []string{"X-Token"}, "Cookie:t="
		},
		"multipart quote": func(m *Method) { m.Format, m.IdentifierField = FormatMultipart, `us"er` },
	}
	if reason := formMethod().Validate(); reason != "" {
		t.Fatal("정상 방법 거부:", reason)
	}
	for name, change := range cases {
		m := formMethod()
		change(&m)
		if reason := m.Validate(); reason == "" {
			t.Errorf("%s 통과", name)
		}
	}
}

func htmlResp(status int, body string) Response {
	return Response{Status: status, Header: http.Header{"Content-Type": {"text/html; charset=utf-8"}}, Body: []byte(body)}
}

const loginPage = `<html><form method="post" action="/login">
<input type="hidden" name="_csrf" value="fresh-token-123">
<input name="userName"><input type="password" name="password">
<input type="checkbox" name="remember">
<input type="submit" name="commit" value="Log in">
</form></html>`

func TestScreenValuesFromLoginForm(t *testing.T) {
	s, reason := ParseScreen(htmlResp(200, loginPage))
	if reason != "" || !s.LoginForm {
		t.Fatalf("로그인 화면: %v %+v", reason, s)
	}
	values, reason := s.Values(formMethod())
	if reason != "" || values["_csrf"] != "fresh-token-123" || values["commit"] != "Log in" {
		t.Fatalf("값: %v %v", reason, values)
	}
	// 체크 칸·사람이 입력하는 칸 값은 모른다.
	m := formMethod()
	m.OtherFields = []string{"_csrf", "remember"}
	if _, reason := s.Values(m); reason != ReasonInputUnavailable {
		t.Fatalf("체크 칸 값을 지어냄: %v", reason)
	}
	// CSRF 칸이 없거나 값이 둘이면 쓰지 않는다.
	missing, _ := ParseScreen(htmlResp(200, strings.Replace(loginPage, `name="_csrf"`, `name="other"`, 1)))
	if _, reason := missing.Values(formMethod()); reason != ReasonPrepFieldMissing {
		t.Fatalf("없는 CSRF: %v", reason)
	}
	twice, _ := ParseScreen(htmlResp(200, loginPage+`<input type="hidden" name="_csrf" value="another">`))
	if _, reason := twice.Values(formMethod()); reason != ReasonPrepFieldMissing {
		t.Fatalf("모호한 CSRF: %v", reason)
	}
	same, _ := ParseScreen(htmlResp(200, loginPage+`<input type="hidden" name="_csrf" value="fresh-token-123">`))
	if v, reason := same.Values(formMethod()); reason != "" || v["_csrf"] != "fresh-token-123" {
		t.Fatalf("같은 값 둘은 모호하지 않음: %v %v", reason, v)
	}
	// 비밀번호 칸이 없는 화면은 로그인 화면이 아니다(다른 화면의 숨은 칸 값을 쓰지 않음).
	notLogin, _ := ParseScreen(htmlResp(200, `<input type="hidden" name="_csrf" value="x"><input type="submit" name="commit" value="Go">`))
	if _, reason := notLogin.Values(formMethod()); reason != ReasonPrepUnavailable {
		t.Fatalf("로그인 화면이 아닌 응답: %v", reason)
	}
	for _, bad := range []Response{htmlResp(404, loginPage), htmlResp(302, ""), {Status: 200, Header: http.Header{"Content-Type": {"text/plain"}}, Body: []byte(loginPage)}} {
		if _, reason := ParseScreen(bad); reason != ReasonPrepUnavailable {
			t.Errorf("화면이 아닌 응답 받음: %d %v", bad.Status, reason)
		}
	}
	control, _ := ParseScreen(htmlResp(200, strings.Replace(loginPage, "fresh-token-123", "a&#10;b", 1)))
	if _, reason := control.Values(formMethod()); reason != ReasonPrepFieldMissing {
		t.Fatalf("제어 문자 든 값: %v", reason)
	}
}

func TestScreenValuesFromJSON(t *testing.T) {
	m := Method{Method: "POST", Path: "/api/login", Format: FormatJSON, IdentifierField: "email", PasswordField: "password",
		OtherFields: []string{"csrf.token"}, FieldsFromResponse: []string{"csrf.token"}, SessionSource: SourceHeader,
		SessionNames: []string{"X-Auth"}, SessionSentAs: "X-Auth:"}
	s, reason := ParseScreen(Response{Status: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"csrf":{"token":"abc-123"}}`)})
	if reason != "" {
		t.Fatal(reason)
	}
	if v, reason := s.Values(m); reason != "" || v["csrf.token"] != "abc-123" {
		t.Fatalf("JSON 값: %v %v", reason, v)
	}
	m.OtherFields = []string{"csrf.token", "remember"}
	if _, reason := s.Values(m); reason != ReasonInputUnavailable {
		t.Fatalf("JSON 화면에서 기록 밖 칸 값을 지어냄: %v", reason)
	}
}

func TestJarKeepsPreLoginCookiesPerPath(t *testing.T) {
	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	j := Jar{}
	h := http.Header{}
	h.Add("Set-Cookie", "pre=one; Path=/")
	h.Add("Set-Cookie", "admin=x; Path=/admin")
	h.Add("Set-Cookie", "gone=1; Max-Age=0")
	h.Add("Set-Cookie", "other=x; Domain=evil.test")
	h.Add("Set-Cookie", "parent=p; Domain=.target.test")
	h.Add("Set-Cookie", "secure=s; Secure")
	h.Add("Set-Cookie", "dup=a")
	h.Add("Set-Cookie", "dup=b")
	httpOrigin, _ := url.Parse("http://app.target.test")
	j.Absorb(Response{Status: 200, Header: h}, "/login", httpOrigin, now)
	// 경로 밖·다른 도메인·http의 Secure·같은 이름 다른 값은 담지 않는다. 상위 도메인 쿠키는 담는다.
	if j.Header() != "parent=p; pre=one" {
		t.Fatalf("쿠키 %q", j.Header())
	}
	httpsOrigin, _ := url.Parse("https://app.target.test")
	k := Jar{}
	k.Absorb(Response{Status: 200, Header: http.Header{"Set-Cookie": {"secure=s; Secure"}}}, "/login", httpsOrigin, now)
	if k.Header() != "secure=s" {
		t.Fatalf("https Secure 쿠키 %q", k.Header())
	}
}

// 같은 키가 둘인 JSON(로그인 화면·로그인 응답)은 어느 값이 맞는지 모르므로 쓰지 않는다.
func TestDuplicateJSONKeysRejected(t *testing.T) {
	if !UniqueJSONKeys([]byte(`{"a":{"b":1,"c":[{"d":1},{"d":2}]},"e":"x"}`)) {
		t.Fatal("중복 없는 문서 거부")
	}
	for _, raw := range []string{`{"a":1,"a":2}`, `{"x":{"t":"1","t":"2"}}`, `[{"k":1,"k":1}]`, `{"a":1} {"b":2}`, `{"a":`} {
		if UniqueJSONKeys([]byte(raw)) {
			t.Errorf("%s 받음", raw)
		}
	}
	jsonHeader := http.Header{"Content-Type": {"application/json"}}
	if _, reason := ParseScreen(Response{Status: 200, Header: jsonHeader, Body: []byte(`{"csrf":"a","csrf":"b"}`)}); reason != ReasonPrepUnavailable {
		t.Fatalf("중복 키 로그인 화면: %v", reason)
	}
	origin, _ := url.Parse("http://target.test")
	tok := Method{Method: "POST", Path: "/api/login", Format: FormatJSON, IdentifierField: "email", PasswordField: "password",
		SessionSource: SourceJSONField, SessionNames: []string{"token"}, SessionSentAs: "Authorization:Bearer "}
	body := `{"token":"aaaaaaaaaaaaaaaaaaaa","token":"bbbbbbbbbbbbbbbbbbbb"}`
	if r := CheckLogin(tok, Response{Status: 200, Header: jsonHeader, Body: []byte(body)}, Jar{}, origin, []string{"/api/login"}, 16, time.Now()); r.Reason != ReasonSessionNotFound {
		t.Fatalf("중복 키 토큰을 세션으로 봄: %#v", r)
	}
}

func TestCheckLoginSignalsAndSessions(t *testing.T) {
	origin, _ := url.Parse("http://target.test")
	now := time.Now()
	m := formMethod()
	set := func(status int, cookies ...string) Response {
		h := http.Header{"Content-Type": {"text/html"}}
		for _, c := range cookies {
			h.Add("Set-Cookie", c)
		}
		return Response{Status: status, Header: h}
	}
	ok := CheckLogin(m, set(302, "sid=new-session-value"), Jar{"pre": "1"}, origin, []string{"/login"}, 16, now)
	if ok.Outcome != OutcomeUnconfirmed || ok.Session.Get("Cookie") != "sid=new-session-value" || ok.Reason != "" {
		t.Fatalf("성공 응답: %#v", ok)
	}
	if r := CheckLogin(m, set(401, "sid=x"), Jar{}, origin, []string{"/login"}, 16, now); r.Outcome != OutcomeFailed || r.Reason != ReasonRejected {
		t.Fatalf("401: %#v", r)
	}
	formAgain := htmlResp(200, loginPage)
	formAgain.Header.Add("Set-Cookie", "sid=x")
	if r := CheckLogin(m, formAgain, Jar{}, origin, []string{"/login"}, 16, now); r.Reason != ReasonRejected || !r.LoginFormReturned {
		t.Fatalf("로그인 화면 다시 줌: %#v", r)
	}
	back := set(302, "sid=x")
	back.Header.Set("Location", "http://target.test/login?error=1")
	if r := CheckLogin(m, back, Jar{}, origin, []string{"/login"}, 16, now); r.Reason != ReasonRejected || r.RedirectPath != "/login" {
		t.Fatalf("로그인 화면으로 리다이렉트: %#v", r)
	}
	// 200만 받거나 다른 쿠키만 받은 것은 세션이 아니다.
	if r := CheckLogin(m, set(200, "other=x"), Jar{"sid": "old-anon"}, origin, []string{"/login"}, 16, now); r.Reason != ReasonSessionNotFound {
		t.Fatalf("세션 없는 200: %#v", r)
	}
	cont := m
	cont.SessionSource = SourceContinued
	if r := CheckLogin(cont, set(302), Jar{"sid": "prelogin-cookie"}, origin, []string{"/login"}, 16, now); r.Session.Get("Cookie") != "sid=prelogin-cookie" {
		t.Fatalf("이어진 세션: %#v", r)
	}
	tok := Method{Method: "POST", Path: "/api/login", Format: FormatJSON, IdentifierField: "email", PasswordField: "password",
		SessionSource: SourceJSONField, SessionNames: []string{"data.token"}, SessionSentAs: "Authorization:Bearer "}
	jsonResp := func(body string) Response {
		return Response{Status: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte(body)}
	}
	if r := CheckLogin(tok, jsonResp(`{"data":{"token":"eyJhbGciOiJIUzI1NiJ9.e30.sig"}}`), Jar{}, origin, []string{"/api/login"}, 16, now); r.Session.Get("Authorization") != "Bearer eyJhbGciOiJIUzI1NiJ9.e30.sig" {
		t.Fatalf("JSON 토큰: %#v", r)
	}
	if r := CheckLogin(tok, jsonResp(`{"data":{"token":"Password is not correct for user"}}`), Jar{}, origin, []string{"/api/login"}, 16, now); r.Reason != ReasonSessionNotFound {
		t.Fatalf("안내 문장을 세션으로 봄: %#v", r)
	}
}

func TestContrastSignals(t *testing.T) {
	paths := []string{"/login"}
	if !Blocked(401, "", paths) || !Blocked(302, "/login", paths) || Blocked(302, "/home", paths) || Blocked(200, "", paths) {
		t.Fatal("막힘 판정")
	}
	if !Open(200, "", paths) || !Open(302, "/dashboard", paths) || Open(302, "/login", paths) || Open(500, "", paths) || Open(0, "", paths) {
		t.Fatal("열림 판정")
	}
}

func TestDigestIgnoresSource(t *testing.T) {
	a, b := formMethod(), formMethod()
	b.OtherFields = append([]string(nil), b.OtherFields...)
	if a.Digest() != b.Digest() || a.Digest() == "" {
		t.Fatal("같은 방법의 digest가 다름")
	}
	b.Path = "/login2"
	if a.Digest() == b.Digest() {
		t.Fatal("다른 방법의 digest가 같음")
	}
	if (Credential{Username: "u", Password: "p"}).String() == "u" || strings.Contains((LoginRequest{Body: []byte("secret")}).String(), "secret") {
		t.Fatal("자격 값이 문자열로 나감")
	}
}

// 방법 객체(login_method)의 칸 이름·순서와 method_sha256은 계약 §6-1-2 표와 같다. learned 저장(⑤-B)과 verified가 같은 값을
// 쓰도록 이 모양을 고정한다.
func TestMethodObjectMatchesContract(t *testing.T) {
	m := formMethod()
	raw, err := json.Marshal(m.Clone())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"http_method":"POST","path":"/login","format":"form","identifier_field":"userName","password_field":"password",` +
		`"other_fields":["_csrf","commit"],"fields_from_response":["_csrf"],"session_source":"set_cookie","session_names":["sid"],"session_sent_as":"cookie"}`
	if string(raw) != want {
		t.Fatalf("방법 객체\n got %s\nwant %s", raw, want)
	}
	sum := sha256.Sum256([]byte(want))
	if m.Digest() != hex.EncodeToString(sum[:]) {
		t.Fatal("method_sha256이 방법 객체 JSON의 SHA-256이 아님")
	}
	empty := Method{Method: "POST", Path: "/l", Format: FormatJSON, PasswordField: "p", SessionSource: SourceHeader, SessionNames: []string{"X-T"}, SessionSentAs: "X-T:"}
	if raw, _ := json.Marshal(empty.Clone()); !strings.Contains(string(raw), `"other_fields":[]`) {
		t.Fatalf("빈 목록이 []가 아님: %s", raw)
	}
}

// 세션 쿠키의 유효 경로가 "/"가 아니면(Path 속성이 좁거나, Path 없이 /auth/login 응답이 준 쿠키) 확정 단계 다른 경로 요청에
// 실을 수 없으므로 쓰지 않는다.
func TestSessionCookieScope(t *testing.T) {
	origin, _ := url.Parse("http://target.test")
	resp := func(cookie string) Response {
		return Response{Status: 302, Header: http.Header{"Set-Cookie": {cookie}, "Content-Type": {"text/html"}}}
	}
	m := formMethod()
	if r := CheckLogin(m, resp("sid=session-value-123456; Path=/"), Jar{}, origin, []string{"/login"}, 16, time.Now()); r.Reason != "" {
		t.Fatalf("루트 경로 쿠키 %#v", r)
	}
	if r := CheckLogin(m, resp("sid=session-value-123456; Path=/auth"), Jar{}, origin, []string{"/login"}, 16, time.Now()); r.Reason != ReasonFormatUnsupported {
		t.Fatalf("좁은 경로 쿠키를 세션으로 씀 %#v", r)
	}
	nested := m
	nested.Path = "/auth/login"
	if r := CheckLogin(nested, resp("sid=session-value-123456"), Jar{}, origin, []string{"/auth/login"}, 16, time.Now()); r.Reason != ReasonFormatUnsupported {
		t.Fatalf("기본 경로 /auth 쿠키를 세션으로 씀 %#v", r)
	}
	if r := CheckLogin(m, resp("sid=session-value-123456"), Jar{}, origin, []string{"/login"}, 16, time.Now()); r.Reason != "" {
		t.Fatalf("/login 응답의 기본 경로 / 쿠키 %#v", r)
	}
	if defaultCookiePath("/a/b/c") != "/a/b" || defaultCookiePath("/login") != "/" || defaultCookiePath("") != "/" {
		t.Fatal("기본 경로")
	}
}

// transport_ref 대조: 진단 행은 같은 회차·같은 시도의 감사 완료 모델 송신 결과(메서드·상태 일치)가 있어야 하고, 탐색 행은
// 경계 순번 형식만 본다(감사 근거 없음). 형식이 틀리거나 단계를 모르면 대조하지 못한다.
func TestVerifyTransport(t *testing.T) {
	row := succeededRow("diagnosis-1", "acct-a")
	send := AuditedSend{CallRef: "call_a", AttemptID: "attempt_1", RunRef: run, ModelCall: true, Method: "POST", Allowed: true, RequestWritten: true,
		ResponseReceived: true, ResponseComplete: true, StatusCode: 302, AuditComplete: true}
	lookup := func(c, a string) (AuditedSend, bool) { return send, c == send.CallRef && a == send.AttemptID }
	if p, reason := VerifyTransport(row, run, lookup); reason != "" || p != ProvenanceE2Audited {
		t.Fatalf("감사된 행 %v %v", p, reason)
	}
	for name, change := range map[string]func(*Row){
		"other ref":    func(r *Row) { r.TransportRef = "call_a/attempt_2" },
		"bad ref":      func(r *Row) { r.TransportRef = "call_a" },
		"extra slash":  func(r *Row) { r.TransportRef = "call_a/attempt_1/x" },
		"row run":      func(r *Row) { r.RunRef = "run-other" },
		"other status": func(r *Row) { r.Status = 200 },
		"stage":        func(r *Row) { r.Stage = StageConfirmation },
	} {
		r := row
		change(&r)
		if _, reason := VerifyTransport(r, run, lookup); reason != ReasonTransportUnverified {
			t.Errorf("%s 통과", name)
		}
	}
	if _, reason := VerifyTransport(row, run, nil); reason != ReasonTransportUnverified {
		t.Fatal("감사 조회 없이 진단 행 통과")
	}
	explore := row
	explore.Stage = StageExploration
	for ref, ok := range map[string]bool{"17": true, "0": false, "017": false, "x": false, "": false} {
		explore.TransportRef = ref
		p, reason := VerifyTransport(explore, run, nil)
		if ok != (reason == "") || ok && p != ProvenanceExplorationUnaudited {
			t.Errorf("탐색 순번 %q: %v %v", ref, p, reason)
		}
	}
	explore.TransportRef = "17"
	if _, reason := VerifyTransport(explore, "run-other", nil); reason != ReasonTransportUnverified {
		t.Fatal("다른 회차의 탐색 행 통과")
	}
}
