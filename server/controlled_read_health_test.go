package server

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/Autumn-27/artex/targethealth"
	"github.com/Autumn-27/artex/transmit"
)

func controlledHealthFields() map[string]any {
	return map[string]any{
		"window_ms": int64(5000), "max_samples": 16, "min_samples": 2,
		"failure_limit": 2, "slow_after_ms": int64(0), "slow_limit": 0,
		"network_timeout_ms": int64(500),
	}
}

func TestControlledReadHealthDecodeExplicitPolicy(t *testing.T) {
	for _, slow := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure only", true: "slow enabled"}[slow], func(t *testing.T) {
			fields := controlledHealthFields()
			if slow {
				fields["slow_after_ms"], fields["slow_limit"] = 100, 2
			}
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			health, err := decodeControlledReadHealth(raw, time.Second, 0, 4)
			if err != nil || health == nil {
				t.Fatalf("decode health: %v", err)
			}
			want := targethealth.Config{ScopeRef: "validation", Window: 5 * time.Second, MaxSamples: 16, MinSamples: 2, FailureLimit: 2}
			if slow {
				want.SlowAfter, want.SlowLimit = 100*time.Millisecond, 2
			}
			if health.Policy != want || health.NetworkTimeout != 500*time.Millisecond {
				t.Fatal("explicit policy changed")
			}
		})
	}
	health, err := decodeControlledReadHealth(nil, time.Second, 0, 1)
	if err != nil || health != nil {
		t.Fatal("omitted health no longer preserves legacy behavior")
	}
}

func TestControlledReadHealthDecodeChecksMinInterval(t *testing.T) {
	fields := controlledHealthFields()
	fields["window_ms"], fields["network_timeout_ms"] = 199, 100
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if health, err := decodeControlledReadHealth(raw, 150*time.Millisecond, 0, 2); err != nil || health == nil {
		t.Fatal("control policy should fit when no minimum interval is configured")
	}
	if health, err := decodeControlledReadHealth(raw, 150*time.Millisecond, 200*time.Millisecond, 2); err != errControlledReadSnapshot || health != nil {
		t.Fatal("decode ignored the configured minimum interval")
	}
}

func TestControlledReadHealthDecodeReservesAuditHeadroom(t *testing.T) {
	fields := controlledHealthFields()
	fields["window_ms"], fields["network_timeout_ms"] = 205, 100
	fields["min_samples"], fields["failure_limit"] = 3, 3
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if health, err := decodeControlledReadHealth(raw, 200*time.Millisecond, 0, 3); err != errControlledReadSnapshot || health != nil {
		t.Fatal("decode accepted a window sized only for network timeouts")
	}
}

func TestControlledReadHealthRejectsMalformedPolicy(t *testing.T) {
	valid := `"max_samples":16,"min_samples":2,"failure_limit":2,"slow_after_ms":0,"slow_limit":0,"network_timeout_ms":500`
	rawCases := []string{
		"null", "{}", "[]", "true", " ",
		`{"window_ms":5000,"window_ms":5000,` + valid + `}`,
		`{"window_ms":5000,"\u0077indow_ms":5000,` + valid + `}`,
		`{} {}`,
	}
	for _, raw := range rawCases {
		t.Run(raw, func(t *testing.T) {
			health, err := decodeControlledReadHealth(json.RawMessage(raw), time.Second, 0, 4)
			if err != errControlledReadSnapshot || health != nil {
				t.Fatal("malformed policy accepted or raw error escaped")
			}
		})
	}
}

func TestControlledReadHealthRequiresEveryField(t *testing.T) {
	for field := range controlledHealthFields() {
		for _, mode := range []string{"missing", "null"} {
			t.Run(field+"/"+mode, func(t *testing.T) {
				fields := controlledHealthFields()
				if mode == "missing" {
					delete(fields, field)
				} else {
					fields[field] = nil
				}
				raw, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				health, err := decodeControlledReadHealth(raw, time.Second, 0, 4)
				if err != errControlledReadSnapshot || health != nil {
					t.Fatal("missing/null policy field silently defaulted")
				}
			})
		}
	}
}

func TestControlledReadHealthRejectsUnsafePolicy(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       any
		budget      uint64
	}{
		{"negative window", "window_ms", -1, 4},
		{"zero window", "window_ms", 0, 4},
		{"window overflow", "window_ms", int64(math.MaxInt64)/int64(time.Millisecond) + 1, 4},
		{"network overflow", "network_timeout_ms", int64(math.MaxInt64)/int64(time.Millisecond) + 1, 4},
		{"slow overflow", "slow_after_ms", int64(math.MaxInt64)/int64(time.Millisecond) + 1, 4},
		{"network negative", "network_timeout_ms", -1, 4},
		{"network zero", "network_timeout_ms", 0, 4},
		{"network outer deadline", "network_timeout_ms", 1000, 4},
		{"network beyond outer deadline", "network_timeout_ms", 1001, 4},
		{"slow negative", "slow_after_ms", -1, 4},
		{"slow network deadline", "slow_after_ms", 500, 4},
		{"slow without threshold", "slow_after_ms", 100, 4},
		{"threshold without slow", "slow_limit", 2, 4},
		{"sample ceiling", "max_samples", targethealth.MaxWindowSamples + 1, 4},
		{"sample count negative", "max_samples", -1, 4},
		{"sample count overflow", "max_samples", json.Number("9223372036854775808"), 4},
		{"too few samples", "min_samples", 1, 4},
		{"sample limit", "min_samples", 17, 32},
		{"too few failures", "failure_limit", 1, 4},
		{"failure limit", "failure_limit", 17, 32},
		{"unreachable samples", "min_samples", 3, 2},
		{"unreachable failures", "failure_limit", 3, 2},
		{"unreachable slow", "slow_limit", 3, 2},
		{"minimum exceeds failure threshold", "min_samples", 3, 4},
		{"minimum exceeds slow threshold", "min_samples", 3, 4},
		{"failure threshold cannot fit window", "failure_limit", 4, 4},
		{"insufficient budget", "max_samples", 16, 1},
		{"fraction", "min_samples", 2.5, 4},
		{"string", "window_ms", "5000", 4},
		{"unknown field", "unexpected", true, 4},
		{"caller scope", "scope_ref", "foreign-run", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := controlledHealthFields()
			fields[tc.field] = tc.value
			if tc.name == "unreachable slow" {
				fields["slow_after_ms"] = 100
			}
			if tc.name == "minimum exceeds failure threshold" {
				fields["failure_limit"] = 2
			}
			if tc.name == "minimum exceeds slow threshold" {
				fields["slow_after_ms"], fields["slow_limit"], fields["failure_limit"] = 100, 2, 3
			}
			if tc.name == "failure threshold cannot fit window" {
				fields["window_ms"], fields["network_timeout_ms"] = 250, 100
				fields["min_samples"] = 2
			}
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			health, err := decodeControlledReadHealth(raw, time.Second, 0, tc.budget)
			if err != errControlledReadSnapshot || health != nil {
				t.Fatal("unsafe policy accepted or raw error escaped")
			}
		})
	}
}

func TestControlledReadHealthConfigCopiesRunScope(t *testing.T) {
	original := &transmit.HealthConfig{Policy: targethealth.Config{ScopeRef: "snapshot-only", Window: 5 * time.Second, MaxSamples: 16, MinSamples: 2, FailureLimit: 2}, NetworkTimeout: 500 * time.Millisecond}
	before := *original
	spec := controlledReadSpec{HTTP: transmit.Config{Health: original, RequestTimeout: time.Second}}
	scopes := []transmit.Scope{{RunRef: "validation"}, {RunRef: "run-one"}, {RunRef: "run-two"}}
	var configs []transmit.Config
	for _, scope := range scopes {
		recorder := controlledReadValidationRecorder{}
		cfg := controlledReadHTTPConfig(spec, scope, recorder)
		if cfg.Health == original || cfg.Scope != scope || cfg.Health.Policy.ScopeRef != scope.RunRef || cfg.Recorder != recorder {
			t.Fatal("scope not bound to independent config")
		}
		configs = append(configs, cfg)
	}
	configs[1].Health.Policy.FailureLimit = 9
	if *original != before || configs[0].Health.Policy.FailureLimit != 2 || configs[2].Health.Policy.FailureLimit != 2 {
		t.Fatal("run mutation modified another policy")
	}
	spec.HTTP.Health = nil
	if cfg := controlledReadHTTPConfig(spec, scopes[0], controlledReadValidationRecorder{}); cfg.Health != nil {
		t.Fatal("omitted policy enabled by scope binding")
	}
}
