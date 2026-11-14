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
	issued         map[string]bool
	intents        map[int64]struct{}
	intentGate     chan struct{}
	mu             sync.Mutex
	recordMu       sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	cfg            Config
	rules          []compiledRule
	calls          map[string]*Call
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
	admitted     uint64
	intentCounts map[int64]*Counts
	secrets      [][]byte
	// secretsGen은 보호 값 목록이 실행 뒤에 바뀐 횟수다(AddProtectedValues). 응답을 읽는 동안 바뀌면 그 응답의 검사 길이가
	// 새 값을 담지 못할 수 있어 통째로 보류한다.
	secretsGen uint64
	// redactOnly는 출력에서만 가리는 값이다(인증 준비가 받은 시험 계정 세션, C14 구현 설계 4). 응답 표현 보류(반사 검사)에는
	// 쓰지 않는다.
	redactOnly    [][]byte
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
	ref                  string
	commandRef           string
	intentID             int64
	expires              time.Time
	closing              bool
	finalized            bool
	active               int
	idle                 chan struct{}
	counts               Counts
	auditComplete        bool
	reason               Reason
	// fence는 이 call의 요청 단위 실패가 상한에 닿았을 때의 사유다. 비어 있으면 열려 있다.
	// 실행 전체가 아니라 이 call의 새 요청만 막는다. 이미 송신 중인 요청은 건드리지 않는다(C01).
	fence Reason
	// closedRefusals는 대기 중 취소·기한 지남·승인 대기 취소처럼 보내기 전에 거둬진 요청 수다(사유 closed). 모델의
	// 범위 밖 요청이 아니므로 거부 상한(MaxDenials) 계수에서 뺀다(#487 리뷰 P3). 감사 계수(Denied)는 그대로다.
	closedRefusals uint64
	closeGate      chan struct{}
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
		if !validPath(r.PathPrefix) {
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
		// 인증 준비 규칙은 로그인 경로 하나에 POST만, 보호 자격·헤더 제약 없이 둔다.
		if r.Purpose != "" && (r.Purpose != PurposeAuthPrep || len(m) != 1 || !m["POST"] || r.PathPrefix == "/" || strings.HasSuffix(r.PathPrefix, "/") ||
			len(r.CredentialHeaders) != 0 || len(r.Headers) != 0) {
			return nil, ErrConfig
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
	if parent.Err() != nil {
		return nil, ErrClosed
	}
	ref, err := randomRef("call_")
	if err != nil {
		return nil, err
	}
	c := &Call{e: e, ref: ref, commandRef: commandRef, intentID: id, auditComplete: true, reason: Complete, idle: make(chan struct{}), closeGate: make(chan struct{}, 1)}
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
	if parent.Err() != nil || !registered || e.blockedLocked() != Complete || len(e.calls) >= e.cfg.Limits.MaxCalls {
		return nil, ErrClosed
	}
	expires := time.Now().Add(e.cfg.Limits.CallTimeout)
	if e.cfg.ExpiresAt.Before(expires) {
		expires = e.cfg.ExpiresAt
	}
	c.expires = expires
	c.ctx, c.cancel = context.WithDeadline(e.ctx, expires)
	context.AfterFunc(parent, c.cancel)
	e.calls[ref] = c
	e.callOrder = append(e.callOrder, c)
	e.counts.Calls++
	if counts := e.intentCounts[id]; counts != nil {
		counts.Calls++
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
	if _, registered := c.e.intents[c.intentID]; !registered {
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
func (e *Execution) record(ctx context.Context, ev Event) (resultErr error) {
	defer func() { diagnosticAudit(e.ctx, ev, resultErr) }()
	e.recordMu.Lock()
	defer e.recordMu.Unlock()
	e.mu.Lock()
	if e.auditFailed {
		e.mu.Unlock()
		return ErrAudit
	}
	ev.Index = e.eventIndex + 1
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
		return ErrAudit
	}
	// A failed candidate is not a normal event or a committed ordinal. The
	// failure remains in the receipt/execution latch; recovery is not attempted.
	e.eventIndex = ev.Index
	e.events = append(e.events, ev)
	return nil
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
		r := Receipt{Version: Version, Scope: c.e.cfg.Scope, IntentID: c.intentID, CallRef: c.ref, CommandRef: c.commandRef, PolicyRevision: c.e.cfg.PolicyRevision, Reason: c.reason, NotSent: true}
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
	return CallResult{Observations: cloneHTTPObservations(c.diagnostics), ObservationsTruncated: c.diagnosticsTruncated, IntentID: c.intentID, CallRef: c.ref, CommandRef: c.commandRef, State: state, Counts: c.counts, AuditComplete: c.auditComplete && !c.e.auditFailed, Final: c.finalized, Reason: c.reason}
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
		State: state, ExpiresAt: e.cfg.ExpiresAt, Counts: e.counts, IntentCounts: intentCounts, Reason: reason, AuditComplete: !e.auditFailed && e.cfg.Recorder != nil}
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
func (e *Execution) BudgetRemaining() (uint64, error) {
	if e == nil {
		return 0, ErrConfig
	}
	e.mu.Lock()
	shared := e.cfg.Budget
	e.mu.Unlock()
	if shared.Ledger == nil {
		return 0, ErrConfig
	}
	return shared.Ledger.Remaining(shared.Key, shared.Binding, time.Now())
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
		return Receipt{Version: Version, Scope: e.cfg.Scope, IntentID: c.intentID, CallRef: c.ref, CommandRef: c.commandRef, PolicyRevision: e.cfg.PolicyRevision, Reason: c.fence, NotSent: true}, ErrClosed
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
	r := Receipt{Version: Version, Scope: e.cfg.Scope, IntentID: c.intentID, CallRef: c.ref, CommandRef: c.commandRef, AttemptID: id, PolicyRevision: e.cfg.PolicyRevision, Sequence: e.next, Method: method, NotSent: true, Reason: Complete}
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
		if req.URL.Scheme == r.origin.Scheme && a == r.authority && r.methods[req.Method] && (r.PathPrefix == "/" || p == strings.TrimSuffix(r.PathPrefix, "/") || strings.HasPrefix(p, strings.TrimSuffix(r.PathPrefix, "/")+"/")) {
			return r, true
		}
	}
	return compiledRule{}, false
}

// matchPurposeLocked는 목적 규칙만 본다. 경로는 정확히 같아야 한다(접두·형제 경로 불가).
func (e *Execution) matchPurposeLocked(req *http.Request, purpose string) (compiledRule, bool) {
	a, err := canonicalAuthority(req.URL)
	p, ok := requestPath(req.URL)
	if err != nil || !ok || purpose == "" {
		return compiledRule{}, false
	}
	for _, r := range e.rules {
		if r.Purpose == purpose && req.URL.Scheme == r.origin.Scheme && a == r.authority && r.methods[req.Method] && p == r.PathPrefix && req.URL.RawQuery == "" {
			return r, true
		}
	}
	return compiledRule{}, false
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

func (c *Call) do(ctx context.Context, req *http.Request, purpose string, extract AuthExtractor) (*http.Response, http.Header, Receipt, error) {
	if ctx == nil {
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
	var session http.Header
	finish := func(resp *http.Response, cause error) (*http.Response, http.Header, Receipt, error) {
		var boundaryErr error
		rr, aerr := c.finish(ctx, r, func(rr Receipt) {
			c.observeHTTP(req, resp, rr)
			c.observeAttemptPath(req, rr)
			// 인증 준비는 진단 요청이 아니라 워크플로 요청 경계에 알리지 않는다(진단 쓰기 수와 따로 셈).
			if purpose == "" && c.e.cfg.RequestBoundary != nil && req != nil && req.URL != nil {
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
	if purpose == "" {
		rule, ok = e.matchLocked(req)
	} else {
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
	// 승인 제안·송신이 모두 실제로 보낼 값을 보도록 규칙 제약 검사 뒤, 승인 전에 바꾼다.
	identityAcceptEncoding(requestHeaders)
	// Consume the caller's bounded body before admission; it never reaches audit.
	var body []byte
	if req.Body != nil {
		body, err = io.ReadAll(io.LimitReader(req.Body, e.cfg.Limits.MaxBodyBytes+1))
		if err != nil || int64(len(body)) > e.cfg.Limits.MaxBodyBytes {
			r.Decision = "unsupported"
			r.Reason = Unsupported
			return finish(nil, ErrDenied)
		}
	}
	r.RuleRef = rule.Ref
	r.PolicyRevision = revision
	view := RequestView{Method: method, URL: req.URL.String(), BodyPresent: len(body) > 0, BodyBytes: len(body)}
	// 진단 요청 경계(워크플로 선택·쓰기 승인)와 요청 승인은 모델 요청에만 적용한다. 인증 준비는 대상 등록 때 승인한 로그인
	// 경로 하나로만 나간다.
	boundary := e.cfg.RequestBoundary
	if purpose != "" {
		boundary = nil
	}
	if boundary != nil && boundary.Before(ctx, view) != nil {
		r.Decision, r.Reason = "denied", PolicyDenied
		return finish(nil, ErrDenied)
	}
	needsApproval := e.cfg.RequestApproval != nil && purpose == ""
	if needsApproval && e.cfg.ApprovalMode == ApprovalWorkbenchV1 && (method == "GET" || method == "HEAD") && len(body) == 0 && e.cfg.RequestBoundary.PreapprovedRead(view) {
		needsApproval = false
	}
	if needsApproval {
		approvalCtx, cancelApproval := context.WithCancel(c.ctx)
		stopApproval := context.AfterFunc(ctx, cancelApproval)
		proposal := RequestApprovalProposal{
			Receipt: r, OwnerRef: e.cfg.OwnerRef, AccountRef: e.cfg.AccountRef, SessionRef: e.cfg.SessionRef,
			ExpiresAt: c.expires, Method: method, URL: req.URL.String(), Host: requestHost,
			Headers: requestHeaders.Clone(), Body: bytes.Clone(body),
		}
		// 보호 자격 원문은 승인 소비자에게 전달하지 않는다. 참조만 결속한다.
		allowed, approvalErr := e.cfg.RequestApproval.AwaitRequestApproval(approvalCtx, proposal)
		canceled := approvalCtx.Err() != nil || ctx.Err() != nil
		stopApproval()
		cancelApproval()
		if approvalErr != nil && !(canceled && (errors.Is(approvalErr, context.Canceled) || errors.Is(approvalErr, context.DeadlineExceeded))) {
			r.Decision, r.Reason = "denied", AuditFailed
			return finish(nil, ErrAudit)
		}
		if !allowed || canceled {
			r.Decision, r.Reason = "denied", PolicyDenied
			if canceled {
				r.Reason = Closed
			}
			return finish(nil, ErrDenied)
		}
	}
	if boundary != nil && boundary.Before(ctx, view) != nil {
		r.Decision, r.Reason = "denied", PolicyDenied
		return finish(nil, ErrDenied)
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
	if waited && boundary != nil && boundary.Before(ctx, view) != nil {
		r.Decision, r.Reason = "denied", PolicyDenied
		return finish(nil, ErrDenied)
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
	if reason == Complete {
		if err = e.cfg.Budget.Ledger.Reserve(e.cfg.Budget.Key, e.cfg.Budget.Binding, budget.Attempt{RunRef: e.cfg.Scope.RunRef, Sequence: r.Sequence}, time.Now()); err != nil {
			reason = BudgetExhausted
		} else {
			e.admitted++
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
	err = e.recordBounded(ctx, Event{Stage: "intent", Receipt: r})
	if err != nil {
		r.Decision = "denied"
		r.Reason = AuditFailed
		return finish(nil, ErrAudit)
	}
	if ctx.Err() != nil {
		r.Reason = Closed
		return finish(nil, ErrClosed)
	}
	workCtx, cancel := context.WithTimeout(c.ctx, e.cfg.Limits.RequestTimeout)
	defer cancel()
	// 보낸 뒤에는 호출자 기한이 아니라 E② 요청 시간 상한(workCtx)으로만 끊는다. 대기열에서 호출자 기한을 거의 다 쓴
	// 요청을 기한으로 끊으면 대상이 받은 요청이 결과 불명이 되고 그 intent가 멈춘다(#487 리뷰 P3). 호출자 취소는 그대로
	// 끊는다.
	stop := context.AfterFunc(ctx, func() {
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
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
	if purpose != "" {
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
		if extractErr != nil {
			return finish(nil, ErrAuthSession)
		}
		session = extracted
		return finish(nil, nil)
	}
	// 대상이 답했지만(상태 줄·헤더 수신) 본문 상한·자격 응답·보호 값 반사·압축 때문에 표현을 주지 않는다. 대상이 요청을
	// 처리한 것은 알므로 결과 불명이 아니라 보류로 센다. 결과 불명 상한에 넣지 않아 그 call을 멈추지 않는다(C16).
	// 길이를 맞출 수 없는 부분 응답(206·Content-Range)은 자르지 않고 통째로 보류한다.
	partial := resp.StatusCode == http.StatusPartialContent || resp.Header.Get("Content-Range") != ""
	// 읽는 동안 보호 값이 더해졌으면 검사 길이가 새 값을 다 담지 못했을 수 있다(전달 구간 끝에 걸친 새 값). 통째로 보류한다.
	if e.protectedGeneration() != generation || !e.safeHTTPResponse(resp, responseBody) || oversized && partial {
		clear(responseBody)
		r.Reason = Withheld
		return finish(nil, ErrWithheld)
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
	return finish(resp, nil)
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

// RedactOutput removes host-supplied session credentials from a tool's output.
// It is a bounded exact-value guard; it is not a universal encoding/DLP oracle.
func (e *Execution) RedactOutput(b []byte) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := bytes.Clone(b)
	for _, set := range [][][]byte{e.secrets, e.redactOnly} {
		for _, v := range set {
			if len(v) > 0 {
				out = bytes.ReplaceAll(out, v, []byte("[REDACTED]"))
			}
		}
	}
	if e.cfg.HeaderMode == HeaderTestAccountPassthrough {
		// 시험 계정 실행만 대상의 자격 응답 헤더를 넘긴다. 등록 하한(8바이트)보다 짧아 값 자체로 가릴 수 없는 발급 세션도
		// 기록에 남지 않게 Set-Cookie 줄의 쿠키 값 자리와 Authentication-Info 값 전체를 구조로 가린다. 쿠키 이름·속성은
		// 남긴다(#522 이동찬 리뷰).
		out = issuedCookieLine.ReplaceAll(out, []byte("${1}[REDACTED]"))
		out = authInfoLine.ReplaceAll(out, []byte("${1}[REDACTED]"))
		// 모델은 받은 세션을 바꿔(예: JWT 내용을 고쳐 다시 붙임) 보내기도 해서 정확한 값 가림을 지나간다. 그래서 모델이 보내는
		// 요청 자격 자리 — 명령의 Authorization·Cookie 헤더 값, curl --cookie 값, curl -v가 되찍는 요청 헤더 줄 — 는 값이
		// 무엇이든 구조로 가린다. 대상이 응답으로 준 내용은 그대로 둔다(CLAUDE.md 규칙 2, 2026-11-14 Qwen3.6 점검 회차에서
		// 서명이 맞지 않는 변형 토큰이 활동 기록에 남은 것을 발견).
		out = requestAuthValue.ReplaceAll(out, []byte("${1}[REDACTED]"))
		out = requestCookieValue.ReplaceAll(out, []byte("${1}${2}[REDACTED]"))
		out = curlCookieArg.ReplaceAll(out, []byte("${1}[REDACTED]"))
	}
	return out
}

// issuedCookieLine은 Set-Cookie·Set-Cookie2 헤더 줄의 첫 이름=값에서 값 자리다(curl -i·-v 출력의 앞 "< " 포함).
// authInfoLine은 Authentication-Info·Proxy-Authentication-Info 헤더 값 전체다.
var (
	issuedCookieLine = regexp.MustCompile(`(?im)^([<>* \t]*set-cookie2?[ \t]*:[ \t]*[^=;\r\n]*=)[^;\r\n]*`)
	authInfoLine     = regexp.MustCompile(`(?im)^([<>* \t]*(?:proxy-)?authentication-info[ \t]*:[ \t]*)[^\r\n]*`)
	// requestAuthValue는 Authorization·Proxy-Authorization 헤더 값이다. 줄 머리(curl -v의 "> ")에서든 명령 인수
	// (-H "Authorization: …", JSON으로 이스케이프된 \"…\" 포함) 안에서든, 따옴표·백슬래시·줄바꿈 전까지 가린다.
	requestAuthValue = regexp.MustCompile(`(?i)((?:proxy-)?authorization[ \t]*:[ \t]*)[^"'\\\r\n]+`)
	// requestCookieValue는 요청 Cookie 헤더 값이다. Set-Cookie는 위 issuedCookieLine이 이름·속성을 남기고 따로 가린다.
	requestCookieValue = regexp.MustCompile(`(?i)(^|[^a-z0-9-])(cookie[ \t]*:[ \t]*)[^"'\\\r\n]+`)
	// curlCookieArg는 curl -b·--cookie 인수 값이다.
	curlCookieArg = regexp.MustCompile(`(?i)((?:^|\s)(?:-b|--cookie)[ \t]+\\?["']?)[^"'\\\s]+`)
)

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
	if !validResponseLimits(e.cfg.Limits, next) {
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
// 표현 보류에는 쓰지 않는다. 너무 짧거나 길거나 많으면 하나도 더하지 않고 ErrConfig다.
func (e *Execution) AddRedactValues(values ...[]byte) error {
	if e == nil {
		return ErrConfig
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.redactOnly)+len(values) > maxRedactValues {
		return ErrConfig
	}
	for _, v := range values {
		if len(v) < minRedactValueBytes || len(v) > maxRedactValueBytes {
			return ErrConfig
		}
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
		if r.origin.Scheme == "https" && r.authority == a {
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
	if !validResponseLimits(l, credentialValues(rules)) {
		return nil, "", nil, ErrConfig
	}
	if cfg.HeaderMode == "" {
		cfg.HeaderMode = HeaderPreserve
	}
	if cfg.HeaderMode != HeaderPreserve && cfg.HeaderMode != HeaderProtectedSessionProposal && cfg.HeaderMode != HeaderEvaluationCallerPassthrough && cfg.HeaderMode != HeaderTestAccountPassthrough {
		return nil, "", nil, ErrConfig
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
	for _, s := range e.secrets {
		longest = max(longest, len(s))
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
