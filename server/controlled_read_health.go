package server

import (
	"encoding/json"
	"math"
	"time"

	"github.com/Autumn-27/artex/targethealth"
	"github.com/Autumn-27/artex/transmit"
)

type controlledHealthJSON struct {
	WindowMS         *int64 `json:"window_ms"`
	MaxSamples       *int   `json:"max_samples"`
	MinSamples       *int   `json:"min_samples"`
	FailureLimit     *int   `json:"failure_limit"`
	SlowAfterMS      *int64 `json:"slow_after_ms"`
	SlowLimit        *int   `json:"slow_limit"`
	NetworkTimeoutMS *int64 `json:"network_timeout_ms"`
}

func decodeControlledReadHealth(raw json.RawMessage, requestTimeout, minInterval time.Duration, maxRequests uint64) (*transmit.HealthConfig, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var in controlledHealthJSON
	if strictJSON(raw, &in) != nil || in.WindowMS == nil || in.MaxSamples == nil || in.MinSamples == nil || in.FailureLimit == nil || in.SlowAfterMS == nil || in.SlowLimit == nil || in.NetworkTimeoutMS == nil {
		return nil, errControlledReadSnapshot
	}
	for _, value := range []int64{*in.WindowMS, *in.SlowAfterMS, *in.NetworkTimeoutMS} {
		if value < 0 || value > math.MaxInt64/int64(time.Millisecond) {
			return nil, errControlledReadSnapshot
		}
	}
	health := &transmit.HealthConfig{
		Policy: targethealth.Config{
			ScopeRef: "validation", Window: time.Duration(*in.WindowMS) * time.Millisecond,
			MaxSamples: *in.MaxSamples, MinSamples: *in.MinSamples, FailureLimit: *in.FailureLimit,
			SlowAfter: time.Duration(*in.SlowAfterMS) * time.Millisecond, SlowLimit: *in.SlowLimit,
		},
		NetworkTimeout: time.Duration(*in.NetworkTimeoutMS) * time.Millisecond,
	}
	if err := transmit.ValidateHealthConfig(*health, requestTimeout, minInterval); err != nil {
		return nil, errControlledReadSnapshot
	}
	for _, limit := range []int{health.Policy.MinSamples, health.Policy.FailureLimit, health.Policy.SlowLimit} {
		if uint64(limit) > maxRequests {
			return nil, errControlledReadSnapshot
		}
	}
	return health, nil
}

// Validation and execution both copy the policy before assigning their own run
// scope. A pending snapshot must not inherit another transmitter's observations.
func controlledReadHTTPConfig(spec controlledReadSpec, scope transmit.Scope, recorder transmit.Recorder) transmit.Config {
	cfg := spec.HTTP
	cfg.Scope, cfg.Recorder = scope, recorder
	if cfg.Health != nil {
		health := *cfg.Health
		health.Policy.ScopeRef = scope.RunRef
		cfg.Health = &health
	}
	return cfg
}
