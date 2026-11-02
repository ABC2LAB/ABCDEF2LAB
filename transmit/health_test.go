package transmit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/targethealth"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

func healthPolicy() *transmit.HealthConfig {
	return &transmit.HealthConfig{Policy: targethealth.Config{
		ScopeRef: "http-execution:one", Window: 5 * time.Second, MaxSamples: 16, MinSamples: 2, FailureLimit: 2,
	}, NetworkTimeout: 500 * time.Millisecond}
}

func healthTransmitter(t *testing.T, ctx context.Context, url string, rec transmit.Recorder, configure func(*transmit.Config)) *transmit.HTTPTransmitter {
	t.Helper()
	cfg := transportConfig(url, rec)
	cfg.Health = healthPolicy()
	if configure != nil {
		configure(&cfg)
	}
	tx, err := transmit.NewHTTP(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tx.StopAndWait)
	return tx
}

func TestHealthHTTPStatusThresholdAndStickyStop(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.WriteHeader(status)
			}))
			defer server.Close()
			tx := healthTransmitter(t, context.Background(), server.URL, &recorder{}, nil)
			for range 2 {
				_, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
				if !ok || receipt.Reason != transmit.Completed || receipt.StatusCode != status {
					t.Fatalf("threshold request changed its receipt: %+v", receipt)
				}
			}
			state := tx.HealthStatus()
			if !state.Halted || !state.InputValid || state.Decision.Counts.Failures != 2 || !reflect.DeepEqual(state.Decision.ReasonCodes, []targethealth.Reason{targethealth.FailureThreshold}) {
				t.Fatalf("health did not halt: %+v", state)
			}
			for range 3 {
				_, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
				if ok || receipt.Admitted || receipt.Reason != transmit.Stopped {
					t.Fatalf("send after health halt: %+v", receipt)
				}
			}
			if hits.Load() != 2 || !reflect.DeepEqual(state, tx.HealthStatus()) {
				t.Fatal("halt was reset or more target requests were sent")
			}
		})
	}
}

func TestHealthConcurrentQueuedSendersStopAtThreshold(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	rec := &recorder{}
	tx := healthTransmitter(t, context.Background(), server.URL, rec, func(c *transmit.Config) { c.MaxRequests = 20 })
	var wg sync.WaitGroup
	var completed, stopped atomic.Int32
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, r, _ := tx.SendDetailed(transportRequest("owner", "h-owner"))
			if r.Reason == transmit.Completed {
				completed.Add(1)
			}
			if r.Reason == transmit.Stopped && !r.Admitted {
				stopped.Add(1)
			}
		}()
	}
	wg.Wait()
	tx.StopAndWait()
	if hits.Load() != 2 || completed.Load() != 2 || stopped.Load() != 10 || !tx.HealthStatus().Halted {
		t.Fatalf("queued admission escaped halt: hits=%d complete=%d stop=%d", hits.Load(), completed.Load(), stopped.Load())
	}
}

func TestHealthNetworkTimeoutsIncludeHeaderAndBodyWait(t *testing.T) {
	for _, flush := range []bool{false, true} {
		t.Run(map[bool]string{false: "headers", true: "body"}[flush], func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if flush {
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			tx := healthTransmitter(t, context.Background(), server.URL, &recorder{}, func(c *transmit.Config) {
				c.Health.NetworkTimeout = 100 * time.Millisecond
			})
			for range 2 {
				_, r, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
				if ok || r.Reason != transmit.TimedOut || !r.Admitted {
					t.Fatalf("network deadline outcome: %+v", r)
				}
			}
			state := tx.HealthStatus()
			_, r, _ := tx.SendDetailed(transportRequest("owner", "h-owner"))
			if !state.Halted || state.Decision.Counts.Timeouts != 2 || r.Reason != transmit.Stopped || hits.Load() != 2 {
				t.Fatalf("network timeouts failed to stop: %+v, hits=%d", state, hits.Load())
			}
		})
	}
}

func TestHealthRealTransportFailureStops(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	tx := healthTransmitter(t, context.Background(), server.URL, &recorder{}, nil)
	for range 2 {
		_, r, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
		if ok || r.Reason != transmit.TransportError {
			t.Fatalf("transport outcome: %+v", r)
		}
	}
	if state := tx.HealthStatus(); !state.Halted || state.Decision.Counts.TransportErrors != 2 || hits.Load() != 2 {
		t.Fatalf("transport errors: %+v, hits=%d", state, hits.Load())
	}
}

func TestHealthSlowResponsesAndNormalDenials(t *testing.T) {
	for _, slow := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary denials", true: "slow responses"}[slow], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if slow {
					time.Sleep(60 * time.Millisecond)
				}
				w.WriteHeader(http.StatusForbidden)
			}))
			defer server.Close()
			tx := healthTransmitter(t, context.Background(), server.URL, &recorder{}, func(c *transmit.Config) {
				if slow {
					c.Health.Policy.SlowAfter, c.Health.Policy.SlowLimit = 20*time.Millisecond, 2
				}
			})
			for range 2 {
				tx.SendDetailed(transportRequest("owner", "h-owner"))
			}
			state := tx.HealthStatus()
			if state.Halted != slow || state.Decision.Counts.Failures != 0 || state.Decision.Counts.Samples != 2 {
				t.Fatalf("normal/slow distinction: %+v", state)
			}
			if slow && !reflect.DeepEqual(state.Decision.ReasonCodes, []targethealth.Reason{targethealth.SlowThreshold}) {
				t.Fatal("missing latency reason")
			}
		})
	}
}

func TestHealthCountsErrorStatusEvenWhenBodyPolicyRefusesIt(t *testing.T) {
	for _, kind := range []string{"encoding", "body limit"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if kind == "encoding" {
					w.Header().Set("Content-Encoding", "unsupported")
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(strings.Repeat("x", 128)))
			}))
			defer server.Close()
			tx := healthTransmitter(t, context.Background(), server.URL, &recorder{}, func(c *transmit.Config) { c.MaxBodyBytes = 32 })
			for range 2 {
				tx.SendDetailed(transportRequest("owner", "h-owner"))
			}
			if state := tx.HealthStatus(); !state.Halted || state.Decision.Counts.ServerErrors != 2 {
				t.Fatalf("known error status omitted: %+v", state)
			}
		})
	}
}

type healthRecorder struct {
	hook func(context.Context, transmit.Event) error
	base recorder
}

func (r *healthRecorder) Record(ctx context.Context, event transmit.Event) error {
	if r.hook != nil {
		if err := r.hook(ctx, event); err != nil {
			return err
		}
	}
	return r.base.Record(ctx, event)
}

func TestHealthExcludesAuditAndRateWaitFromLatency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	rec := &healthRecorder{hook: func(ctx context.Context, _ transmit.Event) error {
		timer := time.NewTimer(300 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	tx := healthTransmitter(t, context.Background(), server.URL, rec, func(c *transmit.Config) {
		c.RequestTimeout = 3 * time.Second
		c.MinInterval = 800 * time.Millisecond
		c.Health.NetworkTimeout = time.Second
		c.Health.Policy.SlowAfter, c.Health.Policy.SlowLimit = 250*time.Millisecond, 2
	})
	for range 2 {
		if _, r, ok := tx.SendDetailed(transportRequest("owner", "h-owner")); !ok || r.Reason != transmit.Completed {
			t.Fatalf("delayed audit: %+v", r)
		}
	}
	if state := tx.HealthStatus(); state.Halted || state.Decision.Counts.SlowResponses != 0 || state.Decision.Counts.Samples != 2 {
		t.Fatalf("local waits became target latency: %+v", state)
	}
}

func TestHealthExcludesOuterDeadlineAfterIntentAudit(t *testing.T) {
	// Intent audit consumes part of the outer request deadline. The remaining
	// network budget must not be reported as a target-scoped timeout.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	rec := &healthRecorder{hook: func(ctx context.Context, event transmit.Event) error {
		if event.Stage != "intent" {
			return nil
		}
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	tx := healthTransmitter(t, context.Background(), server.URL, rec, func(c *transmit.Config) {
		c.RequestTimeout = 400 * time.Millisecond
		c.Health.NetworkTimeout = 300 * time.Millisecond
	})
	_, r, _ := tx.SendDetailed(transportRequest("owner", "h-owner"))
	// Scheduling can exhaust the outer deadline during dial or headers rather
	// than body read. None of those local deadline paths is target degradation.
	if r.Reason != transmit.TimedOut || tx.HealthStatus().Decision.Counts.Samples != 0 || tx.HealthStatus().Halted {
		t.Fatalf("outer deadline counted as health timeout: %+v, %+v", r, tx.HealthStatus())
	}
}

func TestHealthExcludesRateWaitTimeout(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer server.Close()
	tx := healthTransmitter(t, context.Background(), server.URL, &recorder{}, func(c *transmit.Config) {
		c.RequestTimeout, c.MinInterval = 600*time.Millisecond, 2*time.Second
		c.Health.NetworkTimeout = 300 * time.Millisecond
	})
	tx.SendDetailed(transportRequest("owner", "h-owner"))
	_, r, _ := tx.SendDetailed(transportRequest("owner", "h-owner"))
	state := tx.HealthStatus()
	if r.Reason != transmit.TimedOut || r.Admitted || state.Halted || state.Decision.Counts.Samples != 1 || hits.Load() != 1 {
		t.Fatalf("rate wait became health failure: %+v, %+v, hits=%d", r, state, hits.Load())
	}
}

func TestHealthParentCancelStopAndExpiryAreNotTargetFailures(t *testing.T) {
	for _, cause := range []string{"parent", "stop", "approval", "session"} {
		t.Run(cause, func(t *testing.T) {
			entered := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.(http.Flusher).Flush()
				close(entered)
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tx := healthTransmitter(t, ctx, server.URL, &recorder{}, func(c *transmit.Config) {
				if cause == "approval" {
					c.ExpiresAt = time.Now().Add(150 * time.Millisecond)
				}
				if cause == "session" {
					c.Sessions[0].ExpiresAt = time.Now().Add(150 * time.Millisecond)
				}
			})
			done := make(chan transmit.Receipt, 1)
			go func() { _, r, _ := tx.SendDetailed(transportRequest("owner", "h-owner")); done <- r }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("request did not reach target")
			}
			if cause == "parent" {
				cancel()
			}
			if cause == "stop" {
				tx.Stop()
			}
			select {
			case r := <-done:
				if r.Reason == transmit.Completed {
					t.Fatal("canceled request completed")
				}
			case <-time.After(time.Second):
				t.Fatal("canceled request stuck")
			}
			if state := tx.HealthStatus(); state.Halted || state.Decision.Counts.Samples != 0 {
				t.Fatalf("local cancellation became target failure: %+v", state)
			}
		})
	}
}

func TestHealthAuditFailuresDoNotBecomeTransportFailures(t *testing.T) {
	for _, stage := range []string{"intent", "result"} {
		t.Run(stage, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
			defer server.Close()
			tx := healthTransmitter(t, context.Background(), server.URL, &recorder{failAt: stage}, nil)
			_, r, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
			state := tx.HealthStatus()
			if ok || r.Reason != transmit.AuditUnavailable || state.Halted || state.Decision.Counts.Failures != 0 {
				t.Fatalf("audit failure became target failure: %+v, %+v", r, state)
			}
		})
	}
}

func TestHealthInvalidPolicyRejectedAndConfigCopied(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	for _, tt := range []struct {
		name   string
		mutate func(*transmit.Config)
	}{
		{"zero network timeout", func(c *transmit.Config) { c.Health.NetworkTimeout = 0 }},
		{"outer timeout tie", func(c *transmit.Config) { c.Health.NetworkTimeout = c.RequestTimeout }},
		{"network timeout over outer", func(c *transmit.Config) { c.Health.NetworkTimeout = c.RequestTimeout + time.Second }},
		{"unreachable slow threshold", func(c *transmit.Config) {
			c.Health.Policy.SlowAfter, c.Health.Policy.SlowLimit = c.Health.NetworkTimeout, 2
		}},
		{"invalid policy", func(c *transmit.Config) { c.Health.Policy.MinSamples = 1 }},
		{"invalid scope alias", func(c *transmit.Config) { c.Health.Policy.ScopeRef = "not/a/health/ref" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := transportConfig(server.URL, &recorder{})
			cfg.Health = healthPolicy()
			tt.mutate(&cfg)
			if _, err := transmit.NewHTTP(context.Background(), cfg); err == nil {
				t.Fatal("invalid health config accepted")
			}
		})
	}
	cfg := transportConfig(server.URL, &recorder{})
	cfg.Health = healthPolicy()
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.StopAndWait()
	cfg.Health.Policy.ScopeRef, cfg.Health.Policy.MinSamples, cfg.Health.NetworkTimeout = "changed", 1, time.Nanosecond
	tx.SendDetailed(transportRequest("owner", "h-owner"))
	state := tx.HealthStatus()
	if !state.InputValid || state.Halted || state.Decision.Counts.Samples != 1 || state.Decision.Counts.Failures != 0 {
		t.Fatalf("mutable config reached transmitter: %+v", state)
	}
	state.Decision.ReasonCodes[0] = targethealth.InvalidConfig
	if tx.HealthStatus().Decision.ReasonCodes[0] != targethealth.InsufficientSamples {
		t.Fatal("snapshot reason slice is aliased")
	}
	encoded, err := json.Marshal(tx.HealthStatus())
	if err != nil || strings.Contains(string(encoded), "basket/42") || strings.Contains(string(encoded), server.URL) || strings.Contains(string(encoded), "http-execution") {
		t.Fatal("status exposed identifiers")
	}
}

func TestHealthLocalBudgetRejectionIsNotAnObservation(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	tx := healthTransmitter(t, context.Background(), server.URL, &recorder{}, func(c *transmit.Config) { c.MaxRequests = 1 })
	tx.SendDetailed(transportRequest("owner", "h-owner"))
	_, r, _ := tx.SendDetailed(transportRequest("owner", "h-owner"))
	if r.Reason != transmit.BudgetExhausted || tx.HealthStatus().Decision.Counts.Samples != 1 || tx.HealthStatus().Halted {
		t.Fatal("budget refusal became network observation")
	}
}

func TestHealthWaitsForResultAuditWhenStopping(t *testing.T) {
	blocked, release := make(chan struct{}), make(chan struct{})
	releaseAudit := sync.OnceFunc(func() { close(release) })
	defer releaseAudit()
	rec := &healthRecorder{hook: func(ctx context.Context, event transmit.Event) error {
		if event.Stage == "result" && event.Attempt == 2 {
			close(blocked)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	tx := healthTransmitter(t, context.Background(), server.URL, rec, nil)
	tx.SendDetailed(transportRequest("owner", "h-owner"))
	sent := make(chan struct{})
	go func() { tx.SendDetailed(transportRequest("owner", "h-owner")); close(sent) }()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("trigger result not recorded")
	}
	if !tx.HealthStatus().Halted {
		t.Fatal("health latch not set before result audit")
	}
	drained := make(chan struct{})
	go func() { tx.StopAndWait(); close(drained) }()
	select {
	case <-drained:
		t.Fatal("StopAndWait skipped pending result")
	case <-time.After(20 * time.Millisecond):
	}
	releaseAudit()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("StopAndWait deadlocked during health stop")
	}
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("triggering sender failed to finish")
	}
}

func TestHealthQueuedTimeoutIsNotANetworkObservation(t *testing.T) {
	entered, resultBlocked, releaseResult := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseAudit := sync.OnceFunc(func() { close(releaseResult) })
	defer releaseAudit()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	rec := &healthRecorder{hook: func(ctx context.Context, event transmit.Event) error {
		if event.Stage == "result" && event.Attempt == 1 {
			close(resultBlocked)
			select {
			case <-releaseResult:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}}
	tx := healthTransmitter(t, context.Background(), server.URL, rec, func(c *transmit.Config) { c.RequestTimeout = 700 * time.Millisecond })
	first := make(chan struct{})
	go func() { tx.SendDetailed(transportRequest("owner", "h-owner")); close(first) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first sender did not enter network")
	}
	second := make(chan transmit.Receipt, 1)
	go func() { _, r, _ := tx.SendDetailed(transportRequest("owner", "h-owner")); second <- r }()
	select {
	case <-resultBlocked:
	case <-time.After(time.Second):
		t.Fatal("first result not blocked")
	}
	select {
	case r := <-second:
		if r.Reason != transmit.TimedOut || r.Admitted {
			t.Fatalf("queued timeout = %+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("queued timeout did not finish")
	}
	releaseAudit()
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("first sender did not finish")
	}
	if state := tx.HealthStatus(); state.Halted || state.Decision.Counts.Samples != 1 || state.Decision.Counts.Failures != 0 {
		t.Fatalf("queued wait became target failure: %+v", state)
	}
}

func TestHealthIntentAuditDeadlineHasNoNetworkSample(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer server.Close()
	rec := &healthRecorder{hook: func(ctx context.Context, event transmit.Event) error {
		if event.Stage == "intent" {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}}
	tx := healthTransmitter(t, context.Background(), server.URL, rec, func(c *transmit.Config) {
		c.RequestTimeout, c.Health.NetworkTimeout = 400*time.Millisecond, 200*time.Millisecond
	})
	_, r, _ := tx.SendDetailed(transportRequest("owner", "h-owner"))
	if r.Reason != transmit.AuditUnavailable || hits.Load() != 0 || tx.HealthStatus().Decision.Counts.Samples != 0 || tx.HealthStatus().Halted {
		t.Fatalf("audit timeout became health observation: %+v", r)
	}
}

func TestHealthStopPreservesDurableAuditVerification(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	dir := filepath.Join(t.TempDir(), "private-journal")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x42}, auditlog.KeyBytes)
	defer clear(key)
	cfg := transportConfig(server.URL, nil)
	journal, err := auditlog.Create(dir, "journal", auditlog.Config{Scope: cfg.Scope, KeyRef: "health-test-key", Key: key})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Error(err)
		}
	})
	cfg.Recorder, cfg.Health, cfg.RequestTimeout = journal, healthPolicy(), 3*time.Second
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tx.StopAndWait)
	for range 2 {
		_, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
		if !ok || receipt.Reason != transmit.Completed {
			t.Fatalf("triggering result not durable: %+v", receipt)
		}
	}
	_, stopped, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
	if ok || stopped.Reason != transmit.Stopped || !tx.HealthStatus().Halted || hits.Load() != 2 {
		t.Fatal("health did not close admission")
	}
	tx.StopAndWait()
	checkpoint := journal.Checkpoint()
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join(dir, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	summary, verifyErr := auditlog.Verify(context.Background(), file, key, checkpoint)
	closeErr := file.Close()
	if verifyErr != nil || closeErr != nil || summary.Intents != 2 || summary.Results != 3 || checkpoint.Sequence != 5 {
		t.Fatal("health cancellation broke durable result/denial audit")
	}
}
