package e2proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/internal/pathscope"
	"github.com/Autumn-27/artex/loginattempt"
	"github.com/Autumn-27/artex/transmit/budget"
)

var safeRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$`)
var safeAttempt = regexp.MustCompile(`^[0-9a-f]{32}$`)

type compiledRule struct {
	Rule
	origin    *url.URL
	authority string
	methods   map[string]bool
}
type Execution struct {
	// loginAttempts는 모델 직접 로그인 회차의 로그인 시도 판정기다(AttachLoginAttempts). 없으면 판정하지 않는다.
	loginAttempts atomic.Pointer[loginattempt.Tracker]
	// passwords는 호스트가 등록한 비밀번호다. 일반 제어 비밀과 응답 처리 의미를 분리한다.
	passwords  [][]byte
	issued     map[string]bool
	intents    map[int64]struct{}
	intentGate chan struct{}
	mu         sync.Mutex
	recordMu   sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	cfg        Config
	rules      []compiledRule
	calls      map[string]*Call
	e3Admitted uint64
	// modelClosed는 모델 단계가 마감됐는지다(CloseModelStage). 그 뒤로는 확정 단계 요청만 받는다.
	modelClosed    bool
	e3Created      map[e3contract.CallRef]string
	callOrder      []*Call
	events         []Event
	counts         Counts
	next           uint64
	eventIndex     uint64
	stopped        bool
	auditFailed    bool
	stopReason     Reason
	controlPending bool
	stopDone       chan struct{}
	stopErr        error
	// slots는 동시 송신 자리다. 용량이 MaxConcurrent이고, 자리가 없으면 거부하지 않고 기다린다(C20).
	// 기다리는 쪽은 제안 순서대로 자리를 얻는다(채널 송신 대기열). 대기 수는 프록시 연결 상한×call 수로 묶인다.
	slots chan struct{}
	// admitted는 예산을 예약하고 송신 단계로 넘어간 시도 수다. MaxAttempts는 이 값만 센다.
	// 보내지 않은 거부·미지원은 송신 상한을 쓰지 않고 call 단위 차단으로만 제한한다(C01).
	admitted      uint64
	intentCounts  map[int64]*Counts
	programCounts Counts
	secrets       [][]byte
	// secretsGen은 일반 보호 값·비밀번호 목록이 실행 뒤에 바뀐 횟수다. 응답을 읽는 동안 바뀌면 그 응답의 검사 길이가
	// 새 값을 담지 못할 수 있어 통째로 보류한다.
	secretsGen uint64
	// redactOnly는 출력에서만 가리는 값이다(인증 준비가 받은 시험 계정 세션, C14 구현 설계 4). 응답 표현 보류(반사 검사)에는
	// 쓰지 않는다.
	redactOnly [][]byte
	// marker는 확정 단계의 보호 표식이다. 일반 보호 값의 응답 보류 정책과 구분한다.
	marker        []byte
	secretHeaders map[string]struct{}
	// accountPassthroughOff는 시험 계정 모드를 이 실행에서 끈 표시다(DisableTestAccountPassthrough). 한 번 켜면 되돌리지 않는다.
	accountPassthroughOff atomic.Bool
}
type Call struct {
	diagnostics          []HTTPObservation
	diagnosticsTruncated bool
	ingressToken         [32]byte
	e                    *Execution
	ctx                  context.Context
	cancel               context.CancelFunc
	// cancelCause는 call 문맥을 원인과 함께 끊는다(모델 단계 마감 ErrModelStageClosed).
	cancelCause context.CancelCauseFunc
	ref         string
	commandRef  string
	intentID    int64
	program     ProgramCallKind
	// e3Share는 확정 단계가 연 다시 로그인 호출이다(BeginE3ReloginCall). E③ 프로그램 요청과 함께 떼어 둔 확정 몫을 쓴다.
	e3Share bool
	// selectedLogin은 선택 후보 검증 실행이 연 학습 로그인 호출이다(BeginSelectedLoginCall). 승인 계획에 결속한 전용
	// 규칙(auth_prep POST·login_prep GET)만 쓰고 Worker 규칙·확정 몫은 쓰지 않는다.
	selectedLogin bool
	expires       time.Time
	closing       bool
	finalized     bool
	active        int
	idle          chan struct{}
	counts        Counts
	auditComplete bool
	reason        Reason
	// fence는 이 call의 요청 단위 실패가 상한에 닿았을 때의 사유다. 비어 있으면 열려 있다.
	// 실행 전체가 아니라 이 call의 새 요청만 막는다. 이미 송신 중인 요청은 건드리지 않는다(C01).
	fence Reason
	// closedRefusals는 대기 중 취소·기한 지남·승인 대기 취소처럼 보내기 전에 거둬진 요청 수다(사유 closed). 모델의
	// 범위 밖 요청이 아니므로 거부 상한(MaxDenials) 계수에서 뺀다(#487 리뷰 P3). 감사 계수(Denied)는 그대로다.
	closedRefusals uint64
	closeGate      chan struct{}
	// approvals는 이 call에서 운영자 승인을 기다리는 요청 수다(러너의 명령 시간 예산을 그동안 멈춘다).
	approvals approvalWait
}

func validRef(v string) bool { return safeRef.MatchString(v) && !strings.Contains(v, "://") }
func randomRef(prefix string) (string, error) {
	var b [randomRefBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", ErrConfig
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

func NewExecution(parent context.Context, cfg Config) (*Execution, error) {
	if cfg.AuditVersion == "" {
		cfg.AuditVersion = Version
	}
	if cfg.AuditVersion != Version && cfg.AuditVersion != JournalVersionV3 {
		return nil, ErrConfig
	}
	if (cfg.SelectedSessionHeaders != nil || cfg.SelectedAccountRef != "") && (cfg.SelectedSessionHeaders == nil || cfg.SelectedAccountRef == "" ||
		cfg.HeaderMode != HeaderTestAccountPassthrough || cfg.ApprovalMode != ApprovalPreauthorizedScope || cfg.ResolveSentAccount == nil) {
		return nil, ErrConfig
	}
	// 선택 실행의 학습 로그인 호출은 목적별 규칙을 찾는다. 목적 규칙이 둘 이상이면 승인한 정확한 경로 외에도
	// 맞을 수 있으므로 실행 자체를 거부한다.
	authPrepRules, loginPrepRules := 0, 0
	for _, rule := range cfg.Rules {
		switch rule.Purpose {
		case PurposeAuthPrep:
			authPrepRules++
		case PurposeLoginPrep:
			loginPrepRules++
		}
	}
	if cfg.SelectedAccountRef != "" && (authPrepRules > 1 || loginPrepRules > 1) ||
		cfg.SelectedAccountRef == "" && loginPrepRules != 0 {
		return nil, ErrConfig
	}
	if !validApprovalConfig(cfg) || parent == nil || cfg.Recorder == nil || cfg.IntentID <= 0 || !validRef(cfg.OwnerRef) || !validRef(cfg.PolicyRevision) ||
		!validRef(cfg.AccountRef) || !validRef(cfg.SessionRef) || cfg.ExpiresAt.IsZero() || !time.Now().Before(cfg.ExpiresAt) {
		return nil, ErrConfig
	}
	for _, ref := range []string{cfg.Scope.TaskRef, cfg.Scope.RunRef, cfg.Scope.ResetRef, cfg.Scope.PolicyRef, cfg.Scope.CaseRef} {
		if !validRef(ref) {
			return nil, ErrConfig
		}
	}
	if cfg.Budget.Ledger == nil || cfg.Budget.Key.TaskRef != cfg.Scope.TaskRef || cfg.Budget.Binding.PolicyRef != cfg.Scope.PolicyRef ||
		cfg.Budget.Binding.ResetRef != cfg.Scope.ResetRef {
		return nil, ErrConfig
	}
	if _, err := cfg.Budget.Ledger.Remaining(cfg.Budget.Key, cfg.Budget.Binding, time.Now()); err != nil {
		return nil, ErrConfig
	}
	rules, mode, evaluation, err := compilePolicyTemplate(cfg)
	if err != nil {
		return nil, err
	}
	cfg.HeaderMode = mode
	cfg.Evaluation = evaluation
	cfg.Rules = nil
	cfg.ExcludedPaths = append([]string(nil), cfg.ExcludedPaths...)
	if cfg.RootCAs != nil {
		cfg.RootCAs = cfg.RootCAs.Clone()
	}
	ctx, cancel := context.WithCancel(parent)
	return &Execution{ctx: ctx, cancel: cancel, cfg: cfg, rules: rules, calls: make(map[string]*Call), secrets: credentialValues(rules), secretHeaders: credentialHeaderNames(rules), issued: make(map[string]bool), intents: map[int64]struct{}{cfg.IntentID: struct{}{}}, intentCounts: map[int64]*Counts{cfg.IntentID: {}}, intentGate: make(chan struct{}, 1), slots: make(chan struct{}, cfg.Limits.MaxConcurrent)}, nil
}
func compileRules(rules []Rule) ([]compiledRule, error) {
	if len(rules) == 0 || len(rules) > maxExecutionRules {
		return nil, ErrConfig
	}
	out := make([]compiledRule, 0, len(rules))
	seen := map[string]bool{}
	for _, r := range rules {
		if r.TLSSPKISHA256 != "" {
			b, e := hex.DecodeString(r.TLSSPKISHA256)
			if e != nil || len(b) != 32 || hex.EncodeToString(b) != r.TLSSPKISHA256 {
				return nil, ErrConfig
			}
		}
		if !validRef(r.Ref) || !validRef(r.PathScopeRef) || seen[r.Ref] || !r.ConnectIP.IsValid() || r.ConnectIP.IsUnspecified() || r.ConnectIP.IsMulticast() || r.ConnectIP.Zone() != "" {
			return nil, ErrConfig
		}
		seen[r.Ref] = true
		u, err := url.Parse(r.Origin)
		if err == nil && r.TLSSPKISHA256 != "" && u.Scheme != "https" {
			return nil, ErrConfig
		}
		if err != nil {
			return nil, ErrConfig
		}
		a, err := canonicalAuthority(u)
		if err != nil || u.RawQuery != "" || u.ForceQuery || (u.Path != "" && u.Path != "/") {
			return nil, ErrConfig
		}
		if !validPath(r.PathPrefix) || r.PathMatch != "" && r.PathMatch != "exact" || r.Purpose != "" && r.PathMatch != "" ||
			r.StrictHeaders && (r.PathMatch != "exact" || r.Purpose != "" || len(r.Methods) != 1 || r.Methods[0] != "GET" && r.Methods[0] != "HEAD") ||
			r.ExactQuery != "" && (r.PathMatch != "exact" || len(r.ExactQuery) > 512 || !validExactQuery(r.ExactQuery)) {
			return nil, ErrConfig
		}
		m := map[string]bool{}
		for _, method := range r.Methods {
			if !supportedMethod(method) || m[method] {
				return nil, ErrConfig
			}
			m[method] = true
		}
		if len(m) == 0 {
			return nil, ErrConfig
		}
		if r.Purpose != "" {
			// 프로그램 규칙에는 임의 헤더나 자격 원문을 저장하지 않는다.
			if len(r.CredentialHeaders) != 0 || len(r.Headers) != 0 || len(m) != 1 {
				return nil, ErrConfig
			}
			switch r.Purpose {
			case PurposeAuthPrep:
				if !m["POST"] || r.PathPrefix == "/" || strings.HasSuffix(r.PathPrefix, "/") {
					return nil, ErrConfig
				}
			case PurposeLoginPrep:
				// 선택 후보 검증 학습 로그인의 준비 GET(로그인 화면·별도 CSRF 발급 경로) 하나다. 정확한 경로에서 GET만
				// 받고, 선택 실행의 전용 로그인 호출(BeginSelectedLoginCall)만 쓴다(docs/contracts/selected-candidate-learned-login.md).
				if !m["GET"] {
					return nil, ErrConfig
				}
			case PurposeE3Identity, PurposeE3Create, PurposeE3Read:
				if !validE3RulePath(r.PathPrefix, r.Purpose == PurposeE3Read) ||
					(r.Purpose == PurposeE3Identity || r.Purpose == PurposeE3Read) && !m["GET"] ||
					r.Purpose == PurposeE3Create && !m["POST"] && !m["PUT"] {
					return nil, ErrConfig
				}
			default:
				return nil, ErrConfig
			}
		}
		r.Methods = append([]string(nil), r.Methods...)
		r, err = validateRuleHeaders(r)
		if err != nil {
			return nil, err
		}
		r.ConnectIP = r.ConnectIP.Unmap()
		u.Host = a
		out = append(out, compiledRule{Rule: r, origin: u, authority: a, methods: m})
	}
	return out, nil
}
func supportedMethod(m string) bool {
	switch m {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return true
	}
	return false
}
func validPath(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.ContainsAny(p, "\\%\r\n") && !strings.Contains(p, "//") && path.Clean(p) == strings.TrimSuffix(p, "/") || p == "/"
}

// E③ 선언 경로와 같은 모양만 받는다. 조회 틀의 ID는 독립된 조각 하나다.
func validE3RulePath(p string, read bool) bool {
	if !validPath(p) || strings.HasSuffix(p, "/") || p == "/{id}" || len(p) > 512 || !utf8.ValidString(p) || strings.ContainsAny(p, "?# \t\r\n\\%") {
		return false
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return false
		}
	}
	ids := 0
	for _, segment := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		if segment == "{id}" {
			ids++
		} else if strings.ContainsAny(segment, "{}") {
			return false
		}
	}
	return read && ids == 1 || !read && ids == 0
}

// e3InWorkerScope는 같은 승인 규칙 하나가 목적 규칙의 연결·메서드·경로 전체를 덮는지 확인한다.
// 조회 틀은 ID 앞의 고정 접두 전체가 승인되어야 한다. 정확 일치는 가변 ID를 덮지 못한다.
func e3InWorkerScope(program compiledRule, rules []compiledRule) bool {
	prefix := program.PathPrefix
	read := program.Purpose == PurposeE3Read
	if read {
		prefix, _, _ = strings.Cut(prefix, "/{id}")
		if prefix == "" {
			prefix = "/"
		}
	}
	for _, worker := range rules {
		if worker.Purpose != "" || worker.origin.Scheme != program.origin.Scheme || worker.authority != program.authority ||
			worker.ConnectIP != program.ConnectIP || worker.TLSSPKISHA256 != program.TLSSPKISHA256 || !worker.methods[program.Methods[0]] {
			continue
		}
		scope := pathscope.Scope{Path: worker.PathPrefix, Mode: pathscope.Mode(worker.PathMatch)}
		if (!read || scope.Mode == pathscope.Prefix) && scope.Allows(prefix, false) {
			return true
		}
	}
	return false
}

func canonicalAuthority(u *url.URL) (string, error) {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return "", ErrConfig
	}
	h := strings.ToLower(u.Hostname())
	if h == "" || strings.ContainsAny(h, "%\\\r\n") {
		return "", ErrConfig
	}
	for _, c := range h {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == ':') {
			return "", ErrConfig
		}
	}
	p := u.Port()
	if p == "" {
		p = "80"
		if u.Scheme == "https" {
			p = "443"
		}
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return "", ErrConfig
	}
	return net.JoinHostPort(h, strconv.Itoa(n)), nil
}

// RegisterIntent accepts only a trusted host's task-store binding. It issues
// neither a grant nor a new budget. The durable control event describes the
// binding request; only the post-ACK current-policy check publishes membership.
// A late ACK racing revocation is never authority to execute this intent.
func (e *Execution) RegisterIntent(ctx context.Context, id int64) error {
	if e == nil || e.intentGate == nil || ctx == nil || id <= 0 {
		return ErrConfig
	}
	select {
	case e.intentGate <- struct{}{}:
		defer func() { <-e.intentGate }()
	case <-ctx.Done():
		return ErrClosed
	}
	e.mu.Lock()
	if ctx.Err() != nil || e.blockedLocked() != Complete {
		e.mu.Unlock()
		return ErrClosed
	}
	if _, known := e.intents[id]; known {
		e.mu.Unlock()
		return nil
	}
	// Metadata retention is bounded by the execution's existing finite work
	// ceilings. This does not spend or recharge any HTTP/call reservation.
	if uint64(len(e.intents)) >= e.cfg.Limits.MaxAttempts+uint64(e.cfg.Limits.MaxCalls)+1 {
		e.mu.Unlock()
		return ErrClosed
	}
	revision := e.cfg.PolicyRevision
	receipt := Receipt{Version: Version, Scope: e.cfg.Scope, IntentID: id, PolicyRevision: revision, Reason: Complete, NotSent: true}
	e.mu.Unlock()
	if err := e.recordBounded(ctx, Event{Stage: "control", Receipt: receipt}); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if ctx.Err() != nil || e.blockedLocked() != Complete || e.cfg.PolicyRevision != revision {
		return ErrClosed
	}
	e.intents[id] = struct{}{}
	e.intentCounts[id] = &Counts{}
	return nil
}

// Done은 실행 취소·철회·감사 실패를 기다리는 호스트 전용 수명 신호다.
func (e *Execution) Done() <-chan struct{} { return e.ctx.Done() }

// MaxConcurrent는 승인된 실제 HTTP 송신 상한이며 Bash 슬롯 확대 권위가 아니다.
func (e *Execution) MaxConcurrent() int { return e.cfg.Limits.MaxConcurrent }

func (e *Execution) HasIntent(id int64) bool {
	if e == nil || id <= 0 {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, known := e.intents[id]
	return known
}

func (e *Execution) BeginCall(parent context.Context, commandRef string) (*Call, error) {
	if e == nil {
		return nil, ErrConfig
	}
	e.mu.Lock()
	anchor := e.cfg.IntentID
	e.mu.Unlock()
	return e.BeginCallForIntent(parent, anchor, commandRef)
}

func (e *Execution) BeginCallForIntent(parent context.Context, id int64, commandRef string) (*Call, error) {
	if e == nil || parent == nil || id <= 0 || !validRef(commandRef) {
		return nil, ErrConfig
	}
	return e.beginCall(parent, id, "", commandRef, false, false)
}

// BeginProgramCall은 실제 intent를 만들거나 빌리지 않는다. 실행 전체의 기존
// 호출·시도·요청 예산과 감사는 공유하고 프로그램 누계만 따로 기록한다.
func (e *Execution) BeginProgramCall(parent context.Context, program ProgramCallKind, commandRef string) (*Call, error) {
	if e == nil || parent == nil || !validRef(commandRef) {
		return nil, ErrConfig
	}
	// v2 기록에는 E③의 실제 계정·동결 선언·구조화 관찰을 봉인할 수 없다. 호출 계수나 감사 행도 만들지 않는다.
	if program.isE3() {
		return nil, ErrE3JournalUnavailable
	}
	if !program.Valid() {
		return nil, ErrConfig
	}
	return e.beginCall(parent, 0, program, commandRef, false, false)
}

// BeginE3ReloginCall은 확정 단계 직전 다시 로그인(사용자 결정 2026-11-20 (가)) 호출을 연다. 경로·감사 행은 Worker의
// refresh_session과 같고(ProgramRefreshSession), 다른 점은 떼어 둔 확정 몫을 쓴다는 것뿐이다. 신뢰된 호스트의 확정 단계만
// 부른다. 모델 단계 요청은 확정 몫을 쓰지 못하므로(늦게 도착한 Worker 요청 포함) 예산 통제를 우회할 수 없다.
func (e *Execution) BeginE3ReloginCall(parent context.Context, commandRef string) (*Call, error) {
	if e == nil || parent == nil || !validRef(commandRef) {
		return nil, ErrConfig
	}
	return e.beginCall(parent, 0, ProgramRefreshSession, commandRef, true, false)
}

// BeginSelectedLoginCall은 선택 후보 검증 실행(SelectedAccountRef가 있는 실행)의 학습 로그인 호출을 연다(계약
// docs/contracts/selected-candidate-learned-login.md). 귀속은 인증 준비(ProgramAuthPrep)와 같아 감사·영수증 어휘는 그대로다.
// 이 호출은 학습 로그인 목적(learned_login POST·login_prep GET)만 쓰고, 둘 다 운영자가 승인한 계획에 결속한 전용 규칙
// (auth_prep POST·login_prep GET, 정확한 경로)으로만 나간다. 실행의 일반 예산을 쓰고 확정 몫(E③)은 쓰지 않는다. 선택
// 실행이 아니면 열지 않는다(일반 진단·E③ 회차에서 이 경계로 보낼 수 없음).
func (e *Execution) BeginSelectedLoginCall(parent context.Context, commandRef string) (*Call, error) {
	if e == nil || parent == nil || !validRef(commandRef) || e.cfg.SelectedAccountRef == "" {
		return nil, ErrConfig
	}
	return e.beginCall(parent, 0, ProgramAuthPrep, commandRef, false, true)
}

// confirmationCall은 확정 단계 호출(E③ 프로그램 요청·확정 단계가 연 다시 로그인)인지다. 확정 몫을 쓰고, 모델 단계가
// 마감된 뒤에도 열린다.
func (c *Call) confirmationCall() bool { return c.program.isE3() || c.e3Share }

// CloseModelStage는 모델 단계를 마감한다(감독자가 모델 단계가 끝났다고 본 때, 강제 중지·취소 때). 그 뒤로 E②는 확정 단계
// 요청만 받는다: 확정 단계 밖 호출(Worker·인증 준비·Worker refresh_session)은 새로 열 수 없고(ErrClosed), 이미 열린 그런
// 호출은 문맥을 끊어 진행 중 요청도 멈추며, 그 뒤 요청은 closed로 거부한다. 실행 자체(감사·확정 단계 송신)는 닫지 않는다.
// 실행 전체를 닫는 것은 감독자의 FenceTask다. 여러 번 불러도 같다.
func (e *Execution) CloseModelStage() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.modelClosed = true
	var cancels []context.CancelCauseFunc
	for _, c := range e.calls {
		if !c.confirmationCall() && c.cancelCause != nil {
			cancels = append(cancels, c.cancelCause)
		}
	}
	e.mu.Unlock()
	for _, cancel := range cancels {
		cancel(ErrModelStageClosed)
	}
}

// ModelStageClosed는 모델 단계가 마감됐는지다.
func (e *Execution) ModelStageClosed() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.modelClosed
}

func (e *Execution) beginCall(parent context.Context, id int64, program ProgramCallKind, commandRef string, e3Share, selectedLogin bool) (*Call, error) {
	if parent.Err() != nil {
		return nil, ErrClosed
	}
	ref, err := randomRef("call_")
	if err != nil {
		return nil, err
	}
	c := &Call{e: e, ref: ref, commandRef: commandRef, intentID: id, program: program, e3Share: e3Share, selectedLogin: selectedLogin, auditComplete: true, reason: Complete, idle: make(chan struct{}), closeGate: make(chan struct{}, 1)}
	accepted := false
	defer func() {
		if !accepted {
			clear(c.ingressToken[:])
		}
	}()
	close(c.idle)
	if _, err := rand.Read(c.ingressToken[:]); err != nil {
		return nil, ErrConfig
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, registered := e.intents[id]
	validAttribution := ValidCallAttribution(id, program) || id == 0 && e.cfg.AuditVersion == JournalVersionV3 && program.isE3()
	if parent.Err() != nil || !validAttribution || program == "" && !registered || e.blockedLocked() != Complete || len(e.calls) >= e.cfg.Limits.MaxCalls {
		return nil, ErrClosed
	}
	// 모델 단계가 마감되면 확정 단계 호출(E③ 프로그램·확정 단계 다시 로그인)만 새로 연다.
	if e.modelClosed && !c.confirmationCall() {
		return nil, ErrClosed
	}
	expires := time.Now().Add(e.cfg.Limits.CallTimeout)
	if e.cfg.ExpiresAt.Before(expires) {
		expires = e.cfg.ExpiresAt
	}
	c.expires = expires
	// call 문맥은 원인이 실리는 취소 위에 기한을 둔다. 모델 단계 마감은 ErrModelStageClosed를 원인으로 끊어, 승인 대기가
	// 왜 닫혔는지 승인 이력이 가릴 수 있게 한다(context.Cause). 기한이면 원인은 DeadlineExceeded다.
	causeCtx, cancelCause := context.WithCancelCause(e.ctx)
	deadlineCtx, cancelDeadline := context.WithDeadline(causeCtx, expires)
	c.ctx, c.cancelCause = deadlineCtx, cancelCause
	c.cancel = func() { cancelDeadline(); cancelCause(context.Canceled) }
	context.AfterFunc(parent, c.cancel)
	e.calls[ref] = c
	e.callOrder = append(e.callOrder, c)
	e.counts.Calls++
	if counts := e.intentCounts[id]; counts != nil {
		counts.Calls++
	}
	if program.Valid() || program.isE3() {
		e.programCounts.Calls++
	}
	accepted = true
	return c, nil
}
func (c *Call) Context() context.Context { return c.ctx }
func (c *Call) Ref() string              { return c.ref }
func (c *Call) CommandRef() string       { return c.commandRef }
func (c *Call) IntentID() int64          { return c.intentID }
func (e *Execution) blockedLocked() Reason {
	if e.ctx == nil {
		return Invalid
	}
	if e.auditFailed {
		return AuditFailed
	}
	if e.controlPending {
		return PolicyDenied
	}
	if e.stopped {
		return e.stopReason
	}
	if e.ctx.Err() != nil {
		return Closed
	}
	if !time.Now().Before(e.cfg.ExpiresAt) {
		return Expired
	}
	return Complete
}
func (c *Call) blockedLocked() Reason {
	validAttribution := ValidCallAttribution(c.intentID, c.program) || c.intentID == 0 && c.e.cfg.AuditVersion == JournalVersionV3 && c.program.isE3()
	if _, registered := c.e.intents[c.intentID]; !validAttribution || c.program == "" && !registered {
		return Invalid
	}
	if r := c.e.blockedLocked(); r != Complete {
		return r
	}
	if c.closing || c.ctx.Err() != nil {
		return Closed
	}
	if !time.Now().Before(c.expires) {
		return Expired
	}
	return Complete
}
func (e *Execution) record(ctx context.Context, ev Event) error {
	_, err := e.recordEvent(ctx, ev)
	return err
}

func (e *Execution) recordEvent(ctx context.Context, ev Event) (_ Event, resultErr error) {
	defer func() { diagnosticAudit(e.ctx, ev, resultErr) }()
	e.recordMu.Lock()
	defer e.recordMu.Unlock()
	e.mu.Lock()
	if e.auditFailed {
		e.mu.Unlock()
		return Event{}, ErrAudit
	}
	ev.Index = e.eventIndex + 1
	ev.Version = e.cfg.AuditVersion
	ev.At = time.Now().UTC()
	e.mu.Unlock()
	ev.AuditComplete = ev.Reason != AuditFailed
	err := e.cfg.Recorder.Record(ctx, ev)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		e.auditFailed = true
		e.stopReason = AuditFailed
		e.cancel()
		return Event{}, ErrAudit
	}
	// A failed candidate is not a normal event or a committed ordinal. The
	// failure remains in the receipt/execution latch; recovery is not attempted.
	e.eventIndex = ev.Index
	e.events = append(e.events, ev)
	return ev, nil
}

// RecordAdmissionNotSent records a host command rejected before a Call exists.
// It spends no HTTP/call reservation and cannot authorize transmission.
func (e *Execution) RecordAdmissionNotSent(ctx context.Context, intentID int64, commandRef string) (Receipt, error) {
	if e == nil || ctx == nil || intentID <= 0 || !validRef(commandRef) {
		return Receipt{}, ErrConfig
	}
	e.mu.Lock()
	_, registered := e.intents[intentID]
	reason := e.blockedLocked()
	if reason == Complete {
		reason = Closed
	}
	r := Receipt{Version: Version, Scope: e.cfg.Scope, IntentID: intentID, CommandRef: commandRef, PolicyRevision: e.cfg.PolicyRevision, Reason: reason, NotSent: true}
	e.mu.Unlock()
	if !registered {
		return Receipt{}, ErrConfig
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.cfg.Limits.RequestTimeout)
	defer cancel()
	ev, err := e.recordEvent(auditCtx, Event{Stage: "control", Receipt: r})
	return ev.Receipt, err
}

// HasAdmissionNotSent requires the exact acknowledged event, not caller fields.
func (e *Execution) HasAdmissionNotSent(r Receipt) bool {
	if e == nil || !r.NotSent || !r.AuditComplete || r.CallRef != "" || r.CommandRef == "" || r.AttemptID != "" || r.ForwardAttempted || r.Sequence != 0 {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range e.events {
		if ev.Stage == "control" && ev.Receipt == r {
			return true
		}
	}
	return false
}

// recordBounded persists control and completion events independently from
// caller cancellation while retaining a finite write deadline.
func (e *Execution) recordBounded(ctx context.Context, ev Event) error {
	if ctx == nil {
		return ErrConfig
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.cfg.Limits.RequestTimeout)
	defer cancel()
	return e.record(auditCtx, ev)
}

func (e *Execution) Stop(ctx context.Context) error   { return e.stop(ctx, Closed) }
func (e *Execution) Revoke(ctx context.Context) error { return e.stop(ctx, Revoked) }
func (e *Execution) stop(ctx context.Context, reason Reason) error {
	if e.cancel != nil {
		e.cancel()
	}
	if ctx == nil {
		return ErrConfig
	}
	e.mu.Lock()
	if e.auditFailed {
		e.stopped = true
		e.cancel()
		e.mu.Unlock()
		return ErrAudit
	}
	if e.stopped && e.stopDone != nil {
		done := e.stopDone
		e.mu.Unlock()
		select {
		case <-done:
			e.mu.Lock()
			err := e.stopErr
			if e.auditFailed {
				err = ErrAudit
			}
			e.mu.Unlock()
			return err
		case <-ctx.Done():
			return ErrDrain
		}
	}
	e.stopped = true
	e.stopReason = reason
	e.cancel()
	e.stopDone = make(chan struct{})
	done := e.stopDone
	r := Receipt{Version: Version, Scope: e.cfg.Scope, PolicyRevision: e.cfg.PolicyRevision, Reason: reason, NotSent: true}
	e.mu.Unlock()
	err := e.recordBounded(ctx, Event{Stage: "control", Receipt: r})
	e.mu.Lock()
	e.stopErr = err
	close(done)
	e.mu.Unlock()
	return err
}
func (e *Execution) Restrict(ctx context.Context, in Restriction) error {
	if ctx == nil || !validRef(in.PolicyRevision) || in.ExpiresAt.IsZero() || len(in.RuleRefs) == 0 || len(in.Methods) == 0 {
		return ErrConfig
	}
	refs := map[string]bool{}
	methods := map[string]bool{}
	for _, v := range in.RuleRefs {
		if !validRef(v) || refs[v] {
			return ErrConfig
		}
		refs[v] = true
	}
	for _, v := range in.Methods {
		if !supportedMethod(v) || methods[v] {
			return ErrConfig
		}
		methods[v] = true
	}
	// Close admission while the reduction is durably acknowledged. A failed
	// control write leaves the whole execution fenced, never widened.
	e.mu.Lock()
	if e.blockedLocked() != Complete || in.ExpiresAt.After(e.cfg.ExpiresAt) || !time.Now().Before(in.ExpiresAt) || in.PolicyRevision == e.cfg.PolicyRevision {
		e.mu.Unlock()
		return ErrDenied
	}
	old := e.rules
	next := make([]compiledRule, 0, len(old))
	for _, r := range old {
		if !refs[r.Ref] {
			continue
		}
		delete(refs, r.Ref)
		rr := r
		rr.methods = map[string]bool{}
		rr.Methods = nil
		for _, m := range r.Methods {
			if methods[m] {
				rr.methods[m] = true
				rr.Methods = append(rr.Methods, m)
			}
		}
		if len(rr.Methods) > 0 {
			next = append(next, rr)
		}
	}
	if len(refs) != 0 || len(next) == 0 {
		e.mu.Unlock()
		return ErrDenied
	}
	// Unknown method requests are reductions only if they exist somewhere now.
	for m := range methods {
		found := false
		for _, r := range old {
			if r.methods[m] {
				found = true
			}
		}
		if !found {
			e.mu.Unlock()
			return ErrDenied
		}
	}
	e.controlPending = true
	e.rules = next
	e.cfg.ExpiresAt = in.ExpiresAt
	e.cfg.PolicyRevision = in.PolicyRevision
	r := Receipt{Version: Version, Scope: e.cfg.Scope, PolicyRevision: e.cfg.PolicyRevision, Reason: PolicyDenied, NotSent: true}
	e.mu.Unlock()
	err := e.recordBounded(ctx, Event{Stage: "control", Receipt: r})
	e.mu.Lock()
	if err == nil {
		e.controlPending = false
	}
	e.mu.Unlock()
	return err
}
func (c *Call) Block() { c.e.mu.Lock(); c.closing = true; c.cancel(); c.e.mu.Unlock() }
func (c *Call) Close(ctx context.Context) (CallResult, error) {
	if ctx == nil {
		return CallResult{}, ErrConfig
	}
	c.Block()
	select {
	case c.closeGate <- struct{}{}:
		defer func() { <-c.closeGate }()
	case <-ctx.Done():
		return c.Result(), ErrDrain
	}
	c.e.mu.Lock()
	idle := c.idle
	final := c.finalized
	c.e.mu.Unlock()
	if !final {
		select {
		case <-idle:
		case <-ctx.Done():
			return c.Result(), ErrDrain
		}
		c.e.mu.Lock()
		r := Receipt{Version: Version, Scope: c.e.cfg.Scope, IntentID: c.intentID, Program: c.program, CallRef: c.ref, CommandRef: c.commandRef, PolicyRevision: c.e.cfg.PolicyRevision, Reason: c.reason, NotSent: true}
		c.e.mu.Unlock()
		if err := c.e.recordBounded(ctx, Event{Stage: "call_closed", Receipt: r}); err != nil {
			return c.Result(), err
		}
		c.e.mu.Lock()
		c.finalized = true
		clear(c.ingressToken[:])
		c.e.mu.Unlock()
	}
	return c.Result(), nil
}
func (c *Call) Result() CallResult { c.e.mu.Lock(); defer c.e.mu.Unlock(); return c.resultLocked() }
func (c *Call) resultLocked() CallResult {
	state := "open"
	if c.closing {
		state = "closing"
	}
	if c.finalized {
		state = "closed"
	}
	return CallResult{Observations: cloneHTTPObservations(c.diagnostics), ObservationsTruncated: c.diagnosticsTruncated, IntentID: c.intentID, Program: c.program, CallRef: c.ref, CommandRef: c.commandRef, State: state, Counts: c.counts, AuditComplete: c.auditComplete && !c.e.auditFailed, Final: c.finalized, Reason: c.reason}
}

// Snapshot uses the original approval anchor. Counts remain execution-wide;
// per-call counters and receipts separately carry their immutable actual ID.
func (e *Execution) Snapshot() ExecutionView {
	if e == nil {
		return ExecutionView{Version: Version, State: "blocked", Reason: Invalid}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshotLocked(e.cfg.IntentID)
}
func (e *Execution) SnapshotForIntent(id int64) ExecutionView {
	if e == nil {
		return ExecutionView{Version: Version, IntentID: id, State: "blocked", Reason: Invalid}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshotLocked(id)
}
func (e *Execution) snapshotLocked(id int64) ExecutionView {
	reason := e.blockedLocked()
	_, known := e.intents[id]
	if id <= 0 || !known {
		reason = Invalid
	}
	state := "active"
	if reason != Complete {
		state = "blocked"
	}
	ids := make([]int64, 0, len(e.intents))
	for intent := range e.intents {
		ids = append(ids, intent)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var intentCounts Counts
	if counts := e.intentCounts[id]; known && counts != nil {
		intentCounts = *counts
	}
	return ExecutionView{ApprovalMode: e.cfg.ApprovalMode, Evaluation: cloneEvaluationCallerContext(e.cfg.Evaluation), HeaderMode: e.cfg.HeaderMode, Version: Version, Scope: e.cfg.Scope, IntentID: id, IntentIDs: ids, OwnerRef: e.cfg.OwnerRef, PolicyRevision: e.cfg.PolicyRevision,
		State: state, ExpiresAt: e.cfg.ExpiresAt, Counts: e.counts, IntentCounts: intentCounts, ProgramCounts: e.programCounts, Reason: reason, AuditComplete: !e.auditFailed && e.cfg.Recorder != nil}
}
func (e *Execution) Policy() PolicyView {
	e.mu.Lock()
	defer e.mu.Unlock()
	v := PolicyView{ApprovalMode: e.cfg.ApprovalMode, Evaluation: cloneEvaluationCallerContext(e.cfg.Evaluation), HeaderMode: e.cfg.HeaderMode, Version: Version, PolicyRevision: e.cfg.PolicyRevision, ExpiresAt: e.cfg.ExpiresAt}
	for _, r := range e.rules {
		v.Rules = append(v.Rules, RuleView{Ref: r.Ref, Methods: append([]string(nil), r.Methods...), PathScopeRef: r.PathScopeRef})
	}
	v.BudgetRef = e.cfg.Budget.Key.BudgetRef
	if e.cfg.Budget.Ledger != nil {
		remaining, err := e.cfg.Budget.Ledger.Remaining(e.cfg.Budget.Key, e.cfg.Budget.Binding, time.Now())
		v.RemainingRequests = remaining
		v.BudgetAvailable = err == nil
	}
	return v
}

// BudgetRemaining is an observation of the existing shared grant, never send
// permission. Reading zero must not cancel already reserved in-flight work.
// 떼어 둔 확정 몫 가운데 확정 단계 요청이 아직 쓰지 않은 만큼을 뺀 값이다. 모델 단계가 그 몫을 남겨 둔 채 예산
// 소진으로 마감하게 한다.
func (e *Execution) BudgetRemaining() (uint64, error) {
	if e == nil {
		return 0, ErrConfig
	}
	e.mu.Lock()
	shared := e.cfg.Budget
	held := e.e3ShareHeldLocked()
	attemptsLeft := e.attemptsLeftLocked()
	e.mu.Unlock()
	if shared.Ledger == nil {
		return 0, ErrConfig
	}
	remaining, err := shared.Ledger.Remaining(shared.Key, shared.Binding, time.Now())
	if err != nil {
		return 0, err
	}
	if held == 0 {
		// 확정 몫이 없으면(꺼진 회차) 지금처럼 예산만 본다.
		return remaining, nil
	}
	// 확정 몫은 송신 시도 상한에서도 뗀다. 시도 상한이 예산보다 작으면 그쪽이 먼저 닿는다(#826 윤재상 봇 리뷰).
	if attemptsLeft < remaining {
		remaining = attemptsLeft
	}
	if remaining <= held {
		return 0, nil
	}
	return remaining - held, nil
}

// attemptsLeftLocked는 송신 시도 상한(MaxAttempts) 가운데 남은 수다. e.mu를 잡고 부른다.
func (e *Execution) attemptsLeftLocked() uint64 {
	if e.admitted >= e.cfg.Limits.MaxAttempts {
		return 0
	}
	return e.cfg.Limits.MaxAttempts - e.admitted
}

// e3ShareHeldLocked는 확정 몫 가운데 확정 단계 요청이 아직 쓰지 않은 수다. e.mu를 잡고 부른다.
func (e *Execution) e3ShareHeldLocked() uint64 {
	if e.e3Admitted >= e.cfg.E3ConfirmationRequests {
		return 0
	}
	return e.cfg.E3ConfirmationRequests - e.e3Admitted
}

// e3ShareAvailableLocked는 확정 단계 밖 요청 하나가 떼어 둔 확정 몫을 건드리지 않고 예산과 송신 시도 상한을 예약할 수
// 있는지다. e.mu를 잡고 부른다. 예산을 읽지 못하면 뒤의 예약(Reserve)이 같은 이유로 거부하므로 여기서는 막지 않는다.
func (e *Execution) e3ShareAvailableLocked() bool {
	held := e.e3ShareHeldLocked()
	if held == 0 {
		return true
	}
	// 송신 시도 상한(MaxAttempts)이 예산보다 작게 설정돼도 모델 단계가 시도를 다 써서 확정 요청이 AttemptLimit으로
	// 막히지 않게, 시도 상한에서도 같은 몫을 뗀다(#826 윤재상 봇 리뷰).
	if e.attemptsLeftLocked() <= held {
		return false
	}
	if e.cfg.Budget.Ledger == nil {
		return true
	}
	remaining, err := e.cfg.Budget.Ledger.Remaining(e.cfg.Budget.Key, e.cfg.Budget.Binding, time.Now())
	if err != nil {
		return true
	}
	return remaining > held
}

func (e *Execution) Calls() []CallResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]CallResult, 0, len(e.callOrder))
	for _, c := range e.callOrder {
		out = append(out, c.resultLocked())
	}
	return out
}
func (e *Execution) Events(after uint64, limit int) []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	if limit <= 0 || limit > maxEventPageSize {
		limit = maxEventPageSize
	}
	out := []Event{}
	for _, ev := range e.events {
		if ev.Index > after {
			out = append(out, ev)
			if len(out) == limit {
				break
			}
		}
	}
	return out
}

// EventsForIntent filters before applying the page limit and preserves the
// execution-wide index. Global controls (IntentID zero) remain in Events.
func (e *Execution) EventsForIntent(intentID int64, after uint64, limit int) []Event {
	out := []Event{}
	if e == nil || intentID <= 0 {
		return out
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, registered := e.intents[intentID]; !registered {
		return out
	}
	if limit <= 0 || limit > maxEventPageSize {
		limit = maxEventPageSize
	}
	for _, event := range e.events {
		if event.IntentID == intentID && event.Index > after {
			out = append(out, event)
			if len(out) == limit {
				break
			}
		}
	}
	return out
}

func (c *Call) start(method string) (Receipt, error) {
	e := c.e
	e.mu.Lock()
	defer e.mu.Unlock()
	if c.closing || c.finalized {
		return Receipt{}, ErrClosed
	}
	// 상한에 닿은 call의 새 요청은 시도 번호·감사 기록 없이 거절한다. 기록되는 거절이 call마다
	// 상한으로 묶이므로 실행 전체의 기록도 MaxCalls와 call 상한의 곱 안에 머문다(C01).
	if c.fence != "" {
		return Receipt{Version: Version, Scope: e.cfg.Scope, IntentID: c.intentID, Program: c.program, CallRef: c.ref, CommandRef: c.commandRef, PolicyRevision: e.cfg.PolicyRevision, Reason: c.fence, NotSent: true}, ErrClosed
	}
	id, err := randomRef("")
	if err != nil || e.issued[id] {
		e.stopped = true
		e.stopReason = Invalid
		e.cancel()
		return Receipt{}, ErrConfig
	}
	e.issued[id] = true
	e.next++
	r := Receipt{Version: Version, Scope: e.cfg.Scope, IntentID: c.intentID, Program: c.program, CallRef: c.ref, CommandRef: c.commandRef, AttemptID: id, PolicyRevision: e.cfg.PolicyRevision, Sequence: e.next, Method: method, NotSent: true, Reason: Complete}
	if !supportedMethod(method) {
		r.Method = ""
		r.Decision = "unsupported"
		r.Reason = Unsupported
	}
	if c.active == 0 {
		c.idle = make(chan struct{})
	}
	c.active++
	c.counts.Attempts++
	e.counts.Attempts++
	if counts := e.intentCounts[c.intentID]; counts != nil {
		counts.Attempts++
	}
	if c.program.Valid() || c.program.isE3() {
		e.programCounts.Attempts++
	}
	return r, nil
}

// 기록을 먼저 마친 정상 요청도 다른 요청의 실패 고정을 덮어쓰면 안 된다.
// 호출자는 execution 잠금을 보유한다.
func (c *Call) preserveOutcomeLocked(r Receipt, err error) {
	e := c.e
	c.reason = r.Reason
	if err != nil || r.Reason == AuditFailed || e.auditFailed {
		e.auditFailed = true
		e.stopReason = AuditFailed
		e.cancel()
		c.auditComplete = false
		c.reason = AuditFailed
	}
}

func (c *Call) finish(ctx context.Context, r Receipt, observe ...func(Receipt)) (Receipt, error) {
	r.At = time.Now().UTC()
	err := c.e.recordBounded(ctx, Event{Stage: "result", Receipt: r})
	// 모델 응답·호출 결과에는 기존 정책 거부를 보내고, 정본 감사에는 구체적인 거부 사유를 남긴다.
	if r.Reason == WorkflowPaused {
		r.Reason = PolicyDenied
	}
	r.AuditComplete = err == nil && r.Reason != AuditFailed
	for _, capture := range observe {
		capture(r)
	}
	e := c.e
	e.mu.Lock()
	defer e.mu.Unlock()
	add := func(counts *Counts) {
		switch r.Decision {
		case "allowed":
			counts.Allowed++
		case "denied":
			counts.Denied++
		case "unsupported":
			counts.Unsupported++
		}
		if r.ForwardAttempted {
			counts.ForwardAttempted++
		}
		if r.ResponseReceived {
			counts.Responses++
		}
		if r.Reason == Unknown {
			counts.Unknown++
		}
		if r.Reason == TransportFailed {
			counts.Transient++
		}
		if r.Reason == Withheld {
			counts.Withheld++
		}
	}
	add(&c.counts)
	add(&e.counts)
	if counts := e.intentCounts[c.intentID]; counts != nil {
		add(counts)
	}
	if c.program.Valid() || c.program.isE3() {
		add(&e.programCounts)
	}
	if (r.Decision == "denied" || r.Decision == "unsupported") && r.Reason == Closed {
		c.closedRefusals++
	}
	c.preserveOutcomeLocked(r, err)
	c.active--
	if c.active == 0 {
		close(c.idle)
	}
	c.fenceLocked()
	return r, err
}

// fenceLocked는 요청 단위 실패(거부·미지원·결과 불명·송신 실패)가 이 call의 상한에 닿으면 이 call의
// 새 요청만 막는다. 다른 call·intent와 실행 전체는 계속된다. 실행 전체를 멈추는 사유는 감사 실패,
// 집행 경계 장애, 권한 철회·만료·결속 실패, 운영자 중지·취소뿐이고 그 경로는 따로 있다(C01).
// 예산·송신 시도 상한 소진은 그 뒤 모든 예약이 거부되므로 새 송신이 생기지 않는다.
// 호출자는 execution 잠금을 보유한다.
func (c *Call) fenceLocked() {
	if c.fence != "" {
		return
	}
	limits := c.e.cfg.Limits
	switch {
	case c.counts.Denied+c.counts.Unsupported-min(c.closedRefusals, c.counts.Denied+c.counts.Unsupported) >= limits.MaxDenials:
		c.fence = DenialLimit
	case c.counts.Unknown >= limits.MaxUnknown:
		c.fence = UnknownLimit
	case c.counts.Transient >= limits.MaxTransient:
		c.fence = TransientLimit
	}
}
func (c *Call) Reject(ctx context.Context, method string, reason Reason) (Receipt, error) {
	if ctx == nil {
		return Receipt{}, ErrConfig
	}
	r, err := c.start(method)
	if err != nil {
		return r, err
	}
	if reason != Unsupported && reason != PolicyDenied {
		reason = Unsupported
	}
	r.Decision = "unsupported"
	if reason == PolicyDenied {
		r.Decision = "denied"
	}
	r.Reason = reason
	// 형식 오류·CONNECT 단계 거부는 요청 URL이 없어 경로를 만들 수 없다(평가 집계에서 "경로 미확보").
	return c.finish(ctx, r, func(rr Receipt) { c.observeAttemptPath(nil, rr) })
}

// encodedPathHazards는 요청 경로의 percent 인코딩 중 대상이 범위 검사와 다르게 해석할 수 있는 것이다.
// 인코딩한 slash·backslash는 조각 경계를, 인코딩한 점은 점 조각을, 인코딩한 %는 두 번 풀기를 숨긴다(C17).
var encodedPathHazards = []string{"%2f", "%5c", "%2e", "%25", "%00"}

// requestPath는 범위 검사에 쓸 풀린 경로다. 정상 percent 인코딩(공백·비ASCII 등)은 받는다. 실제로 보내는 것은
// 같은 요청의 EscapedPath이고, 위험 인코딩을 막았으므로 풀린 경로와 조각 경계·점 조각이 같다.
func requestPath(u *url.URL) (string, bool) {
	escaped := u.EscapedPath()
	if escaped == "" {
		escaped = "/"
	}
	lower := strings.ToLower(escaped)
	for _, hazard := range encodedPathHazards {
		if strings.Contains(lower, hazard) {
			return "", false
		}
	}
	decoded, err := url.PathUnescape(escaped)
	if err != nil || !utf8.ValidString(decoded) || !strings.HasPrefix(decoded, "/") || strings.Contains(decoded, "//") || strings.ContainsAny(decoded, "\\%") {
		return "", false
	}
	for _, r := range decoded {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	for _, segment := range strings.Split(decoded, "/") {
		// 경로 매개변수(;...)를 조각에서 떼는 서버(Tomcat·Spring)는 "..;"를 점 조각으로 풀어 범위 밖으로 간다.
		// 매개변수를 뗀 이름이 점 조각이면 막는다(%3b로 인코딩한 것도 풀린 뒤라 같이 걸린다).
		name, _, _ := strings.Cut(segment, ";")
		if segment == "." || segment == ".." || name == "." || name == ".." {
			return "", false
		}
	}
	return decoded, true
}

func (e *Execution) matchLocked(req *http.Request) (compiledRule, bool) {
	a, err := canonicalAuthority(req.URL)
	p, ok := requestPath(req.URL)
	if err != nil || !ok {
		return compiledRule{}, false
	}
	for _, r := range e.rules {
		// 목적 규칙(인증 준비)은 모델 요청이 쓰지 못한다.
		if r.Purpose != "" {
			continue
		}
		withQuery := req.URL.RawQuery != "" || req.URL.ForceQuery
		if req.URL.Scheme == r.origin.Scheme && a == r.authority && r.methods[req.Method] &&
			(r.PathMatch == "exact" && !req.URL.ForceQuery && req.URL.RawQuery == r.ExactQuery &&
				(pathscope.Scope{Path: r.PathPrefix, Mode: pathscope.Exact}).Allows(p, false) ||
				r.PathMatch == "" && (pathscope.Scope{Path: r.PathPrefix, Mode: pathscope.Prefix}).Allows(p, withQuery)) {
			return r, true
		}
	}
	return compiledRule{}, false
}

// matchPurposeLocked는 목적 규칙만 본다. 조회 틀의 ID 조각을 제외한 경로는 정확히 같아야 한다.
func (e *Execution) matchPurposeLocked(req *http.Request, purpose string) (compiledRule, bool) {
	if req == nil || req.URL == nil {
		return compiledRule{}, false
	}
	a, err := canonicalAuthority(req.URL)
	p, ok := requestPath(req.URL)
	if err != nil || !ok || purpose == "" || req.URL.RawQuery != "" || req.URL.ForceQuery {
		return compiledRule{}, false
	}
	for _, r := range e.rules {
		if r.Purpose != purpose {
			continue
		}
		pathOK := p == r.PathPrefix
		if purpose == PurposeE3Read {
			pathOK = matchE3ReadPath(r.PathPrefix, p)
		}
		if req.URL.Scheme == r.origin.Scheme && a == r.authority && r.methods[req.Method] && pathOK {
			// Restrict로 Worker 승인이 줄어들 수 있다. 조회 ID를 채운 실제 경로도 제외 목록에 대조한다.
			if ProgramCallKind(purpose).isE3() && (!e3InWorkerScope(r, e.rules) || pathscope.ExcludedAny(p, e.cfg.ExcludedPaths)) {
				continue
			}
			return r, true
		}
	}
	return compiledRule{}, false
}

// matchLearnedLoginLocked는 확정 단계 학습 로그인 요청을 Worker 승인 규칙으로 본다(§6-2). 로그인 POST는 그 경로에 POST를
// 승인한 규칙(쓰기 승인 범위)이, 로그인 화면·대조 GET은 GET을 승인한 규칙이 있어야 한다. 질의가 있거나 운영자 제외 경로면
// 맞지 않는다(모델 요청과 같은 한 번 푼 경로로 본다).
func (e *Execution) matchLearnedLoginLocked(req *http.Request, purpose string) (compiledRule, bool) {
	if req == nil || req.URL == nil || req.URL.RawQuery != "" || req.URL.ForceQuery || req.URL.Fragment != "" {
		return compiledRule{}, false
	}
	want := http.MethodGet
	if purpose == PurposeLearnedLogin {
		want = http.MethodPost
	}
	if req.Method != want {
		return compiledRule{}, false
	}
	p, ok := requestPath(req.URL)
	if !ok || len(e.cfg.ExcludedPaths) > 0 && pathscope.ExcludedAny(p, e.cfg.ExcludedPaths) {
		return compiledRule{}, false
	}
	return e.matchLocked(req)
}

// matchSelectedLoginLocked는 선택 후보 검증 학습 로그인 요청을 승인 계획에 결속한 전용 규칙으로만 본다. 로그인 POST는
// 인증 준비 규칙(auth_prep), 준비 GET은 login_prep 규칙과 정확히 같은 경로여야 한다. Worker 규칙(선택 실행에서는 계획의
// 정확한 GET·HEAD 행동)으로는 보내지 않는다. 질의·조각이 있거나 운영자 제외 경로면 맞지 않는다.
func (e *Execution) matchSelectedLoginLocked(req *http.Request, purpose string) (compiledRule, bool) {
	if req == nil || req.URL == nil || req.URL.RawQuery != "" || req.URL.ForceQuery || req.URL.Fragment != "" {
		return compiledRule{}, false
	}
	want, rulePurpose := http.MethodGet, PurposeLoginPrep
	if purpose == PurposeLearnedLogin {
		want, rulePurpose = http.MethodPost, PurposeAuthPrep
	}
	p, ok := requestPath(req.URL)
	if req.Method != want || !ok || len(e.cfg.ExcludedPaths) > 0 && pathscope.ExcludedAny(p, e.cfg.ExcludedPaths) {
		return compiledRule{}, false
	}
	return e.matchPurposeLocked(req, rulePurpose)
}

func matchE3ReadPath(template, actual string) bool {
	want, got := strings.Split(template, "/"), strings.Split(actual, "/")
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if want[i] == "{id}" {
			if got[i] == "" || got[i] == "." || got[i] == ".." || strings.ContainsAny(got[i], "{};?") {
				return false
			}
		} else if want[i] != got[i] {
			return false
		}
	}
	return true
}

type observed struct{ forward, connected, written atomic.Bool }
type guardedConn struct {
	net.Conn
	c        *Call
	revision string
	ob       *observed
}

// This constructor owns conn. Only an armed connection may become the
// write-guarded capability returned to net/http's transport.
func newDeadlineGuardedConn(conn net.Conn, call *Call, revision string, ob *observed) (net.Conn, error) {
	if err := conn.SetDeadline(time.Now().Add(call.e.cfg.Limits.RequestTimeout)); err != nil {
		if closeErr := conn.Close(); closeErr != nil {
			// Fixed diagnostics only: transport errors may contain private peer details.
			log.Print("[e2proxy] request_connection_close_failed after=deadline_unavailable")
		}
		return nil, ErrDenied
	}
	return &guardedConn{Conn: conn, c: call, revision: revision, ob: ob}, nil
}

// 외부 파일 변경과 송신을 원자적으로 묶는 검사는 아니다.
// 각 지원 송신 경계 직전에 확인하고 한 번 실패한 위임은 다시 허용하지 않는다.
// 호출자는 execution 잠금을 보유한다.
// Dial 뒤 TLS/요청 조각을 쓰기 전에도 철회될 수 있으므로 Write 검사를 생략하지 않는다.
// 판정·정책 세대와 송신의 잠금 경계를 유지하며 callback은 잠금 아래 실행된다.
// URL 준비 callback은 held FD의 metadata를 확인하고 큰 helper 원문을 매번 다시 읽지 않는다.
func (e *Execution) authorityCurrentLocked() bool {
	if e.cfg.CheckAuthority == nil {
		return true
	}
	if e.cfg.CheckAuthority() == nil {
		return true
	}
	e.stopped, e.stopReason = true, Revoked
	e.cancel()
	return false
}

func (g *guardedConn) Write(p []byte) (int, error) {
	e := g.c.e
	e.mu.Lock()
	defer e.mu.Unlock()
	if g.c.blockedLocked() != Complete || e.cfg.PolicyRevision != g.revision || !e.authorityCurrentLocked() || g.c.blockedLocked() != Complete || e.cfg.PolicyRevision != g.revision {
		return 0, ErrDenied
	}
	g.ob.forward.Store(true)
	return g.Conn.Write(p)
}
func (c *Call) Do(ctx context.Context, req *http.Request) (*http.Response, Receipt, error) {
	resp, _, r, err := c.do(ctx, req, "", nil)
	return resp, r, err
}

// AuthExtractor는 인증 준비 응답(상태·헤더·상한까지 읽은 본문)에서 보낼 세션 헤더만 뽑는다. 응답은 도구·모델로 가지 않는다.
type AuthExtractor func(*http.Response, []byte) (http.Header, error)

// AuthPrep는 프로그램이 부르는 인증 준비 요청 하나를 인증 준비 규칙으로만 보낸다(C14 구현 설계 2). 대상·IP·TLS·감사·예산
// 계수는 Do와 같고, 진단 요청 경계(워크플로 훅)·요청 승인·응답 표현 보류는 거치지 않는다. 응답은 extract만 보고, 돌려주는
// 것은 보낼 세션 헤더다. 자동 재시도는 없다. 결과 불명이면 Receipt.Reason이 Unknown이고 다시 보내지 않는 것은 호출자 몫이다.
func (c *Call) AuthPrep(ctx context.Context, req *http.Request, extract AuthExtractor) (http.Header, Receipt, error) {
	if extract == nil {
		return nil, Receipt{}, ErrConfig
	}
	_, session, r, err := c.do(ctx, req, PurposeAuthPrep, extract)
	return session, r, err
}

// LearnedLogin은 확정 단계 다시 로그인 호출(BeginE3ReloginCall)이 학습한 로그인 방법의 요청 하나를 보낸다(계약
// docs/contracts/model-login-attempts.md §6-2). purpose는 PurposeLearnedLogin(로그인 POST) 또는 PurposeLoginPrep(로그인 화면·
// 세션 없는 대조 GET)이다. 전용 규칙이 아니라 그 실행의 Worker 승인 규칙이 메서드·경로를 덮어야 하고(쓰기 승인 범위),
// 운영자 제외 경로에 들면 보내지 않는다. 질의는 받지 않는다. 대상·IP·TLS·감사·예산(확정 몫) 계수와 결과 불명 처리는
// AuthPrep와 같고, 응답은 inspect만 본다(도구·모델로 가지 않음). inspect가 돌려준 헤더(세션·로그인 전 쿠키)는 보호 비밀번호가
// 들어 있지 않은지 확인한 뒤에만 돌려준다. 자동 재시도는 없다.
func (c *Call) LearnedLogin(ctx context.Context, req *http.Request, purpose string, inspect AuthExtractor) (http.Header, Receipt, error) {
	if inspect == nil || !learnedLoginPurpose(purpose) {
		return nil, Receipt{}, ErrConfig
	}
	_, session, r, err := c.do(ctx, req, purpose, inspect)
	return session, r, err
}

func (c *Call) do(ctx context.Context, req *http.Request, purpose string, extract AuthExtractor) (*http.Response, http.Header, Receipt, error) {
	return c.doWithE3(ctx, req, purpose, extract, nil)
}

func (c *Call) doWithE3(ctx context.Context, req *http.Request, purpose string, extract AuthExtractor, e3 *e3ProgramCall) (*http.Response, http.Header, Receipt, error) {
	if ctx == nil || c == nil {
		return nil, nil, Receipt{}, ErrConfig
	}
	if c.program.isE3() && (e3 == nil || c.program != e3.kind || purpose != string(c.program)) ||
		e3 != nil && !c.program.isE3() {
		return nil, nil, Receipt{}, ErrE3JournalUnavailable
	}
	// 학습 로그인 목적은 확정 단계 다시 로그인 호출만 쓴다. Worker의 refresh_session·처음 인증 준비는 지금처럼 인증 준비
	// 규칙 하나뿐이다.
	learned := learnedLoginPurpose(purpose)
	if learned {
		if !(c.program == ProgramRefreshSession && c.e3Share || c.program == ProgramAuthPrep && c.selectedLogin) {
			return nil, nil, Receipt{}, ErrConfig
		}
	} else if c.selectedLogin || c.program == "" && purpose != "" ||
		(c.program == ProgramAuthPrep || c.program == ProgramRefreshSession) && purpose != PurposeAuthPrep {
		return nil, nil, Receipt{}, ErrConfig
	}
	method := ""
	if req != nil {
		// 대기 중 호출자가 URL·헤더를 바꿔 승인 내용과 송신을 분리하지 못한다.
		req = req.Clone(ctx)
		method = req.Method
	}
	r, err := c.start(method)
	if err != nil {
		return nil, nil, r, err
	}
	if e3 != nil {
		r.E3Audit = &e3.audit
	}
	var session http.Header
	var requestHeaders http.Header
	// 모델 직접 로그인 판정(login_attempts.go). 모델 요청만 넘긴다.
	var loginTracker *loginattempt.Tracker
	var loginPlan loginattempt.Plan
	var loginReq loginattempt.Request
	var loginResp *loginattempt.Response
	finish := func(resp *http.Response, cause error) (*http.Response, http.Header, Receipt, error) {
		if e3 != nil {
			e3.finish(&r, ctx)
		}
		var boundaryErr error
		rr, aerr := c.finish(ctx, r, func(rr Receipt) {
			// 로그인 판정을 관찰 기록보다 먼저 한다. 로그인 성공을 세운 요청도 그 계정으로 귀속된다(ResolveSentAccount).
			finishLoginAttempt(loginTracker, loginPlan, loginReq, loginResp, rr)
			c.observeHTTP(req, requestHeaders, resp, rr)
			attempt := rr
			attempt.Reason = r.Reason
			c.observeAttemptPath(req, attempt)
			// 인증 준비는 진단 요청이 아니라 워크플로 요청 경계에 알리지 않는다(진단 쓰기 수와 따로 셈).
			if purpose == "" && c.program != ProgramLoginContrast && c.e.cfg.RequestBoundary != nil && req != nil && req.URL != nil {
				boundaryErr = c.e.cfg.RequestBoundary.After(context.WithoutCancel(ctx), RequestView{Method: req.Method, URL: req.URL.String(), BodyPresent: req.ContentLength != 0}, rr)
			}
		})
		if boundaryErr != nil && !errors.Is(boundaryErr, ErrBoundaryProjection) {
			c.e.mu.Lock()
			c.e.auditFailed = true
			c.e.mu.Unlock()
			return nil, nil, rr, ErrAudit
		}
		if aerr != nil {
			return nil, nil, rr, aerr
		}
		return resp, session, rr, cause
	}
	if req != nil && req.URL != nil && req.URL.Path == "" {
		copyReq := req.Clone(ctx)
		copyURL := *req.URL
		copyURL.Path = "/"
		copyReq.URL = &copyURL
		req = copyReq
	}
	if req == nil || req.URL == nil || !supportedMethod(method) || req.ProtoMajor > 1 || req.ProtoMajor == 1 && req.ProtoMinor > 1 || req.Method == "CONNECT" || headerPresent(req.Header, "Upgrade") || headerPresent(req.Header, "Expect") || connectionUpgrade(req.Header) {
		r.Decision = "unsupported"
		r.Reason = Unsupported
		return finish(nil, ErrDenied)
	}
	e := c.e
	requestHost, hostOK := preservedRequestHost(req)
	if !hostOK || !e.callerCredentialsAllowed(req.Header) {
		r.Decision = "unsupported"
		r.Reason = Unsupported
		return finish(nil, ErrDenied)
	}
	e.mu.Lock()
	reason := c.blockedLocked()
	if reason == Complete && !e.authorityCurrentLocked() {
		reason = Revoked
	}
	if reason == Complete {
		reason = c.blockedLocked()
	}
	var rule compiledRule
	var ok bool
	switch {
	case purpose == "":
		rule, ok = e.matchLocked(req)
	case learned && c.selectedLogin:
		rule, ok = e.matchSelectedLoginLocked(req, purpose)
	case learned:
		rule, ok = e.matchLearnedLoginLocked(req, purpose)
	default:
		rule, ok = e.matchPurposeLocked(req, purpose)
	}
	revision := e.cfg.PolicyRevision
	if reason == Complete && !ok {
		reason = PolicyDenied
	}
	e.mu.Unlock()
	if reason != Complete {
		r.Decision = "denied"
		r.Reason = reason
		return finish(nil, ErrDenied)
	}
	requestHeaders, headersOK := forwardRequestHeaders(req.Header, rule)
	if !headersOK {
		r.Decision, r.Reason = "unsupported", Unsupported
		return finish(nil, ErrDenied)
	}
	if purpose == "" && e.cfg.SelectedSessionHeaders != nil {
		protected, bound := e.cfg.SelectedSessionHeaders()
		if !bound || len(protected) == 0 || e.cfg.ResolveSentAccount(protected) != e.cfg.SelectedAccountRef ||
			headerPresent(requestHeaders, "Authorization") || headerPresent(requestHeaders, "Cookie") ||
			e.cfg.TargetCredentialHeader != "" && headerPresent(requestHeaders, e.cfg.TargetCredentialHeader) {
			r.Decision, r.Reason = "denied", PolicyDenied
			return finish(nil, ErrDenied)
		}
		for key, values := range protected {
			if len(requestHeaders.Values(key)) != 0 || len(values) == 0 {
				r.Decision, r.Reason = "denied", PolicyDenied
				return finish(nil, ErrDenied)
			}
			requestHeaders[key] = append([]string(nil), values...)
		}
	}
	// 승인 제안·송신이 모두 실제로 보낼 값을 보도록 규칙 제약 검사 뒤, 승인 전에 바꾼다.
	identityAcceptEncoding(requestHeaders)
	// Consume the caller's bounded body before admission; it never reaches audit.
	var body []byte
	if req.Body != nil {
		body, err = io.ReadAll(io.LimitReader(req.Body, e.cfg.Limits.MaxBodyBytes+1))
		// 학습 로그인 본문에는 등록 자격이 들어 있다. 보낸 뒤 이 사본도 지운다.
		if e3 != nil || learned {
			defer clear(body)
		}
		if err != nil || int64(len(body)) > e.cfg.Limits.MaxBodyBytes {
			r.Decision = "unsupported"
			r.Reason = Unsupported
			return finish(nil, ErrDenied)
		}
	}
	// 로그인 화면·대조 GET은 본문이 없다.
	if purpose == PurposeLoginPrep && len(body) > 0 {
		r.Decision, r.Reason = "unsupported", Unsupported
		return finish(nil, ErrDenied)
	}
	r.RuleRef = rule.Ref
	r.PolicyRevision = revision
	view := RequestView{Method: method, URL: req.URL.String(), BodyPresent: len(body) > 0, BodyBytes: len(body)}
	// 진단 요청 경계(워크플로 선택·쓰기 승인)와 요청 승인은 모델 요청에만 적용한다. 인증 준비는 대상 등록 때 승인한 로그인
	// 경로 하나로만 나간다.
	boundary := e.cfg.RequestBoundary
	if purpose != "" || c.program == ProgramLoginContrast {
		// 프로그램 대조 요청은 모델이 진단 요청 경계를 지나 이미 GET한 URL을 세션 없이 다시 여는 것이다. 진단 흐름 경계·요청
		// 승인은 거치지 않고, 승인 규칙 범위·제외 경로·예산은 그대로 받는다(계약 §4).
		boundary = nil
	}
	if c.program == ProgramLoginContrast && (method != http.MethodGet || len(body) > 0 || headerPresent(requestHeaders, "Cookie") || headerPresent(requestHeaders, "Authorization") ||
		e.cfg.TargetCredentialHeader != "" && headerPresent(requestHeaders, e.cfg.TargetCredentialHeader)) {
		r.Decision, r.Reason = "unsupported", Unsupported
		return finish(nil, ErrDenied)
	}
	// 운영자 제외 경로(#627)는 진단 흐름 경계보다 먼저 E²가 본다. 진단 흐름 경계 거부와 같은 모양(policy_denied, 맞은
	// 규칙 참조)의 결과 기록 하나·호출당 거부 계수 하나로 끝나고 경계는 부르지 않는다(요청당 한 번, 사유 하나). 그래서
	// 평가 내보내기·시도 대조·작업대의 사유 어휘가 바뀌지 않는다. 경로는 승인 규칙 검사와 같은 한 번 푼 경로(requestPath)라
	// 인코딩 표기로 우회하지 못한다. E③ 목적은 matchPurposeLocked에서 따로 대조한다. 인증 준비는 대상 등록 때 승인한 로그인 경로를 쓴다.
	if purpose == "" && len(e.cfg.ExcludedPaths) > 0 {
		if p, ok := requestPath(req.URL); !ok || pathscope.ExcludedAny(p, e.cfg.ExcludedPaths) {
			r.Decision, r.Reason = "denied", PolicyDenied
			return finish(nil, ErrDenied)
		}
	}
	// 모델 직접 로그인(사용자 결정 2026-11-26): 아이디별 연속 실패 상한에 닿은 로그인은 보내지 않는다. E② 사유 어휘는 늘리지
	// 않고 운영자 제외 경로(#627)처럼 policy_denied로 남긴다. 고정 사유 login_attempts_exhausted는 로그인 시도 기록과 진단
	// 로그에 있다(계약 §5).
	if purpose == "" && e3 == nil && c.program == "" {
		if tracker := e.loginAttempts.Load(); tracker != nil {
			loginTracker = tracker
			// 상한 거부도 이 영수증(call_ref·attempt_id)으로 E② 기록과 잇는다(#969 동찬 리뷰 P2).
			loginReq = loginattempt.Request{TransportRef: loginAttemptRef(r), Method: method, Path: req.URL.EscapedPath(), RawQuery: req.URL.RawQuery, ContentType: requestHeaders.Get("Content-Type"), Body: body, Header: requestHeaders.Clone()}
			loginPlan = tracker.Prepare(loginReq, true)
			if loginPlan.Refusal != "" {
				diagnosticReceipt(e.ctx, r, "login_attempt_refused", "denied", loginPlan.Refusal)
				r.Decision, r.Reason = "denied", PolicyDenied
				return finish(nil, ErrDenied)
			}
		}
	}
	if boundary != nil {
		if err := boundary.Before(ctx, view); err != nil {
			r.Decision, r.Reason = "denied", boundaryDenialReason(err)
			return finish(nil, ErrDenied)
		}
	}
	needsApproval := c.program != ProgramLoginContrast && e.needsRequestApproval(purpose, method, body, view)
	if needsApproval {
		// 승인 대기 문맥은 call 문맥(모델 단계 마감·call 기한)과 요청 문맥(요청 기한·연결 끝남)의 원인을 그대로 싣는다.
		// 승인 이력이 닫힌 까닭(closed_reason)을 가리는 근거다.
		approvalCtx, cancelApproval := context.WithCancelCause(c.ctx)
		stopApproval := context.AfterFunc(ctx, func() { cancelApproval(requestCancelCause(ctx)) })
		// 승인 미리보기는 운영자 화면·승인 기록에 남는 저장 기록이다. model_visible_v1에서는 모델이 대상이 비춘 비밀번호를
		// 요청 본문에 넣을 수 있으므로, 모델 단계·확정 단계 모두 비밀번호까지 가린 본문을 남긴다.
		previewBody := e.RedactRecord(body)
		// 확정 요청은 요청 전용 시간 상한(ctx 기한, 제품 기본 10초) 안에 승인돼야 보낸다. 승인 기한을 그 기한에 맞춰, 운영자가
		// 결정하지 않으면 승인 기록도 만료(expired)로 남게 한다(#836 이동찬 리뷰).
		approvalExpires := c.expires
		if deadline, ok := ctx.Deadline(); e3 != nil && ok && deadline.Before(approvalExpires) {
			approvalExpires = deadline
		}
		proposal := RequestApprovalProposal{
			Receipt: r, OwnerRef: e.cfg.OwnerRef, AccountRef: e.cfg.AccountRef, SessionRef: e.cfg.SessionRef,
			ExpiresAt: approvalExpires, Method: method, URL: req.URL.String(), Host: requestHost,
			Headers: requestHeaders.Clone(), Body: previewBody,
		}
		// 보호 자격 원문은 승인 소비자에게 전달하지 않는다. 참조만 결속한다.
		var allowed bool
		var approvalErr error
		// 승인을 기다리는 동안은 러너가 명령 시간 예산을 쓰지 않는다(#898 발견 3). call 기한·회차 기한은 그대로다.
		// 프록시 요청 상한도 멈추고 승인 대기가 끝나면 처음부터 다시 잰다(request_clock.go, 사용자 결정 2026-11-25).
		// 승인 대기 자체는 승인 기한(approvalExpires)과 call 문맥까지만이다.
		clock := requestClockFrom(ctx)
		clock.pause()
		c.approvals.add(1)
		if gate, ok := e.cfg.RequestApproval.(RequestApprovalOutcomeGate); ok {
			var outcome RequestApprovalOutcome
			outcome, approvalErr = gate.AwaitRequestApprovalOutcome(approvalCtx, proposal)
			allowed = outcome == RequestApprovalAllowed
			if e3 != nil {
				e3.approval = outcome
			}
		} else {
			allowed, approvalErr = e.cfg.RequestApproval.AwaitRequestApproval(approvalCtx, proposal)
		}
		c.approvals.add(-1)
		clock.restart()
		canceled := approvalCtx.Err() != nil || ctx.Err() != nil
		stopApproval()
		cancelApproval(nil)
		if approvalErr != nil && !(canceled && (errors.Is(approvalErr, context.Canceled) || errors.Is(approvalErr, context.DeadlineExceeded))) {
			r.Decision, r.Reason = "denied", AuditFailed
			return finish(nil, ErrAudit)
		}
		if !allowed || canceled {
			r.Decision, r.Reason = "denied", PolicyDenied
			if canceled {
				r.Reason = Closed
			}
			// 확정 요청이 승인을 기다리다 요청 시간 상한에 닿았으면 승인 만료다(계약 approval_timeout, #836 이동찬 리뷰).
			// 운영자 거절이 먼저 왔으면 거절로 둔다. 승인 기한을 요청 상한에 맞췄으므로 기한 만료와 문맥 기한이 경합하는데,
			// 어느 쪽이 먼저든 감사 사유는 모델 요청의 승인 만료와 같은 policy_denied로 고정한다. 실행 중지 등 기한이 아닌
			// 취소는 지금처럼 closed·policy_denied다.
			if e3 != nil && !allowed && e3.approval != RequestApprovalDenied &&
				(errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(c.ctx.Err(), context.DeadlineExceeded)) {
				e3.approval = RequestApprovalExpired
			}
			if e3 != nil && e3.approval == RequestApprovalExpired {
				r.Reason = PolicyDenied
			}
			return finish(nil, ErrDenied)
		}
	}
	// 사용자 승인 모드의 확정 생성 요청은 요청 시간 상한을 여기(승인 뒤)부터 센다. 송신 결과의 시간 초과 판정(TimedOut)도
	// 이 문맥으로 한다.
	if e3 != nil && e3.timeoutAfterApproval {
		var cancelSend context.CancelFunc
		ctx, cancelSend = context.WithTimeout(ctx, e3.timeout)
		defer cancelSend()
		e3.sendCtx = ctx
	}
	if boundary != nil {
		if err := boundary.Before(ctx, view); err != nil {
			r.Decision, r.Reason = "denied", boundaryDenialReason(err)
			return finish(nil, ErrDenied)
		}
	}
	// 동시 송신 자리는 승인 대기 뒤에 잡는다. 사람 승인을 기다리는 요청이 다른 송신의 자리를 막지 않는다.
	// 자리를 기다리는 동안 취소·철회·만료·운영자 중지가 오면 보내지 않고 끝낸다. 자리를 얻은 뒤 아래에서
	// 권한·정책 세대·시도 상한·예산을 다시 검사한다(C20·C21).
	// 빈 자리는 먼저 잡는다. 이미 취소된 호출자도 자리가 있으면 지금처럼 예약·감사 경로로 간다(select의 임의 선택 방지).
	waited := false
	select {
	case e.slots <- struct{}{}:
	default:
		waited = true
		select {
		case e.slots <- struct{}{}:
		case <-ctx.Done():
			r.Decision, r.Reason = "denied", Closed
			return finish(nil, ErrClosed)
		case <-c.ctx.Done():
			r.Decision, r.Reason = "denied", Closed
			return finish(nil, ErrClosed)
		}
	}
	defer func() { <-e.slots }()
	// 자리를 기다리는 동안 워크플로 일시정지·단계 종료·선택 해제가 있었을 수 있다. 기다린 요청은 요청 경계를 다시
	// 본다(#487 리뷰 P2). 기다리지 않은 요청은 위 검사 그대로다.
	if waited && boundary != nil {
		if err := boundary.Before(ctx, view); err != nil {
			r.Decision, r.Reason = "denied", boundaryDenialReason(err)
			return finish(nil, ErrDenied)
		}
	}
	if e3 != nil && !e3.current() {
		r.Decision, r.Reason = "denied", Revoked
		return finish(nil, e3contract.ErrAuthorityRevoked)
	}
	r.Decision = "allowed"
	e.mu.Lock()
	reason = c.blockedLocked()
	// 기다리는 동안 같은 call의 앞선 요청이 결과 불명·거부·송신 실패 상한에 닿았을 수 있다(#487 리뷰 P2). C01의
	// call 상한은 대기열 뒤에도 지킨다.
	if reason == Complete && c.fence != "" {
		reason = c.fence
	}
	if reason == Complete && e.cfg.PolicyRevision != revision {
		reason = PolicyDenied
	}
	if reason == Complete && !e.authorityCurrentLocked() {
		reason = Revoked
	}
	if reason == Complete {
		reason = c.blockedLocked()
	}
	if reason == Complete && e.cfg.PolicyRevision != revision {
		reason = PolicyDenied
	}
	// 송신 시도 상한은 실제로 예산을 예약하는 요청만 센다. 상한에 닿으면 이 요청과 그 뒤 요청만
	// 거부하고, 이미 송신 중인 요청은 끊지 않는다(C01).
	if reason == Complete && e.admitted >= e.cfg.Limits.MaxAttempts {
		reason = AttemptLimit
	}
	// 확정 몫은 확정 단계 요청(E③ 프로그램 요청·확정 단계가 연 다시 로그인)만 쓴다. 그 밖의 요청(모델 단계 Worker·인증
	// 준비·Worker refresh_session)은 확정 단계 전이든 뒤든 예산의 마지막 확정 몫만큼을 쓰지 못한다(계약 §7.1).
	shareConsumer := e3 != nil || c.e3Share
	// 모델 단계가 마감되면 확정 단계 밖 요청은 보내지 않는다(이미 열린 호출의 늦은 요청 포함).
	if reason == Complete && !shareConsumer && e.modelClosed {
		reason = Closed
	}
	if reason == Complete && shareConsumer && e.e3Admitted >= e.cfg.E3ConfirmationRequests {
		reason = BudgetExhausted
	}
	if reason == Complete && !shareConsumer && !e.e3ShareAvailableLocked() {
		reason = BudgetExhausted
	}
	if reason == Complete {
		if err = e.cfg.Budget.Ledger.Reserve(e.cfg.Budget.Key, e.cfg.Budget.Binding, budget.Attempt{RunRef: e.cfg.Scope.RunRef, Sequence: r.Sequence}, time.Now()); err != nil {
			reason = BudgetExhausted
		} else {
			e.admitted++
			if shareConsumer {
				e.e3Admitted++
			}
		}
	}
	e.mu.Unlock()
	if reason != Complete {
		r.Decision = "denied"
		r.Reason = reason
		return finish(nil, ErrDenied)
	}
	// Once reserved, the intent ACK has a bounded persistence context, just like
	// the result. Caller cancellation never abandons an audit write.
	intentReceipt := r
	if e3 != nil {
		// 응답 검사로 결과 칸을 채워도 intent의 메모리 사본은 바뀌지 않는다.
		intentAudit := *r.E3Audit
		intentReceipt.E3Audit = &intentAudit
	}
	err = e.recordBounded(ctx, Event{Stage: "intent", Receipt: intentReceipt})
	if err != nil {
		r.Decision = "denied"
		r.Reason = AuditFailed
		return finish(nil, ErrAudit)
	}
	if ctx.Err() != nil {
		r.Reason = Closed
		return finish(nil, ErrClosed)
	}
	if e3 != nil && !e3.current() {
		r.Reason = Closed
		return finish(nil, e3contract.ErrAuthorityRevoked)
	}
	requestTimeout := e.cfg.Limits.RequestTimeout
	if e3 != nil && e3.timeout < requestTimeout {
		requestTimeout = e3.timeout
	}
	workCtx, cancel := context.WithTimeout(c.ctx, requestTimeout)
	defer cancel()
	// 보낸 뒤에는 호출자 기한이 아니라 E② 요청 시간 상한(workCtx)으로만 끊는다. 대기열에서 호출자 기한을 거의 다 쓴
	// 요청을 기한으로 끊으면 대상이 받은 요청이 결과 불명이 되고 그 intent가 멈춘다(#487 리뷰 P3). 호출자 취소는 그대로
	// 끊는다.
	stop := context.AfterFunc(ctx, func() {
		if e3 != nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			cancel()
		}
	})
	defer stop()
	out := req.Clone(workCtx)
	u := *req.URL
	u.Host = rule.authority
	out.URL = &u
	out.Host = requestHost
	out.RequestURI = ""
	out.Close = true
	out.Header = requestHeaders
	if e3 != nil {
		for name, values := range e3.session {
			out.Header[name] = append([]string(nil), values...)
		}
	}
	for k, vs := range rule.CredentialHeaders {
		out.Header[k] = append([]string(nil), vs...)
	}
	out.Header.Set(AttemptHeader, r.AttemptID)
	if len(body) == 0 {
		out.Body = http.NoBody
	} else {
		out.Body = io.NopCloser(bytes.NewReader(body))
	}
	out.ContentLength = int64(len(body))
	out.TransferEncoding = nil
	out.Trailer = nil
	ob := &observed{}
	out = out.WithContext(httptrace.WithClientTrace(workCtx, &httptrace.ClientTrace{
		// HTTP/1에서 GotConn은 TCP 연결과 TLS 검증이 끝난 뒤, 요청을 쓰기 전에 불린다.
		GotConn: func(httptrace.GotConnInfo) { ob.connected.Store(true) },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				ob.written.Store(true)
			}
		}}))
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true, Protocols: protocols,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: e.cfg.RootCAs},
		TLSHandshakeTimeout: e.cfg.Limits.RequestTimeout, ResponseHeaderTimeout: e.cfg.Limits.RequestTimeout}
	if rule.TLSSPKISHA256 != "" {
		pin := rule.TLSSPKISHA256
		transport.TLSClientConfig.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return ErrDenied
			}
			sum := sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
			if hex.EncodeToString(sum[:]) != pin {
				return ErrDenied
			}
			return nil
		}
	}
	_, port, _ := net.SplitHostPort(rule.authority)
	transport.DialContext = func(dctx context.Context, network, address string) (net.Conn, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if c.blockedLocked() != Complete || e.cfg.PolicyRevision != revision || network != "tcp" || address != rule.authority || !e.authorityCurrentLocked() || c.blockedLocked() != Complete || e.cfg.PolicyRevision != revision {
			return nil, ErrDenied
		}
		ob.forward.Store(true)
		// Stop cancels dctx before waiting for this lock. Policy changes and actual
		// new connection admission therefore have a single linearization boundary.
		conn, derr := (&net.Dialer{Timeout: e.cfg.Limits.RequestTimeout}).DialContext(dctx, "tcp", net.JoinHostPort(rule.ConnectIP.String(), port))
		if derr != nil {
			return nil, ErrDenied
		}
		return newDeadlineGuardedConn(conn, c, revision, ob)
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// 응답 검사 길이와 보호 값 세대는 보내기 전에 잡는다. 보낸 뒤(응답을 기다리거나 읽는 동안) 더해진 보호 값은 세대 비교로
	// 잡아 그 응답을 보류한다.
	longest, generation := e.protectedState()
	resp, sendErr := client.Do(out)
	r.ForwardAttempted = ob.forward.Load()
	if r.ForwardAttempted {
		diagnosticReceipt(e.ctx, r, "http_forward_attempted", "started", "")
	}
	r.RequestWritten = ob.written.Load()
	r.NotSent = !r.ForwardAttempted
	if sendErr != nil {
		if e3 != nil && !r.ForwardAttempted {
			// intent ACK 뒤 연결 전 취소는 E③ 관찰이 아니다.
			r.Reason = Closed
			return finish(nil, ErrClosed)
		}
		r.Reason = TransportFailed
		// 대상이 요청을 받았을 수 있는 건 연결(TLS 포함)이 맺어진 뒤의 실패뿐이다. 그 전의 TCP 연결·TLS
		// 검증 실패는 요청을 전달하지 않은 송신 실패다. 연결 시도 사실은 ForwardAttempted로 남긴다(C02).
		if r.ForwardAttempted && ob.connected.Load() {
			r.Reason = Unknown
		} else if !r.ForwardAttempted {
			e.mu.Lock()
			authorityRevoked := e.cfg.CheckAuthority != nil && e.stopReason == Revoked
			e.mu.Unlock()
			if authorityRevoked {
				r.Decision, r.Reason = "denied", Revoked
			}
		}
		return finish(nil, ErrDenied)
	}
	if resp.StatusCode < 100 || resp.StatusCode > 599 {
		// Go HTTP 클라이언트는 세 자리 상태 코드를 모두 받지만 HTTP 상태는 1xx~5xx뿐이고 감사 기록도 그 범위만 받는다. 범위
		// 밖 응답은 해석할 수 없으니 그 요청만 결과 불명으로 끝낸다(다시 보내지 않음). 그대로 기록하면 감사 쓰기가 거부돼 대상
		// 응답 하나가 새 송신을 모두 멈추게 된다(요청 하나의 실패는 그 요청만 실패, #562 리뷰에서 발견).
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Print("[e2proxy] response_close_failed after=nonstandard_status")
		}
		r.Reason = Unknown
		return finish(nil, ErrDenied)
	}
	r.ResponseReceived = true
	r.StatusCode = resp.StatusCode
	diagnosticReceipt(e.ctx, r, "http_response_received", "succeeded", "")
	// 자격 반사 검사는 전달할 앞부분이 아니라 검사 상한까지 읽은 전체에 한다. 검사 상한은 본문 상한 + 가장 긴 보호 값
	// 길이라서, 전달 구간 끝에 걸친 보호 값도 통째로 검사 범위에 들어온다(C16 결정안 2, 상한 사이 조건).
	limit, scan := responseLimits(e.cfg.Limits, longest)
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, scan+1))
	oversized := int64(len(responseBody)) > limit
	closeErr := resp.Body.Close()
	// 본문을 다 읽기 전에 끊기면 대상이 끝까지 답했는지 모른다. 상한을 넘어 읽기를 멈추고 닫은 것은 끊김이 아니다.
	if readErr != nil || closeErr != nil && !oversized {
		clear(responseBody)
		r.Reason = Unknown
		return finish(nil, ErrDenied)
	}
	if loginTracker != nil {
		loginResp = observedLoginResponse(resp.StatusCode, resp.Header, responseBody)
	}
	if purpose == PurposeAuthPrep {
		// 인증 준비 응답은 도구로 가지 않는다. extract가 보낼 세션 헤더만 뽑고 본문은 지운다. 상한을 넘은 응답은 끝까지 보지
		// 못했으니 세션을 뽑지 않는다(대상은 답했으므로 결과 불명이 아니라 보류).
		if oversized {
			clear(responseBody)
			r.Reason, r.ResponseTruncated = Withheld, true
			return finish(nil, ErrWithheld)
		}
		extracted, extractErr := extract(resp, responseBody)
		clear(responseBody)
		r.ResponseComplete, r.Reason = true, Complete
		if extractErr != nil || !e.authSessionSafe(extracted) {
			return finish(nil, ErrAuthSession)
		}
		session = extracted
		return finish(nil, nil)
	}
	if learned {
		// 학습 로그인 응답도 도구·모델로 가지 않는다. 끝까지 받은 같은 표현의 본문만 판정 함수에 준다(잘림·부분·압축 응답은
		// 판정할 수 없으니 보류: 대상은 답했으므로 결과 불명이 아니다).
		if oversized || resp.StatusCode == http.StatusPartialContent || resp.Header.Get("Content-Range") != "" ||
			resp.Header.Get("Content-Encoding") != "" && !strings.EqualFold(resp.Header.Get("Content-Encoding"), "identity") {
			clear(responseBody)
			r.Reason, r.ResponseTruncated = Withheld, oversized
			return finish(nil, ErrWithheld)
		}
		inspected, inspectErr := extract(resp, responseBody)
		clear(responseBody)
		r.ResponseComplete, r.Reason = true, Complete
		if inspectErr != nil || !e.authSessionSafe(inspected) {
			return finish(nil, ErrAuthSession)
		}
		session = inspected
		return finish(nil, nil)
	}
	if e3 != nil {
		if oversized || resp.StatusCode == http.StatusPartialContent || resp.Header.Get("Content-Range") != "" ||
			resp.Header.Get("Content-Encoding") != "" && !strings.EqualFold(resp.Header.Get("Content-Encoding"), "identity") {
			clear(responseBody)
			r.Reason, r.ResponseTruncated = Withheld, oversized
			return finish(nil, ErrWithheld)
		}
		e3.inspect(resp, responseBody, &r)
		clear(responseBody)
		return finish(nil, nil)
	}
	// 대상이 답했지만(상태 줄·헤더 수신) 본문 상한·자격 응답·보호 값 반사·압축 때문에 표현을 주지 않는다. 대상이 요청을
	// 처리한 것은 알므로 결과 불명이 아니라 보류로 센다. 결과 불명 상한에 넣지 않아 그 call을 멈추지 않는다(C16).
	// 길이를 맞출 수 없는 부분 응답(206·Content-Range)은 자르지 않고 통째로 보류한다.
	partial := resp.StatusCode == http.StatusPartialContent || resp.Header.Get("Content-Range") != ""
	// 읽는 동안 보호 값이 더해졌으면 검사 길이가 새 값을 다 담지 못했을 수 있다(전달 구간 끝에 걸친 새 값). 통째로 보류한다.
	prepared, masked, safe := e.prepareResponse(resp, responseBody, generation, limit, partial, oversized)
	if !safe || oversized && partial {
		clear(responseBody)
		r.Reason = Withheld
		return finish(nil, ErrWithheld)
	}
	if masked {
		clear(responseBody)
		responseBody = prepared
		resp.TransferEncoding = nil
		resp.Header.Del("Transfer-Encoding")
		resp.Header.Del("Trailer")
		resp.Trailer = nil
		for _, name := range []string{"ETag", "Content-MD5", "Digest", "Content-Digest", "Repr-Digest"} {
			resp.Header.Del(name)
		}
		resp.ContentLength = int64(len(responseBody))
		resp.Header.Set("Content-Length", strconv.Itoa(len(responseBody)))
	}
	if oversized {
		// 본문 상한을 넘으면 앞부분만 준다. 도구의 HTTP 읽기가 연결 중단으로 보지 않도록 길이를 맞추고 chunked·trailer를
		// 지운다. 대상이 답한 것은 알고 표현만 줄였으므로 withheld + response_truncated다(결과 불명 아님, C16 결정안 2).
		clear(responseBody[limit:])
		responseBody = responseBody[:limit]
		resp.TransferEncoding = nil
		resp.Header.Del("Transfer-Encoding")
		resp.Header.Del("Trailer")
		resp.Trailer = nil
		resp.ContentLength = int64(len(responseBody))
		resp.Header.Set("Content-Length", strconv.Itoa(len(responseBody)))
		resp.Body = io.NopCloser(bytes.NewReader(responseBody))
		resp.Close = true
		resp.Request = &http.Request{Method: req.Method}
		r.Reason, r.ResponseTruncated = Withheld, true
		return finish(resp, nil)
	}
	r.ResponseComplete = true
	// Preserve the original representation and metadata. Only a minimal,
	// nonsecret method context survives so HEAD retains its length semantics.
	resp.Body = io.NopCloser(bytes.NewReader(responseBody))
	resp.Close = true
	resp.Request = &http.Request{Method: req.Method}
	r.Reason = Complete
	if masked {
		r.Reason, r.ResponseComplete = Withheld, false
	} // 원래 표현은 일부 보류했다. 기존 v2 보류 영수증 모양을 유지하며 결과 불명으로 세지 않는다.
	return finish(resp, nil)
}

// 생성 요청은 읽기 사전 승인으로 생략할 수 없다.
func (e *Execution) needsRequestApproval(purpose, method string, body []byte, view RequestView) bool {
	if e.cfg.RequestApproval == nil {
		return false
	}
	// 확정 단계 학습 로그인은 Worker 승인 규칙(쓰기 승인 범위)으로 나가므로 그 범위의 건별 승인도 같이 따른다(모델 로그인
	// 계약 §2-2·§6-2): 로그인 POST는 E③ 생성처럼 늘 승인을 받고, 로그인 화면·대조 GET은 모델 GET과 같은 미리 승인한 읽기만
	// 승인 없이 나간다(Codex 리뷰). 인증 준비(로그인 설정 9칸)는 지금처럼 승인 대기가 없다.
	if purpose == PurposeE3Create || purpose == PurposeLearnedLogin {
		return true
	}
	if purpose != "" && purpose != PurposeLoginPrep {
		return false
	}
	return !(e.cfg.ApprovalMode == ApprovalWorkbenchV1 && (method == "GET" || method == "HEAD") && len(body) == 0 && e.cfg.RequestBoundary != nil &&
		e.cfg.RequestBoundary.PreapprovedRead(view))
}
func stripHeaders(h http.Header) {
	for key := range h {
		if reservedAttributionHeader(key) {
			delete(h, key)
		}
	}
	for _, v := range h.Values("Connection") {
		for _, part := range strings.Split(v, ",") {
			h.Del(strings.TrimSpace(part))
		}
	}
	for _, k := range []string{"Proxy-Authorization", "Proxy-Authenticate", "Proxy-Connection", "Connection", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade", AttemptHeader} {
		h.Del(k)
	}
}

// RedactOutput은 모델에 가는 글의 가림이다. E③ 표식과 우리 제어 비밀(설정한 보호 값)은 늘 가린다(절대규칙 2). 시험 계정
// 실행은 대상이 발급한 세션·응답을 모델에 주고(세션 값을 가림 목록에 넣지 않는다), model_visible_v1(제품)은 시험 계정
// 비밀번호도 준다(절대규칙 2, 2026-11-26 DLP 폐기). 이전 비밀번호 정책(보류·mask_spans_v1)은 비밀번호를, 시험 계정 실행이
// 아닌 실행은 출력 가림 전용 세션 값을 지금처럼 가린다.
func (e *Execution) RedactOutput(b []byte) []byte {
	return e.redact(b, false)
}

// RedactRecord는 저장 기록(도구 출력 기록·근거·활동·보고서·화면 API·승인 미리보기)에 쓰는 가림이다. 제품 기록은 내부
// 담당자만 보므로 대상 응답·시험 계정 자격·세션 값을 가리지 않는다(절대규칙 2, 2026-11-26 DLP 폐기). 기록은 뒤에 모델
// 입력(그래프 요약·후보)으로 다시 들어갈 수 있으므로 E③ 표식과 제어 비밀은 늘 가린다.
func (e *Execution) RedactRecord(b []byte) []byte {
	return e.redact(b, true)
}

func (e *Execution) redact(b []byte, record bool) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := bytes.Clone(b)
	if len(e.marker) != 0 {
		out = redactMarker(out, e.marker)
	}
	values := append([][]byte(nil), e.secrets...)
	if !record {
		// 이전 비밀번호 정책(보류·mask_spans_v1)은 헤더 모드와 상관없이 모델 출력에서 비밀번호를 가린다. model_visible_v1은
		// 비밀번호를 등록하지 않는다(AddProtectedPasswords).
		if e.cfg.PasswordResponse != PasswordResponseModelVisible {
			values = append(values, e.passwords...)
		}
		// 출력 가림 전용 세션 값은 시험 계정 실행이 아닌 실행에만 있다(AddRedactValues가 시험 계정 실행에서는 넣지 않는다).
		values = append(values, e.redactOnly...)
	}
	// 긴 값부터 가려 한 값이 다른 값의 일부여도 남지 않게 한다. JSON 문자열 안에 이스케이프된 값도 가린다.
	sort.SliceStable(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	out = redactEscapedJSONValues(out, values)
	for _, v := range values {
		if len(v) > 0 {
			out = bytes.ReplaceAll(out, v, []byte("[REDACTED]"))
		}
	}
	return out
}

// 출력 가림 전용 값의 하한·상한이다. 짧은 값은 흔한 문자열까지 가려 출력이 망가지므로 받지 않는다.
const (
	minRedactValueBytes = 8
	maxRedactValueBytes = 8 << 10
	maxRedactValues     = 64
)

// 응답 보류(반사 검사)와 출력 가림에 함께 쓰는 보호 값의 하한이다. 짧은 값은 흔한 문자열과 겹쳐 응답을 과하게 보류한다.
const minProtectedValueBytes = 6

// AddProtectedValues는 실행을 만든 뒤에 알게 된 보호 값(인증 준비에 쓴 비밀번호)을 더한다(C14, S09). 이 값이 대상 응답에
// 그대로 있으면 그 응답은 통째로 보류하고(withheld), 도구 출력에서도 가린다. 너무 짧거나 길거나 많거나, 검사 상한을
// 설정으로 고정한 실행에서 상한 사이 조건(검사 상한 ≥ 응답 상한 + 가장 긴 보호 값 − 1)을 깨면 하나도 더하지 않고 ErrConfig다.
func (e *Execution) AddProtectedValues(values ...[]byte) error {
	if e == nil {
		return ErrConfig
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(values) == 0 || len(values) > maxRedactValues {
		return ErrConfig
	}
	next := append([][]byte(nil), e.secrets...)
	for _, v := range values {
		if len(v) < minProtectedValueBytes || len(v) > maxRedactValueBytes {
			return ErrConfig
		}
		next = append(next, bytes.Clone(v))
	}
	if !validResponseLimits(e.cfg.Limits, append(append([][]byte(nil), next...), e.passwords...)) {
		return ErrConfig
	}
	e.secrets = next
	e.secretsGen++
	return nil
}

// DisableTestAccountPassthrough는 시험 계정 모드(test_account_passthrough)로 만든 실행을 preserve처럼 묶는다. 인증 준비에서
// 인증된 계정이 하나도 없으면 서버가 첫 모델 요청 전에 부른다(C14 결정 5·6: 계정 없이 진행하는 실행은 preserve). 그 뒤로는
// 모델이 쓴 Authorization·Cookie를 거부하고 대상의 자격 응답(Set-Cookie 등)을 보류한다. 되돌리는 방법은 없다. 정책 지문과
// 실행 보기의 header_mode는 설정값 그대로이고, 실제로 묶였는지는 계정별 인증 결과(account_auth)로 드러난다.
func (e *Execution) DisableTestAccountPassthrough() {
	if e != nil {
		e.accountPassthroughOff.Store(true)
	}
}

// testAccountPassthrough는 이 실행이 지금 시험 계정 모드로 동작하는지다.
func (e *Execution) testAccountPassthrough() bool {
	return e.cfg.HeaderMode == HeaderTestAccountPassthrough && !e.accountPassthroughOff.Load()
}

// AddRedactValues는 출력 가림 전용 값을 더한다(인증 준비 세션 값). 이 값들은 도구 출력·근거·보고서에서 가리지만 응답
// 표현 보류에는 쓰지 않는다. 시험 계정 세션의 정규 JWT는 payload·signature도 함께 등록한다(머리·짧은 조각 제외).
// 너무 짧거나 길거나, 조각까지 포함해 많으면 하나도 더하지 않고 ErrConfig다.
func (e *Execution) AddRedactValues(values ...[]byte) error {
	if e == nil {
		return ErrConfig
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cfg.HeaderMode == HeaderTestAccountPassthrough {
		// 시험 계정 실행은 세션을 모델에 주고 저장 기록에서도 가리지 않으므로(절대규칙 2, 2026-11-26 DLP 폐기) 가림 목록에
		// 넣지 않는다. 짧은 세션 값도 받고, 세션 가림 칸 상한(#840)에도 닿지 않는다.
		return nil
	}
	if len(values) > maxRedactValues {
		return ErrConfig
	}
	for _, v := range values {
		if len(v) < minRedactValueBytes || len(v) > maxRedactValueBytes {
			return ErrConfig
		}
	}
	if len(e.redactOnly)+len(values) > maxRedactValues {
		return ErrConfig
	}
	for _, v := range values {
		e.redactOnly = append(e.redactOnly, bytes.Clone(v))
	}
	return nil
}

func credentialValues(rules []compiledRule) [][]byte {
	seen := map[string]bool{}
	add := func(v string) {
		if v != "" {
			seen[v] = true
		}
	}
	for _, r := range rules {
		for k, vs := range r.CredentialHeaders {
			for _, v := range vs {
				add(v)
				switch strings.ToLower(k) {
				case "authorization":
					fields := strings.Fields(v)
					if len(fields) >= 2 {
						add(strings.Join(fields[1:], " "))
						if strings.EqualFold(fields[0], "Basic") {
							if decoded, err := base64.StdEncoding.DecodeString(fields[1]); err == nil {
								add(string(decoded))
								parts := strings.SplitN(string(decoded), ":", 2)
								for _, p := range parts {
									add(p)
								}
							}
						}
					}
				case "cookie":
					// Include the standard effective value (notably quoted cookies)
					// without changing the protected field sent to the target.
					request := &http.Request{Header: http.Header{"Cookie": {v}}}
					for _, cookie := range request.Cookies() {
						add(cookie.Value)
					}
					for _, p := range strings.Split(v, ";") {
						pair := strings.SplitN(strings.TrimSpace(p), "=", 2)
						if len(pair) == 2 {
							add(pair[1])
						}
					}
				}
			}
		}
	}
	out := make([][]byte, 0, len(seen))
	for v := range seen {
		out = append(out, []byte(v))
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}
func (*Execution) String() string   { return "e2proxy.Execution" }
func (*Execution) GoString() string { return "e2proxy.Execution" }
func (*Call) String() string        { return "e2proxy.Call" }
func (*Call) GoString() string      { return "e2proxy.Call" }

// CheckCONNECTAuthority is a preliminary policy check, never send authority.
// The decrypted request still passes Do with its own proxy-issued attempt ID.
func (c *Call) CheckCONNECTAuthority(authority string) bool {
	if c == nil || c.e == nil {
		return false
	}
	u, err := url.Parse("https://" + authority)
	if err != nil {
		return false
	}
	a, err := canonicalAuthority(u)
	if err != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery {
		return false
	}
	e := c.e
	e.mu.Lock()
	defer e.mu.Unlock()
	if c.blockedLocked() != Complete {
		return false
	}
	for _, r := range e.rules {
		if !ProgramCallKind(r.Purpose).isE3() && r.origin.Scheme == "https" && r.authority == a {
			return true
		}
	}
	return false
}

// The bridge and proxy share a trusted address space. This private capability
// binds ingress to a call without serializing a token into the tool process.
// Kernel peer/endpoint checks remain required and the token grants no policy.
func (c *Call) newIngressCapability() ([32]byte, error) {
	if c == nil || c.e == nil {
		return [32]byte{}, ErrConfig
	}
	c.e.mu.Lock()
	defer c.e.mu.Unlock()
	if c.blockedLocked() != Complete {
		return [32]byte{}, ErrClosed
	}
	return c.ingressToken, nil
}
func (c *Call) validIngressCapability(token [32]byte) bool {
	if c == nil || c.e == nil {
		return false
	}
	c.e.mu.Lock()
	defer c.e.mu.Unlock()
	return c.blockedLocked() == Complete && subtle.ConstantTimeCompare(c.ingressToken[:], token[:]) == 1
}

func reservedAttributionHeader(key string) bool {
	lower := strings.ToLower(key)
	return strings.EqualFold(key, AttemptHeader) || strings.HasPrefix(lower, "x-e2-") || strings.HasPrefix(lower, "x-abc2lab-e2-")
}

// Host is compared canonically for policy, then retained byte-for-byte for the
// HTTP request. A virtual host or signature must not see a rewritten authority.
func preservedRequestHost(req *http.Request) (string, bool) {
	if req == nil || req.URL == nil {
		return "", false
	}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	u, err := url.Parse(req.URL.Scheme + "://" + host)
	if err != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery {
		return "", false
	}
	want, wantErr := canonicalAuthority(req.URL)
	got, gotErr := canonicalAuthority(u)
	return host, wantErr == nil && gotErr == nil && got == want
}

// Compression is outside the initial bounded credential-redaction contract.
// Preserve explicit identity; reject other negotiation before any reservation
// or send. An unsolicited encoded response is withheld as a sent/unknown result.
func identityEncoding(headers http.Header, field string) bool {
	values := 0
	for name, entries := range headers {
		if !strings.EqualFold(name, field) {
			continue
		}
		for _, value := range entries {
			values++
			if !strings.EqualFold(strings.TrimSpace(value), "identity") {
				return false
			}
		}
	}
	return values <= 1
}

// ValidatePolicyTemplate은 정적 규칙·한도·헤더 모드만 검사한다.
// 실제 task/intent/예산/권위의 수용 또는 실행 생성은 하지 않으며 NewExecution 검사를 대체하지 않는다.
func ValidatePolicyTemplate(cfg Config) error {
	_, _, _, err := compilePolicyTemplate(cfg)
	return err
}

func compilePolicyTemplate(cfg Config) ([]compiledRule, HeaderMode, *EvaluationCallerContext, error) {
	l := cfg.Limits
	if l.MaxAttempts == 0 || l.MaxAttempts > maxExecutionAttempts || l.MaxCalls <= 0 || l.MaxCalls > maxExecutionCalls || l.MaxConcurrent <= 0 || l.MaxConcurrent > maxConcurrentRequests ||
		l.MaxBodyBytes <= 0 || l.MaxBodyBytes > maxRequestBodyBytes || l.RequestTimeout <= 0 || l.RequestTimeout > maxRequestTimeout ||
		l.CallTimeout <= 0 || l.CallTimeout > maxCallTimeout || l.MaxDenials == 0 || l.MaxTransient == 0 || l.MaxUnknown == 0 {
		return nil, "", nil, ErrConfig
	}
	rules, err := compileRules(cfg.Rules)
	if err != nil {
		return nil, "", nil, err
	}
	if len(cfg.ExcludedPaths) > pathscope.MaxExcluded {
		return nil, "", nil, ErrConfig
	}
	for _, p := range cfg.ExcludedPaths {
		if !pathscope.ValidExcluded(p) {
			return nil, "", nil, ErrConfig
		}
	}
	for _, rule := range rules {
		if !ProgramCallKind(rule.Purpose).isE3() {
			continue
		}
		p := rule.PathPrefix
		if rule.Purpose == PurposeE3Read {
			// '?'는 제외 패턴의 리터럴에 쓸 수 없다. ID 자리를 덮는 '*'나 ID 앞에서 끝나는 패턴만 일치한다.
			p = strings.Replace(p, "{id}", "?", 1)
		}
		if !e3InWorkerScope(rule, rules) || pathscope.ExcludedAny(p, cfg.ExcludedPaths) {
			return nil, "", nil, ErrConfig
		}
	}
	if !validResponseLimits(l, credentialValues(rules)) {
		return nil, "", nil, ErrConfig
	}
	if cfg.HeaderMode == "" {
		cfg.HeaderMode = HeaderPreserve
	}
	if cfg.HeaderMode != HeaderPreserve && cfg.HeaderMode != HeaderProtectedSessionProposal && cfg.HeaderMode != HeaderEvaluationCallerPassthrough && cfg.HeaderMode != HeaderTestAccountPassthrough {
		return nil, "", nil, ErrConfig
	}
	if cfg.PasswordResponse != "" {
		login := false
		for _, rule := range rules {
			login = login || rule.Purpose == PurposeAuthPrep
		}
		if cfg.PasswordResponse != PasswordResponseMaskSpans && cfg.PasswordResponse != PasswordResponseModelVisible || cfg.HeaderMode != HeaderTestAccountPassthrough || !login {
			return nil, "", nil, ErrConfig
		}
	}

	cfg.Evaluation, err = validateEvaluationCallerContext(cfg)
	if err != nil {
		return nil, "", nil, err
	}
	for _, rule := range rules {
		if cfg.HeaderMode != HeaderProtectedSessionProposal && len(rule.CredentialHeaders) != 0 {
			return nil, "", nil, ErrConfig
		}
	}
	return rules, cfg.HeaderMode, cfg.Evaluation, nil
}

// longestSecret은 이 실행에 등록된 보호 값(명시 비밀 헤더·보호 자격) 중 가장 긴 바이트 길이다. 응답 검사 상한에 더한다.
func (e *Execution) longestSecret() int64 {
	longest, _ := e.protectedState()
	return longest
}

// protectedState는 가장 긴 보호 값 길이와 지금 보호 값 목록의 세대다. 응답을 읽기 전에 함께 잡아, 읽는 동안 목록이 바뀌었는지
// 검사 때 비교한다.
func (e *Execution) protectedState() (int64, uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	longest := 0
	for _, set := range [][][]byte{e.secrets, e.passwords} {
		for _, s := range set {
			longest = max(longest, len(s))
		}
	}
	return int64(longest), e.secretsGen
}

func (e *Execution) protectedGeneration() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.secretsGen
}

// responseLimits는 응답 전달 상한과 검사 상한이다. 설정이 없으면 요청 상한과 계산값을 쓴다(C16 결정안 3 전과 같음).
func responseLimits(l Limits, longestSecret int64) (body, scan int64) {
	body = l.MaxBodyBytes
	if l.MaxResponseBodyBytes > 0 {
		body = l.MaxResponseBodyBytes
	}
	scan = body + longestSecret
	if l.MaxResponseScanBytes > 0 {
		scan = l.MaxResponseScanBytes
	}
	// 전달 상한보다 하나 더 읽어야 상한을 넘는 응답을 알아챈다. 검사 상한이 그보다 작으면 넘는 응답을 완전 응답으로
	// 잘못 기록하므로 적재 검사(validResponseLimits)와 별개로 여기서도 전달 상한 아래로 내려가지 않게 한다.
	scan = max(scan, body)
	return body, scan
}

// validResponseLimits는 응답 상한·검사 상한의 범위와 상한 사이 조건이다. 어기면 정책 적재·실행 생성을 거부한다.
func validResponseLimits(l Limits, secrets [][]byte) bool {
	if l.MaxResponseBodyBytes < 0 || l.MaxResponseBodyBytes > maxResponseBodyBytes || l.MaxResponseScanBytes < 0 || l.MaxResponseScanBytes > maxResponseScanBytes {
		return false
	}
	if l.MaxResponseScanBytes == 0 {
		return true
	}
	longest := 0
	for _, s := range secrets {
		longest = max(longest, len(s))
	}
	body, _ := responseLimits(l, 0)
	// 상한을 넘는 응답을 알아채려면 전달 상한 이상을 읽어야 하고(보호 값이 없어도), 전달 구간 끝에 걸친 보호 값까지
	// 보려면 그보다 가장 긴 보호 값 길이 − 1만큼 더 읽어야 한다.
	return l.MaxResponseScanBytes >= body+max(int64(longest)-1, 0)
}

// requestCancelCause는 요청 문맥이 끝난 원인이다. 프록시 요청 시계(requestClock)는 원인 값을 따로 두지 않으므로 기한이면
// Err()의 DeadlineExceeded를 먼저 본다.
func requestCancelCause(ctx context.Context) error {
	if err := ctx.Err(); errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return ctx.Err()
}
