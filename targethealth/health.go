// Package targethealth evaluates bounded observations of one target execution.
// It recommends a safety stop; it neither diagnoses an outage nor grants request
// admission. It performs no I/O, retains no state and does not read the clock.
package targethealth

import (
	"errors"
	"net/http"
	"time"
)

const (
	// MaxWindowSamples bounds validation work and temporary bookkeeping.
	MaxWindowSamples = 10000
	maxRefBytes      = 128
	minSignalSamples = 2
	maxHTTPStatus    = 599
)

var (
	ErrConfig      = errors.New("targethealth: invalid configuration")
	ErrObservation = errors.New("targethealth: invalid observation batch")
)

// Config is trusted operator policy with no implicit thresholds. ScopeRef must
// identify one target and one transmitter execution, changing across resets or
// transmitter recreation so attempt numbers cannot be reused within a scope.
type Config struct {
	ScopeRef     string
	Window       time.Duration
	MaxSamples   int
	MinSamples   int
	FailureLimit int
	SlowAfter    time.Duration
	SlowLimit    int
}

type Outcome string

const (
	Response       Outcome = "response"
	Timeout        Outcome = "timeout"
	TransportError Outcome = "transport_error"
)

// Observation contains trusted, normalized network metadata only. NetworkStarted
// must be set by the adapter when the network call begins, not on admission or
// queue entry. Elapsed excludes local queue, rate-limit and audit waits. A timeout
// or transport error can retain an HTTP status if headers preceded a body error.
// These assertions are validated for consistency, not authenticated here.
type Observation struct {
	ScopeRef       string
	Attempt        uint64
	ResourceRef    string
	NetworkStarted bool
	CompletedAt    time.Time
	Elapsed        time.Duration
	Outcome        Outcome
	StatusCode     int
}

type Recommendation string

const (
	Continue        Recommendation = "continue"
	StopRecommended Recommendation = "stop_recommended"
)

type Reason string

const (
	InsufficientSamples Reason = "insufficient_samples"
	BelowThreshold      Reason = "below_threshold"
	FailureThreshold    Reason = "failure_threshold"
	SlowThreshold       Reason = "slow_threshold"
	InvalidConfig       Reason = "invalid_config"
	InvalidObservation  Reason = "invalid_observation"
	DuplicateAttempt    Reason = "duplicate_attempt"
	TooManyObservations Reason = "too_many_observations"
)

// Counts includes only observations in (now-Window, now]. Each observation
// contributes at most one failure; a completed slow 429/5xx also counts as slow.
type Counts struct {
	Samples           int
	Failures          int
	Timeouts          int
	TransportErrors   int
	RateLimited       int
	ServerErrors      int
	SlowResponses     int
	DistinctResources int
}

// Decision contains no input identifiers, payloads or raw errors. Continue only
// means this batch did not meet the stop criteria, not that the target is
// healthy or a new request is allowed. The caller owns stop latching and resume
// approval. Any error returns StopRecommended with zero counts and an input
// failure reason, which must not be recorded as target degradation.
type Decision struct {
	Recommendation Recommendation
	ReasonCodes    []Reason
	Counts         Counts
}

// Evaluate is deterministic and does not modify observations. It validates the
// entire batch, including expired samples, before returning any assessment.
// Callers must supply the complete bounded window for the registered scope;
// omission or fabrication of otherwise consistent samples cannot be detected.
func Evaluate(cfg Config, observations []Observation, now time.Time) (Decision, error) {
	if !validConfig(cfg) {
		return rejected(InvalidConfig, ErrConfig)
	}
	if now.IsZero() {
		return rejected(InvalidObservation, ErrObservation)
	}
	if len(observations) > cfg.MaxSamples {
		return rejected(TooManyObservations, ErrObservation)
	}
	attempts := make(map[uint64]bool, len(observations))
	resources := make(map[string]bool)
	start := now.Add(-cfg.Window)
	var counts Counts
	for _, observation := range observations {
		if !validObservation(cfg.ScopeRef, observation, now) {
			return rejected(InvalidObservation, ErrObservation)
		}
		if attempts[observation.Attempt] {
			return rejected(DuplicateAttempt, ErrObservation)
		}
		attempts[observation.Attempt] = true
		if !observation.CompletedAt.After(start) {
			continue
		}
		counts.Samples++
		resources[observation.ResourceRef] = true
		switch {
		case observation.Outcome == Timeout:
			counts.Timeouts++
			counts.Failures++
		case observation.Outcome == TransportError:
			counts.TransportErrors++
			counts.Failures++
		case observation.StatusCode == http.StatusTooManyRequests:
			counts.RateLimited++
			counts.Failures++
		case observation.StatusCode >= http.StatusInternalServerError:
			counts.ServerErrors++
			counts.Failures++
		}
		if cfg.SlowAfter > 0 && observation.Outcome == Response && observation.Elapsed >= cfg.SlowAfter {
			counts.SlowResponses++
		}
	}
	counts.DistinctResources = len(resources)
	decision := Decision{Recommendation: Continue, Counts: counts}
	if counts.Samples < cfg.MinSamples {
		decision.ReasonCodes = []Reason{InsufficientSamples}
		return decision, nil
	}
	if counts.Failures >= cfg.FailureLimit {
		decision.ReasonCodes = append(decision.ReasonCodes, FailureThreshold)
	}
	if cfg.SlowLimit > 0 && counts.SlowResponses >= cfg.SlowLimit {
		decision.ReasonCodes = append(decision.ReasonCodes, SlowThreshold)
	}
	if len(decision.ReasonCodes) > 0 {
		decision.Recommendation = StopRecommended
	} else {
		decision.ReasonCodes = []Reason{BelowThreshold}
	}
	return decision, nil
}

func validConfig(cfg Config) bool {
	if !validRef(cfg.ScopeRef) || cfg.Window <= 0 || cfg.MaxSamples < minSignalSamples || cfg.MaxSamples > MaxWindowSamples ||
		cfg.MinSamples < minSignalSamples || cfg.MinSamples > cfg.MaxSamples || cfg.FailureLimit < minSignalSamples || cfg.FailureLimit > cfg.MaxSamples {
		return false
	}
	if cfg.SlowAfter == 0 {
		return cfg.SlowLimit == 0
	}
	return cfg.SlowAfter > 0 && cfg.SlowLimit >= minSignalSamples && cfg.SlowLimit <= cfg.MaxSamples
}

func validObservation(scope string, observation Observation, now time.Time) bool {
	if observation.ScopeRef != scope || observation.Attempt == 0 || !validRef(observation.ResourceRef) ||
		!observation.NetworkStarted || observation.CompletedAt.IsZero() || observation.CompletedAt.After(now) || observation.Elapsed < 0 {
		return false
	}
	validStatus := observation.StatusCode >= http.StatusOK && observation.StatusCode <= maxHTTPStatus
	switch observation.Outcome {
	case Response:
		return validStatus
	case Timeout, TransportError:
		return observation.StatusCode == 0 || validStatus
	default:
		return false
	}
}

func validRef(value string) bool {
	if len(value) == 0 || len(value) > maxRefBytes {
		return false
	}
	for i, c := range []byte(value) {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || c != '_' && c != '-' && c != '.' && c != ':') {
			return false
		}
	}
	return true
}

func rejected(reason Reason, err error) (Decision, error) {
	return Decision{Recommendation: StopRecommended, ReasonCodes: []Reason{reason}}, err
}
