package transmit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
)

func registeredCumulativeBudget(t *testing.T, requests uint64) (*budget.Ledger, *transmit.CumulativeBudget) {
	t.Helper()
	ledger, err := budget.New(budget.Limits{MaxApprovals: 2, MaxRequestsPerApproval: 8})
	if err != nil {
		t.Fatal(err)
	}
	shared := &transmit.CumulativeBudget{
		Ledger:  ledger,
		Key:     budget.Key{TaskRef: "task-1", BudgetRef: "approved-budget-1"},
		Binding: budget.Binding{PolicyRef: "policy-1", ResetRef: "reset-1"},
	}
	now := time.Now()
	if err := ledger.Register(budget.Grant{
		Key: shared.Key, Binding: shared.Binding, MaxRequests: requests,
		ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute),
	}, now); err != nil {
		t.Fatal(err)
	}
	return ledger, shared
}

func TestHTTPCumulativeBudgetAcrossExecutions(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()

	ledger, shared := registeredCumulativeBudget(t, 2)
	results := make(chan transmit.Receipt, 4)
	var wg sync.WaitGroup
	for _, run := range []string{"run-a", "run-b"} {
		rec := &recorder{}
		cfg := transportConfig(target.URL, rec)
		cfg.Scope.RunRef = run
		cfg.CumulativeBudget = shared
		tx, err := transmit.NewHTTP(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(tx.Stop)
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, receipt, _ := tx.SendDetailed(transportRequest("owner", "h-owner"))
				results <- receipt
			}()
		}
	}
	wg.Wait()
	close(results)
	var completed, exhausted int
	for receipt := range results {
		switch receipt.Reason {
		case transmit.Completed:
			if !receipt.Admitted {
				t.Fatalf("completed without admission: %+v", receipt)
			}
			completed++
		case transmit.BudgetExhausted:
			if receipt.Admitted {
				t.Fatalf("exhausted send admitted: %+v", receipt)
			}
			exhausted++
		default:
			t.Fatalf("unexpected result: %+v", receipt)
		}
	}
	if completed != 2 || exhausted != 2 || hits.Load() != 2 {
		t.Fatalf("completed=%d exhausted=%d target hits=%d", completed, exhausted, hits.Load())
	}
	remaining, err := ledger.Remaining(shared.Key, shared.Binding, time.Now())
	if err != nil || remaining != 0 {
		t.Fatalf("remaining=%d error=%v", remaining, err)
	}
}

func TestHTTPCumulativeBudgetUnregisteredGrantFailsClosed(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	ledger, err := budget.New(budget.Limits{MaxApprovals: 2, MaxRequestsPerApproval: 8})
	if err != nil {
		t.Fatal(err)
	}
	shared := &transmit.CumulativeBudget{
		Ledger:  ledger,
		Key:     budget.Key{TaskRef: "task-1", BudgetRef: "approved-budget-1"},
		Binding: budget.Binding{PolicyRef: "policy-1", ResetRef: "reset-1"},
	}
	rec := &recorder{}
	cfg := transportConfig(target.URL, rec)
	cfg.CumulativeBudget = shared
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tx.Stop)
	_, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
	if ok || receipt.Admitted || receipt.Reason != transmit.BudgetUnavailable || hits.Load() != 0 {
		t.Fatalf("unregistered grant: ok=%t receipt=%+v hits=%d", ok, receipt, hits.Load())
	}
	if len(rec.events) != 2 || rec.events[0].Stage != "intent" || rec.events[1].Stage != "result" {
		t.Fatalf("refusal audit stages: %+v", rec.events)
	}
	now := time.Now()
	if err := ledger.Register(budget.Grant{
		Key: shared.Key, Binding: shared.Binding, MaxRequests: 1,
		ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute),
	}, now); err != nil {
		t.Fatal(err)
	}
	_, receipt, ok = tx.SendDetailed(transportRequest("owner", "h-owner"))
	if !ok || receipt.Reason != transmit.Completed || hits.Load() != 1 {
		t.Fatalf("registered grant: ok=%t receipt=%+v hits=%d", ok, receipt, hits.Load())
	}
}

func TestHTTPCumulativeBudgetRejectsInvalidBindingAndDoesNotChargeRefusal(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	ledger, shared := registeredCumulativeBudget(t, 1)
	cfg := transportConfig(target.URL, &recorder{})
	cfg.CumulativeBudget = shared
	invalid := *shared
	invalid.Binding.ResetRef = "wrong-reset"
	cfg.CumulativeBudget = &invalid
	if tx, err := transmit.NewHTTP(context.Background(), cfg); err == nil {
		tx.Stop()
		t.Fatal("mismatched reset accepted")
	}
	cfg.CumulativeBudget = shared
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tx.Stop)
	// A malformed proposal is rejected before the shared reservation.
	proposal := transportRequest("owner", "h-owner")
	proposal.Method = http.MethodPost
	_, receipt, ok := tx.SendDetailed(proposal)
	if ok || receipt.Admitted || receipt.Reason != transmit.UnsupportedAction {
		t.Fatalf("unsupported action: ok=%t receipt=%+v", ok, receipt)
	}
	remaining, err := ledger.Remaining(shared.Key, shared.Binding, time.Now())
	if err != nil || remaining != 1 {
		t.Fatalf("refusal charged budget: remaining=%d error=%v", remaining, err)
	}
	// Mutation of the caller's binding after construction cannot redirect it.
	shared.Key.BudgetRef = "unregistered-budget"
	_, receipt, ok = tx.SendDetailed(transportRequest("owner", "h-owner"))
	if !ok || receipt.Reason != transmit.Completed {
		t.Fatalf("copied binding: ok=%t receipt=%+v", ok, receipt)
	}
}

func TestHTTPCumulativeBudgetExpiredGrant(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 2})
	if err != nil {
		t.Fatal(err)
	}
	shared := &transmit.CumulativeBudget{
		Ledger: ledger, Key: budget.Key{TaskRef: "task-1", BudgetRef: "expired-budget"},
		Binding: budget.Binding{PolicyRef: "policy-1", ResetRef: "reset-1"},
	}
	now := time.Now()
	// Register while valid using a historical host clock; Send uses the real
	// current time. Only the cumulative grant has expired, not the HTTP config.
	if err := ledger.Register(budget.Grant{
		Key: shared.Key, Binding: shared.Binding, MaxRequests: 2,
		ValidFrom: now.Add(-3 * time.Minute), ValidUntil: now.Add(-time.Minute),
	}, now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	cfg := transportConfig(target.URL, rec)
	cfg.CumulativeBudget = shared
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tx.Stop)
	_, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
	if ok || receipt.Admitted || receipt.Reason != transmit.ExpiredApproval || hits.Load() != 0 {
		t.Fatalf("expired grant: ok=%t receipt=%+v hits=%d", ok, receipt, hits.Load())
	}
	if len(rec.events) != 2 || rec.events[0].Stage != "intent" ||
		rec.events[1].Stage != "result" || rec.events[1].Reason != transmit.ExpiredApproval {
		t.Fatalf("expired grant audit: %+v", rec.events)
	}
}

func TestHTTPCumulativeBudgetConstructorBinding(t *testing.T) {
	_, shared := registeredCumulativeBudget(t, 2)
	cases := []struct {
		name   string
		mutate func(*transmit.CumulativeBudget)
	}{
		{"valid", nil},
		{"task", func(c *transmit.CumulativeBudget) { c.Key.TaskRef = "other-task" }},
		{"policy", func(c *transmit.CumulativeBudget) { c.Binding.PolicyRef = "other-policy" }},
		{"reset", func(c *transmit.CumulativeBudget) { c.Binding.ResetRef = "other-reset" }},
		{"nil_ledger", func(c *transmit.CumulativeBudget) { c.Ledger = nil }},
		{"empty_budget", func(c *transmit.CumulativeBudget) { c.Key.BudgetRef = "" }},
		{"invalid_budget", func(c *transmit.CumulativeBudget) { c.Key.BudgetRef = "bad budget" }},
		{"url_budget", func(c *transmit.CumulativeBudget) { c.Key.BudgetRef = "https://example.test" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := *shared
			if tc.mutate != nil {
				tc.mutate(&candidate)
			}
			// NewHTTP performs validation without dialing this loopback origin.
			cfg := transportConfig("http://127.0.0.1:1", &recorder{})
			cfg.CumulativeBudget = &candidate
			tx, err := transmit.NewHTTP(context.Background(), cfg)
			if tx != nil {
				t.Cleanup(tx.Stop)
			}
			if tc.mutate == nil {
				if err != nil || tx == nil {
					t.Fatalf("valid binding rejected: tx=%v err=%v", tx, err)
				}
			} else if err == nil || tx != nil {
				t.Fatalf("invalid binding accepted: tx=%v err=%v", tx, err)
			}
		})
	}
}

func TestHTTPCumulativeBudgetPreservesInstanceLimit(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	ledger, shared := registeredCumulativeBudget(t, 2)
	cfg := transportConfig(target.URL, &recorder{})
	cfg.MaxRequests, cfg.CumulativeBudget = 1, shared
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tx.Stop)
	_, first, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
	if !ok || !first.Admitted || first.Reason != transmit.Completed {
		t.Fatalf("first send: ok=%t receipt=%+v", ok, first)
	}
	_, second, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
	if ok || second.Admitted || second.Reason != transmit.BudgetExhausted || hits.Load() != 1 {
		t.Fatalf("instance limit: ok=%t receipt=%+v hits=%d", ok, second, hits.Load())
	}
	remaining, err := ledger.Remaining(shared.Key, shared.Binding, time.Now())
	if err != nil || remaining != 1 {
		t.Fatalf("instance refusal charged shared budget: remaining=%d err=%v", remaining, err)
	}
}

type cumulativeRecordFunc func(context.Context, transmit.Event) error

func (f cumulativeRecordFunc) Record(ctx context.Context, event transmit.Event) error {
	return f(ctx, event)
}

func TestHTTPCumulativeBudgetStopDuringIntentDoesNotReserve(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	ledger, shared := registeredCumulativeBudget(t, 2)
	rec := &recorder{}
	var tx *transmit.HTTPTransmitter
	stopCalls := 0
	sink := cumulativeRecordFunc(func(ctx context.Context, event transmit.Event) error {
		if err := rec.Record(ctx, event); err != nil {
			return err
		}
		if event.Stage == "intent" {
			stopCalls++
			tx.Stop()
		}
		return nil
	})
	cfg := transportConfig(target.URL, sink)
	cfg.CumulativeBudget = shared
	var err error
	tx, err = transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tx.Stop)
	_, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
	if stopCalls != 1 || ok || receipt.Admitted || receipt.Reason != transmit.Stopped || hits.Load() != 0 {
		t.Fatalf("stopped intent: stops=%d ok=%t receipt=%+v hits=%d", stopCalls, ok, receipt, hits.Load())
	}
	remaining, err := ledger.Remaining(shared.Key, shared.Binding, time.Now())
	if err != nil || remaining != 2 {
		t.Fatalf("stopped intent reserved shared budget: remaining=%d err=%v", remaining, err)
	}
	if len(rec.events) != 2 || rec.events[0].Stage != "intent" ||
		rec.events[1].Stage != "result" || rec.events[1].Reason != transmit.Stopped {
		t.Fatalf("stopped intent audit: %+v", rec.events)
	}
}
