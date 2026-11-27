package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"github.com/Autumn-27/artex/learnedlogin"
)

// 확정 단계 학습 로그인 재사용(docs/contracts/model-login-attempts.md §6-2). E③ 직전 다시 로그인이 로그인 설정(9칸) 대신
// 모델이 성공한 로그인에서 배운 방법으로 로그인한다. 보내는 길은 지금 다시 로그인과 같다: 확정 몫을 쓰는 E② 호출 하나
// (BeginE3ReloginCall), 같은 계정 줄 세우기·연속 실패·간격 상한(e2HTTPRefreshCounter), 보호 값 등록·세션 출력 가림. 다른
// 점은 E② 허용이 로그인 전용 규칙이 아니라 그 실행의 Worker 승인 규칙(쓰기 승인 범위)이라는 것이다(§2-2).

// e2HTTPLearnedLoginTokenMinBytes는 응답 JSON·헤더 값을 세션 토큰으로 볼 최소 길이다(계약 §3 기본값, 탐색·진단 판정과 같음).
const e2HTTPLearnedLoginTokenMinBytes = 16

// E2HTTPLearnedLoginRequest는 계정 하나의 학습 로그인 재사용 입력이다. 방법은 확정 단계(제품)가 이 회차 기록·대상 계정
// 저널에서 고른다. 서버는 방법을 다시 검증하고 자격은 실행에 결속한 파일에서만 읽는다.
type E2HTTPLearnedLoginRequest struct {
	Label  string
	Method learnedlogin.Method
	// PriorFailures는 이 회차 기록이 센 아이디별 연속 실패 수다(§5, 탐색·진단·확정 합산). MaxFailures에 닿은 아이디는
	// 보내지 않는다(login_attempts_exhausted).
	PriorFailures map[string]int
	MaxFailures   int
}

// E2HTTPLearnedLoginResult는 학습 로그인 재사용 결과다. 값(아이디 말고 자격·세션·CSRF)은 없다. Identifier는 로그인 시도
// 기록(§3 identifier)에 남는 등록 아이디다.
type E2HTTPLearnedLoginResult struct {
	// Result는 지금 다시 로그인과 같은 고정 결과 코드다(authenticated·login_failed·session_not_found·result_unknown·
	// not_sent·refresh_limit·refresh_cooldown). authenticated여도 인증 성공 확정이 아니다(대조 전, §6-2).
	Result string
	Reason learnedlogin.Reason
	// Requests는 이 다시 로그인이 대상에 보낸(보내기 시작한) 요청 수다(로그인 화면 GET + 로그인 POST).
	Requests int
	// Login은 로그인 POST의 기록 칸이다(보내지 않았으면 Transport가 not_sent).
	Transport         string
	TransportRef      string
	Status            int
	RedirectPath      string
	LoginFormReturned bool
	At                time.Time
	Identifier        string
	// LoginPaths는 로그인 화면 경로다(대조 판정의 막힘 기준, §4).
	LoginPaths []string
	// BindingRef는 성공했을 때 새 세션에 발급한 계정 결속 참조다. 확정 단계 신원 요청이 이 세션으로 나갔는지 보는 데 쓴다.
	BindingRef string
}

// E2HTTPLoginContrastResult는 세션 없는 대조 GET 하나의 결과다(§4 프로그램 대조 요청).
type E2HTTPLoginContrastResult struct {
	Sent         bool
	Status       int
	RedirectPath string
	TransportRef string
	Reason       learnedlogin.Reason
}

// E2HTTPLearnedRelogin은 확정 단계(E③) 직전 학습 로그인 재사용이다. 세션 저장소만 구현한다. 연결되지 않은 실행이면 보내지
// 않는다(not_sent).
type E2HTTPLearnedRelogin interface {
	LearnedRelogin(ctx context.Context, req E2HTTPLearnedLoginRequest) E2HTTPLearnedLoginResult
	LoginContrast(ctx context.Context, path string) E2HTTPLoginContrastResult
}

var _ E2HTTPLearnedRelogin = (*e2HTTPSessionStore)(nil)

// LearnedRelogin은 같은 계정 다시 로그인과 같은 줄에 서서 학습 방법으로 로그인한다. 성공하면 세션과 결속 참조를 바꾼다.
func (s *e2HTTPSessionStore) LearnedRelogin(ctx context.Context, req E2HTTPLearnedLoginRequest) E2HTTPLearnedLoginResult {
	out := E2HTTPLearnedLoginResult{Result: E2HTTPAuthNotSent, Reason: learnedlogin.ReasonNotSent, Transport: learnedlogin.TransportNotSent}
	if s == nil || s.learned == nil {
		return out
	}
	_, _, ref := s.refreshWithBinding(ctx, req.Label, func() (http.Header, string) {
		var session http.Header
		out, session = s.learned(ctx, req)
		return session, out.Result
	})
	out.BindingRef = ref
	return out
}

// LoginContrast는 세션 없는 대조 GET을 하나 보낸다. 연결되지 않은 실행이면 보내지 않는다.
func (s *e2HTTPSessionStore) LoginContrast(ctx context.Context, path string) E2HTTPLoginContrastResult {
	if s == nil || s.contrast == nil {
		return E2HTTPLoginContrastResult{Reason: learnedlogin.ReasonNotSent}
	}
	return s.contrast(ctx, path)
}

// e2HTTPLearnedReason은 E② 거부 사유를 학습 로그인 고정 사유로 바꾼다.
func e2HTTPLearnedReason(reason e2proxy.Reason) learnedlogin.Reason {
	switch reason {
	case e2proxy.PolicyDenied:
		return learnedlogin.ReasonScopeDenied
	case e2proxy.BudgetExhausted, e2proxy.AttemptLimit:
		return learnedlogin.ReasonBudgetExhausted
	}
	return learnedlogin.ReasonNotSent
}

func learnedResponse(resp *http.Response, body []byte) learnedlogin.Response {
	return learnedlogin.Response{Status: resp.StatusCode, Header: resp.Header.Clone(), Body: bytes.Clone(body)}
}

// e2HTTPLearnedLogin은 계정 하나를 학습 방법으로 로그인시킨다. 순서: 방법·범위 검사 → 연속 실패·간격 상한 → 자격 읽기·보호
// 값 등록 → (필요하면) 로그인 화면 GET으로 CSRF 같은 칸·로그인 전 쿠키 받기 → 로그인 POST → 실패 신호·배운 세션 자리 판정 →
// 세션 출력 가림 등록. 어느 단계든 값을 지어내지 않고, 결과 불명 로그인은 다시 보내지 않는다. 쿠키는 이 계정 이번 로그인
// 전용 문맥에만 담는다.
func e2HTTPLearnedLogin(ctx context.Context, execution *e2proxy.Execution, dir, origin string, auth E2HTTPFileAuth, counter *e2HTTPRefreshCounter, program *e2HTTPProgramCalls, req E2HTTPLearnedLoginRequest) (out E2HTTPLearnedLoginResult, session http.Header) {
	out = E2HTTPLearnedLoginResult{Result: E2HTTPAuthNotSent, Reason: learnedlogin.ReasonNotSent, Transport: learnedlogin.TransportNotSent}
	m := req.Method.Clone()
	out.LoginPaths = []string{m.Path}
	var account *E2HTTPFileAuthAccount
	for i := range auth.Accounts {
		if auth.Accounts[i].Label == req.Label {
			account = &auth.Accounts[i]
		}
	}
	originURL, parseErr := url.Parse(origin)
	if execution == nil || ctx == nil || ctx.Err() != nil || account == nil || counter == nil || parseErr != nil || originURL.Host == "" {
		return out, nil
	}
	defer func() { logLearnedLogin(ctx, req.Label, out) }()
	if reason := m.Validate(); reason != "" {
		out.Reason = reason
		return out, nil
	}
	// 모델 단계 진단 쓰기 정책과 같은 위험 단어로 거른다(탈퇴·해지·삭제 같은 쓰기를 로그인으로 다시 보내지 않음).
	if learnedRiskyPath(http.MethodPost, m.Path) || m.NeedsPrep() && learnedRiskyPath(http.MethodGet, m.PrepPath()) {
		out.Reason = learnedlogin.ReasonScopeDenied
		return out, nil
	}
	if status := counter.begin(req.Label); status != "" {
		out.Result, out.Reason = status, learnedlogin.ReasonNotSent
		if status == E2HTTPRefreshLimit {
			out.Reason = learnedlogin.ReasonAttemptsExhausted
		}
		return out, nil
	}
	defer func() { counter.finish(req.Label, out.Result == E2HTTPAuthAuthenticated && len(session) > 0) }()
	raw, err := e2HTTPPinnedBytes(dir, account.Credential, maxAuthCredentialBytes)
	if err != nil {
		out.Reason = learnedlogin.ReasonCredentialUnavailable
		return out, nil
	}
	var cred learnedlogin.Credential
	var file struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	decodeErr := json.Unmarshal(raw, &file)
	clear(raw)
	cred.Username, cred.Password = file.Username, file.Password
	file.Username, file.Password = "", ""
	defer func() { cred = learnedlogin.Credential{} }()
	if decodeErr != nil || cred.Username == "" || cred.Password == "" {
		out.Reason = learnedlogin.ReasonCredentialUnavailable
		return out, nil
	}
	out.Identifier = cred.Username
	if req.MaxFailures > 0 && req.PriorFailures[cred.Username] >= req.MaxFailures {
		out.Reason = learnedlogin.ReasonAttemptsExhausted
		return out, nil
	}
	// 비밀번호는 보내기 전에 보호 값으로 등록한다(인증 준비와 같음). 보호할 수 없는 비밀번호로는 로그인하지 않는다.
	if execution.AddProtectedPasswords([]byte(cred.Password)) != nil {
		out.Reason = learnedlogin.ReasonCredentialUnavailable
		return out, nil
	}
	call, err := execution.BeginE3ReloginCall(ctx, "e3-learned-login")
	if err != nil {
		return out, nil
	}
	program.add(call.Ref())
	defer func() {
		if _, closeErr := call.Close(context.WithoutCancel(ctx)); closeErr != nil {
			log.Print("[e2-http] 학습 로그인 call 마감 실패")
		}
	}()
	jar := learnedlogin.Jar{}
	values := map[string]string{}
	if m.NeedsPrep() {
		// V1은 로그인 화면 GET, V2는 출처가 결속된 별도 GET 한 건이다.
		// 둘 다 현재 E② 승인 범위와 위험 경로 검사를 통과해야 한다.
		var screen learnedlogin.Screen
		reason := learnedlogin.Reason("")
		prepPath := m.PrepPath()
		prep, buildErr := http.NewRequestWithContext(ctx, http.MethodGet, origin+prepPath, nil)
		if buildErr != nil {
			out.Reason = learnedlogin.ReasonPrepUnavailable
			return out, nil
		}
		_, receipt, prepErr := call.LearnedLogin(ctx, prep, e2proxy.PurposeLoginPrep, func(resp *http.Response, body []byte) (http.Header, error) {
			copied := learnedResponse(resp, body)
			defer clear(copied.Body)
			if screen, reason = learnedlogin.ParseScreen(copied); reason != "" {
				return nil, errors.New("login screen unavailable")
			}
			if values, reason = screen.Values(m); reason != "" {
				return nil, errors.New("login screen values unavailable")
			}
			jar.AbsorbForLogin(copied, prepPath, m.Path, originURL, time.Now())
			if m.SessionSource == learnedlogin.SourceContinued {
				// 로그인 전부터 이어질 세션 쿠키도 모든 경로에 실리는 쿠키여야 한다(확정 단계 다른 경로 요청에 싣는다).
				if !learnedlogin.SessionCookieScopeOK(copied, prepPath, m.SessionNames) {
					reason = learnedlogin.ReasonFormatUnsupported
					return nil, errors.New("pre-login session cookie path too narrow")
				}
				for _, name := range m.SessionNames {
					if jar[name] == "" {
						reason = learnedlogin.ReasonPrepUnavailable
						return nil, errors.New("pre-login session cookie missing")
					}
				}
			}
			if cookie := jar.Header(); cookie != "" {
				return http.Header{"Cookie": {cookie}}, nil
			}
			return http.Header{}, nil
		})
		if receipt.ForwardAttempted {
			out.Requests++
		}
		switch {
		case prepErr == nil:
		case reason != "":
			out.Reason = reason
			return out, nil
		case receipt.Reason == e2proxy.Complete || receipt.Reason == e2proxy.Withheld || receipt.Reason == e2proxy.Unknown:
			out.Reason = learnedlogin.ReasonPrepUnavailable
			return out, nil
		default:
			out.Reason = e2HTTPLearnedReason(receipt.Reason)
			return out, nil
		}
		out.LoginPaths = learnedlogin.LoginPaths(m, prepPath, screen)
	}
	built, reason := learnedlogin.Build(m, cred, values)
	if reason != "" {
		out.Reason = reason
		return out, nil
	}
	defer clear(built.Body)
	loginReq, err := http.NewRequestWithContext(ctx, built.Method, origin+built.Path, bytes.NewReader(built.Body))
	if err != nil {
		out.Reason = learnedlogin.ReasonInputUnavailable
		return out, nil
	}
	loginReq.Header.Set("Content-Type", built.ContentType)
	if cookie := jar.Header(); cookie != "" {
		loginReq.Header.Set("Cookie", cookie)
	}
	out.At = time.Now().UTC()
	var checked learnedlogin.LoginResult
	extracted, receipt, loginErr := call.LearnedLogin(ctx, loginReq, e2proxy.PurposeLearnedLogin, func(resp *http.Response, body []byte) (http.Header, error) {
		copied := learnedResponse(resp, body)
		defer clear(copied.Body)
		checked = learnedlogin.CheckLogin(m, copied, jar, originURL, out.LoginPaths, e2HTTPLearnedLoginTokenMinBytes, time.Now())
		if checked.Outcome != learnedlogin.OutcomeUnconfirmed {
			return nil, errors.New("learned login failed")
		}
		return checked.Session, nil
	})
	out.TransportRef = receipt.CallRef + "/" + receipt.AttemptID
	if receipt.ForwardAttempted {
		out.Requests++
	}
	out.Status, out.RedirectPath, out.LoginFormReturned = receipt.StatusCode, checked.RedirectPath, checked.LoginFormReturned
	switch {
	case loginErr == nil:
	case receipt.Reason == e2proxy.Unknown:
		// 대상이 받았을 수 있다. 다시 보내지 않는다.
		out.Transport, out.Result, out.Reason = learnedlogin.TransportUnknown, E2HTTPAuthResultUnknown, learnedlogin.ReasonResultUnknown
		return out, nil
	case receipt.Reason == e2proxy.Withheld:
		// 대상은 답했지만 끝까지 판정할 수 없는 응답이다(상한 초과·부분·압축). 세션을 모르니 이 계정은 쓰지 않는다. 실패 신호를
		// 본 것이 아니므로 성공 근거 부족이다(연속 실패에 세지 않음).
		out.Transport, out.Result, out.Reason = learnedlogin.TransportSent, E2HTTPAuthSessionNotFound, learnedlogin.ReasonAuthUnconfirmed
		return out, nil
	case errors.Is(loginErr, e2proxy.ErrAuthSession):
		out.Transport, out.Result = learnedlogin.TransportSent, E2HTTPAuthSessionNotFound
		out.Reason = checked.Reason
		if checked.Reason == learnedlogin.ReasonRejected {
			out.Result = E2HTTPAuthLoginFailed
		}
		if out.Reason == "" {
			// 판정은 세션을 냈지만 E②가 보호 비밀번호가 든 세션으로 거부했다.
			out.Reason = learnedlogin.ReasonSessionNotFound
		}
		return out, nil
	default:
		out.Reason = e2HTTPLearnedReason(receipt.Reason)
		return out, nil
	}
	out.Transport = learnedlogin.TransportSent
	if err := execution.AddRedactValues(learnedSessionValues(m, extracted)...); err != nil {
		out.Result, out.Reason = E2HTTPAuthSessionNotFound, learnedlogin.ReasonSessionNotFound
		return out, nil
	}
	out.Result, out.Reason = E2HTTPAuthAuthenticated, ""
	return out, extracted
}

// learnedRiskyPath는 위험 동작 단어 검사를 원래 경로와 E②가 범위를 볼 때 쓰는 한 번 푼 경로 둘 다에 적용한다(Codex 2차
// 리뷰: /a/%64elete 같은 인코딩 표기로 검사를 지나지 않게). 풀 수 없는 경로는 위험으로 본다(보내지 않음).
func learnedRiskyPath(method, raw string) bool {
	decoded, err := url.PathUnescape(raw)
	return err != nil || diagnosisflow.RiskyRequest(method, raw) || diagnosisflow.RiskyRequest(method, decoded)
}

// learnedSessionValues는 출력 가림에 등록할 세션 값이다(쿠키 각각 또는 접두 없는 토큰).
func learnedSessionValues(m learnedlogin.Method, session http.Header) [][]byte {
	var out [][]byte
	if m.SessionSentAs == learnedlogin.SentAsCookie {
		for _, part := range strings.Split(session.Get("Cookie"), "; ") {
			if _, value, ok := strings.Cut(part, "="); ok && value != "" {
				out = append(out, []byte(value))
			}
		}
		return out
	}
	name, prefix, ok := learnedlogin.SentAsHeader(m.SessionSentAs)
	if ok {
		out = append(out, []byte(strings.TrimPrefix(session.Get(name), prefix)))
	}
	return out
}

// e2HTTPLoginContrast는 세션 없는 GET 하나를 확정 몫으로 보낸다(§4 프로그램 대조 요청). 요청에는 인증 헤더가 없다. 응답은
// 상태와 같은 origin 리다이렉트 경로만 본다.
func e2HTTPLoginContrast(ctx context.Context, execution *e2proxy.Execution, origin string, program *e2HTTPProgramCalls, path string) E2HTTPLoginContrastResult {
	out := E2HTTPLoginContrastResult{Reason: learnedlogin.ReasonNotSent}
	originURL, parseErr := url.Parse(origin)
	if execution == nil || ctx == nil || ctx.Err() != nil || parseErr != nil || originURL.Host == "" || !strings.HasPrefix(path, "/") ||
		learnedRiskyPath(http.MethodGet, path) {
		return out
	}
	call, err := execution.BeginE3ReloginCall(ctx, "e3-login-contrast")
	if err != nil {
		return out
	}
	program.add(call.Ref())
	defer func() {
		if _, closeErr := call.Close(context.WithoutCancel(ctx)); closeErr != nil {
			log.Print("[e2-http] 대조 요청 call 마감 실패")
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+path, nil)
	if err != nil {
		return out
	}
	_, receipt, sendErr := call.LearnedLogin(ctx, req, e2proxy.PurposeLoginPrep, func(resp *http.Response, _ []byte) (http.Header, error) {
		out.Status = resp.StatusCode
		out.RedirectPath = learnedlogin.RedirectPath(originURL, learnedlogin.Response{Status: resp.StatusCode, Header: resp.Header})
		return http.Header{}, nil
	})
	out.TransportRef = receipt.CallRef + "/" + receipt.AttemptID
	out.Sent = receipt.ForwardAttempted
	switch {
	case sendErr == nil:
		out.Reason = ""
	case receipt.Reason == e2proxy.Unknown || receipt.Reason == e2proxy.Withheld:
		out.Status, out.Reason = 0, learnedlogin.ReasonAuthUnconfirmed
	default:
		out.Reason = e2HTTPLearnedReason(receipt.Reason)
	}
	_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "server", Event: "service_request_finished", Stage: "e3_confirmation",
		Status: contrastStatus(out), ReasonCode: learnedLogReason(out.Reason), CallRef: receipt.CallRef})
	return out
}

func contrastStatus(out E2HTTPLoginContrastResult) string {
	if out.Reason == "" {
		return "succeeded"
	}
	if out.Sent {
		return "unknown"
	}
	return "not_sent"
}

// logLearnedLogin은 계정별 학습 로그인 결과를 고정 코드로 남긴다(값 없음).
func logLearnedLogin(ctx context.Context, label string, out E2HTTPLearnedLoginResult) {
	status := "failed"
	switch out.Result {
	case E2HTTPAuthAuthenticated:
		status = "succeeded"
	case E2HTTPAuthResultUnknown:
		status = "unknown"
	case E2HTTPAuthNotSent, E2HTTPRefreshLimit, E2HTTPRefreshCooldown:
		status = "not_sent"
	}
	_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "server", Event: "account_auth_checked", Stage: "e3_confirmation",
		Status: status, ReasonCode: learnedLogReason(out.Reason), AccountLabel: label})
}

// learnedLogReason은 진단 로그 사유 어휘로 바꾼다. 연속 실패 상한은 기록 칸(§5 login_attempts_exhausted)과 따로 학습 로그인
// 어휘를 쓴다(모델 로그인 판정 로그와 구분).
func learnedLogReason(reason learnedlogin.Reason) string {
	if reason == learnedlogin.ReasonAttemptsExhausted {
		return "learned_login_attempts_exhausted"
	}
	return string(reason)
}
