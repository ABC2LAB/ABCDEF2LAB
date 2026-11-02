package transmit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/Autumn-27/artex/targethealth"
)

// HealthConfig is explicit operator policy for one transmitter instance.
// Policy.ScopeRef is a caller-issued alias for this execution, not a URL.
// NetworkTimeout starts at ConnectStart and must be shorter than RequestTimeout:
// the outer execution/approval deadline must not be mistaken for target failure.
type HealthConfig struct {
	Policy         targethealth.Config
	NetworkTimeout time.Duration
}

// HealthStatus is the last in-memory assessment, never an admission permission
// or an outage diagnosis. Halted is sticky for this instance. InputValid=false
// distinguishes a monitor input/capacity failure from observed degradation.
type HealthStatus struct {
	Enabled     bool
	Halted      bool
	InputValid  bool
	EvaluatedAt time.Time
	Decision    targethealth.Decision
}

// healthState is owned by HTTPTransmitter.mu; immutable policy is copied at
// construction and no external observation can be inserted into this window.
type healthState struct {
	config       HealthConfig
	observations []targethealth.Observation
	status       HealthStatus
}

func newHealthState(config *HealthConfig, requestTimeout time.Duration) (*healthState, error) {
	if config == nil {
		return nil, nil
	}
	cfg := *config
	if cfg.NetworkTimeout <= 0 || cfg.NetworkTimeout >= requestTimeout {
		return nil, invalid("health network timeout")
	}
	if cfg.Policy.SlowAfter >= cfg.NetworkTimeout {
		return nil, invalid("health slow threshold")
	}
	decision, err := targethealth.Evaluate(cfg.Policy, nil, time.Now())
	if err != nil {
		return nil, invalid("health policy")
	}
	return &healthState{config: cfg, status: HealthStatus{Enabled: true, InputValid: true, Decision: decision}}, nil
}

func (t *HTTPTransmitter) HealthStatus() HealthStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.health == nil {
		return HealthStatus{}
	}
	status := t.health.status
	status.Decision.ReasonCodes = append([]targethealth.Reason(nil), status.Decision.ReasonCodes...)
	return status
}

func (h *healthState) observe(observation targethealth.Observation, now time.Time) {
	if h.status.Halted {
		return
	}
	start := now.Add(-h.config.Policy.Window)
	kept := h.observations[:0]
	for _, old := range h.observations {
		if old.CompletedAt.After(start) {
			kept = append(kept, old)
		}
	}
	clear(h.observations[len(kept):])
	// One excess sample is retained solely to let Evaluate reject overflow.
	// Never discard still-valid samples to manufacture a below-limit window.
	h.observations = append(kept, observation)
	decision, err := targethealth.Evaluate(h.config.Policy, h.observations, now)
	h.status = HealthStatus{Enabled: true, InputValid: err == nil, EvaluatedAt: now, Decision: decision,
		Halted: err != nil || decision.Recommendation == targethealth.StopRecommended}
}

func (t *HTTPTransmitter) observeHealth(network *networkObservation, receipt Receipt) {
	observation, observed := network.snapshot()
	if !observed {
		return
	}
	t.mu.Lock()
	if t.health == nil || t.stopped || t.auditFailed {
		t.mu.Unlock()
		return
	}
	observation.ScopeRef = t.health.config.Policy.ScopeRef
	observation.Attempt = receipt.Attempt
	// ARTEX resource refs may contain '/', which the health core does not accept.
	// A deterministic opaque alias preserves distinct resources without URLs.
	sum := sha256.Sum256([]byte(receipt.ResourceRef))
	observation.ResourceRef = "r-" + hex.EncodeToString(sum[:])
	t.health.observe(observation, time.Now())
	stop := t.health.status.Halted
	if stop {
		// Share Stop's admission linearization point with the health latch.
		t.stopped = true
	}
	t.mu.Unlock()
	if stop {
		// This is Stop's cancellation half, outside mu. Never drain our own send
		// with StopAndWait; the caller still needs its durable result record.
		t.cancel()
	}
}

var errHealthNetworkTimeout = errors.New("transmit: health network deadline")

// networkObservation is private per-request trace state. It retains no raw
// errors, addresses, headers, credentials or payloads. Callbacks can outlive Do.
type networkObservation struct {
	mu       sync.Mutex
	parent   context.Context
	ctx      context.Context
	cancel   context.CancelCauseFunc
	timeout  time.Duration
	timer    *time.Timer
	started  time.Time
	ended    time.Time
	done     bool
	prepared bool
	valid    bool
	outcome  targethealth.Outcome
	status   int
}

func newNetworkObservation(parent context.Context, timeout time.Duration) (*networkObservation, context.Context) {
	ctx, cancel := context.WithCancelCause(parent)
	return &networkObservation{parent: parent, ctx: ctx, cancel: cancel, timeout: timeout}, ctx
}

func (n *networkObservation) start() {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.done || !n.started.IsZero() || !n.parentActive() {
		return
	}
	n.started = time.Now()
	n.timer = time.AfterFunc(n.timeout, func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		if !n.done {
			// Serialize cancellation with finish so a completed response cannot
			// be canceled later by a timer callback that was already scheduled.
			n.cancel(errHealthNetworkTimeout)
		}
	})
}

func (n *networkObservation) finish(networkErr error, status int) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.done {
		return
	}
	n.outcome, n.status = networkOutcome(networkErr), status
	n.finalizeLocked()
}

// responseRead retains only normalized metadata while body Close is pending.
// Keep the network timer alive until all body I/O has actually been recovered.
func (n *networkObservation) responseRead(networkErr error, status int) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.done || n.prepared {
		return
	}
	n.outcome, n.status, n.prepared = networkOutcome(networkErr), status, true
}

func networkOutcome(err error) targethealth.Outcome {
	if err == nil {
		return targethealth.Response
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return targethealth.Timeout
	}
	return targethealth.TransportError
}

func (n *networkObservation) finalizeLocked() {
	n.done, n.ended, n.valid = true, time.Now(), n.parentActive()
	if n.timer != nil {
		n.timer.Stop()
	}
	// Timer callbacks can wait for this lock. Classify by the actual deadline
	// too, so delayed callback delivery cannot accept an over-budget response.
	if errors.Is(context.Cause(n.ctx), errHealthNetworkTimeout) || !n.started.IsZero() && !n.ended.Before(n.started.Add(n.timeout)) {
		n.outcome = targethealth.Timeout
		n.cancel(errHealthNetworkTimeout)
	}
}

func (n *networkObservation) bodyClosed(closeErr error) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.done || !n.prepared {
		return
	}
	if closeErr != nil && n.outcome != targethealth.Timeout {
		n.outcome = targethealth.TransportError
	}
	n.finalizeLocked()
}

func (n *networkObservation) dispose() {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.done = true
	if n.timer != nil {
		n.timer.Stop()
	}
	n.mu.Unlock()
	n.cancel(nil)
}

func (n *networkObservation) snapshot() (targethealth.Observation, bool) {
	if n == nil {
		return targethealth.Observation{}, false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.done || n.started.IsZero() || n.ended.IsZero() || !n.valid {
		return targethealth.Observation{}, false
	}
	return targethealth.Observation{NetworkStarted: true, CompletedAt: n.ended,
		Elapsed: n.ended.Sub(n.started), Outcome: n.outcome, StatusCode: n.status}, true
}

func (n *networkObservation) parentActive() bool {
	if n.parent.Err() != nil {
		return false
	}
	// Deadline delivery can lag its timestamp under load. A local deadline is
	// still local even before the context's timer goroutine reports Err.
	deadline, ok := n.parent.Deadline()
	return !ok || time.Now().Before(deadline)
}
