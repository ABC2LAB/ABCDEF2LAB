package transmit

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Autumn-27/artex/idor"
)

// HTTPTransmitter owns an immutable execution snapshot. Create a fresh instance
// for each case/reset/policy generation; approvals cannot be widened in place.
type HTTPTransmitter struct {
	ctx       context.Context
	cancel    context.CancelFunc
	scope     Scope
	expires   time.Time
	sessions  map[string]Session
	resources map[string]binding
	recorder  Recorder
	client    *http.Client
	timeout   time.Duration
	bodyLimit int64
	interval  time.Duration
	budget    uint64
	serial    chan struct{}
	attempt   atomic.Uint64
	inflight  sync.WaitGroup

	mu          sync.Mutex
	stopped     bool
	closing     bool
	auditFailed bool
	used        uint64
	lastStart   time.Time
}

var _ idor.Transmitter = (*HTTPTransmitter)(nil)

// NewHTTP validates trusted configuration without sending any network traffic.
// A Recorder is required; its storage/permissions are the server's responsibility.
func NewHTTP(parent context.Context, cfg Config) (*HTTPTransmitter, error) {
	if parent == nil || cfg.Recorder == nil {
		return nil, invalid("context or recorder")
	}
	for _, ref := range []string{cfg.Scope.RunRef, cfg.Scope.TaskRef, cfg.Scope.CaseRef, cfg.Scope.ResetRef, cfg.Scope.PolicyRef} {
		if !refPattern.MatchString(ref) {
			return nil, invalid("scope reference")
		}
	}
	if cfg.ExpiresAt.IsZero() || cfg.MaxRequests == 0 || cfg.RequestTimeout <= 0 || cfg.MinInterval < 0 || cfg.MaxBodyBytes <= 0 || cfg.MaxBodyBytes == math.MaxInt64 {
		return nil, invalid("limits or approval expiry")
	}
	ip := cfg.ConnectIP.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip.Zone() != "" {
		return nil, invalid("connection IP")
	}
	origin, err := url.Parse(cfg.Origin)
	if err != nil {
		return nil, invalid("origin")
	}
	addr, err := authority(origin)
	if err != nil || origin.RawQuery != "" || origin.ForceQuery || origin.Path != "" && origin.Path != "/" {
		return nil, invalid("origin")
	}
	sessions, err := copySessions(cfg.Sessions)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 || len(cfg.Resources) == 0 {
		return nil, invalid("empty registry")
	}
	resources := make(map[string]binding, len(cfg.Resources))
	for _, resource := range cfg.Resources {
		if !refPattern.MatchString(resource.Ref) || !refPattern.MatchString(resource.ApprovalRef) || !resource.ReadOnly || len(resource.Accounts) == 0 {
			return nil, invalid("resource registration")
		}
		if _, exists := resources[resource.Ref]; exists {
			return nil, invalid("duplicate resource")
		}
		u, err := url.Parse(resource.URL)
		if err != nil {
			return nil, invalid("resource URL")
		}
		resourceAddr, err := authority(u)
		if err != nil || u.Scheme != origin.Scheme || resourceAddr != addr {
			return nil, invalid("resource origin")
		}
		// Store the canonical authority used by both the Host header and dial gate.
		u.Host = addr
		if _, err := http.NewRequest(http.MethodGet, u.String(), nil); err != nil {
			return nil, invalid("resource URL")
		}
		accounts := make(map[string]bool, len(resource.Accounts))
		for _, account := range resource.Accounts {
			if _, exists := sessions[account]; !exists {
				return nil, invalid("approved account")
			}
			accounts[account] = true
		}
		rule, err := copyRule(resource.View)
		if err != nil {
			return nil, err
		}
		resource.View = rule
		resource.Accounts = append([]string(nil), resource.Accounts...)
		resources[resource.Ref] = binding{resource, u, accounts}
	}
	ctx, cancel := context.WithCancel(parent)
	t := &HTTPTransmitter{
		ctx: ctx, cancel: cancel, scope: cfg.Scope, expires: cfg.ExpiresAt,
		sessions: sessions, resources: resources, recorder: cfg.Recorder,
		timeout: cfg.RequestTimeout, bodyLimit: cfg.MaxBodyBytes, interval: cfg.MinInterval,
		budget: cfg.MaxRequests, serial: make(chan struct{}, 1),
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	transport := &http.Transport{
		// Fresh HTTP/1 connections also prevent net/http's reused-connection retry.
		Proxy: nil, DisableKeepAlives: true, DisableCompression: true, Protocols: protocols,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: cfg.RequestTimeout, ResponseHeaderTimeout: cfg.RequestTimeout,
	}
	if cfg.RootCAs != nil {
		transport.TLSClientConfig.RootCAs = cfg.RootCAs.Clone()
	}
	_, port, _ := net.SplitHostPort(addr) // authority already validated addr.
	connectAddr := net.JoinHostPort(ip.String(), port)
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != addr || ctx.Err() != nil {
			return nil, errors.New("transmit: dial refused")
		}
		t.mu.Lock()
		blocked := t.stopped || t.auditFailed || !time.Now().Before(t.expires)
		t.mu.Unlock()
		if blocked {
			return nil, errors.New("transmit: dial refused")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", connectAddr)
	}
	t.client = &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return t, nil
}

// Stop closes admission and cancels queued/in-flight work. Cancellation cannot
// retract bytes already sent by an admitted request. Stop is idempotent.
func (t *HTTPTransmitter) Stop() {
	t.mu.Lock()
	t.stopped = true
	t.mu.Unlock()
	t.cancel()
}

// StopAndWait seals admission, cancels active sends and waits for their result
// records. A caller may close its audit journal only after this returns.
func (t *HTTPTransmitter) StopAndWait() {
	t.mu.Lock()
	t.closing = true
	t.stopped = true
	t.mu.Unlock()
	t.cancel()
	t.inflight.Wait()
}

func (t *HTTPTransmitter) Send(req idor.AccessRequest) (idor.Capture, bool) {
	capture, _, ok := t.SendDetailed(req)
	return capture, ok
}

// SendDetailed preserves execution uncertainty even if transmission or auditing
// fails. ok means a bounded response was captured, never that access succeeded.
func (t *HTTPTransmitter) SendDetailed(proposal idor.AccessRequest) (capture idor.Capture, receipt Receipt, ok bool) {
	ctx, cancel := context.WithTimeout(t.ctx, t.timeout)
	defer cancel()
	receipt = Receipt{Scope: t.scope, Attempt: t.attempt.Add(1), At: time.Now(), Method: "UNSUPPORTED"}
	t.mu.Lock()
	if t.closing {
		t.mu.Unlock()
		receipt.Reason = Stopped
		return
	}
	t.inflight.Add(1)
	t.mu.Unlock()
	defer t.inflight.Done()
	acquired := false
	if proposal.Method == http.MethodGet {
		receipt.Method = http.MethodGet
	}
	b, knownResource := t.resources[proposal.ResourceRef]
	if knownResource {
		receipt.ResourceRef, receipt.ApprovalRef = b.resource.Ref, b.resource.ApprovalRef
	}
	session, knownAccount := t.sessions[proposal.As.Label]
	if knownAccount {
		receipt.AccountRef = session.Account.Label
		receipt.SessionRef = session.Account.Handle
	}
	defer func() {
		if acquired {
			defer func() { <-t.serial }()
		}
		receipt.At = time.Now()
		if !t.record("result", receipt) {
			receipt.Reason, capture, ok = AuditUnavailable, idor.Capture{}, false
		}
	}()
	select {
	case t.serial <- struct{}{}:
		acquired = true
	case <-ctx.Done():
		receipt.Reason = t.contextReason(ctx)
		return
	}
	if reason := t.gate(ctx); reason != "" {
		receipt.Reason = reason
		return
	}
	switch {
	case proposal.Method != http.MethodGet || proposal.StateChanging:
		receipt.Reason = UnsupportedAction
	case !knownResource:
		receipt.Reason = UnknownResource
	case !knownAccount:
		receipt.Reason = UnknownAccount
	case !b.resource.ReadOnly || !b.accounts[session.Account.Label]:
		receipt.Reason = AccountNotApproved
	default:
		receipt.Reason = checkAccount(proposal, session, time.Now())
	}
	if receipt.Reason != "" {
		return
	}
	// Expiry also bounds the connection, TLS handshake and response body read.
	deadline := session.ExpiresAt
	if t.expires.Before(deadline) {
		deadline = t.expires
	}
	ctx, expiryCancel := context.WithDeadline(ctx, deadline)
	defer expiryCancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, b.url.String(), nil)
	if err != nil {
		receipt.Reason = TransportError
		return
	}
	request.Header = session.Headers.Clone()
	if _, exists := request.Header["User-Agent"]; !exists {
		request.Header.Set("User-Agent", "ABC2LAB-E2")
	}
	if !t.waitInterval(ctx) {
		receipt.Reason = t.contextReason(ctx)
		return
	}
	if !t.record("intent", receipt) {
		receipt.Reason = AuditUnavailable
		return
	}
	// This is the admission linearization point shared with Stop. No blocking
	// user callbacks run under mu, and all conditions are rechecked after waits.
	t.mu.Lock()
	receipt.Reason = t.gateLocked(ctx)
	if receipt.Reason == "" {
		receipt.Reason = checkAccount(proposal, session, time.Now())
	}
	if receipt.Reason == "" {
		t.used++
		t.lastStart = time.Now()
		receipt.Admitted = true
		receipt.AdmittedAt = t.lastStart
	}
	t.mu.Unlock()
	if !receipt.Admitted {
		return
	}
	var written atomic.Bool
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if info.Err == nil {
			written.Store(true)
		}
	}}
	request = request.WithContext(httptrace.WithClientTrace(ctx, trace))
	response, err := t.client.Do(request)
	receipt.RequestWritten = written.Load()
	if err != nil {
		receipt.Reason = TransportError
		if ctx.Err() != nil {
			receipt.Reason = t.contextReason(ctx)
		}
		return
	}
	receipt.ResponseReceived, receipt.StatusCode = true, response.StatusCode
	defer func() {
		if err := response.Body.Close(); err != nil && ok {
			receipt.Reason, capture, ok = TransportError, idor.Capture{}, false
		}
	}()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		receipt.Reason = RedirectRefused
		return
	}
	if encoding := response.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		receipt.Reason = UnsupportedResponse
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, t.bodyLimit+1))
	if err != nil {
		receipt.Reason = TransportError
		if ctx.Err() != nil {
			receipt.Reason = t.contextReason(ctx)
		}
		return
	}
	if int64(len(body)) > t.bodyLimit {
		receipt.Reason = ResponseTooLarge
		return
	}
	if reason := t.contextReasonIfDone(ctx); reason != "" {
		receipt.Reason = reason
		return
	}
	view := project(body, response.Header.Get("Content-Type"), b.resource.Ref, b.resource.View, request, proposal)
	observed := time.Now()
	if !observed.Before(session.ExpiresAt) {
		receipt.Reason = ExpiredAccount
		return
	}
	if !observed.Before(t.expires) {
		receipt.Reason = ExpiredApproval
		return
	}
	if reason := t.contextReasonIfDone(ctx); reason != "" {
		receipt.Reason = reason
		return
	}
	// Even a registered normal shape cannot make an HTTP error a normal payload.
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		view.Normal, view.TemplateClassified, view.Body = false, false, ""
	}
	capture = idor.Capture{View: view, ObservedAt: observed}
	receipt.Reason, ok = Completed, true
	return
}

func (t *HTTPTransmitter) record(stage string, receipt Receipt) bool {
	// Final records must survive Stop cancellation, but remain time bounded.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.ctx), t.timeout)
	defer cancel()
	receipt.At = time.Now()
	if err := t.recorder.Record(ctx, Event{Stage: stage, Receipt: receipt}); err != nil {
		t.mu.Lock()
		t.auditFailed = true
		t.mu.Unlock()
		t.cancel()
		return false
	}
	return true
}

func (t *HTTPTransmitter) gate(ctx context.Context) Reason {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.gateLocked(ctx)
}

func (t *HTTPTransmitter) gateLocked(ctx context.Context) Reason {
	switch {
	case t.auditFailed:
		return AuditUnavailable
	case t.stopped:
		return Stopped
	case !time.Now().Before(t.expires):
		return ExpiredApproval
	case ctx.Err() == context.DeadlineExceeded:
		return TimedOut
	case ctx.Err() != nil:
		return Canceled
	case t.used >= t.budget:
		return BudgetExhausted
	}
	return ""
}

func (t *HTTPTransmitter) contextReason(ctx context.Context) Reason {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.auditFailed {
		return AuditUnavailable
	}
	if t.stopped {
		return Stopped
	}
	if !time.Now().Before(t.expires) {
		return ExpiredApproval
	}
	if ctx.Err() == context.DeadlineExceeded {
		return TimedOut
	}
	return Canceled
}

func (t *HTTPTransmitter) contextReasonIfDone(ctx context.Context) Reason {
	if ctx.Err() != nil {
		return t.contextReason(ctx)
	}
	return ""
}

func (t *HTTPTransmitter) waitInterval(ctx context.Context) bool {
	t.mu.Lock()
	wait := time.Until(t.lastStart.Add(t.interval))
	t.mu.Unlock()
	if wait <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}
