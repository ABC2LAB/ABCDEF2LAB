package targethealth

import (
	"errors"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

var observedNow = time.Date(2026, time.October, 30, 12, 0, 0, 0, time.UTC)

func testConfig() Config {
	return Config{ScopeRef: "target-execution:one", Window: time.Minute, MaxSamples: 10,
		MinSamples: 3, FailureLimit: 3, SlowAfter: 2 * time.Second, SlowLimit: 3}
}

func response(attempt uint64, status int) Observation {
	return Observation{ScopeRef: testConfig().ScopeRef, Attempt: attempt, ResourceRef: "document:one",
		NetworkStarted: true, CompletedAt: observedNow, Elapsed: time.Millisecond,
		Outcome: Response, StatusCode: status}
}

func failure(attempt uint64, outcome Outcome) Observation {
	o := response(attempt, 0)
	o.Outcome = outcome
	return o
}

func TestEvaluateSignals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   []Observation
		mutate  func([]Observation)
		want    Recommendation
		reasons []Reason
		counts  Counts
	}{
		{name: "normal", input: []Observation{response(1, 200), response(2, 201), response(3, 204)},
			want: Continue, reasons: []Reason{BelowThreshold}, counts: Counts{Samples: 3, DistinctResources: 1}},
		{name: "ordinary denials and missing resources", input: []Observation{response(1, 401), response(2, 403), response(3, 404)},
			want: Continue, reasons: []Reason{BelowThreshold}, counts: Counts{Samples: 3, DistinctResources: 1}},
		{name: "redirects and other client errors", input: []Observation{response(1, 302), response(2, 408), response(3, 422)},
			want: Continue, reasons: []Reason{BelowThreshold}, counts: Counts{Samples: 3, DistinctResources: 1}},
		{name: "mixed failures reach limit", input: []Observation{failure(1, Timeout), failure(2, TransportError), response(3, 429), response(4, 503)},
			want: StopRecommended, reasons: []Reason{FailureThreshold},
			counts: Counts{Samples: 4, Failures: 4, Timeouts: 1, TransportErrors: 1, RateLimited: 1, ServerErrors: 1, DistinctResources: 1}},
		{name: "repeated timeouts", input: []Observation{failure(1, Timeout), failure(2, Timeout), failure(3, Timeout)},
			want: StopRecommended, reasons: []Reason{FailureThreshold}, counts: Counts{Samples: 3, Failures: 3, Timeouts: 3, DistinctResources: 1}},
		{name: "repeated rate limiting", input: []Observation{response(1, 429), response(2, 429), response(3, 429)},
			want: StopRecommended, reasons: []Reason{FailureThreshold}, counts: Counts{Samples: 3, Failures: 3, RateLimited: 3, DistinctResources: 1}},
		{name: "server error range", input: []Observation{response(1, 500), response(2, 503), response(3, 599)},
			want: StopRecommended, reasons: []Reason{FailureThreshold}, counts: Counts{Samples: 3, Failures: 3, ServerErrors: 3, DistinctResources: 1}},
		{name: "slow successful responses", input: []Observation{response(1, 200), response(2, 200), response(3, 200)},
			mutate: func(in []Observation) {
				for i := range in {
					in[i].Elapsed = 2 * time.Second
				}
			},
			want: StopRecommended, reasons: []Reason{SlowThreshold}, counts: Counts{Samples: 3, SlowResponses: 3, DistinctResources: 1}},
		{name: "slow 5xx counts both signals in stable order", input: []Observation{response(1, 503), response(2, 503), response(3, 503)},
			mutate: func(in []Observation) {
				for i := range in {
					in[i].Elapsed = 3 * time.Second
				}
			},
			want: StopRecommended, reasons: []Reason{FailureThreshold, SlowThreshold},
			counts: Counts{Samples: 3, Failures: 3, ServerErrors: 3, SlowResponses: 3, DistinctResources: 1}},
		{name: "slow denials remain latency signals", input: []Observation{response(1, 403), response(2, 403), response(3, 404)},
			mutate: func(in []Observation) {
				for i := range in {
					in[i].Elapsed = 3 * time.Second
				}
			},
			want: StopRecommended, reasons: []Reason{SlowThreshold}, counts: Counts{Samples: 3, SlowResponses: 3, DistinctResources: 1}},
		{name: "body timeout with headers counts once", input: []Observation{failure(1, Timeout), failure(2, TransportError), failure(3, Timeout)},
			mutate: func(in []Observation) {
				for i := range in {
					in[i].StatusCode, in[i].Elapsed = 503, 3*time.Second
				}
			},
			want: StopRecommended, reasons: []Reason{FailureThreshold},
			counts: Counts{Samples: 3, Failures: 3, Timeouts: 2, TransportErrors: 1, DistinctResources: 1}},
		{name: "distinct resources counted without requiring breadth", input: []Observation{response(1, 500), response(2, 500), response(3, 500)},
			mutate: func(in []Observation) { in[2].ResourceRef = "document:two" },
			want:   StopRecommended, reasons: []Reason{FailureThreshold}, counts: Counts{Samples: 3, Failures: 3, ServerErrors: 3, DistinctResources: 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.mutate != nil {
				tt.mutate(tt.input)
			}
			got, err := Evaluate(testConfig(), tt.input, observedNow)
			if err != nil || got.Recommendation != tt.want || !reflect.DeepEqual(got.ReasonCodes, tt.reasons) || got.Counts != tt.counts {
				t.Fatalf("Evaluate = %+v, %v; want %s, %v, %+v", got, err, tt.want, tt.reasons, tt.counts)
			}
		})
	}
}

func TestEvaluateMinimumSamplesAndIsolatedFailures(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		input  []Observation
		reason Reason
	}{
		{name: "no samples", reason: InsufficientSamples},
		{name: "one failure", input: []Observation{failure(1, Timeout)}, reason: InsufficientSamples},
		{name: "below sample minimum", input: []Observation{failure(1, Timeout), failure(2, Timeout)}, reason: InsufficientSamples},
		{name: "isolated 5xx", input: []Observation{response(1, 200), response(2, 500), response(3, 200)}, reason: BelowThreshold},
		{name: "two failures below configured limit", input: []Observation{response(1, 503), response(2, 429), response(3, 200)}, reason: BelowThreshold},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Evaluate(testConfig(), tt.input, observedNow)
			if err != nil || got.Recommendation != Continue || !reflect.DeepEqual(got.ReasonCodes, []Reason{tt.reason}) {
				t.Fatalf("Evaluate = %+v, %v", got, err)
			}
		})
	}
	// Even a configured failure limit does not bypass the explicit sample minimum.
	cfg := testConfig()
	cfg.FailureLimit = 2
	got, err := Evaluate(cfg, []Observation{failure(1, Timeout), failure(2, Timeout)}, observedNow)
	if err != nil || got.Recommendation != Continue || got.ReasonCodes[0] != InsufficientSamples || got.Counts.Failures != 2 {
		t.Fatalf("sample minimum bypassed: %+v, %v", got, err)
	}
}

func TestEvaluateConfiguredLimits(t *testing.T) {
	t.Parallel()
	t.Run("failure limit differs from sample minimum", func(t *testing.T) {
		cfg := testConfig()
		cfg.FailureLimit = 2
		got, err := Evaluate(cfg, []Observation{response(1, 200), response(2, 503), response(3, 429)}, observedNow)
		if err != nil || got.Recommendation != StopRecommended || !reflect.DeepEqual(got.ReasonCodes, []Reason{FailureThreshold}) || got.Counts.Failures != 2 {
			t.Fatalf("configured failure limit = %+v, %v", got, err)
		}
	})
	t.Run("independent slow limit", func(t *testing.T) {
		cfg := testConfig()
		cfg.SlowLimit = 2
		input := []Observation{response(1, 503), response(2, 200), response(3, 200)}
		input[0].Elapsed, input[2].Elapsed = cfg.SlowAfter, cfg.SlowAfter
		got, err := Evaluate(cfg, input, observedNow)
		if err != nil || got.Recommendation != StopRecommended || !reflect.DeepEqual(got.ReasonCodes, []Reason{SlowThreshold}) || got.Counts.Failures != 1 || got.Counts.SlowResponses != 2 {
			t.Fatalf("configured slow limit = %+v, %v", got, err)
		}
	})
	t.Run("smallest permitted window capacity", func(t *testing.T) {
		cfg := testConfig()
		cfg.MaxSamples, cfg.MinSamples, cfg.FailureLimit, cfg.SlowLimit = 2, 2, 2, 2
		got, err := Evaluate(cfg, []Observation{failure(1, Timeout), failure(2, TransportError)}, observedNow)
		if err != nil || got.Recommendation != StopRecommended || got.Counts.Samples != 2 {
			t.Fatalf("minimum valid configuration = %+v, %v", got, err)
		}
	})
}

func TestEvaluateWindowBoundaries(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		age     time.Duration
		samples int
	}{
		{name: "at now", samples: 1},
		{name: "inside lower edge", age: time.Minute - time.Nanosecond, samples: 1},
		{name: "exact lower edge excluded", age: time.Minute},
		{name: "older excluded", age: time.Minute + time.Nanosecond},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := response(1, 503)
			o.CompletedAt = observedNow.Add(-tt.age)
			got, err := Evaluate(testConfig(), []Observation{o}, observedNow)
			if err != nil || got.Counts.Samples != tt.samples || got.Counts.Failures != tt.samples || got.Counts.DistinctResources != tt.samples {
				t.Fatalf("window counts = %+v, %v", got, err)
			}
		})
	}
}

func TestEvaluateLatencyBoundariesAndDisable(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		elapsed  time.Duration
		disabled bool
		slow     int
	}{
		{name: "zero latency"},
		{name: "below threshold", elapsed: 2*time.Second - time.Nanosecond},
		{name: "equal threshold", elapsed: 2 * time.Second, slow: 3},
		{name: "above threshold", elapsed: 2*time.Second + time.Nanosecond, slow: 3},
		{name: "latency disabled", elapsed: time.Hour, disabled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			if tt.disabled {
				cfg.SlowAfter, cfg.SlowLimit = 0, 0
			}
			input := []Observation{response(1, 200), response(2, 200), response(3, 200)}
			for i := range input {
				input[i].Elapsed = tt.elapsed
			}
			got, err := Evaluate(cfg, input, observedNow)
			want := Continue
			if tt.slow == 3 {
				want = StopRecommended
			}
			if err != nil || got.Counts.SlowResponses != tt.slow || got.Recommendation != want {
				t.Fatalf("latency = %+v, %v", got, err)
			}
		})
	}
}

func TestEvaluateRejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"scope missing", func(c *Config) { c.ScopeRef = "" }},
		{"scope is a URL", func(c *Config) { c.ScopeRef = "https://target.invalid" }},
		{"scope too long", func(c *Config) { c.ScopeRef = strings.Repeat("x", maxRefBytes+1) }},
		{"zero window", func(c *Config) { c.Window = 0 }},
		{"negative window", func(c *Config) { c.Window = -1 }},
		{"zero max", func(c *Config) { c.MaxSamples = 0 }},
		{"negative max", func(c *Config) { c.MaxSamples = -1 }},
		{"single sample max", func(c *Config) { c.MaxSamples = 1 }},
		{"max over hard cap", func(c *Config) { c.MaxSamples = MaxWindowSamples + 1 }},
		{"zero minimum", func(c *Config) { c.MinSamples = 0 }},
		{"single sample minimum", func(c *Config) { c.MinSamples = 1 }},
		{"minimum above max", func(c *Config) { c.MinSamples = c.MaxSamples + 1 }},
		{"zero failure limit", func(c *Config) { c.FailureLimit = 0 }},
		{"single failure limit", func(c *Config) { c.FailureLimit = 1 }},
		{"failure limit above max", func(c *Config) { c.FailureLimit = c.MaxSamples + 1 }},
		{"negative slow duration", func(c *Config) { c.SlowAfter = -1 }},
		{"disabled slow duration but active limit", func(c *Config) { c.SlowAfter = 0 }},
		{"enabled slow duration but zero limit", func(c *Config) { c.SlowLimit = 0 }},
		{"single slow limit", func(c *Config) { c.SlowLimit = 1 }},
		{"slow limit above max", func(c *Config) { c.SlowLimit = c.MaxSamples + 1 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			tt.mutate(&cfg)
			got, err := Evaluate(cfg, nil, observedNow)
			assertRejected(t, got, err, ErrConfig, InvalidConfig)
		})
	}
}

func TestEvaluateRejectsInvalidObservations(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		mutate func(*Observation)
	}{
		{"scope missing", func(o *Observation) { o.ScopeRef = "" }},
		{"another execution", func(o *Observation) { o.ScopeRef = "target-execution:two" }},
		{"zero attempt", func(o *Observation) { o.Attempt = 0 }},
		{"resource missing", func(o *Observation) { o.ResourceRef = "" }},
		{"resource contains payload", func(o *Observation) { o.ResourceRef = "resource\nprivate" }},
		{"resource starts with separator", func(o *Observation) { o.ResourceRef = ":resource" }},
		{"resource too long", func(o *Observation) { o.ResourceRef = strings.Repeat("x", maxRefBytes+1) }},
		{"resource contains unicode", func(o *Observation) { o.ResourceRef = "자료" }},
		{"queue timeout is not network evidence", func(o *Observation) { o.NetworkStarted = false; o.Outcome = Timeout; o.StatusCode = 0 }},
		{"zero time", func(o *Observation) { o.CompletedAt = time.Time{} }},
		{"future time", func(o *Observation) { o.CompletedAt = observedNow.Add(time.Nanosecond) }},
		{"negative duration", func(o *Observation) { o.Elapsed = -1 }},
		{"missing outcome", func(o *Observation) { o.Outcome = "" }},
		{"unknown outcome", func(o *Observation) { o.Outcome = "canceled" }},
		{"response without status", func(o *Observation) { o.StatusCode = 0 }},
		{"interim status", func(o *Observation) { o.StatusCode = 199 }},
		{"status over range", func(o *Observation) { o.StatusCode = 600 }},
		{"negative status", func(o *Observation) { o.StatusCode = -1 }},
		{"timeout invalid status", func(o *Observation) { o.Outcome = Timeout; o.StatusCode = 100 }},
		{"transport invalid status", func(o *Observation) { o.Outcome = TransportError; o.StatusCode = 600 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := response(1, 200)
			tt.mutate(&o)
			got, err := Evaluate(testConfig(), []Observation{o}, observedNow)
			assertRejected(t, got, err, ErrObservation, InvalidObservation)
		})
	}
}

func assertRejected(t *testing.T, got Decision, err, wantErr error, reason Reason) {
	t.Helper()
	if !errors.Is(err, wantErr) || got.Recommendation != StopRecommended || !reflect.DeepEqual(got.ReasonCodes, []Reason{reason}) || got.Counts != (Counts{}) {
		t.Fatalf("invalid input assessment = %+v, %v; want %v, %s with zero counts", got, err, wantErr, reason)
	}
}

func TestEvaluateRejectsInvalidBatch(t *testing.T) {
	t.Parallel()
	t.Run("zero clock", func(t *testing.T) {
		got, err := Evaluate(testConfig(), nil, time.Time{})
		assertRejected(t, got, err, ErrObservation, InvalidObservation)
	})
	t.Run("duplicate attempts across resources", func(t *testing.T) {
		a, b := response(1, 200), response(1, 503)
		b.ResourceRef = "document:two"
		got, err := Evaluate(testConfig(), []Observation{a, b}, observedNow)
		assertRejected(t, got, err, ErrObservation, DuplicateAttempt)
	})
	t.Run("expired malformed sample is not hidden", func(t *testing.T) {
		o := response(4, 200)
		o.CompletedAt, o.NetworkStarted = observedNow.Add(-time.Hour), false
		got, err := Evaluate(testConfig(), []Observation{response(1, 503), response(2, 503), response(3, 503), o}, observedNow)
		assertRejected(t, got, err, ErrObservation, InvalidObservation)
	})
	t.Run("duplicate expired sample", func(t *testing.T) {
		o := response(1, 200)
		o.CompletedAt = observedNow.Add(-time.Hour)
		got, err := Evaluate(testConfig(), []Observation{response(1, 200), o}, observedNow)
		assertRejected(t, got, err, ErrObservation, DuplicateAttempt)
	})
	t.Run("over max observations", func(t *testing.T) {
		input := make([]Observation, testConfig().MaxSamples+1)
		for i := range input {
			input[i] = response(uint64(i+1), 200)
		}
		got, err := Evaluate(testConfig(), input, observedNow)
		assertRejected(t, got, err, ErrObservation, TooManyObservations)
	})
	t.Run("maximum batch accepted", func(t *testing.T) {
		cfg := testConfig()
		cfg.MaxSamples = MaxWindowSamples
		input := make([]Observation, cfg.MaxSamples)
		for i := range input {
			input[i] = response(uint64(i+1), 200)
		}
		got, err := Evaluate(cfg, input, observedNow)
		if err != nil || got.Recommendation != Continue || got.Counts.Samples != cfg.MaxSamples {
			t.Fatalf("max batch = %+v, %v", got, err)
		}
	})
}

func TestEvaluateDoesNotLatchOrAuthorizeResume(t *testing.T) {
	t.Parallel()
	input := []Observation{response(1, 503), response(2, 503), response(3, 503)}
	got, err := Evaluate(testConfig(), input, observedNow)
	if err != nil || got.Recommendation != StopRecommended {
		t.Fatalf("initial = %+v, %v", got, err)
	}
	// The same evidence ages out. The pure function has no memory of a stop;
	// a future engine adapter must retain the stop until explicitly resumed.
	got, err = Evaluate(testConfig(), input, observedNow.Add(time.Minute))
	if err != nil || got.Recommendation != Continue || got.ReasonCodes[0] != InsufficientSamples || got.Counts != (Counts{}) {
		t.Fatalf("expired observations = %+v, %v", got, err)
	}
}

func TestEvaluateOrderIndependentAndInputUnchanged(t *testing.T) {
	t.Parallel()
	input := []Observation{response(9, 200), failure(3, Timeout), response(1, 503), response(7, 429)}
	input[0].CompletedAt = observedNow.Add(-time.Hour)
	input[3].ResourceRef = "document:two"
	before := append([]Observation(nil), input...)
	want, err := Evaluate(testConfig(), input, observedNow)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatal("input mutated")
	}
	rng := rand.New(rand.NewSource(1))
	for range 20 {
		rng.Shuffle(len(input), func(i, j int) { input[i], input[j] = input[j], input[i] })
		got, err := Evaluate(testConfig(), input, observedNow)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("order affected decision: %+v, %v; want %+v", got, err, want)
		}
	}
	// The result owns its reason slice and cannot change another evaluation.
	want.ReasonCodes[0] = InvalidConfig
	got, err := Evaluate(testConfig(), input, observedNow)
	if err != nil || got.ReasonCodes[0] != FailureThreshold {
		t.Fatalf("result alias: %+v, %v", got, err)
	}
}

func FuzzEvaluateOrderIndependent(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		cfg := testConfig()
		if len(data) > cfg.MaxSamples {
			data = data[:cfg.MaxSamples]
		}
		input := make([]Observation, len(data))
		for i, b := range data {
			input[i] = response(uint64(i+1), []int{200, 403, 404, 429, 500}[int(b)%5])
			input[i].Elapsed = time.Duration(b%5) * time.Second
			input[i].CompletedAt = observedNow.Add(-time.Duration(b%90) * time.Second)
			if b%7 == 0 {
				input[i].Outcome, input[i].StatusCode = Timeout, 0
			}
		}
		want, err := Evaluate(cfg, input, observedNow)
		if err != nil {
			t.Fatal(err)
		}
		for i, j := 0, len(input)-1; i < j; i, j = i+1, j-1 {
			input[i], input[j] = input[j], input[i]
		}
		got, err := Evaluate(cfg, input, observedNow)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("order-dependent decision: %+v, %v; want %+v", got, err, want)
		}
	})
}
