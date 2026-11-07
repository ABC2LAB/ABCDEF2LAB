package e2proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
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
	sending        int
	transient      uint64
	secrets        [][]byte
	secretHeaders  map[string]struct{}
}
type Call struct {
	ingressToken  [32]byte
	e             *Execution
	ctx           context.Context
	cancel        context.CancelFunc
	ref           string
	commandRef    string
	intentID      int64
	expires       time.Time
	closing       bool
	finalized     bool
	active        int
	idle          chan struct{}
	counts        Counts
	auditComplete bool
	reason        Reason
	closeGate     chan struct{}
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
	return &Execution{ctx: ctx, cancel: cancel, cfg: cfg, rules: rules, calls: make(map[string]*Call), secrets: credentialValues(rules), secretHeaders: credentialHeaderNames(rules), issued: make(map[string]bool), intents: map[int64]struct{}{cfg.IntentID: struct{}{}}, intentGate: make(chan struct{}, 1)}, nil
}
func compileRules(rules []Rule) ([]compiledRule, error) {
	if len(rules) == 0 || len(rules) > maxExecutionRules {
		return nil, ErrConfig
	}
	out := make([]compiledRule, 0, len(rules))
	seen := map[string]bool{}
	for _, r := range rules {
		if !validRef(r.Ref) || !validRef(r.PathScopeRef) || seen[r.Ref] || !r.ConnectIP.IsValid() || r.ConnectIP.IsUnspecified() || r.ConnectIP.IsMulticast() || r.ConnectIP.Zone() != "" {
			return nil, ErrConfig
		}
		seen[r.Ref] = true
		u, err := url.Parse(r.Origin)
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
func (e *Execution) record(ctx context.Context, ev Event) error {
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
	return CallResult{IntentID: c.intentID, CallRef: c.ref, CommandRef: c.commandRef, State: state, Counts: c.counts, AuditComplete: c.auditComplete && !c.e.auditFailed, Final: c.finalized, Reason: c.reason}
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
	return ExecutionView{ApprovalMode: e.cfg.ApprovalMode, Evaluation: cloneEvaluationCallerContext(e.cfg.Evaluation), HeaderMode: e.cfg.HeaderMode, Version: Version, Scope: e.cfg.Scope, IntentID: id, IntentIDs: ids, OwnerRef: e.cfg.OwnerRef, PolicyRevision: e.cfg.PolicyRevision,
		State: state, ExpiresAt: e.cfg.ExpiresAt, Counts: e.counts, Reason: reason, AuditComplete: !e.auditFailed && e.cfg.Recorder != nil}
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
	if e.next >= e.cfg.Limits.MaxAttempts {
		e.stopped = true
		e.stopReason = AttemptLimit
		e.cancel()
		return Receipt{}, ErrClosed
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
	return r, nil
}
func (c *Call) finish(ctx context.Context, r Receipt) (Receipt, error) {
	r.At = time.Now().UTC()
	err := c.e.recordBounded(ctx, Event{Stage: "result", Receipt: r})
	r.AuditComplete = err == nil && r.Reason != AuditFailed
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
	}
	add(&c.counts)
	add(&e.counts)
	c.reason = r.Reason
	if err != nil {
		c.auditComplete = false
		c.reason = AuditFailed
	}
	c.active--
	if c.active == 0 {
		close(c.idle)
	}
	if e.counts.Denied+e.counts.Unsupported >= e.cfg.Limits.MaxDenials {
		e.stopped = true
		e.stopReason = DenialLimit
		e.cancel()
	}
	if e.counts.Unknown >= e.cfg.Limits.MaxUnknown {
		e.stopped = true
		e.stopReason = UnknownLimit
		e.cancel()
	}
	if r.Reason == TransportFailed {
		e.transient++
	}
	if e.transient >= e.cfg.Limits.MaxTransient {
		e.stopped = true
		e.stopReason = TransientLimit
		e.cancel()
	}
	return r, err
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
	return c.finish(ctx, r)
}
func (e *Execution) matchLocked(req *http.Request) (compiledRule, bool) {
	a, err := canonicalAuthority(req.URL)
	if err != nil || req.URL.RawPath != "" || !validPath(req.URL.Path) {
		return compiledRule{}, false
	}
	for _, r := range e.rules {
		if req.URL.Scheme == r.origin.Scheme && a == r.authority && r.methods[req.Method] && (r.PathPrefix == "/" || req.URL.Path == strings.TrimSuffix(r.PathPrefix, "/") || strings.HasPrefix(req.URL.Path, strings.TrimSuffix(r.PathPrefix, "/")+"/")) {
			return r, true
		}
	}
	return compiledRule{}, false
}

type observed struct{ forward, written atomic.Bool }
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

func (g *guardedConn) Write(p []byte) (int, error) {
	e := g.c.e
	e.mu.Lock()
	defer e.mu.Unlock()
	if g.c.blockedLocked() != Complete || e.cfg.PolicyRevision != g.revision {
		return 0, ErrDenied
	}
	g.ob.forward.Store(true)
	return g.Conn.Write(p)
}
func (c *Call) Do(ctx context.Context, req *http.Request) (*http.Response, Receipt, error) {
	if ctx == nil {
		return nil, Receipt{}, ErrConfig
	}
	method := ""
	if req != nil {
		// 대기 중 호출자가 URL·헤더를 바꿔 승인 내용과 송신을 분리하지 못한다.
		req = req.Clone(ctx)
		method = req.Method
	}
	r, err := c.start(method)
	if err != nil {
		return nil, r, err
	}
	finish := func(resp *http.Response, cause error) (*http.Response, Receipt, error) {
		rr, aerr := c.finish(ctx, r)
		if aerr != nil {
			return nil, rr, aerr
		}
		return resp, rr, cause
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
	if !hostOK || !identityEncoding(req.Header, "Accept-Encoding") || !e.callerCredentialsAllowed(req.Header) {
		r.Decision = "unsupported"
		r.Reason = Unsupported
		return finish(nil, ErrDenied)
	}
	e.mu.Lock()
	reason := c.blockedLocked()
	rule, ok := e.matchLocked(req)
	revision := e.cfg.PolicyRevision
	if reason == Complete && !ok {
		reason = PolicyDenied
	}
	if reason == Complete && e.sending >= e.cfg.Limits.MaxConcurrent {
		reason = PolicyDenied
	}
	if reason == Complete {
		e.sending++
	}
	e.mu.Unlock()
	if reason != Complete {
		r.Decision = "denied"
		r.Reason = reason
		return finish(nil, ErrDenied)
	}
	defer func() { e.mu.Lock(); e.sending--; e.mu.Unlock() }()
	requestHeaders, headersOK := forwardRequestHeaders(req.Header, rule)
	if !headersOK {
		r.Decision, r.Reason = "unsupported", Unsupported
		return finish(nil, ErrDenied)
	}
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
	if e.cfg.RequestApproval != nil {
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
		if approvalErr != nil || !allowed || canceled {
			r.Decision, r.Reason = "denied", PolicyDenied
			if canceled {
				r.Reason = Closed
			}
			return finish(nil, ErrDenied)
		}
	}
	r.Decision = "allowed"
	e.mu.Lock()
	reason = c.blockedLocked()
	if reason == Complete && e.cfg.PolicyRevision != revision {
		reason = PolicyDenied
	}
	if reason == Complete {
		if err = e.cfg.Budget.Ledger.Reserve(e.cfg.Budget.Key, e.cfg.Budget.Binding, budget.Attempt{RunRef: e.cfg.Scope.RunRef, Sequence: r.Sequence}, time.Now()); err != nil {
			reason = BudgetExhausted
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
	stop := context.AfterFunc(ctx, cancel)
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
	out = out.WithContext(httptrace.WithClientTrace(workCtx, &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if info.Err == nil {
			ob.written.Store(true)
		}
	}}))
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true, Protocols: protocols,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: e.cfg.RootCAs},
		TLSHandshakeTimeout: e.cfg.Limits.RequestTimeout, ResponseHeaderTimeout: e.cfg.Limits.RequestTimeout}
	_, port, _ := net.SplitHostPort(rule.authority)
	transport.DialContext = func(dctx context.Context, network, address string) (net.Conn, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if c.blockedLocked() != Complete || e.cfg.PolicyRevision != revision || network != "tcp" || address != rule.authority {
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
	resp, sendErr := client.Do(out)
	r.ForwardAttempted = ob.forward.Load()
	r.RequestWritten = ob.written.Load()
	r.NotSent = !r.ForwardAttempted
	if sendErr != nil {
		r.Reason = TransportFailed
		if r.ForwardAttempted {
			r.Reason = Unknown
		}
		return finish(nil, ErrDenied)
	}
	r.ResponseReceived = true
	r.StatusCode = resp.StatusCode
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, e.cfg.Limits.MaxBodyBytes+1))
	closeErr := resp.Body.Close()
	r.ResponseComplete = readErr == nil && closeErr == nil && int64(len(responseBody)) <= e.cfg.Limits.MaxBodyBytes
	if !r.ResponseComplete || !e.safeHTTPResponse(resp, responseBody) {
		clear(responseBody)
		r.Reason = Unknown
		return finish(nil, ErrDenied)
	}
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
	for _, v := range e.secrets {
		if len(v) > 0 {
			out = bytes.ReplaceAll(out, v, []byte("[REDACTED]"))
		}
	}
	return out
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
	if cfg.HeaderMode == "" {
		cfg.HeaderMode = HeaderPreserve
	}
	if cfg.HeaderMode != HeaderPreserve && cfg.HeaderMode != HeaderProtectedSessionProposal && cfg.HeaderMode != HeaderEvaluationCallerPassthrough {
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
