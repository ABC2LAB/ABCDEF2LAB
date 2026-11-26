package exploreboundary

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/loginattempt"
)

var loginAccounts = []loginattempt.Account{{Label: "tester-a", Username: "user1", Password: "alpha-pass-1"}}

// loginSession은 대상 요청 상한을 넉넉히 둔 탐색 세션에 로그인 판정기를 붙인다.
func loginSession(t *testing.T, write string) *Session {
	t.Helper()
	_, source, clock := fixtureSession(t, write, exploration.FullLocal, nil)
	b := fixtureBinding(write, exploration.FullLocal)
	b.Limits.MaxRequests = 64
	s, err := Open(t.Context(), b, fixtureDigest, source, Policy{Resolver: fakeResolver{}, PathPattern: func(p string) string { return p }, MaxDeniedRecords: 64, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AttachLogin(LoginConfig{Accounts: loginAccounts, MaxConsecutiveFailures: 3, MaxContrastProbes: 3, TokenMinBytes: 16}); err != nil {
		t.Fatal(err)
	}
	return s
}

func loginPost(user, pass string) Request {
	r := approved("POST", "/app/login")
	r.ContentType = "application/x-www-form-urlencoded"
	r.Body = []byte("username=" + user + "&password=" + pass)
	r.Header = http.Header{}
	r.FetchMode = "navigate"
	return r
}

func lastRow(t *testing.T, s *Session) loginattempt.Row {
	t.Helper()
	rows, _, _ := s.LoginAttemptsPage(0, maxLoginRows)
	if len(rows) == 0 {
		t.Fatal("로그인 시도 행 없음")
	}
	return rows[len(rows)-1]
}

func lastRecord(t *testing.T, s *Session) Record {
	t.Helper()
	records, _ := s.Records()
	if len(records) == 0 {
		t.Fatal("경계 기록 없음")
	}
	return records[len(records)-1]
}

// 쓰기 승인 없는 탐색(read_only): 등록 아이디·비밀번호 쌍이 정확히 든 로그인 POST만 '로그인 안 됨'인 계정에 한해 보낸다.
// 판정 전에는 그 계정의 다른 예외 POST를 보류하고, 분명한 실패면 다시 켜고, 실패가 분명하지 않으면 끈다. 그 밖의 POST는
// 지금처럼 막는다. 보내지 않은 시도도 경계 기록 순번으로 이어진다(계약 §2-2, 팀 합의 2026-11-26).
func TestReadOnlyExplorationLoginException(t *testing.T) {
	s := loginSession(t, exploration.ReadOnly)
	if d, _ := s.Admit(t.Context(), loginPost("user1", "wrong-pass")); d.Forward || d.Reason != ReasonMethodNotApproved {
		t.Fatalf("등록 쌍이 아닌 POST를 보냄: %+v", d)
	}
	if r, rec := lastRow(t, s), lastRecord(t, s); r.Transport != loginattempt.TransportNotSent || r.Refusal != ReasonMethodNotApproved || r.TransportRef != strconv.FormatUint(rec.Seq, 10) {
		t.Fatalf("미전송 행: %+v / 경계 기록 %d", r, rec.Seq)
	}
	d, ticket := s.Admit(t.Context(), loginPost("user1", "alpha-pass-1"))
	if !d.Forward {
		t.Fatalf("등록 쌍 로그인을 예외로 안 보냄: %+v", d)
	}
	if d, _ := s.Admit(t.Context(), loginPost("user1", "alpha-pass-1")); d.Forward || d.Reason != ReasonLoginVerificationPending {
		t.Fatalf("확인 중에 같은 계정 예외 POST를 또 보냄: %+v", d)
	}
	ticket.Done(Result{Status: 401, ResponseReceived: true, ResponseHeader: http.Header{}})
	if r := s.login.ExceptionState("tester-a"); r != loginattempt.StateLoggedOut {
		t.Fatalf("분명한 실패 뒤 상태: %s", r)
	}
	d, ticket = s.Admit(t.Context(), loginPost("user1", "alpha-pass-1"))
	if !d.Forward {
		t.Fatal("실패 뒤 다시 로그인을 못 보냄")
	}
	ticket.Done(Result{Status: 302, ResponseReceived: true, ResponseHeader: http.Header{"Location": {"/app/home"}, "Set-Cookie": {"sid=user-session-77; Path=/"}}})
	if s.login.ExceptionState("tester-a") != loginattempt.StateLoggedIn {
		t.Fatal("실패가 분명하지 않은데 예외를 안 끔")
	}
	if d, _ := s.Admit(t.Context(), loginPost("user1", "alpha-pass-1")); d.Forward || d.Reason != ReasonMethodNotApproved {
		t.Fatalf("로그인됨인데 예외로 보냄: %+v", d)
	}
	other := approved("POST", "/app/items")
	other.ContentType, other.Body = "application/json", []byte(`{"name":"x"}`)
	if d, _ := s.Admit(t.Context(), other); d.Forward || d.Reason != ReasonMethodNotApproved {
		t.Fatalf("로그인이 아닌 POST를 보냄: %+v", d)
	}
	rows, _, _ := s.LoginAttemptsPage(0, maxLoginRows)
	exceptions := 0
	for _, r := range rows {
		if r.WriteException {
			exceptions++
		}
		if r.Stage != loginattempt.StageExploration || r.RunRef != "run-1" {
			t.Fatalf("행 단계·회차: %+v", r)
		}
	}
	if exceptions == 0 {
		t.Fatal("예외 표시 행 없음")
	}
	raw, _ := json.Marshal(rows)
	if strings.Contains(string(raw), "alpha-pass-1") || strings.Contains(string(raw), "user-session-77") {
		t.Fatal("행에 비밀번호·세션 값이 남음")
	}
	records, _ := s.Records()
	for _, r := range records {
		if fmt.Sprint(r) != "" && strings.Contains(fmt.Sprintf("%+v", r), "alpha-pass-1") {
			t.Fatal("경계 기록에 비밀번호가 남음")
		}
	}
}

// 같은 아이디로 세 번 연달아 실패하면 네 번째는 경계가 보내기 전에 막는다(쓰기 승인 회차도 같다). 사유는
// login_attempts_exhausted이고 거부 행은 경계 기록 순번으로 이어진다.
func TestExplorationLoginConsecutiveFailureLimit(t *testing.T) {
	for _, write := range []string{exploration.ReadOnly, exploration.ApprovedWrites} {
		t.Run(write, func(t *testing.T) {
			s := loginSession(t, write)
			for i := 0; i < 3; i++ {
				d, ticket := s.Admit(t.Context(), loginPost("user1", "alpha-pass-1"))
				if !d.Forward {
					t.Fatalf("%d번째 로그인을 못 보냄: %+v", i+1, d)
				}
				ticket.Done(Result{Status: 401, ResponseReceived: true, ResponseHeader: http.Header{}})
				if r := lastRow(t, s); r.WriteException != (write == exploration.ReadOnly) {
					t.Fatalf("예외 표시가 쓰기 승인과 맞지 않음: %+v", r)
				}
			}
			d, _ := s.Admit(t.Context(), loginPost("user1", "alpha-pass-1"))
			if d.Forward || d.Reason != ReasonLoginAttemptsExhausted {
				t.Fatalf("상한 뒤에도 보냄: %+v", d)
			}
			r, rec := lastRow(t, s), lastRecord(t, s)
			if r.Refusal != loginattempt.RefusalExhausted || rec.Reason != ReasonLoginAttemptsExhausted || r.TransportRef != strconv.FormatUint(rec.Seq, 10) {
				t.Fatalf("상한 거부 행·기록: %+v / %+v", r, rec)
			}
		})
	}
}

// 탐색 단계는 세션 없는 크롤 응답과 로그인 뒤 세션 응답을 같은 URL에서 대조해 성공을 판정한다. 응답이 없이 끝난 로그인은
// 결과 불명이다.
func TestExplorationLoginContrastAndUnknown(t *testing.T) {
	s := loginSession(t, exploration.ReadOnly)
	d, ticket := s.Admit(t.Context(), approved("GET", "/app/home"))
	if !d.Forward {
		t.Fatal(d)
	}
	ticket.Done(Result{Status: 302, ResponseReceived: true, ResponseHeader: http.Header{"Location": {"/app/login"}}})
	d, ticket = s.Admit(t.Context(), loginPost("user1", "alpha-pass-1"))
	if !d.Forward {
		t.Fatal(d)
	}
	ticket.Done(Result{Status: 302, ResponseReceived: true, ResponseHeader: http.Header{"Location": {"/app/home"}, "Set-Cookie": {"sid=user-session-77; Path=/"}}})
	home := approved("GET", "/app/home")
	home.Header = http.Header{"Cookie": {"sid=user-session-77"}}
	d, ticket = s.Admit(t.Context(), home)
	if !d.Forward {
		t.Fatal(d)
	}
	ticket.Done(Result{Status: 200, ResponseReceived: true, ResponseHeader: http.Header{}})
	if got := s.login.AccountAuth()["tester-a"]; got != loginattempt.AuthAuthenticated {
		t.Fatalf("탐색 대조 성공 안 함: %s", got)
	}
	s2 := loginSession(t, exploration.ApprovedWrites)
	d, ticket = s2.Admit(t.Context(), loginPost("user1", "alpha-pass-1"))
	if !d.Forward {
		t.Fatal(d)
	}
	ticket.Done(Result{ResponseReceived: false})
	if r := lastRow(t, s2); r.Outcome != loginattempt.OutcomeUnknown || r.Transport != loginattempt.TransportUnknown {
		t.Fatalf("응답 없는 로그인: %+v", r)
	}
}

// 판정 설정은 제어 소켓 open으로만 들어오고, 로그인 시도 행은 login_attempts 쪽으로 읽는다(세션을 닫은 뒤에도). 설정
// 표현은 값을 문자열로 내지 않는다.
func TestControlLoginConfigAndRows(t *testing.T) {
	m, client, source := controlFixture(t, sameUID)
	binding := fixtureBinding(exploration.ReadOnly, exploration.FullLocal)
	login := &WireLogin{Accounts: []WireLoginAccount{{Label: "tester-a", Username: "user1", Password: "alpha-pass-1"}}, MaxConsecutiveFailures: 3, MaxContrastProbes: 3, TokenMinBytes: 16}
	if strings.Contains(fmt.Sprint(*login, login.Accounts[0]), "alpha-pass-1") {
		t.Fatal("판정 설정이 값을 문자열로 냄")
	}
	boundary := ControlBoundary{Client: client, SourceDigest: fixtureDigest, Target: source.Current, Login: login}
	lease, err := boundary.Open(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	s := m.Current()
	d, ticket := s.Admit(t.Context(), loginPost("user1", "alpha-pass-1"))
	if !d.Forward {
		t.Fatalf("open으로 받은 판정 설정이 안 붙음: %+v", d)
	}
	ticket.Done(Result{Status: 401, ResponseReceived: true, ResponseHeader: http.Header{}})
	if _, err := lease.Close(t.Context(), exploration.Completed); err != nil {
		t.Fatal(err)
	}
	rows, omitted, err := ControlReader{Client: client, Binding: binding}.LoginAttempts(t.Context())
	if err != nil || omitted != 0 || len(rows) == 0 || rows[len(rows)-1].Outcome != loginattempt.OutcomeFailed {
		t.Fatalf("소켓으로 읽은 행: %v %d %+v", err, omitted, rows)
	}
	// 판정 설정 없이 연 세션은 지금처럼 로그인 POST를 막는다.
	plain := loginSession(t, exploration.ReadOnly)
	plain.login = nil
	if d, _ := plain.Admit(t.Context(), loginPost("user1", "alpha-pass-1")); d.Forward {
		t.Fatal("판정기 없는 세션이 예외로 보냄")
	}
}

// 브라우저 문맥 하나에는 등록 계정 하나만 로그인한다. 다른 계정의 브라우저 로그인은 browser_account_in_use로 막고, 직접
// 입구 로그인은 막지 않는다. 엔진이 새 문맥을 열었다고 알리면(browser_context_reset) 앞 계정은 '로그인 안 됨'이고 다른
// 계정 로그인이 열린다(팀 합의 2026-11-26 (나)).
func TestBrowserContextOneAccountAndReset(t *testing.T) {
	m, client, source := controlFixture(t, sameUID)
	binding := fixtureBinding(exploration.ReadOnly, exploration.FullLocal)
	login := &WireLogin{Accounts: []WireLoginAccount{{Label: "tester-a", Username: "user1", Password: "alpha-pass-1"}, {Label: "tester-b", Username: "user2", Password: "bravo-pass-2"}},
		MaxConsecutiveFailures: 3, MaxContrastProbes: 3, TokenMinBytes: 16}
	lease, err := ControlBoundary{Client: client, SourceDigest: fixtureDigest, Target: source.Current, Login: login}.Open(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	s := m.Current()
	d, ticket := s.Admit(t.Context(), loginPost("user1", "alpha-pass-1"))
	if !d.Forward {
		t.Fatal(d)
	}
	ticket.Done(Result{Status: 302, ResponseReceived: true, ResponseHeader: http.Header{"Location": {"/app/home"}, "Set-Cookie": {"sid=user-session-a1; Path=/"}}})
	if d, _ := s.Admit(t.Context(), loginPost("user2", "bravo-pass-2")); d.Forward || d.Reason != ReasonBrowserAccountInUse {
		t.Fatalf("다른 계정이 로그인한 문맥에서 브라우저 로그인을 보냄: %+v", d)
	}
	direct := loginPost("user2", "bravo-pass-2")
	direct.Ingress, direct.FetchMode = DirectIngress, ""
	if d, _ := s.Admit(t.Context(), direct); d.Reason == ReasonBrowserAccountInUse {
		t.Fatal("직접 입구 로그인에 브라우저 문맥 규칙을 적용함")
	}
	resetter, ok := lease.(interface{ ResetBrowserContext(context.Context) error })
	if !ok || resetter.ResetBrowserContext(t.Context()) != nil {
		t.Fatal("문맥 초기화 알림 실패")
	}
	if s.login.ExceptionState("tester-a") != loginattempt.StateLoggedOut {
		t.Fatal("닫힌 문맥의 계정이 로그인 안 됨으로 안 돌아감")
	}
	if d, _ := s.Admit(t.Context(), loginPost("user2", "bravo-pass-2")); !d.Forward && d.Reason == ReasonBrowserAccountInUse {
		t.Fatalf("새 문맥에서 다른 계정 로그인을 막음: %+v", d)
	}
}

// 직접 입구(Bash·curl) 로그인도 계정 상태·쓰기 예외·연속 실패 상한을 똑같이 받는다. 빠지는 것은 '한 문맥 한 계정'
// 규칙뿐이다(사용자 확인 2026-11-26). 확인된 세션 값을 실은 직접 요청은 예외를 받지 않는다.
func TestDirectIngressLoginFollowsSameRules(t *testing.T) {
	direct := func(user, pass string) Request {
		r := loginPost(user, pass)
		r.Ingress, r.FetchMode = DirectIngress, ""
		return r
	}
	s := loginSession(t, exploration.ReadOnly)
	for i := 0; i < 3; i++ {
		d, ticket := s.Admit(t.Context(), direct("user1", "alpha-pass-1"))
		if !d.Forward {
			t.Fatalf("%d번째 직접 로그인을 예외로 못 보냄: %+v", i+1, d)
		}
		ticket.Done(Result{Status: 401, ResponseReceived: true, ResponseHeader: http.Header{}})
	}
	if d, _ := s.Admit(t.Context(), direct("user1", "alpha-pass-1")); d.Forward || d.Reason != ReasonLoginAttemptsExhausted {
		t.Fatalf("직접 입구가 연속 실패 상한을 피함: %+v", d)
	}
	s2 := loginSession(t, exploration.ReadOnly)
	d, ticket := s2.Admit(t.Context(), approved("GET", "/app/home"))
	ticket.Done(Result{Status: 302, ResponseReceived: true, ResponseHeader: http.Header{"Location": {"/app/login"}}})
	// 브라우저로 로그인해 대조가 서면 로그인됨이고, 그동안 직접 로그인도 예외를 받지 않는다.
	d, ticket = s2.Admit(t.Context(), loginPost("user1", "alpha-pass-1"))
	if !d.Forward {
		t.Fatal(d)
	}
	ticket.Done(Result{Status: 302, ResponseReceived: true, ResponseHeader: http.Header{"Location": {"/app/home"}, "Set-Cookie": {"sid=user-session-77; Path=/"}}})
	if d, _ := s2.Admit(t.Context(), direct("user1", "alpha-pass-1")); d.Forward || d.Reason != ReasonMethodNotApproved {
		t.Fatalf("로그인됨인 계정의 직접 로그인을 예외로 보냄: %+v", d)
	}
	home := approved("GET", "/app/home")
	home.Header = http.Header{"Cookie": {"sid=user-session-77"}}
	d, ticket = s2.Admit(t.Context(), home)
	ticket.Done(Result{Status: 200, ResponseReceived: true, ResponseHeader: http.Header{}})
	if s2.login.AccountAuth()["tester-a"] != loginattempt.AuthAuthenticated {
		t.Fatal("대조 성공 안 함")
	}
	// 브라우저 문맥을 바꿔 '로그인 안 됨'이 돼도, curl이 확인된 세션 값을 실었으면 예외가 아니다. 싣지 않으면 예외다.
	s2.ResetBrowserContext()
	carrying := direct("user1", "alpha-pass-1")
	carrying.Header = http.Header{"Cookie": {"sid=user-session-77"}}
	if d, _ := s2.Admit(t.Context(), carrying); d.Forward || d.Reason != ReasonMethodNotApproved {
		t.Fatalf("확인된 세션을 실은 직접 로그인에 예외를 줌: %+v", d)
	}
	if d, _ := s2.Admit(t.Context(), direct("user1", "alpha-pass-1")); !d.Forward {
		t.Fatalf("세션 없는 직접 로그인을 막음: %+v", d)
	}
}
