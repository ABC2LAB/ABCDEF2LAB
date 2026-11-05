package e2proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
)

type coreRecorder struct {
	mu               sync.Mutex
	events           []Event
	failStage        string
	entered, release chan struct{}
	blockStage       string
}

func (r *coreRecorder) Record(ctx context.Context, e Event) error {
	if e.Stage == r.blockStage && r.entered != nil {
		select {
		case r.entered <- struct{}{}:
		default:
		}
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.Stage == r.failStage {
		return errors.New("synthetic secret must not escape")
	}
	r.events = append(r.events, e)
	return nil
}
func coreConfig(t testing.TB, origin string, rec Recorder, max uint64) Config {
	t.Helper()
	now := time.Now()
	ledger, err := budget.New(budget.Limits{MaxApprovals: 4, MaxRequestsPerApproval: 1000})
	if err != nil {
		t.Fatal(err)
	}
	grant := budget.Grant{Key: budget.Key{TaskRef: "task", BudgetRef: "approval"}, Binding: budget.Binding{PolicyRef: "policy", ResetRef: "reset"}, MaxRequests: max, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour)}
	if err := ledger.Register(grant, now); err != nil {
		t.Fatal(err)
	}
	return Config{Scope: transmit.Scope{TaskRef: "task", RunRef: "run", CaseRef: "case", ResetRef: "reset", PolicyRef: "policy"}, IntentID: 1, OwnerRef: "ARTEX", PolicyRevision: "revision1", AccountRef: "anonymous", SessionRef: "anonymous-session", ExpiresAt: now.Add(time.Hour),
		Rules:  []Rule{{Ref: "rule", Origin: origin, ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/allowed", PathScopeRef: "allowed-prefix", Methods: []string{"GET"}}},
		Budget: transmit.CumulativeBudget{Ledger: ledger, Key: grant.Key, Binding: grant.Binding}, Recorder: rec,
		Limits: Limits{MaxAttempts: 1000, MaxCalls: 100, MaxConcurrent: 100, MaxBodyBytes: 1 << 20, RequestTimeout: time.Second, CallTimeout: time.Minute, MaxDenials: 1000, MaxTransient: 1000, MaxUnknown: 1000}}
}
func coreOpen(t testing.TB, cfg Config) (*Execution, *Call) {
	t.Helper()
	e, err := NewExecution(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.BeginCall(context.Background(), "command-ref")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = c.Close(ctx)
	})
	return e, c
}
func coreRequest(t testing.TB, method, url string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestCoreHTTPPolicyAndBudget(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); io.WriteString(w, "actual target") }))
	defer target.Close()
	e, c := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 2))
	for _, item := range []struct {
		name, method, path string
		allowed            bool
	}{{"allow", "GET", "/allowed/a?x=1", true}, {"method", "POST", "/allowed", false}, {"prefix", "GET", "/allowed-escape", false}, {"dot", "GET", "/allowed/../else", false}, {"encoded", "GET", "/allowed/%2e%2e/else", false}, {"allow-again", "GET", "/allowed", true}, {"budget", "GET", "/allowed", false}} {
		t.Run(item.name, func(t *testing.T) {
			resp, r, err := c.Do(context.Background(), coreRequest(t, item.method, target.URL+item.path))
			if item.allowed {
				if err != nil || resp == nil || !r.ResponseComplete || !r.AuditComplete {
					t.Fatalf("allowed result: %+v %v", r, err)
				}
				b, _ := io.ReadAll(resp.Body)
				if string(b) != "actual target" {
					t.Fatal("body")
				}
			} else {
				if err == nil || resp != nil || !r.NotSent || r.ForwardAttempted {
					t.Fatalf("denied result %+v %v", r, err)
				}
			}
		})
	}
	if received.Load() != 2 {
		t.Fatalf("target received %d", received.Load())
	}
	if e.Snapshot().Counts.Attempts != 7 {
		t.Fatal("attempt accounting")
	}
}
func TestCoreTargetDeniedAndNoProxyFallback(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { received.Add(1) }))
	defer target.Close()
	_, c := coreOpen(t, coreConfig(t, "http://approved.invalid:80", &coreRecorder{}, 2))
	_, r, err := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err == nil || !r.NotSent || received.Load() != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestCoreAuditFailureBeforeSend(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { received.Add(1) }))
	defer target.Close()
	e, c := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{failStage: "intent"}, 2))
	_, r, err := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
	if !errors.Is(err, ErrAudit) || !r.NotSent || r.AuditComplete || received.Load() != 0 || e.Snapshot().Reason != AuditFailed {
		t.Fatalf("%+v %v", r, err)
	}
	if err := e.Stop(context.Background()); !errors.Is(err, ErrAudit) {
		t.Fatalf("audit failure must remain latched: %v", err)
	}
}
func TestCoreRevokeBetweenIntentAndSend(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { received.Add(1) }))
	defer target.Close()
	rec := &coreRecorder{blockStage: "intent", entered: make(chan struct{}, 1), release: make(chan struct{})}
	e, c := coreOpen(t, coreConfig(t, target.URL, rec, 2))
	done := make(chan Receipt, 1)
	go func() { _, r, _ := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed")); done <- r }()
	<-rec.entered
	revoked := make(chan error, 1)
	go func() { revoked <- e.Revoke(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for e.Snapshot().State != "blocked" {
		if time.Now().After(deadline) {
			t.Fatal("revoke latch")
		}
		time.Sleep(time.Millisecond)
	}
	close(rec.release)
	r := <-done
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	if received.Load() != 0 || r.ForwardAttempted || !r.NotSent {
		t.Fatalf("late send %+v received%d", r, received.Load())
	}
}
func TestCoreRestrictWaitsForAudit(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); io.WriteString(w, "ok") }))
	defer target.Close()
	rec := &coreRecorder{blockStage: "control", entered: make(chan struct{}, 1), release: make(chan struct{})}
	cfg := coreConfig(t, target.URL, rec, 3)
	e, c := coreOpen(t, cfg)
	changed := make(chan error, 1)
	go func() {
		changed <- e.Restrict(context.Background(), Restriction{PolicyRevision: "revision2", ExpiresAt: cfg.ExpiresAt.Add(-time.Minute), RuleRefs: []string{"rule"}, Methods: []string{"GET"}})
	}()
	<-rec.entered
	done := make(chan Receipt, 1)
	go func() { _, r, _ := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed")); done <- r }()
	time.Sleep(15 * time.Millisecond)
	if received.Load() != 0 {
		t.Fatal("send before control audit ACK")
	}
	close(rec.release)
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.Decision != "denied" {
		t.Fatalf("control pending %+v", r)
	}
	_, r, err := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err != nil || !r.ResponseComplete || received.Load() != 1 {
		t.Fatalf("reduced policy %+v %v", r, err)
	}
}
func TestCoreStopAuditFailureIsNeverSuccessfulRetry(t *testing.T) {
	e, _ := coreOpen(t, coreConfig(t, "http://127.0.0.1", &coreRecorder{failStage: "control"}, 1))
	for i := 0; i < 2; i++ {
		if err := e.Stop(context.Background()); !errors.Is(err, ErrAudit) {
			t.Fatalf("stop %d: %v", i, err)
		}
	}
}
func TestCoreAllowedProxyMetadataRemovalKeepsSafeResponse(t *testing.T) {
	var got http.Header
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("X-Public", "untouched")
		io.WriteString(w, "public response")
	}))
	defer target.Close()
	_, call := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 2))
	req := coreRequest(t, "GET", target.URL+"/allowed")
	req.Header.Set("Proxy-Authorization", "caller-proxy-data")
	req.Header.Set(AttemptHeader, "caller-forged")
	response, receipt, err := call.Do(t.Context(), req)
	if err != nil || response == nil {
		t.Fatal("positive public response failed")
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if got.Get("Proxy-Authorization") != "" || got.Get(AttemptHeader) != receipt.AttemptID || !safeAttempt.MatchString(receipt.AttemptID) || string(body) != "public response" || response.Header.Get("X-Public") != "untouched" {
		t.Fatal("proxy metadata or public response was altered")
	}
}

func TestCorePartialResponseIsUnknown(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, ok := w.(http.Hijacker)
		if !ok {
			panic("hijack")
		}
		conn, _, err := h.Hijack()
		if err != nil {
			panic(err)
		}
		defer conn.Close()
		io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\npartial")
	}))
	defer target.Close()
	e, c := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 2))
	_, r, err := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err == nil || r.Reason != Unknown || r.NotSent || !r.ForwardAttempted || !r.ResponseReceived || r.ResponseComplete {
		t.Fatalf("%+v %v", r, err)
	}
	counts := e.Snapshot().Counts
	if counts.Allowed != 1 || counts.Unknown != 1 || counts.Attempts != 1 {
		t.Fatalf("overlapping axes %+v", counts)
	}
	if e.Snapshot().State != "active" {
		t.Fatal("unsigned transient accounting underflow")
	}
}
func TestCorePostSendTimeoutIsUnknown(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); <-r.Context().Done() }))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
	cfg.Limits.RequestTimeout = 40 * time.Millisecond
	_, c := coreOpen(t, cfg)
	_, r, err := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err == nil || r.Reason != Unknown || r.NotSent || received.Load() != 1 {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestCoreCumulativeBudgetSurvivesNewRunAndCall(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 1)
	_, c := coreOpen(t, cfg)
	if _, _, err := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed")); err != nil {
		t.Fatal(err)
	}
	cfg.Scope.RunRef = "run2"
	_, c2 := coreOpen(t, cfg)
	_, r, err := c2.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err == nil || r.Reason != BudgetExhausted || received.Load() != 1 {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestCoreConcurrentReservationsNeverExceedGrant(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	_, c := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 5))
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _, _ := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
			if resp != nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
	if received.Load() != 5 {
		t.Fatalf("target received %d; want exactly 5", received.Load())
	}
}
func TestCoreClosedCallAndAttemptLimitAreFinite(t *testing.T) {
	cfg := coreConfig(t, "http://127.0.0.1", &coreRecorder{}, 5)
	cfg.Limits.MaxAttempts = 2
	e, c := coreOpen(t, cfg)
	c.Block()
	if _, r, err := c.Do(context.Background(), coreRequest(t, "GET", "http://127.0.0.1/allowed")); !errors.Is(err, ErrClosed) || r.AttemptID != "" {
		t.Fatal("closed call admitted")
	}
	c2, err := e.BeginCall(context.Background(), "command2")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, r, err := c2.Do(context.Background(), coreRequest(t, "GET", "http://127.0.0.1/denied"))
		if err == nil || !r.NotSent {
			t.Fatal("denied send")
		}
	}
	if _, _, err := c2.Do(context.Background(), coreRequest(t, "GET", "http://127.0.0.1/denied")); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if e.Snapshot().Reason != AttemptLimit {
		t.Fatal("finite guard")
	}
}
func TestCoreCallCloseIsSingleAndFinal(t *testing.T) {
	rec := &coreRecorder{}
	_, c := coreOpen(t, coreConfig(t, "http://127.0.0.1", rec, 1))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := c.Close(context.Background())
			if err != nil || !r.Final {
				t.Errorf("%+v %v", r, err)
			}
		}()
	}
	wg.Wait()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	n := 0
	for _, e := range rec.events {
		if e.Stage == "call_closed" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("close audit count %d", n)
	}
}
func TestCoreExpiredApprovalIsBlockedAtWrite(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cfg := coreConfig(t, "http://"+l.Addr().String(), &coreRecorder{}, 1)
	cfg.ExpiresAt = time.Now().Add(25 * time.Millisecond)
	_, c := coreOpen(t, cfg)
	time.Sleep(35 * time.Millisecond)
	_, r, err := c.Do(context.Background(), coreRequest(t, "GET", cfg.Rules[0].Origin+"/allowed"))
	if err == nil || !r.NotSent {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestCoreRetainsCredentialSubvaluesAfterReduction(t *testing.T) {
	cfg := coreConfig(t, "http://127.0.0.1", &coreRecorder{}, 2)
	cfg.HeaderMode = HeaderProtectedSessionProposal
	cfg.Rules[0].CredentialHeaders = http.Header{"Authorization": {"Bearer synthetic-bearer"}, "Cookie": {"sid=synthetic-cookie; other=synthetic-other"}}
	cfg.Rules = append(cfg.Rules, Rule{Ref: "rule2", Origin: "http://127.0.0.1", ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/remaining", PathScopeRef: "remaining", Methods: []string{"GET"}})
	e, _ := coreOpen(t, cfg)
	if err := e.Restrict(context.Background(), Restriction{PolicyRevision: "revision2", ExpiresAt: cfg.ExpiresAt, RuleRefs: []string{"rule2"}, Methods: []string{"GET"}}); err != nil {
		t.Fatal(err)
	}
	if out := string(e.RedactOutput([]byte("synthetic-bearer synthetic-cookie synthetic-other"))); strings.Contains(out, "synthetic-") {
		t.Fatal("retired credential leaked")
	}
}
func TestCoreNoEventsAfterFinalCall(t *testing.T) {
	e, c := coreOpen(t, coreConfig(t, "http://127.0.0.1", &coreRecorder{}, 1))
	if _, err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := len(e.Events(0, 200))
	if _, err := c.Reject(context.Background(), "GET", Unsupported); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if len(e.Events(0, 200)) != before || !c.Result().Final {
		t.Fatal("final call mutated")
	}
}

func TestCoreIngressCapabilityIsCallScopedAndRevoked(t *testing.T) {
	e, c := coreOpen(t, coreConfig(t, "http://127.0.0.1", &coreRecorder{}, 2))
	token, err := c.newIngressCapability()
	if err != nil {
		t.Fatal(err)
	}
	c2, err := e.BeginCall(context.Background(), "other-command")
	if err != nil {
		t.Fatal(err)
	}
	if !c.validIngressCapability(token) || c2.validIngressCapability(token) {
		t.Fatal("capability call confusion")
	}
	c.Block()
	if c.validIngressCapability(token) {
		t.Fatal("closed capability accepted")
	}
	if _, err := c.newIngressCapability(); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestCoreIntentRegistrationSharesApprovalAndAttribution(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.Header.Get("X-ABC2LAB-E2-Intent-ID") != "" || r.Header.Get("X-E2-Intent-ID") != "" {
			t.Error("caller-supplied intent attribution reached target")
		}
		io.WriteString(w, "same approved target")
	}))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
	execution, anchor := coreOpen(t, cfg)
	if !execution.HasIntent(cfg.IntentID) || execution.HasIntent(2) {
		t.Fatal("initial intent set")
	}
	first, receipt, err := anchor.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err != nil || receipt.IntentID != 1 {
		t.Fatal("anchor receipt lost actual intent")
	}
	first.Body.Close()
	if err = execution.RegisterIntent(t.Context(), 2); err != nil {
		t.Fatal("trusted intent registration failed")
	}
	if err = execution.RegisterIntent(t.Context(), 2); err != nil {
		t.Fatal("identical binding was not idempotent")
	}
	secondCall, err := execution.BeginCallForIntent(t.Context(), 2, "second-command")
	if err != nil {
		t.Fatal(err)
	}
	request := coreRequest(t, "GET", target.URL+"/allowed")
	request.Header.Set("X-ABC2LAB-E2-Intent-ID", "99999")
	request.Header.Set("X-E2-Intent-ID", "99999")
	second, secondReceipt, err := secondCall.Do(t.Context(), request)
	if err != nil || secondReceipt.IntentID != 2 || secondReceipt.Scope != cfg.Scope || secondReceipt.AttemptID == receipt.AttemptID {
		t.Fatal("new intent did not retain execution scope or actual attribution")
	}
	second.Body.Close()
	if _, err = secondCall.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if secondCall.Result().IntentID != 2 {
		t.Fatal("closed call lost intent")
	}
	if err = execution.RegisterIntent(t.Context(), 3); err != nil {
		t.Fatal(err)
	}
	thirdCall, err := execution.BeginCallForIntent(t.Context(), 3, "third-command")
	if err != nil {
		t.Fatal(err)
	}
	_, thirdReceipt, err := thirdCall.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err == nil || thirdReceipt.Reason != BudgetExhausted || thirdReceipt.IntentID != 3 || !thirdReceipt.NotSent {
		t.Fatal("new intent recharged the existing budget")
	}
	if _, err = thirdCall.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	remaining, err := execution.BudgetRemaining()
	if err != nil || remaining != 0 || received.Load() != 2 {
		t.Fatal("shared grant accounting")
	}
	view := execution.SnapshotForIntent(2)
	if view.IntentID != 2 || len(view.IntentIDs) != 3 || view.IntentIDs[0] != 1 || view.IntentIDs[1] != 2 || view.IntentIDs[2] != 3 ||
		view.Counts.Attempts != 3 || view.Scope != cfg.Scope || !view.ExpiresAt.Equal(cfg.ExpiresAt) {
		t.Fatal("intent projection reset execution state")
	}
	if execution.Snapshot().IntentID != 1 {
		t.Fatal("approval anchor was reassigned")
	}
}

func TestCoreIntentRegistrationWaitsForACKWithoutPublishing(t *testing.T) {
	recorder := &coreRecorder{blockStage: "control", entered: make(chan struct{}, 1), release: make(chan struct{})}
	execution, _ := coreOpen(t, coreConfig(t, "http://approved.invalid", recorder, 2))
	done := make(chan error, 1)
	go func() { done <- execution.RegisterIntent(t.Context(), 2) }()
	select {
	case <-recorder.entered:
	case <-time.After(time.Second):
		t.Fatal("registration did not reach durable ACK boundary")
	}
	if execution.HasIntent(2) || execution.SnapshotForIntent(2).Reason != Invalid {
		t.Fatal("pending ACK became intent authority")
	}
	if _, err := execution.BeginCallForIntent(t.Context(), 2, "unregistered-command"); err == nil {
		t.Fatal("unregistered intent acquired a call")
	}
	if execution.Snapshot().State != "active" {
		t.Fatal("metadata ACK changed the current target policy")
	}
	close(recorder.release)
	if err := <-done; err != nil || !execution.HasIntent(2) {
		t.Fatal("ACK did not publish validated membership")
	}
}

func TestCoreIntentRegistrationLateACKCannotSurviveRevocation(t *testing.T) {
	recorder := &coreRecorder{blockStage: "control", entered: make(chan struct{}, 1), release: make(chan struct{})}
	execution, _ := coreOpen(t, coreConfig(t, "http://approved.invalid", recorder, 2))
	registered := make(chan error, 1)
	go func() { registered <- execution.RegisterIntent(t.Context(), 2) }()
	select {
	case <-recorder.entered:
	case <-time.After(time.Second):
		t.Fatal("registration ACK boundary")
	}
	revoked := make(chan error, 1)
	go func() { revoked <- execution.Revoke(t.Context()) }()
	select {
	case <-execution.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("revocation did not fence before storage ACK")
	}
	close(recorder.release)
	if err := <-registered; err == nil || execution.HasIntent(2) {
		t.Fatal("late registration ACK revived a revoked approval")
	}
	if err := <-revoked; err != nil {
		t.Fatal("revocation record did not complete")
	}
	if _, err := execution.BeginCallForIntent(t.Context(), 2, "revoked-command"); err == nil {
		t.Fatal("revoked intent acquired a call")
	}
}

func TestCoreIntentRegistrationLateACKRequiresCurrentRevision(t *testing.T) {
	recorder := &coreRecorder{blockStage: "control", entered: make(chan struct{}, 1), release: make(chan struct{})}
	cfg := coreConfig(t, "http://approved.invalid", recorder, 2)
	execution, _ := coreOpen(t, cfg)
	registered := make(chan error, 1)
	go func() { registered <- execution.RegisterIntent(t.Context(), 2) }()
	select {
	case <-recorder.entered:
	case <-time.After(time.Second):
		t.Fatal("registration ACK boundary")
	}
	restricted := make(chan error, 1)
	go func() {
		restricted <- execution.Restrict(t.Context(), Restriction{PolicyRevision: "revision2", ExpiresAt: cfg.ExpiresAt.Add(-time.Minute), RuleRefs: []string{"rule"}, Methods: []string{"GET"}})
	}()
	deadline := time.Now().Add(time.Second)
	for execution.Snapshot().PolicyRevision != "revision2" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if execution.Snapshot().PolicyRevision != "revision2" {
		t.Fatal("policy reduction was not installed")
	}
	close(recorder.release)
	if err := <-registered; err == nil || execution.HasIntent(2) {
		t.Fatal("stale revision ACK became a current intent binding")
	}
	if err := <-restricted; err != nil {
		t.Fatal("policy reduction ACK failed")
	}
	if err := execution.RegisterIntent(t.Context(), 2); err != nil || !execution.HasIntent(2) {
		t.Fatal("explicit current-policy registration could not proceed")
	}
}

func TestCoreIntentRegistrationRefusesMissingAuthority(t *testing.T) {
	for _, name := range []string{"audit", "canceled", "expired", "invalid"} {
		t.Run(name, func(t *testing.T) {
			recorder := &coreRecorder{}
			cfg := coreConfig(t, "http://approved.invalid", recorder, 2)
			if name == "audit" {
				recorder.failStage = "control"
			}
			if name == "expired" {
				cfg.ExpiresAt = time.Now().Add(100 * time.Millisecond)
			}
			execution, call := coreOpen(t, cfg)
			ctx := t.Context()
			id := int64(2)
			if name == "canceled" {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			if name == "expired" {
				<-call.Context().Done()
			}
			if name == "invalid" {
				id = 0
			}
			if err := execution.RegisterIntent(ctx, id); err == nil || execution.HasIntent(id) {
				t.Fatal("invalid or unacknowledged intent was registered")
			}
			if _, err := execution.BeginCallForIntent(t.Context(), 2, "no-authority"); err == nil {
				t.Fatal("missing membership acquired a call")
			}
			if name == "audit" && execution.Snapshot().Reason != AuditFailed {
				t.Fatal("required audit failure did not fence the execution")
			}
		})
	}
}

func TestCoreConcurrentIntentRegistrationIsIdempotent(t *testing.T) {
	recorder := &coreRecorder{}
	cfg := coreConfig(t, "http://approved.invalid", recorder, 8)
	execution, _ := coreOpen(t, cfg)
	var group sync.WaitGroup
	errorsFound := make(chan error, 20)
	for index := 0; index < 20; index++ {
		group.Add(1)
		go func(id int64) { defer group.Done(); errorsFound <- execution.RegisterIntent(t.Context(), id) }(int64(2 + index%4))
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal("concurrent registration failed")
		}
	}
	snapshot := execution.Snapshot()
	if len(snapshot.IntentIDs) != 5 || snapshot.Counts.Calls != 1 || snapshot.Counts.Attempts != 0 || !snapshot.ExpiresAt.Equal(cfg.ExpiresAt) {
		t.Fatal("registration reset execution or produced duplicate membership")
	}
	control := 0
	for _, event := range execution.Events(0, 200) {
		if event.Stage == "control" {
			control++
		}
	}
	if control != 4 {
		t.Fatal("idempotent registration emitted duplicate commits")
	}
	remaining, err := execution.BudgetRemaining()
	if err != nil || remaining != 8 {
		t.Fatal("registration spent or reissued HTTP budget")
	}
}

func TestCoreIntentRegistrationDoesNotResetWorkCeilings(t *testing.T) {
	for _, name := range []string{"calls", "attempts", "denials"} {
		t.Run(name, func(t *testing.T) {
			var received atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); io.WriteString(w, "ok") }))
			defer target.Close()
			cfg := coreConfig(t, target.URL, &coreRecorder{}, 8)
			switch name {
			case "calls":
				cfg.Limits.MaxCalls = 1
			case "attempts":
				cfg.Limits.MaxAttempts = 1
			case "denials":
				cfg.Limits.MaxDenials = 1
			}
			execution, anchor := coreOpen(t, cfg)
			if name == "denials" {
				if _, _, err := anchor.Do(t.Context(), coreRequest(t, "POST", target.URL+"/allowed")); err == nil {
					t.Fatal("denied method was sent")
				}
				if err := execution.RegisterIntent(t.Context(), 2); err == nil || execution.HasIntent(2) {
					t.Fatal("new intent reset the denial stop")
				}
				return
			}
			response, _, err := anchor.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if err = execution.RegisterIntent(t.Context(), 2); err != nil {
				t.Fatal(err)
			}
			call, err := execution.BeginCallForIntent(t.Context(), 2, "next-intent-command")
			if name == "calls" {
				if err == nil {
					t.Fatal("new intent reset MaxCalls")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed")); err == nil {
				t.Fatal("new intent reset MaxAttempts")
			}
			if _, err = call.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if execution.Snapshot().Reason != AttemptLimit || received.Load() != 1 {
				t.Fatal("attempt ceiling did not retain execution scope")
			}
		})
	}
}

func TestCoreIntentEventPaginationFiltersBeforeLimit(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer target.Close()
	execution, anchor := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 2))
	if err := execution.RegisterIntent(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	second, err := execution.BeginCallForIntent(t.Context(), 2, "other-command")
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []*Call{anchor, second} {
		response, _, err := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
	}
	for _, call := range []*Call{anchor, second} {
		if _, err := call.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err := execution.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := execution.EventsForIntent(2, 0, 1)
	if len(first) != 1 || first[0].IntentID != 2 {
		t.Fatal("first intent page")
	}
	next := execution.EventsForIntent(2, first[0].Index, 1)
	if len(next) != 1 || next[0].IntentID != 2 || next[0].Index <= first[0].Index+1 {
		t.Fatal("limit was applied before filtering or cursor was renumbered")
	}
	all := execution.EventsForIntent(2, 0, 200)
	if len(all) != 4 {
		t.Fatal("intent event scope included another intent or global control")
	}
	for _, event := range all {
		if event.IntentID != 2 {
			t.Fatal("cross-intent event escaped")
		}
	}
	if len(execution.EventsForIntent(999, 0, 200)) != 0 {
		t.Fatal("unknown intent exposed events")
	}
}

func TestCoreBudgetObservationDoesNotCancelReservedRequest(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		io.WriteString(w, "reserved response")
	}))
	defer target.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	execution, call := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 1))
	completed := make(chan bool, 1)
	go func() {
		response, receipt, err := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
		if response != nil {
			response.Body.Close()
		}
		completed <- err == nil && receipt.ResponseComplete
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("reserved request did not reach target")
	}
	remaining, err := execution.BudgetRemaining()
	policy := execution.Policy()
	if err != nil || remaining != 0 || !policy.BudgetAvailable || policy.RemainingRequests != 0 || policy.BudgetRef != "approval" || execution.Snapshot().State != "active" {
		t.Fatal("budget projection is not a non-authoritative observation")
	}
	close(release)
	select {
	case success := <-completed:
		if !success {
			t.Fatal("reading zero canceled an already reserved request")
		}
	case <-time.After(time.Second):
		t.Fatal("reserved response did not complete")
	}
}

func TestCorePreservedRequestHost(t *testing.T) {
	for _, tc := range []struct {
		name, scheme, authority, host, want string
		ok                                  bool
	}{
		{"http_case", "http", "example.test:80", "EXAMPLE.test:80", "EXAMPLE.test:80", true},
		{"http_omitted_default", "http", "example.test:80", "EXAMPLE.test", "EXAMPLE.test", true},
		{"https_omitted_default", "https", "example.test:443", "EXAMPLE.test", "EXAMPLE.test", true},
		{"https_explicit_default", "https", "example.test", "EXAMPLE.test:443", "EXAMPLE.test:443", true},
		{"fallback", "http", "EXAMPLE.test", "", "EXAMPLE.test", true},
		{"other_host", "http", "example.test", "other.test", "", false},
		{"other_port", "http", "example.test", "example.test:81", "", false},
		{"path", "http", "example.test", "example.test/path", "", false},
		{"query", "http", "example.test", "example.test?x=1", "", false},
		{"userinfo", "http", "example.test", "user@example.test", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, ok := preservedRequestHost(&http.Request{URL: &url.URL{Scheme: tc.scheme, Host: tc.authority}, Host: tc.host})
			if ok != tc.ok || ok && host != tc.want {
				t.Fatalf("host preservation: %q %t", host, ok)
			}
		})
	}
}

func TestCoreTargetObservesOriginalHostAndIdentity(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			observed := make(chan string, 1)
			target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				observed <- r.Host + "|" + r.Header.Get("Accept-Encoding")
				io.WriteString(w, "preserved")
			}))
			var roots *x509.CertPool
			name := "EXAMPLE.test"
			if scheme == "https" {
				target.StartTLS()
				roots = x509.NewCertPool()
				roots.AddCert(target.Certificate())
				if len(target.Certificate().DNSNames) == 0 {
					t.Fatal("TLS fixture has no named authority")
				}
				name = strings.ToUpper(target.Certificate().DNSNames[0])
			} else {
				target.Start()
			}
			defer target.Close()
			_, port, err := net.SplitHostPort(target.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			origin := scheme + "://" + strings.ToLower(name) + ":" + port
			cfg := coreConfig(t, origin, &coreRecorder{}, 2)
			cfg.RootCAs = roots
			_, call := coreOpen(t, cfg)
			req := coreRequest(t, "GET", origin+"/allowed")
			req.Host = name + ":" + port
			req.Header.Set("Accept-Encoding", "identity")
			response, receipt, err := call.Do(t.Context(), req)
			if err != nil || response == nil || !receipt.ResponseComplete {
				t.Fatal("positive delivery failed")
			}
			response.Body.Close()
			if got := <-observed; got != req.Host+"|identity" {
				t.Fatalf("target observed rewritten request: %q", got)
			}
		})
	}
}

func TestCoreUnsupportedEncodingAndHostNeverReachTarget(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	e, call := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 10))
	for _, encoding := range []string{"gzip", "br", "deflate", "gzip, identity", "identity;q=1"} {
		t.Run(encoding, func(t *testing.T) {
			req := coreRequest(t, "GET", target.URL+"/allowed")
			req.Header.Set("Accept-Encoding", encoding)
			response, receipt, err := call.Do(t.Context(), req)
			if err == nil || response != nil || receipt.Decision != "unsupported" || receipt.Reason != Unsupported || !receipt.NotSent || receipt.ForwardAttempted {
				t.Fatal("unsupported negotiation was sent")
			}
		})
	}
	req := coreRequest(t, "GET", target.URL+"/allowed")
	req.Host = "other.invalid"
	if response, receipt, err := call.Do(t.Context(), req); err == nil || response != nil || !receipt.NotSent {
		t.Fatal("unchecked Host reached target")
	}
	if remaining, err := e.BudgetRemaining(); err != nil || remaining != 10 || received.Load() != 0 {
		t.Fatal("pre-send refusal spent budget or reached target")
	}
}

func TestCoreEncodedCredentialEchoIsWithheldAfterSend(t *testing.T) {
	const secret = "owned-host-session-echo"
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		var body bytes.Buffer
		writer := gzip.NewWriter(&body)
		_, _ = writer.Write([]byte(r.Header.Get("Authorization")))
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(body.Bytes())
	}))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
	cfg.HeaderMode = HeaderProtectedSessionProposal
	cfg.Rules[0].CredentialHeaders = http.Header{"Authorization": {"Bearer " + secret}}
	_, call := coreOpen(t, cfg)
	response, receipt, err := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err == nil || response != nil || received.Load() != 1 || receipt.Decision != "allowed" || !receipt.ForwardAttempted || !receipt.ResponseReceived || receipt.NotSent || receipt.Reason != Unknown {
		t.Fatal("encoded credential echo was exposed or called not sent")
	}
}

func TestCoreRejectsTrustedCompressedNegotiation(t *testing.T) {
	for _, encoding := range []string{"gzip", "br", "deflate"} {
		t.Run(encoding, func(t *testing.T) {
			cfg := coreConfig(t, "http://target.invalid", &coreRecorder{}, 2)
			cfg.Rules[0].Headers = http.Header{"aCcEpT-eNcOdInG": {encoding}}
			if execution, err := NewExecution(t.Context(), cfg); err == nil || execution != nil {
				t.Fatal("trusted headers opened unsupported compression")
			}
		})
	}
}
