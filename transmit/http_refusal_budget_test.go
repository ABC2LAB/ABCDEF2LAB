package transmit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

func TestHTTPTransmitterPreAdmissionRefusalPreservesBudget(t *testing.T) {
	cases := []struct {
		name       string
		configure  func(*transmit.Config, *idor.AccessRequest)
		wantReason transmit.Reason
	}{
		{
			name: "unsupported_method", wantReason: transmit.UnsupportedAction,
			configure: func(_ *transmit.Config, req *idor.AccessRequest) {
				req.Method = http.MethodPost
			},
		},
		{
			name: "state_changing_get", wantReason: transmit.UnsupportedAction,
			configure: func(_ *transmit.Config, req *idor.AccessRequest) {
				req.StateChanging = true
			},
		},
		{
			name: "unknown_resource", wantReason: transmit.UnknownResource,
			configure: func(_ *transmit.Config, req *idor.AccessRequest) {
				req.ResourceRef = "unregistered-resource"
			},
		},
		{
			name: "unknown_account", wantReason: transmit.UnknownAccount,
			configure: func(_ *transmit.Config, req *idor.AccessRequest) {
				req.As.Label = "unregistered-account"
			},
		},
		{
			name: "spoofed_handle", wantReason: transmit.UnverifiedAccount,
			configure: func(_ *transmit.Config, req *idor.AccessRequest) {
				req.As.Handle = "wrong-handle"
			},
		},
		{
			name: "unverified_status", wantReason: transmit.UnverifiedAccount,
			configure: func(_ *transmit.Config, req *idor.AccessRequest) {
				req.As.Status = oracle.SubjectUnknown
			},
		},
		{
			name: "unapproved_account", wantReason: transmit.AccountNotApproved,
			configure: func(cfg *transmit.Config, req *idor.AccessRequest) {
				cfg.Resources[0].Accounts = []string{"owner"}
				*req = transportRequest("evaluated", "h-evaluated")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(server.Close)
			cfg := transportConfig(server.URL, &recorder{})
			cfg.MaxRequests = 1
			cfg.Health = nil
			rejected := transportRequest("owner", "h-owner")
			tc.configure(&cfg, &rejected)
			tx, err := transmit.NewHTTP(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tx.StopAndWait)

			_, receipt, ok := tx.SendDetailed(rejected)
			if ok || receipt.Admitted || receipt.Reason != tc.wantReason || receipt.RequestWritten || receipt.ResponseReceived {
				t.Fatalf("refusal: want unsent %s, got ok=%v receipt=%+v", tc.wantReason, ok, receipt)
			}
			if got := hits.Load(); got != 0 {
				t.Fatalf("refused request reached target: hits=%d, want 0", got)
			}

			// A zero-hit refusal alone cannot show that its budget stayed intact.
			// Reuse this exact instance for a valid read before testing exhaustion.
			allowed := transportRequest("owner", "h-owner")
			_, receipt, ok = tx.SendDetailed(allowed)
			if !ok || !receipt.Admitted || receipt.Reason != transmit.Completed || !receipt.ResponseReceived || receipt.StatusCode != http.StatusOK {
				t.Fatalf("valid read after refusal lost its budget: ok=%v receipt=%+v", ok, receipt)
			}
			if got := hits.Load(); got != 1 {
				t.Fatalf("valid read: hits=%d, want 1", got)
			}

			_, receipt, ok = tx.SendDetailed(allowed)
			if ok || receipt.Admitted || receipt.Reason != transmit.BudgetExhausted || receipt.RequestWritten || receipt.ResponseReceived {
				t.Fatalf("after valid read: want unsent budget refusal, got ok=%v receipt=%+v", ok, receipt)
			}
			if got := hits.Load(); got != 1 {
				t.Fatalf("exhausted budget reached target: hits=%d, want 1", got)
			}
		})
	}
}
