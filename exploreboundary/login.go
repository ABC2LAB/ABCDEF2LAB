package exploreboundary

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/loginattempt"
)

// LoginConfig는 모델 직접 로그인 회차의 탐색 단계 판정 설정이다(docs/contracts/model-login-attempts.md). 엔진 서버가
// 제어 소켓의 open으로만 준다. 계정 값은 세션 메모리에만 있고 기록·원자료·로그에 넣지 않는다.
type LoginConfig struct {
	Accounts               []loginattempt.Account
	MaxConsecutiveFailures int
	MaxContrastProbes      int
	TokenMinBytes          int
}

func (LoginConfig) String() string { return "exploreboundary.LoginConfig[protected]" }

// browserLoginContext는 브라우저 입구 요청의 로그인 문맥 이름이다. 브라우저는 한 번에 문맥 하나만 쓰고, 계정을 바꾸면
// 엔진이 새 문맥을 연 뒤 ResetBrowserContext로 알린다.
const browserLoginContext = "browser"

// ResetBrowserContext는 브라우저 문맥을 새로 열었다는 엔진 알림이다. 닫힌 문맥에서 로그인한 계정은 쿠키가 사라졌으므로
// '로그인 안 됨'으로 돌아가 다시 로그인할 수 있고, 다른 계정 로그인도 막지 않는다.
func (s *Session) ResetBrowserContext() {
	s.mu.Lock()
	tracker := s.login
	s.mu.Unlock()
	if tracker != nil {
		tracker.ResetContext(browserLoginContext)
	}
}

// maxLoginRows는 세션이 들고 있는 로그인 시도 행 상한이다. 넘는 행은 omitted로만 센다.
const maxLoginRows = 4096

// loginRowsPerPage는 제어 소켓 한 쪽에 싣는 행 수다(행 하나는 칸 이름·상한 있는 목록뿐이라 한 줄 상한 안에 든다).
const loginRowsPerPage = 256

// AttachLogin은 세션에 로그인 시도 판정기를 붙인다. 프록시에 세션을 드러내기 전에 한 번만 부른다. 탐색 단계는 쓰기
// 승인이 없으면(read_only) §2-2 예외로 등록 계정 로그인만 보낸다.
func (s *Session) AttachLogin(cfg LoginConfig) error {
	origin := &url.URL{Scheme: s.target.scheme(), Host: s.target.Host}
	if s.target.Port != 0 {
		origin.Host = s.target.Host + ":" + strconv.Itoa(int(s.target.Port))
	}
	tracker, err := loginattempt.New(loginattempt.Config{RunRef: s.binding.RunRef, Stage: loginattempt.StageExploration, Origin: origin, Accounts: cfg.Accounts,
		MaxConsecutiveFailures: cfg.MaxConsecutiveFailures, MaxContrastProbes: cfg.MaxContrastProbes, TokenMinBytes: cfg.TokenMinBytes, Exception: true,
		Now: s.policy.Now, Sink: s.appendLoginRow})
	if err != nil {
		return ErrConfig
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.login != nil {
		return ErrConfig
	}
	s.login = tracker
	return nil
}

// appendLoginRow는 판정기가 행을 바꿀 때 받는다. 판정기 잠금 안에서 불리므로 세션 잠금(s.mu)을 잡지 않는다.
func (s *Session) appendLoginRow(row loginattempt.Row) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if len(s.loginRows) >= maxLoginRows {
		s.loginOmitted++
		return
	}
	s.loginRows = append(s.loginRows, row)
}

// LoginAttemptsPage는 로그인 시도 행을 offset부터 n개 준다. next가 0이면 끝이다. 같은 시도의 뒤 행이 그 시도의 지금 값이다.
func (s *Session) LoginAttemptsPage(offset uint64, n int) (rows []loginattempt.Row, omitted, next uint64) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	total := uint64(len(s.loginRows))
	if offset >= total {
		return []loginattempt.Row{}, s.loginOmitted, 0
	}
	end := min(total, offset+uint64(n))
	rows = append([]loginattempt.Row(nil), s.loginRows[offset:end]...)
	if end < total {
		next = end
	}
	return rows, s.loginOmitted, next
}

// loginRequestLocked는 승인 대상 요청의 판정기 입력이다. 기록 참조는 이 요청이 받을 경계 기록 순번이다(거부든 허용이든
// 다음 순번). 호출자는 s.mu를 보유한다.
func (s *Session) loginRequestLocked(req Request) loginattempt.Request {
	context := ""
	if req.Ingress == BrowserIngress {
		// 브라우저 입구 요청은 한 쿠키 저장소(지금 브라우저 문맥)에서 나온다. 직접 입구(Bash)는 문맥 규칙을 받지 않는다.
		context = browserLoginContext
	}
	return loginattempt.Request{TransportRef: strconv.FormatUint(s.seq+1, 10), Context: context, Method: req.Method, Path: req.EscapedPath, RawQuery: req.RawQuery,
		ContentType: req.ContentType, Body: req.Body, Header: cloneHeader(req.Header)}
}

func cloneHeader(h http.Header) http.Header {
	if h == nil {
		return http.Header{}
	}
	return h.Clone()
}

// loginWriteAllowed는 이 세션의 쓰기 승인 범위가 로그인 POST를 이미 허용하는지다(예외가 필요 없음).
func (s *Session) loginWriteAllowed() bool { return s.binding.WriteMode == exploration.ApprovedWrites }

// finishLogin은 허용한 요청이 끝난 뒤 판정기에 알린다. 응답을 받았으면 로그인은 판정, 아니면 관찰이다. 로그인 요청이
// 응답 없이 끝나면 보냈는지 모르므로 결과 불명이다(다시 보내지 않음). 호출자는 s.mu를 보유한다.
func (t *Ticket) finishLogin(r Result) {
	tracker := t.s.login
	if tracker == nil || !t.loginTracked {
		return
	}
	ref := strconv.FormatUint(t.seq, 10)
	resp := loginattempt.Response{Status: r.Status, Header: cloneHeader(r.ResponseHeader), Body: r.ResponseBody}
	switch {
	case t.loginPlan.Login && r.ResponseReceived:
		tracker.Complete(t.loginPlan, ref, t.loginReq, resp)
	case t.loginPlan.Login:
		tracker.Unknown(t.loginPlan, ref)
	case r.ResponseReceived:
		tracker.Observe(t.loginReq, resp)
	}
}
