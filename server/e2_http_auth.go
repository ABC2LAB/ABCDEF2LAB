package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
)

// E2HTTPFileAuth는 실행 정책의 인증 준비 칸이다(C14 구현 설계 2·4, docs/account-login-decision.md). 실행 시작 때 서버가
// 계정마다 로그인 요청을 한 번 보낸다. 로그인 방식(앱별 설정)과 인증 준비 규칙 참조, 계정별 자격 보호 파일 pin만 담고
// 자격 값은 담지 않는다. 자격 파일은 실행 결속 디렉터리 안에 있고 SHA-256으로 고정한다.
type E2HTTPFileAuth struct {
	PasswordResponse string                    `json:"password_response,omitempty"`
	RuleRef          string                    `json:"rule_ref"`
	Login            diagnosisprep.TargetLogin `json:"login"`
	Accounts         []E2HTTPFileAuthAccount   `json:"accounts"`
	// ModelLogin은 모델이 직접 로그인하는 회차다(사용자 결정 2026-11-26, docs/contracts/model-login-attempts.md). 켜면 실행을
	// 잡을 때 프로그램이 로그인하지 않고 결속 계정의 자격을 Worker 지침으로 준다. 끈 정책(선택 후보 검증 회차·이전 결속)은
	// 지금처럼 프로그램이 인증 준비로 로그인한다. 확정 단계 다시 로그인은 둘 다 프로그램이 한다.
	ModelLogin bool `json:"model_login,omitempty"`
}

type E2HTTPFileAuthAccount struct {
	Label      string        `json:"label"`
	Credential E2HTTPFilePin `json:"credential"`
}

// CandidateAccountRevision is a non-secret stable reference for one marked
// test account and one credential-file revision. The credential file includes
// a random nonce, so its SHA-256 is not an unsalted password verifier.
func CandidateAccountRevision(label, credentialSHA256 string) string {
	if !diagnosisprep.ValidAccountLabel(label) || len(credentialSHA256) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(credentialSHA256); err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(label + "\x00" + credentialSHA256))
	return hex.EncodeToString(sum[:])
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
	if a.PasswordResponse != "" && (a.PasswordResponse != e2proxy.PasswordResponseMaskSpans && a.PasswordResponse != e2proxy.PasswordResponseModelVisible || mode != e2proxy.HeaderTestAccountPassthrough) || !diagnosisprep.ValidTargetLogin(a.Login) || len(a.Accounts) < 1 || len(a.Accounts) > diagnosisprep.MaxTargetAccounts {
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

// 계정 결속 참조(E③↔E② v3 계약 §2.2·§4, account_binding_ref). C14 인증에 성공한 세션마다 세션 저장소가 새 난수로
// 발급한다. 실행(run_ref)·계정 라벨·세션 판에 결속하는 것은 이 저장소의 표이고, 참조 값에는 실행·라벨·자격·세션 값이 하나도
// 들어가지 않는다(그래서 참조에서 아무것도 추정할 수 없다). 다시 로그인으로 세션이 바뀌면 참조도 새로 발급하고 옛 참조는
// 버린다. 참조는 비밀이 아니다(v3 이벤트·영수증에 실린다).
const (
	e2HTTPAccountBindingPrefix      = "acct-"
	e2HTTPAccountBindingRandomBytes = 16
)

// ErrE2HTTPAccountBinding은 계정 결속 참조가 그 실행·라벨의 지금 결속이 아니어서 세션을 주지 않는 경우다(옛 참조·다른
// 라벨·다른 실행·발급 안 됨).
var ErrE2HTTPAccountBinding = errors.New("server: account binding is not current")

// E2HTTPAccountBindings는 E²가 확정 단계(E③) 프로그램 요청에 시험 계정 세션을 붙일 때 쓰는 조회다(v3 계약 §9의
// AccountBindingRef). 신뢰된 세션 저장소만 구현한다. 모델·요청이 준 참조로는 세션을 얻을 수 없고, 지금 결속과 같은
// 참조만 그 순간의 세션 사본을 받는다.
type E2HTTPAccountBindings interface {
	// AccountBindingRef는 그 실행·라벨에 지금 결속된 참조다. 인증된 세션이 없거나 실행이 다르면 ok=false다.
	AccountBindingRef(runRef, label string) (ref string, ok bool)
	// BoundSession은 ref가 그 실행·라벨의 지금 결속일 때만 세션 헤더 사본을 준다. 아니면 ErrE2HTTPAccountBinding이다.
	BoundSession(runRef, label, ref string) (http.Header, error)
}

// e2HTTPSessionStore는 실행이 들고 있는 시험 계정 세션이다. refresh_session이 성공하면 그 계정 세션을 바꾸고, Worker를
// 시작할 때마다 사본을 읽는다(#550 이동찬 리뷰: 로그인이 이전 세션을 무효로 하는 대상에서 다음 Worker가 옛 세션으로 시작하지
// 않게). 여러 Worker가 동시에 읽고 다시 로그인이 쓰므로 잠근다. 세션마다 계정 결속 참조를 같은 잠금 안에서 바꾼다.
type e2HTTPSessionStore struct {
	mu        sync.RWMutex
	sessions  E2HTTPSessions
	gateMu    sync.Mutex
	gates     map[string]chan struct{}
	refreshes *e2HTTPRefreshCounter
	// runRef는 이 저장소가 속한 실행이다. 비었으면 결속 참조를 발급하지 않는다(세션은 그대로 쓴다).
	runRef   string
	random   io.Reader
	bindings map[string]string
	// relogin은 확정 단계 직전 다시 로그인이다. refresh_session과 같은 경로(인증 준비 규칙·같은 계정 줄 세우기·실패·간격
	// 상한)이고, 확정 몫을 쓰는 호출로 연다. 시험 계정 실행이고 인증된 계정이 있을 때만 채운다(저장소를 넘기기 전에 정하고
	// 바꾸지 않는다).
	relogin func(context.Context, string) (http.Header, string)
}

var (
	_ E2HTTPAccountBindings = (*e2HTTPSessionStore)(nil)
	_ E2HTTPAccountRelogin  = (*e2HTTPSessionStore)(nil)
)

// E2HTTPAccountRelogin은 확정 단계(E③) 직전에 시험 계정 하나를 다시 로그인시킨다(사용자 결정 2026-11-20 (가)). 세션
// 저장소만 구현한다. 결과는 인증 준비와 같은 고정 코드(authenticated·login_failed·session_not_found·result_unknown·
// not_sent·refresh_limit·refresh_cooldown)이고 세션 값은 저장소 안에만 둔다.
type E2HTTPAccountRelogin interface {
	Relogin(ctx context.Context, label string) string
}

// Relogin은 refresh_session과 같은 경로로 계정 하나를 다시 로그인시킨다. 다시 로그인 경로가 없는 실행이면 보내지 않는다.
func (s *e2HTTPSessionStore) Relogin(ctx context.Context, label string) string {
	if s == nil || s.relogin == nil {
		return E2HTTPAuthNotSent
	}
	_, result := s.relogin(ctx, label)
	return result
}

// E2HTTPAuthSent는 다시 로그인 결과 코드가 로그인 요청을 실제로 보낸 경우인지다(보내지 않음·상한·간격 거부는 아니다).
func E2HTTPAuthSent(result string) bool {
	switch result {
	case E2HTTPAuthAuthenticated, E2HTTPAuthLoginFailed, E2HTTPAuthSessionNotFound, E2HTTPAuthResultUnknown:
		return true
	}
	return false
}

// newE2HTTPSessionStore는 처음 인증 준비의 세션(인증 성공만 들어 있음)으로 저장소를 만들고 계정마다 결속 참조를 발급한다.
// random이 nil이면 crypto/rand다.
func newE2HTTPSessionStore(initial E2HTTPSessions, runRef string, random io.Reader) *e2HTTPSessionStore {
	if random == nil {
		random = rand.Reader
	}
	s := &e2HTTPSessionStore{sessions: E2HTTPSessions{}, refreshes: &e2HTTPRefreshCounter{}, runRef: runRef, random: random,
		bindings: map[string]string{}}
	labels := make([]string, 0, len(initial))
	for label := range initial {
		labels = append(labels, label)
	}
	sort.Strings(labels) // 발급 순서를 정해 시험의 난수 소비가 재현되게 한다
	for _, label := range labels {
		s.sessions[label] = initial[label].Clone()
		s.issueLocked(label) // 빈 세션에는 발급하지 않는다
	}
	return s
}

// issueLocked는 label의 지금 세션에 새 결속 참조를 발급한다. 실행을 모르거나 난수를 읽지 못하면 결속을 지운다(옛 참조가
// 새 세션을 가리키지 않게, 그 계정은 확정 단계에 쓸 수 없음). 호출자는 s.mu를 쓰기로 잡는다.
func (s *e2HTTPSessionStore) issueLocked(label string) {
	delete(s.bindings, label)
	if s.runRef == "" || len(s.sessions[label]) == 0 {
		return
	}
	b := make([]byte, e2HTTPAccountBindingRandomBytes)
	if _, err := io.ReadFull(s.random, b); err != nil {
		log.Printf("E② 계정 결속 참조를 발급하지 못함: 확정 단계에서 이 계정을 쓰지 않음")
		return
	}
	s.bindings[label] = e2HTTPAccountBindingPrefix + hex.EncodeToString(b)
}

func (s *e2HTTPSessionStore) AccountBindingRef(runRef, label string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ref, ok := s.bindings[label]
	if s.runRef == "" || runRef != s.runRef || !ok || len(s.sessions[label]) == 0 {
		return "", false
	}
	return ref, true
}

func (s *e2HTTPSessionStore) BoundSession(runRef, label, ref string) (http.Header, error) {
	if s == nil {
		return nil, ErrE2HTTPAccountBinding
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	current, ok := s.bindings[label]
	header := s.sessions[label]
	if s.runRef == "" || runRef != s.runRef || !ok || ref == "" || ref != current || len(header) == 0 {
		return nil, ErrE2HTTPAccountBinding
	}
	return header.Clone(), nil
}

func (s *e2HTTPSessionStore) snapshot() E2HTTPSessions {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(E2HTTPSessions, len(s.sessions))
	for label, header := range s.sessions {
		out[label] = header.Clone()
	}
	return out
}

// matchSentAccount inspects only the protected in-memory sessions and the
// actual headers passed to the target. A claimed label or matching header
// category alone is never account proof. Ambiguous matches fail closed.
func (s *e2HTTPSessionStore) matchSentAccount(sent http.Header, customHeader string) (string, bool) {
	if s == nil || sent == nil {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	matched := ""
	for label, session := range s.sessions {
		if len(session) == 0 || s.bindings[label] == "" {
			continue
		}
		equal := true
		for key, values := range session {
			if !slices.Equal(sent.Values(key), values) {
				equal = false
				break
			}
		}
		for _, key := range []string{"Authorization", "Cookie", customHeader} {
			if key != "" && len(sent.Values(key)) > 0 && len(session.Values(key)) == 0 {
				equal = false
			}
		}
		if !equal {
			continue
		}
		if matched != "" {
			return "", false
		}
		matched = label
	}
	return matched, matched != ""
}

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

// replace는 세션을 바꾸고 같은 잠금 안에서 결속 참조를 새로 발급한다. 조회가 새 세션과 옛 참조를 같이 보는 순간이 없다.
func (s *e2HTTPSessionStore) replace(label string, header http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[label] = header.Clone()
	s.issueLocked(label)
}

func (s *e2HTTPSessionStore) gate(label string) chan struct{} {
	s.gateMu.Lock()
	defer s.gateMu.Unlock()
	if s.gates == nil {
		s.gates = map[string]chan struct{}{}
	}
	if s.gates[label] == nil {
		s.gates[label] = make(chan struct{}, 1)
	}
	return s.gates[label]
}

// refresh는 계정 하나의 다시 로그인을 보내기부터 세션 교체까지 한 번에 하나만 한다(#550 이동찬 리뷰: 두 Worker가 같은
// 계정을 동시에 다시 로그인시키면 먼저 보낸 쪽 응답이 늦게 와서 더 새 세션을 옛 세션으로 덮을 수 있다). 다른 계정은 막지
// 않는다. 기다리는 동안 문맥이 끝나면 보내지 않는다.
func (s *e2HTTPSessionStore) refresh(ctx context.Context, label string, login func() (http.Header, string)) (http.Header, string) {
	if ctx == nil {
		return nil, E2HTTPAuthNotSent
	}
	gate := s.gate(label)
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return nil, E2HTTPAuthNotSent
	}
	defer func() { <-gate }()
	header, result := login()
	if result == E2HTTPAuthAuthenticated && len(header) > 0 {
		s.replace(label, header)
	}
	return header, result
}

// 성공 횟수는 제한하지 않는다. 같은 실행·계정의 연속 실패와 로그인 폭주만 제한하며,
// 처음 인증 준비는 세지 않는다. 간격은 이전 재로그인 종료부터 다음 시작까지다.
const (
	maxSessionRefreshFailuresPerAccount = 3
	minSessionRefreshInterval           = 10 * time.Second
)

// E2HTTPRefreshLimit는 계정별 연속 실패 상한이다. E2HTTPRefreshCooldown은 짧은 간격/실행 중 거부다.
// 둘 다 새 로그인 요청을 보내지 않고 해당 명령만 실패시킨다.
const E2HTTPRefreshLimit = "refresh_limit"
const E2HTTPRefreshCooldown = "refresh_cooldown"

// e2HTTPRefreshCounter는 실행 전체의 계정별 연속 실패·재로그인 간격을 보호한다.
// now는 신뢰 호스트 내부 시계다(없으면 time.Now). 외부 정책/모델 입력이 아니다.
type e2HTTPRefreshCounter struct {
	mu       sync.Mutex
	accounts map[string]*e2HTTPRefreshState
	now      func() time.Time
}

type e2HTTPRefreshState struct {
	failures int
	running  bool
	next     time.Time
}

func (c *e2HTTPRefreshCounter) timeLocked() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *e2HTTPRefreshCounter) begin(label string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accounts == nil {
		c.accounts = map[string]*e2HTTPRefreshState{}
	}
	s := c.accounts[label]
	if s == nil {
		s = &e2HTTPRefreshState{}
		c.accounts[label] = s
	}
	if s.failures >= maxSessionRefreshFailuresPerAccount {
		return E2HTTPRefreshLimit
	}
	if s.running || c.timeLocked().Before(s.next) {
		return E2HTTPRefreshCooldown
	}
	s.running = true
	return ""
}

func (c *e2HTTPRefreshCounter) finish(label string, success bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.accounts[label]
	if success {
		s.failures = 0
	} else {
		s.failures++
	}
	s.running = false
	s.next = c.timeLocked().Add(minSessionRefreshInterval)
}

// e2HTTPRefreshSession은 계정 하나를 인증 준비와 같은 경로(E② 인증 준비 규칙, 보호 값 등록, 세션 가림)로 다시
// 로그인시킨다. 정책에 없는 라벨은 보내지 않는다. 상한에 닿으면 보내지 않는다.
// e3Share가 참이면 확정 단계 직전 다시 로그인이라 떼어 둔 확정 몫을 쓰는 호출로 연다(e2proxy.BeginE3ReloginCall). Worker의
// refresh_session은 거짓이라 확정 몫을 쓰지 못한다.
func e2HTTPRefreshSession(ctx context.Context, execution *e2proxy.Execution, dir, origin string, auth E2HTTPFileAuth, counter *e2HTTPRefreshCounter, program *e2HTTPProgramCalls, label string, e3Share bool) (header http.Header, result string) {
	var account *E2HTTPFileAuthAccount
	for i := range auth.Accounts {
		if auth.Accounts[i].Label == label {
			account = &auth.Accounts[i]
		}
	}
	if execution == nil || ctx == nil || ctx.Err() != nil || account == nil || counter == nil {
		return nil, E2HTTPAuthNotSent
	}
	if status := counter.begin(label); status != "" {
		logAccountAuthNotSent(ctx, label)
		return nil, status
	}
	defer func() { counter.finish(label, result == E2HTTPAuthAuthenticated && len(header) > 0) }()
	begin := func() (*e2proxy.Call, error) {
		return execution.BeginProgramCall(ctx, e2proxy.ProgramRefreshSession, "auth-refresh")
	}
	if e3Share {
		begin = func() (*e2proxy.Call, error) { return execution.BeginE3ReloginCall(ctx, "e3-relogin") }
	}
	call, err := begin()
	if err != nil {
		logAccountAuthNotSent(ctx, label)
		return nil, E2HTTPAuthNotSent
	}
	// Worker Bash 결과가 없는 서버 호출이라 마감 대조에서 따로 센다(#555).
	program.add(call.Ref())
	defer func() {
		if _, closeErr := call.Close(context.WithoutCancel(ctx)); closeErr != nil {
			log.Print("[e2-http] 다시 로그인 call 마감 실패")
		}
	}()
	sessions := E2HTTPSessions{}
	result = authPrepOne(ctx, call, execution, dir, origin, auth.Login, *account, sessions)
	return sessions[label], result
}

// e2HTTPAccountUsernames는 결속된 시험 계정명을 표시용 틀 가림에만 사용한다.
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

// e2HTTPAccountCredentials는 결속된 등록 계정의 자격을 읽어 모델 지침에 넣을 값으로 돌려준다(사용자 결정 2026-11-26:
// 모델이 직접 로그인한다, CLAUDE.md 절대규칙 2). 읽은 비밀번호는 이 실행에 등록한다. model_visible_v1(제품)은 저장 기록에서도
// 가리지 않으므로(2026-11-26 DLP 폐기) E②가 받기만 하고, 짧은 비밀번호 계정도 결속된다. 이전 정책 실행은 보호 값으로
// 등록하지 못하는 값이면 그 계정을 모델에 주지 않는다. 읽지 못하거나 등록하지 못한 계정은 두 번째 값에 not_sent로 남긴다.
func e2HTTPAccountCredentials(ctx context.Context, execution *e2proxy.Execution, dir string, auth E2HTTPFileAuth) ([]agent.E2HTTPAccountCredential, map[string]string) {
	out := []agent.E2HTTPAccountCredential{}
	unread := map[string]string{}
	if execution == nil || ctx == nil {
		return out, unread
	}
	for _, account := range auth.Accounts {
		raw, err := e2HTTPPinnedBytes(dir, account.Credential, maxAuthCredentialBytes)
		if err != nil {
			unread[account.Label] = E2HTTPAuthNotSent
			logAccountAuthNotSent(ctx, account.Label)
			continue
		}
		var cred struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		decodeErr := json.Unmarshal(raw, &cred)
		clear(raw)
		if decodeErr != nil || cred.Username == "" || cred.Password == "" {
			unread[account.Label] = E2HTTPAuthNotSent
			logAccountAuthNotSent(ctx, account.Label)
			continue
		}
		// 이전 정책 실행에서 보호 값으로 등록하지 못하는 비밀번호는 모델에 주지 않는다(그 계정은 not_sent). model_visible_v1은
		// 등록이 늘 성공한다(가리지 않으므로 받기만 함).
		if execution.AddProtectedPasswords([]byte(cred.Password)) != nil {
			unread[account.Label] = E2HTTPAuthNotSent
			logAccountAuthNotSent(ctx, account.Label)
			continue
		}
		out = append(out, agent.E2HTTPAccountCredential{Label: account.Label, Username: cred.Username, Password: cred.Password})
	}
	return out, unread
}

// e2HTTPAuthPrep는 계정마다 로그인 요청을 E② 인증 준비 규칙으로 한 번씩 보낸다. 재시도는 없다. 세션 값은 출력 가림
// 전용 값으로 등록하고(응답 보류에는 쓰지 않음) 결과에만 담는다. 자격 값은 쓴 뒤 지운다.
func e2HTTPAuthPrep(ctx context.Context, execution *e2proxy.Execution, dir, origin string, auth E2HTTPFileAuth) (E2HTTPAuthResult, error) {
	out := E2HTTPAuthResult{AccountAuth: map[string]string{}, Sessions: E2HTTPSessions{}}
	if execution == nil || ctx == nil {
		return out, ErrE2HTTPHost
	}
	call, err := execution.BeginProgramCall(ctx, e2proxy.ProgramAuthPrep, "auth-prep")
	if err != nil {
		for _, account := range auth.Accounts {
			out.AccountAuth[account.Label] = E2HTTPAuthNotSent
			logAccountAuthNotSent(ctx, account.Label)
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

func logAccountAuthNotSent(ctx context.Context, label string) {
	_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "server", Event: "account_auth_checked", Stage: "execution",
		Status: "not_sent", ReasonCode: "auth_not_sent", AccountLabel: label})
}

func authPrepOne(ctx context.Context, call *e2proxy.Call, execution *e2proxy.Execution, dir, origin string, login diagnosisprep.TargetLogin, account E2HTTPFileAuthAccount, sessions E2HTTPSessions) (result string) {
	reason := ""
	defer func() {
		status := "failed"
		if result == E2HTTPAuthAuthenticated {
			status = "succeeded"
		} else if result == E2HTTPAuthResultUnknown {
			status = "unknown"
		} else if result == E2HTTPAuthNotSent {
			status = "not_sent"
		}
		_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "server", Event: "account_auth_checked", Stage: "execution",
			Status: status, ReasonCode: reason, AccountLabel: account.Label, CallRef: call.Ref()})
	}()
	raw, err := e2HTTPPinnedBytes(dir, account.Credential, maxAuthCredentialBytes)
	if err != nil {
		reason = "auth_credential_unavailable"
		return E2HTTPAuthNotSent
	}
	var cred struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	decodeErr := json.Unmarshal(raw, &cred)
	clear(raw)
	if decodeErr != nil || cred.Username == "" || cred.Password == "" {
		reason = "auth_credential_unavailable"
		return E2HTTPAuthNotSent
	}
	// 비밀번호는 보내기 전에 이 실행의 보호 값으로 등록한다. 대상이 나중에 다른 응답에 비밀번호를 그대로 되돌려 주면 그
	// 응답은 보류되고 출력에서도 가려진다(S09: 비밀번호는 모델 입력에 없다). 보호할 수 없는 비밀번호(너무 짧음 등)면
	// 로그인하지 않는다.
	if execution.AddProtectedPasswords([]byte(cred.Password)) != nil {
		cred.Username, cred.Password = "", ""
		reason = "auth_password_unprotectable"
		return E2HTTPAuthNotSent
	}
	body, contentType := authPrepBody(login, cred.Username, cred.Password)
	cred.Username, cred.Password = "", ""
	defer clear(body)
	req, err := http.NewRequestWithContext(ctx, login.Method, origin+login.Path, bytes.NewReader(body))
	if err != nil {
		reason = "auth_request_invalid"
		return E2HTTPAuthNotSent
	}
	req.Header.Set("Content-Type", contentType)
	session, receipt, err := call.AuthPrep(ctx, req, authExtractor(login))
	switch {
	case err == nil:
	case errors.Is(err, e2proxy.ErrAuthSession):
		if receipt.StatusCode >= http.StatusBadRequest {
			reason = "auth_login_rejected"
			return E2HTTPAuthLoginFailed
		}
		reason = "auth_session_unavailable"
		return E2HTTPAuthSessionNotFound
	case receipt.Reason == e2proxy.Unknown:
		// 대상이 받았을 수 있다. 다시 보내지 않는다.
		reason = "auth_request_unknown"
		return E2HTTPAuthResultUnknown
	default:
		reason = "auth_not_sent"
		return E2HTTPAuthNotSent
	}
	values := authSessionValues(login, session)
	if len(values) == 0 {
		reason = "auth_session_value_missing"
		return E2HTTPAuthSessionNotFound
	}
	// 시험 계정 실행은 세션을 모델에 주고 기록에서도 가리지 않아 값을 받기만 한다(절대규칙 2, 2026-11-26 DLP 폐기). 시험 계정
	// 실행이 아닌 실행에서 출력 가림 목록에 넣지 못한 세션(길이·개수 상한)은 모델용 세션으로 내보내지 않는다.
	if err := execution.AddRedactValues(values...); err != nil {
		reason = "auth_session_redact_failed"
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

// authSessionValues는 인증 준비가 추출한 세션 값이다(쿠키 각각 또는 접두 없는 토큰). 시험 계정 실행이 아닌 실행에서만
// 출력 전용 가림에 들어간다(AddRedactValues). 응답 보류용 보호 값에는 넣지 않는다.
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

func e2HTTPPasswordResponse(auth *E2HTTPFileAuth) string {
	if auth == nil {
		return ""
	}
	return auth.PasswordResponse
}
