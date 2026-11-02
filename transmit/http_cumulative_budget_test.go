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
