package transmit_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

func TestHTTPPreviewRegistrationAndNoEffects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	rec := &recorder{}
	cfg := transportConfig(target.URL, rec)
	cfg.Resources[0].Accounts = []string{"owner"}
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.StopAndWait()
	valid := transportRequest("owner", "h-owner")
	tests := []struct {
		name                        string
		change                      func(*idor.AccessRequest)
		status                      transmit.PreviewStatus
		reason                      transmit.PreviewReason
		method                      string
		resource, account, approval string
	}{
		{"match", func(*idor.AccessRequest) {}, transmit.PreviewRegistrationMatch, "", http.MethodGet, "basket/42", "owner", "approval-1"},
		{"post", func(r *idor.AccessRequest) { r.Method = "POST-secret" }, transmit.PreviewRegistrationRejected, transmit.PreviewReason(transmit.UnsupportedAction), "UNSUPPORTED", "basket/42", "owner", "approval-1"},
		{"lowercase get", func(r *idor.AccessRequest) { r.Method = "get" }, transmit.PreviewRegistrationRejected, transmit.PreviewReason(transmit.UnsupportedAction), "UNSUPPORTED", "basket/42", "owner", "approval-1"},
		{"state changing", func(r *idor.AccessRequest) { r.StateChanging = true }, transmit.PreviewRegistrationRejected, transmit.PreviewReason(transmit.UnsupportedAction), http.MethodGet, "basket/42", "owner", "approval-1"},
		{"unknown resource", func(r *idor.AccessRequest) { r.ResourceRef = "secret-resource" }, transmit.PreviewRegistrationRejected, transmit.PreviewReason(transmit.UnknownResource), http.MethodGet, "", "owner", ""},
		{"unknown account", func(r *idor.AccessRequest) { r.As.Label = "secret-account" }, transmit.PreviewRegistrationRejected, transmit.PreviewReason(transmit.UnknownAccount), http.MethodGet, "basket/42", "", "approval-1"},
		{"unapproved account", func(r *idor.AccessRequest) { r.As = transportRequest("evaluated", "h-evaluated").As }, transmit.PreviewRegistrationRejected, transmit.PreviewReason(transmit.AccountNotApproved), http.MethodGet, "basket/42", "evaluated", "approval-1"},
		{"wrong handle", func(r *idor.AccessRequest) { r.As.Handle = "secret-handle" }, transmit.PreviewRegistrationRejected, transmit.PreviewReason(transmit.UnverifiedAccount), http.MethodGet, "basket/42", "owner", "approval-1"},
		{"wrong status", func(r *idor.AccessRequest) { r.As.Status = oracle.SubjectExpired }, transmit.PreviewRegistrationRejected, transmit.PreviewReason(transmit.UnverifiedAccount), http.MethodGet, "basket/42", "owner", "approval-1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := valid
			request.Inputs = []string{"secret-input"}
			tc.change(&request)
			got := tx.Preview(request)
			if got.Status != tc.status || got.Reason != tc.reason || got.Method != tc.method ||
				got.ResourceRef != tc.resource || got.AccountRef != tc.account || got.ApprovalRef != tc.approval {
				t.Fatalf("preview = %+v", got)
			}
			for _, secret := range []string{"secret-input", "secret-resource", "secret-account", "secret-handle", "POST-secret", target.URL} {
				if strings.Contains(fmt.Sprint(got), secret) {
					t.Fatalf("preview reflected unregistered input or target URL")
				}
			}
		})
	}
	if hits.Load() != 0 || len(rec.events) != 0 {
		t.Fatalf("preview side effects: hits=%d events=%d", hits.Load(), len(rec.events))
	}
	first := tx.Preview(valid)
	first.ResourceRef = "changed"
	first.Status = transmit.PreviewUnavailable
	if again := tx.Preview(valid); again.ResourceRef != "basket/42" || again.Status != transmit.PreviewRegistrationMatch {
		t.Fatalf("preview result mutated registry: %+v", again)
	}
}

func TestHTTPPreviewDoesNotConsumeAttemptOrSharedBudget(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	ledger, shared := registeredCumulativeBudget(t, 1)
	rec := &recorder{}
	cfg := transportConfig(target.URL, rec)
	cfg.MaxRequests = 1
	cfg.CumulativeBudget = shared
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.StopAndWait()
	proposal := transportRequest("owner", "h-owner")
	for range 5 {
		if got := tx.Preview(proposal); got.Status != transmit.PreviewRegistrationMatch {
			t.Fatalf("preview = %+v", got)
		}
	}
	if hits.Load() != 0 || len(rec.events) != 0 {
		t.Fatalf("preview sent or audited: hits=%d events=%d", hits.Load(), len(rec.events))
	}
	remaining, err := ledger.Remaining(shared.Key, shared.Binding, time.Now())
	if err != nil || remaining != 1 {
		t.Fatalf("preview consumed shared budget: remaining=%d error=%v", remaining, err)
	}
	_, receipt, ok := tx.SendDetailed(proposal)
	if !ok || receipt.Reason != transmit.Completed || receipt.Attempt != 1 || hits.Load() != 1 {
		t.Fatalf("first send: ok=%v receipt=%+v hits=%d", ok, receipt, hits.Load())
	}
	if got := tx.Preview(proposal); got.Status != transmit.PreviewRegistrationMatch {
		t.Fatalf("preview after exhaustion = %+v", got)
	}
	_, receipt, ok = tx.SendDetailed(proposal)
	if ok || receipt.Reason != transmit.BudgetExhausted || hits.Load() != 1 {
		t.Fatalf("exhausted send: ok=%v receipt=%+v hits=%d", ok, receipt, hits.Load())
	}
}

func TestHTTPPreviewUnavailableAndStopped(t *testing.T) {
	var nilTx *transmit.HTTPTransmitter
	for _, tx := range []*transmit.HTTPTransmitter{nilTx, {}} {
		got := tx.Preview(transportRequest("owner", "h-owner"))
		if got.Status != transmit.PreviewUnavailable || got.Reason != transmit.PreviewUnavailableReason ||
			got.Method != "UNSUPPORTED" || got.ResourceRef != "" || got.AccountRef != "" || got.ApprovalRef != "" {
			t.Fatalf("uninitialized preview = %+v", got)
		}
	}
	target := httptest.NewServer(http.NotFoundHandler())
	defer target.Close()
	tx, err := transmit.NewHTTP(context.Background(), transportConfig(target.URL, &recorder{}))
	if err != nil {
		t.Fatal(err)
	}
	tx.StopAndWait()
	proposal := transportRequest("owner", "h-owner")
	if got := tx.Preview(proposal); got.Status != transmit.PreviewRegistrationMatch {
		t.Fatalf("stopped preview = %+v", got)
	}
	_, receipt, ok := tx.SendDetailed(proposal)
	if ok || receipt.Reason != transmit.Stopped {
		t.Fatalf("stopped send: ok=%v receipt=%+v", ok, receipt)
	}
}

func TestHTTPPreviewConcurrentWithSendAndStop(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	tx, err := transmit.NewHTTP(context.Background(), transportConfig(target.URL, &recorder{}))
	if err != nil {
		t.Fatal(err)
	}
	defer tx.StopAndWait()
	proposal := transportRequest("owner", "h-owner")
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := tx.Preview(proposal); got.Status != transmit.PreviewRegistrationMatch {
				t.Errorf("concurrent preview = %+v", got)
			}
		}()
	}
	wg.Add(2)
	go func() { defer wg.Done(); _, _, _ = tx.SendDetailed(proposal) }()
	go func() { defer wg.Done(); tx.Stop() }()
	wg.Wait()
}

func TestHTTPPreviewMatchesSendRegistrationRefusals(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	cfg := transportConfig(target.URL, &recorder{})
	cfg.Resources[0].Accounts = []string{"owner"}
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.StopAndWait()
	cases := []struct {
		name   string
		change func(*idor.AccessRequest)
	}{
		{"method", func(r *idor.AccessRequest) { r.Method = http.MethodPost }},
		{"lowercase method", func(r *idor.AccessRequest) { r.Method = "get" }},
		{"state", func(r *idor.AccessRequest) { r.StateChanging = true }},
		{"resource", func(r *idor.AccessRequest) { r.ResourceRef = "outside" }},
		{"account", func(r *idor.AccessRequest) { r.As.Label = "outside" }},
		{"approval", func(r *idor.AccessRequest) { r.As = transportRequest("evaluated", "h-evaluated").As }},
		{"handle", func(r *idor.AccessRequest) { r.As.Handle = "wrong" }},
		{"status", func(r *idor.AccessRequest) { r.As.Status = oracle.SubjectExpired }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proposal := transportRequest("owner", "h-owner")
			tc.change(&proposal)
			preview := tx.Preview(proposal)
			_, receipt, ok := tx.SendDetailed(proposal)
			if preview.Status != transmit.PreviewRegistrationRejected ||
				preview.Reason != transmit.PreviewReason(receipt.Reason) || ok || receipt.Admitted {
				t.Fatalf("preview=%+v send=%+v ok=%v", preview, receipt, ok)
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("refused requests reached target: %d", hits.Load())
	}
	_, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
	if !ok || receipt.Reason != transmit.Completed || hits.Load() != 1 {
		t.Fatalf("valid send after refusals: ok=%v receipt=%+v hits=%d", ok, receipt, hits.Load())
	}
}

func TestHTTPPreviewAfterAnotherExecutionUsesSharedBudget(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	_, shared := registeredCumulativeBudget(t, 1)
	newExecution := func(run string) *transmit.HTTPTransmitter {
		t.Helper()
		cfg := transportConfig(target.URL, &recorder{})
		cfg.Scope.RunRef = run
		cfg.CumulativeBudget = shared
		tx, err := transmit.NewHTTP(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(tx.StopAndWait)
		return tx
	}
	first := newExecution("preview-budget-first")
	second := newExecution("preview-budget-second")
	proposal := transportRequest("owner", "h-owner")
	_, receipt, ok := first.SendDetailed(proposal)
	if !ok || receipt.Reason != transmit.Completed || hits.Load() != 1 {
		t.Fatalf("first execution: ok=%v receipt=%+v hits=%d", ok, receipt, hits.Load())
	}
	if preview := second.Preview(proposal); preview.Status != transmit.PreviewRegistrationMatch {
		t.Fatalf("preview after other execution spent budget: %+v", preview)
	}
	_, receipt, ok = second.SendDetailed(proposal)
	if ok || receipt.Reason != transmit.BudgetExhausted || hits.Load() != 1 {
		t.Fatalf("second execution: ok=%v receipt=%+v hits=%d", ok, receipt, hits.Load())
	}
}

func TestHTTPPreviewChecksRegisteredSessionStatus(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	cases := []struct {
		name   string
		status oracle.SubjectStatus
		want   transmit.PreviewStatus
	}{
		{"verified", oracle.SubjectVerified, transmit.PreviewRegistrationMatch},
		{"unknown", oracle.SubjectUnknown, transmit.PreviewRegistrationRejected},
		{"expired", oracle.SubjectExpired, transmit.PreviewRegistrationRejected},
		{"mismatch", oracle.SubjectMismatch, transmit.PreviewRegistrationRejected},
		{"unset", "", transmit.PreviewRegistrationRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			cfg := transportConfig(target.URL, rec)
			cfg.Sessions[0].Account.Status = tc.status
			proposal := transportRequest("owner", "h-owner")
			// Keep every request identity field equal to the registered session;
			// a mismatch would mask a missing registered-status check.
			proposal.As = cfg.Sessions[0].Account
			tx, err := transmit.NewHTTP(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tx.StopAndWait)
			beforeHits := hits.Load()
			preview := tx.Preview(proposal)
			wantReason := transmit.PreviewReason(transmit.UnverifiedAccount)
			if tc.want == transmit.PreviewRegistrationMatch {
				wantReason = ""
			}
			if preview.Status != tc.want || preview.Reason != wantReason {
				t.Fatalf("matching registered identity: preview=%+v want=%s/%s", preview, tc.want, wantReason)
			}
			if hits.Load() != beforeHits || len(rec.events) != 0 {
				t.Fatalf("preview side effects: hits=%d before=%d events=%d", hits.Load(), beforeHits, len(rec.events))
			}
			_, receipt, ok := tx.SendDetailed(proposal)
			if tc.want == transmit.PreviewRegistrationMatch {
				if !ok || !receipt.Admitted || receipt.Reason != transmit.Completed || hits.Load() != beforeHits+1 {
					t.Fatalf("verified positive control: ok=%v receipt=%+v hits=%d", ok, receipt, hits.Load())
				}
			} else if ok || receipt.Admitted || receipt.Reason != transmit.UnverifiedAccount || hits.Load() != beforeHits {
				t.Fatalf("unverified registered session: ok=%v receipt=%+v hits=%d", ok, receipt, hits.Load())
			}
		})
	}
}
