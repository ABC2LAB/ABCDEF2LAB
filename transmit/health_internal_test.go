package transmit

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/targethealth"
)

func healthTestConfig() targethealth.Config {
	return targethealth.Config{
		ScopeRef:     "health-execution",
		Window:       time.Minute,
		MaxSamples:   4,
		MinSamples:   2,
		FailureLimit: 2,
		SlowAfter:    0,
		SlowLimit:    0,
	}
}

func healthTestObservation(attempt uint64, completed time.Time, outcome targethealth.Outcome, status int) targethealth.Observation {
	return targethealth.Observation{
		ScopeRef:       "health-execution",
		Attempt:        attempt,
		ResourceRef:    "r-resource",
		NetworkStarted: true,
		CompletedAt:    completed,
		Elapsed:        time.Millisecond,
		Outcome:        outcome,
		StatusCode:     status,
	}
}

func newHealthTestState(t *testing.T, cfg targethealth.Config) *healthState {
	t.Helper()
	h, err := newHealthState(&HealthConfig{Policy: cfg, NetworkTimeout: time.Second}, 2*time.Second)
	if err != nil {
		t.Fatalf("newHealthState() error = %v", err)
	}
	return h
}

func TestHealthStateWindowExpiryAndStickyValidationHalt(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	h := newHealthTestState(t, healthTestConfig())
	boundary := healthTestObservation(1, now.Add(-h.config.Policy.Window), targethealth.Response, 200)
	inside := healthTestObservation(2, now.Add(-h.config.Policy.Window+time.Second), targethealth.Response, 200)
	h.observations = []targethealth.Observation{boundary, inside}

	// The left edge is exclusive: boundary is removed before evaluation.
	h.observe(healthTestObservation(3, now, targethealth.TransportError, 0), now)
	if len(h.observations) != 2 {
		t.Fatalf("window retained %d observations, want 2", len(h.observations))
	}
	if h.status.Halted {
		t.Fatalf("single failure after expiry halted health state: %+v", h.status)
	}

	// A second failure reaches the policy threshold and latches the halt.
	h.observe(healthTestObservation(4, now.Add(time.Second), targethealth.TransportError, 0), now.Add(time.Second))
	status := h.status
	if !status.Halted || status.Decision.Recommendation != targethealth.StopRecommended {
		t.Fatalf("failure threshold did not halt: %+v", status)
	}
	if len(status.Decision.ReasonCodes) != 1 || status.Decision.ReasonCodes[0] != targethealth.FailureThreshold {
		t.Fatalf("halt reason = %v, want failure threshold", status.Decision.ReasonCodes)
	}

	before := h.status
	beforeCount := len(h.observations)
	h.observe(healthTestObservation(5, now.Add(2*time.Hour), targethealth.Response, 200), now.Add(2*time.Hour))
	if len(h.observations) != beforeCount || h.status.EvaluatedAt != before.EvaluatedAt || h.status.Decision.Recommendation != before.Decision.Recommendation {
		t.Fatalf("halted state changed after late observation: before=%+v after=%+v", before, h.status)
	}
}

func TestHealthStateOverflowAndDuplicateLatch(t *testing.T) {
	now := time.Date(2026, 10, 30, 13, 0, 0, 0, time.UTC)
	cfg := healthTestConfig()
	cfg.MaxSamples = 2
	h := newHealthTestState(t, cfg)
	h.observe(healthTestObservation(1, now, targethealth.Response, 200), now)
	h.observe(healthTestObservation(2, now.Add(time.Second), targethealth.Response, 200), now.Add(time.Second))
	h.observe(healthTestObservation(3, now.Add(2*time.Second), targethealth.Response, 200), now.Add(2*time.Second))
	if h.status.InputValid || !h.status.Halted || h.status.Decision.Recommendation != targethealth.StopRecommended {
		t.Fatalf("overflow status = %+v", h.status)
	}
	if len(h.status.Decision.ReasonCodes) != 1 || h.status.Decision.ReasonCodes[0] != targethealth.TooManyObservations {
		t.Fatalf("overflow reason = %v, want too_many_observations", h.status.Decision.ReasonCodes)
	}

	cfg.MaxSamples = 4
	h = newHealthTestState(t, cfg)
	h.observe(healthTestObservation(1, now, targethealth.Response, 200), now)
	h.observe(healthTestObservation(1, now.Add(time.Second), targethealth.Response, 200), now.Add(time.Second))
	if h.status.InputValid || !h.status.Halted || h.status.Decision.ReasonCodes[0] != targethealth.DuplicateAttempt {
		t.Fatalf("duplicate status = %+v", h.status)
	}
}

func waitHealthContext(t *testing.T, ctx context.Context) {
	t.Helper()
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
		t.Fatal("timed out waiting for observation context")
	}
}

type healthTimeoutError struct{}

func (healthTimeoutError) Error() string   { return "network timeout" }
func (healthTimeoutError) Timeout() bool   { return true }
func (healthTimeoutError) Temporary() bool { return true }

func TestNetworkObservationTimeoutNormalizationAndLateCallbacks(t *testing.T) {
	network, ctx := newNetworkObservation(context.Background(), 15*time.Millisecond)
	network.start()
	network.start() // duplicate dial/start callback must not replace the timer.
	waitHealthContext(t, ctx)
	network.finish(nil, 0)
	network.finish(errors.New("late response"), 500)
	network.bodyClosed(nil)
	observation, ok := network.snapshot()
	if !ok || observation.Outcome != targethealth.Timeout {
		t.Fatalf("network timeout snapshot = (%+v, %v)", observation, ok)
	}

	request, _ := newNetworkObservation(context.Background(), time.Second)
	request.start()
	request.finish(healthTimeoutError{}, 0)
	observation, ok = request.snapshot()
	if !ok || observation.Outcome != targethealth.Timeout {
		t.Fatalf("net.Error timeout snapshot = (%+v, %v)", observation, ok)
	}

	body, _ := newNetworkObservation(context.Background(), time.Second)
	body.start()
	body.responseRead(nil, 200)
	body.bodyClosed(errors.New("body close"))
	observation, ok = body.snapshot()
	if !ok || observation.Outcome != targethealth.TransportError || observation.StatusCode != 200 {
		t.Fatalf("body close snapshot = (%+v, %v)", observation, ok)
	}
}

func TestNetworkObservationParentDeadlineExcludedAndDisposeStopsTimer(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	network, ctx := newNetworkObservation(parent, time.Second)
	network.start()
	waitHealthContext(t, ctx)
	network.finish(context.DeadlineExceeded, 0)
	if _, ok := network.snapshot(); ok {
		t.Fatal("parent deadline observation was admitted as a target observation")
	}

	disposed, disposedCtx := newNetworkObservation(context.Background(), 15*time.Millisecond)
	disposed.start()
	disposed.dispose()
	waitHealthContext(t, disposedCtx)
	disposed.start() // a late start callback cannot restart a disposed timer.
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	disposed.finish(errors.New("late response"), 500)
	disposed.bodyClosed(errors.New("late close"))
	if _, ok := disposed.snapshot(); ok {
		t.Fatal("disposed observation became admissible after late callbacks")
	}
}

func TestNetworkObservationDisposeFreezesCompletedMetadata(t *testing.T) {
	network, _ := newNetworkObservation(context.Background(), time.Second)
	network.start()
	network.responseRead(nil, 200)
	network.bodyClosed(nil)
	want, ok := network.snapshot()
	if !ok {
		t.Fatal("completed observation missing")
	}
	network.dispose()
	network.bodyClosed(errors.New("late close"))
	got, ok := network.snapshot()
	if !ok || got != want {
		t.Fatal("late body close changed disposed observation")
	}
}

func TestNetworkObservationCallbacksAreRaceSafe(t *testing.T) {
	network, _ := newNetworkObservation(context.Background(), 25*time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch i % 4 {
			case 0:
				network.start()
			case 1:
				network.finish(nil, 200)
			case 2:
				network.bodyClosed(errors.New("concurrent close"))
			default:
				network.dispose()
			}
		}(i)
	}
	wg.Wait()
	if observation, ok := network.snapshot(); ok && observation.CompletedAt.IsZero() {
		t.Fatalf("admitted observation has zero completion time: %+v", observation)
	}
}

var _ net.Error = healthTimeoutError{}

type healthExpiredContext struct{ context.Context }

func (healthExpiredContext) Deadline() (time.Time, bool) {
	return time.Unix(1, 0), true
}

func TestNetworkObservationExpiredDeadlineBeforeErrDeliveryIsExcluded(t *testing.T) {
	network, _ := newNetworkObservation(healthExpiredContext{context.Background()}, time.Second)
	network.start()
	network.finish(healthTimeoutError{}, 0)
	defer network.dispose()
	if _, ok := network.snapshot(); ok {
		t.Fatal("expired local deadline counted before Err delivery")
	}
}

func TestNetworkObservationLaterParentExpiryPreservesCompletedResponse(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	network, _ := newNetworkObservation(parent, time.Second)
	defer network.dispose()
	network.start()
	network.responseRead(nil, 200)
	network.bodyClosed(nil)
	want, ok := network.snapshot()
	if !ok {
		t.Fatal("response did not complete before parent expiry")
	}
	waitHealthContext(t, parent)
	got, ok := network.snapshot()
	if !ok || got != want {
		t.Fatal("later parent expiry erased completed network observation")
	}
}

func TestNetworkObservationBodyCloseStillHasNetworkDeadline(t *testing.T) {
	network, ctx := newNetworkObservation(context.Background(), 20*time.Millisecond)
	defer network.dispose()
	network.start()
	network.responseRead(nil, 200)
	if _, ok := network.snapshot(); ok {
		t.Fatal("response finalized before body close")
	}
	waitHealthContext(t, ctx)
	network.bodyClosed(nil)
	got, ok := network.snapshot()
	if !ok || got.Outcome != targethealth.Timeout {
		t.Fatal("pending body close escaped network deadline")
	}
}

func TestNetworkObservationDeadlineBeforeCallbackDeliveryIsTimeout(t *testing.T) {
	network, ctx := newNetworkObservation(context.Background(), 20*time.Millisecond)
	defer network.dispose()
	network.start()
	network.responseRead(nil, 200)
	network.mu.Lock()
	// Keep the timer callback waiting, as a stalled scheduler/contended lock
	// can do. Finalization must enforce the timestamp without that callback.
	timer := time.NewTimer(40 * time.Millisecond)
	<-timer.C
	network.finalizeLocked()
	network.mu.Unlock()
	got, ok := network.snapshot()
	if !ok || got.Outcome != targethealth.Timeout || !errors.Is(context.Cause(ctx), errHealthNetworkTimeout) {
		t.Fatal("late timer callback accepted an over-budget response")
	}
}

type healthUndeliveredDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c healthUndeliveredDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }

func TestNetworkObservationParentExpiresAfterStartBeforeErrDelivery(t *testing.T) {
	parent := healthUndeliveredDeadlineContext{Context: context.Background(), deadline: time.Now().Add(100 * time.Millisecond)}
	network, _ := newNetworkObservation(parent, time.Second)
	defer network.dispose()
	network.start()
	if network.started.IsZero() {
		t.Fatal("fixture parent expired before connection start")
	}
	timer := time.NewTimer(time.Until(parent.deadline) + 10*time.Millisecond)
	<-timer.C
	network.finish(healthTimeoutError{}, 0)
	if _, ok := network.snapshot(); ok {
		t.Fatal("parent deadline counted as target failure before Err delivery")
	}
}

type healthRoundTripper func(*http.Request) (*http.Response, error)

func (f healthRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type healthSlowCloseBody struct{ *strings.Reader }

func (*healthSlowCloseBody) Close() error {
	timer := time.NewTimer(40 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	return nil
}

type healthNoopRecorder struct{}

func (healthNoopRecorder) Record(context.Context, Event) error { return nil }

func TestHealthBodyCloseTimeoutOverridesResponsePolicyReason(t *testing.T) {
	for _, kind := range []string{"redirect", "encoding", "body limit"} {
		t.Run(kind, func(t *testing.T) {
			cfg := Config{Scope: Scope{RunRef: "run", TaskRef: "task", CaseRef: "case", ResetRef: "reset", PolicyRef: "policy"},
				Origin: "http://health.test", ConnectIP: netip.MustParseAddr("127.0.0.1"), ExpiresAt: time.Now().Add(time.Minute),
				Sessions:    []Session{{Account: idor.AccountRef{Label: "owner", Handle: "session", Status: oracle.SubjectVerified}, ExpiresAt: time.Now().Add(time.Minute)}},
				Resources:   []Resource{{Ref: "object/one", URL: "http://health.test/item", ApprovalRef: "approval", ReadOnly: true, Accounts: []string{"owner"}}},
				MaxRequests: 4, RequestTimeout: time.Second, MaxBodyBytes: 8, Recorder: healthNoopRecorder{},
				Health: &HealthConfig{Policy: healthTestConfig(), NetworkTimeout: 20 * time.Millisecond}}
			tx, err := NewHTTP(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.StopAndWait()
			tx.client.Transport = healthRoundTripper(func(r *http.Request) (*http.Response, error) {
				// Unit-only trace injection isolates the body-close lifecycle from
				// sockets; the integration suite measures actual loopback requests.
				httptrace.ContextClientTrace(r.Context()).ConnectStart("tcp", "synthetic")
				response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: &healthSlowCloseBody{strings.NewReader(strings.Repeat("x", 64))}, Request: r}
				if kind == "redirect" {
					response.StatusCode = http.StatusFound
					response.Header.Set("Location", "http://health.test/elsewhere")
				}
				if kind == "encoding" {
					response.Header.Set("Content-Encoding", "unsupported")
				}
				return response, nil
			})
			_, receipt, ok := tx.SendDetailed(idor.AccessRequest{As: cfg.Sessions[0].Account, ResourceRef: "object/one", Method: http.MethodGet})
			if ok || receipt.Reason != TimedOut || tx.HealthStatus().Decision.Counts.Timeouts != 1 {
				t.Fatalf("close timeout disagrees with receipt: %+v, %+v", receipt, tx.HealthStatus())
			}
		})
	}
}
