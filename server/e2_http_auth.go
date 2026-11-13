package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
)

// E2HTTPFileAuth는 실행 정책의 인증 준비 칸이다(C14 구현 설계 2·4, docs/account-login-decision.md). 실행 시작 때 서버가
// 계정마다 로그인 요청을 한 번 보낸다. 로그인 방식(앱별 설정)과 인증 준비 규칙 참조, 계정별 자격 보호 파일 pin만 담고
// 자격 값은 담지 않는다. 자격 파일은 실행 결속 디렉터리 안에 있고 SHA-256으로 고정한다.
type E2HTTPFileAuth struct {
	RuleRef  string                    `json:"rule_ref"`
	Login    diagnosisprep.TargetLogin `json:"login"`
	Accounts []E2HTTPFileAuthAccount   `json:"accounts"`
}

type E2HTTPFileAuthAccount struct {
	Label      string        `json:"label"`
	Credential E2HTTPFilePin `json:"credential"`
}

// 인증 준비 결과(실행 기록의 account_auth 값)다. 세션 값은 여기에 없다.
const (
	E2HTTPAuthAuthenticated   = "authenticated"
	E2HTTPAuthLoginFailed     = "login_failed"
	E2HTTPAuthSessionNotFound = "session_not_found"
	E2HTTPAuthResultUnknown   = "result_unknown"
	// E2HTTPAuthNotSent는 E②가 보내지 않은 경우다(예산 소진·정책 거부·자격 파일을 읽지 못함 등).
	E2HTTPAuthNotSent = "not_sent"
)

const maxAuthCredentialBytes = 8 << 10

// E2HTTPSessions는 인증된 시험 계정의 보낼 세션 헤더다. 실행 메모리에만 있고 디스크·저널·기록에 쓰지 않는다.
type E2HTTPSessions map[string]http.Header

func (E2HTTPSessions) String() string   { return "server.E2HTTPSessions[protected]" }
func (E2HTTPSessions) GoString() string { return "server.E2HTTPSessions[protected]" }

// E2HTTPAuthResult는 인증 준비 결과다. AccountAuth는 라벨 → 결과(값 없음), Sessions는 인증된 계정의 세션 헤더다.
// CallRefs는 인증 준비가 연 E② 호출이다. Worker Bash 결과가 없는 프로그램 호출이라 마감 대조에서 따로 센다.
type E2HTTPAuthResult struct {
	AccountAuth map[string]string
	Sessions    E2HTTPSessions
	CallRefs    []string
}

func validE2HTTPFileAuth(a *E2HTTPFileAuth, mode e2proxy.HeaderMode, rules []E2HTTPFileRule) bool {
	if a == nil {
		// 시험 계정 모드는 인증 준비 칸이 있는 정책에서만 받는다(C14 결정 6). 계정 없이 모델이 쓴 자격을 그대로 보내는 실행은 없다.
		return mode != e2proxy.HeaderTestAccountPassthrough
	}
	if !diagnosisprep.ValidTargetLogin(a.Login) || len(a.Accounts) < 1 || len(a.Accounts) > diagnosisprep.MaxTargetAccounts {
		return false
	}
	found := false
	for _, r := range rules {
		if r.Ref == a.RuleRef {
			found = r.Purpose == e2proxy.PurposeAuthPrep && r.PathPrefix == a.Login.Path && len(r.Methods) == 1 && r.Methods[0] == a.Login.Method
		}
	}
	if !found {
		return false
	}
	seen := map[string]bool{}
	for _, account := range a.Accounts {
		if !diagnosisprep.ValidAccountLabel(account.Label) || seen[account.Label] || !e2HTTPHostRef.MatchString(account.Credential.File) || !e2HTTPHash(account.Credential.SHA256) {
			return false
		}
		seen[account.Label] = true
	}
	return true
}

var errAuthLoginFailed = errors.New("auth_login_failed")

// e2HTTPProgramCalls는 서버가 연 E② 호출(인증 준비 등)이다. Worker Bash 결과가 없으므로 마감 때 E② 기록의 최종·감사
// 완료만 확인하고 신뢰 결과 대조에서는 뺀다. 다시 로그인처럼 실행 중에 더해질 수 있어 잠근다.
type e2HTTPProgramCalls struct {
	mu   sync.Mutex
	refs map[string]bool
}

func newE2HTTPProgramCalls(refs ...string) *e2HTTPProgramCalls {
	p := &e2HTTPProgramCalls{refs: map[string]bool{}}
	for _, ref := range refs {
		p.add(ref)
	}
	return p
}

func (p *e2HTTPProgramCalls) add(ref string) {
	if p == nil || ref == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refs[ref] = true
}

func (p *e2HTTPProgramCalls) snapshot() map[string]bool {
	out := map[string]bool{}
	if p == nil {
		return out
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for ref := range p.refs {
		out[ref] = true
	}
	return out
}

// e2HTTPAccountUsernames는 결속 시험 계정의 사용자명이다(같은 pin 검사로 읽음). 표시용 경로 패턴에서 같은 조각을 가리는
// 데만 쓰고 메모리에만 둔다. 읽지 못한 계정은 빠진다(그 계정은 인증 준비도 보내지 않는다).
func e2HTTPAccountUsernames(dir string, auth E2HTTPFileAuth) map[string]bool {
	out := map[string]bool{}
	for _, account := range auth.Accounts {
		raw, err := e2HTTPPinnedBytes(dir, account.Credential, maxAuthCredentialBytes)
		if err != nil {
			continue
		}
		var cred struct {
			Username string `json:"username"`
		}
		if json.Unmarshal(raw, &cred) == nil && cred.Username != "" {
			out[cred.Username] = true
		}
		clear(raw)
	}
	return out
}

// e2HTTPAuthPrep는 계정마다 로그인 요청을 E② 인증 준비 규칙으로 한 번씩 보낸다. 재시도는 없다. 세션 값은 출력 가림
// 전용 값으로 등록하고(응답 보류에는 쓰지 않음) 결과에만 담는다. 자격 값은 쓴 뒤 지운다.
func e2HTTPAuthPrep(ctx context.Context, execution *e2proxy.Execution, dir, origin string, auth E2HTTPFileAuth) (E2HTTPAuthResult, error) {
	out := E2HTTPAuthResult{AccountAuth: map[string]string{}, Sessions: E2HTTPSessions{}}
	if execution == nil || ctx == nil {
		return out, ErrE2HTTPHost
	}
	call, err := execution.BeginCall(ctx, "auth-prep")
	if err != nil {
		for _, account := range auth.Accounts {
			out.AccountAuth[account.Label] = E2HTTPAuthNotSent
		}
		return out, nil
	}
	out.CallRefs = append(out.CallRefs, call.Ref())
	defer func() {
		if _, closeErr := call.Close(context.WithoutCancel(ctx)); closeErr != nil {
			log.Print("[e2-http] 인증 준비 call 마감 실패")
		}
	}()
	for _, account := range auth.Accounts {
		out.AccountAuth[account.Label] = authPrepOne(ctx, call, execution, dir, origin, auth.Login, account, out.Sessions)
	}
	return out, nil
}

func authPrepOne(ctx context.Context, call *e2proxy.Call, execution *e2proxy.Execution, dir, origin string, login diagnosisprep.TargetLogin, account E2HTTPFileAuthAccount, sessions E2HTTPSessions) string {
	raw, err := e2HTTPPinnedBytes(dir, account.Credential, maxAuthCredentialBytes)
	if err != nil {
		return E2HTTPAuthNotSent
	}
	var cred struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	decodeErr := json.Unmarshal(raw, &cred)
	clear(raw)
	if decodeErr != nil || cred.Username == "" || cred.Password == "" {
		return E2HTTPAuthNotSent
	}
	// 비밀번호는 보내기 전에 이 실행의 보호 값으로 등록한다. 대상이 나중에 다른 응답에 비밀번호를 그대로 되돌려 주면 그
	// 응답은 보류되고 출력에서도 가려진다(S09: 비밀번호는 모델 입력에 없다). 보호할 수 없는 비밀번호(너무 짧음 등)면
	// 로그인하지 않는다.
	if execution.AddProtectedValues([]byte(cred.Password)) != nil {
		cred.Username, cred.Password = "", ""
		return E2HTTPAuthNotSent
	}
	body, contentType := authPrepBody(login, cred.Username, cred.Password)
	cred.Username, cred.Password = "", ""
	defer clear(body)
	req, err := http.NewRequestWithContext(ctx, login.Method, origin+login.Path, bytes.NewReader(body))
	if err != nil {
		return E2HTTPAuthNotSent
	}
	req.Header.Set("Content-Type", contentType)
	session, receipt, err := call.AuthPrep(ctx, req, authExtractor(login))
	switch {
	case err == nil:
	case errors.Is(err, e2proxy.ErrAuthSession):
		if receipt.StatusCode >= http.StatusBadRequest {
			return E2HTTPAuthLoginFailed
		}
		return E2HTTPAuthSessionNotFound
	case receipt.Reason == e2proxy.Unknown:
		// 대상이 받았을 수 있다. 다시 보내지 않는다.
		return E2HTTPAuthResultUnknown
	default:
		return E2HTTPAuthNotSent
	}
	values := authSessionValues(login, session)
	if len(values) == 0 || execution.AddRedactValues(values...) != nil {
		// 가릴 수 없는 세션(너무 짧음)은 쓰지 않는다.
		return E2HTTPAuthSessionNotFound
	}
	sessions[account.Label] = session
	return E2HTTPAuthAuthenticated
}

// authPrepBody는 로그인 방식의 본문 형식대로 사용자명·비밀번호를 담는다.
func authPrepBody(login diagnosisprep.TargetLogin, username, password string) ([]byte, string) {
	if login.Format == "form" {
		return []byte(url.Values{login.UsernameField: {username}, login.PasswordField: {password}}.Encode()), "application/x-www-form-urlencoded"
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]string{login.UsernameField: username, login.PasswordField: password})
	return bytes.TrimSpace(b.Bytes()), "application/json"
}

// authExtractor는 로그인 방식의 세션 위치에서 보낼 헤더를 뽑는다. 4xx·5xx는 로그인 실패다.
func authExtractor(login diagnosisprep.TargetLogin) e2proxy.AuthExtractor {
	return func(resp *http.Response, body []byte) (http.Header, error) {
		if resp.StatusCode >= http.StatusBadRequest {
			return nil, errAuthLoginFailed
		}
		if len(login.Cookies) > 0 {
			got := map[string]string{}
			for _, c := range resp.Cookies() {
				got[c.Name] = c.Value
			}
			parts := make([]string, 0, len(login.Cookies))
			for _, name := range login.Cookies {
				if got[name] == "" {
					return nil, e2proxy.ErrAuthSession
				}
				parts = append(parts, name+"="+got[name])
			}
			return http.Header{"Cookie": {strings.Join(parts, "; ")}}, nil
		}
		var doc any
		if json.Unmarshal(body, &doc) != nil {
			return nil, e2proxy.ErrAuthSession
		}
		for _, segment := range strings.Split(login.JSONField, ".") {
			object, ok := doc.(map[string]any)
			if !ok {
				return nil, e2proxy.ErrAuthSession
			}
			doc = object[segment]
		}
		value, ok := doc.(string)
		if !ok || value == "" || strings.ContainsAny(value, "\r\n") {
			return nil, e2proxy.ErrAuthSession
		}
		return http.Header{http.CanonicalHeaderKey(login.Header): {login.Prefix + value}}, nil
	}
}

// authSessionValues는 출력에서 가릴 세션 값이다: 쿠키 값 각각, 또는 토큰 값(접두 없이)과 헤더 값 전체.
func authSessionValues(login diagnosisprep.TargetLogin, session http.Header) [][]byte {
	var values []string
	if len(login.Cookies) > 0 {
		for _, part := range strings.Split(session.Get("Cookie"), "; ") {
			if _, value, ok := strings.Cut(part, "="); ok && value != "" {
				values = append(values, value)
			}
		}
	} else {
		full := session.Get(login.Header)
		values = append(values, strings.TrimPrefix(full, login.Prefix))
	}
	sort.Strings(values)
	out := make([][]byte, 0, len(values))
	for i, v := range values {
		if i > 0 && values[i-1] == v {
			continue
		}
		out = append(out, []byte(v))
	}
	return out
}
