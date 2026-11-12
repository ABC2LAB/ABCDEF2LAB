package e2proxy

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestPreserveCallerHeadersAreRefusedBeforeReservation(t *testing.T) {
	for _, mode := range []HeaderMode{"", HeaderPreserve} {
		name := "default"
		if mode != "" {
			name = "explicit"
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				header http.Header
			}{
				{"authorization", http.Header{"Authorization": {"caller"}}},
				{"empty-authorization", http.Header{"aUtHoRiZaTiOn": {""}}},
				{"nil-cookie", http.Header{"Cookie": nil}},
				{"cookie", http.Header{"cOoKiE": {"fixture=caller"}}},
				{"connection-authorization", http.Header{"Connection": {"Authorization"}, "Authorization": {"caller"}}},
				{"connection-cookie", http.Header{"Connection": {"Cookie"}, "Cookie": {"fixture=caller"}}},
				{"empty-user-agent", http.Header{"User-Agent": {""}}},
				{"casefold-duplicate", http.Header{"X-Public": {"one"}, "x-public": {"two"}}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var received atomic.Int64
					target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						received.Add(1)
						_, _ = io.WriteString(w, "public response")
					}))
					defer target.Close()
					cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
					cfg.HeaderMode = mode
					e, call := coreOpen(t, cfg)
					if e.Policy().HeaderMode != HeaderPreserve {
						t.Fatal("fixture did not select preserve")
					}
					req := coreRequest(t, "GET", target.URL+"/allowed")
					req.Header = tc.header.Clone()
					response, receipt, err := call.Do(t.Context(), req)
					remaining, budgetErr := e.BudgetRemaining()
					if err == nil || response != nil || receipt.Decision != "unsupported" || receipt.Reason != Unsupported ||
						!receipt.NotSent || receipt.ForwardAttempted || received.Load() != 0 || budgetErr != nil || remaining != 2 ||
						e.Snapshot().Counts.Unsupported != 1 {
						t.Fatal("preserve refusal was bypassed, uncounted, or reserved/sent")
					}
					// A refusal must neither poison the execution nor consume its grant.
					response, _, err = call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
					if err != nil || response == nil || received.Load() != 1 {
						t.Fatal("valid anonymous request failed after refusal")
					}
					_ = response.Body.Close()
				})
			}
		})
	}
}

// Current draft limitation: representation withholding shares the unknown
// ceiling. A new reason/counter requires the transfer/export contract agreement.
func TestPreserveWithheldResponsesUseDeclaredUnknownCeiling(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.Header().Set("Set-Cookie", "fixture=public")
		_, _ = io.WriteString(w, "public response")
	}))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 4)
	cfg.HeaderMode = HeaderPreserve
	cfg.Limits.MaxUnknown = 3
	e, call := coreOpen(t, cfg)
	for i := uint64(1); i <= 3; i++ {
		response, receipt, err := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
		view := e.Snapshot()
		if err == nil || response != nil || receipt.Decision != "allowed" || receipt.Reason != Unknown ||
			!receipt.ResponseReceived || !receipt.ResponseComplete || !receipt.AuditComplete ||
			view.Counts.Unknown != i || view.Counts.Responses != i || received.Load() != int64(i) {
			t.Fatal("declared withholding/unknown behavior changed")
		}
	}
	response, receipt, err := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
	if !errors.Is(err, ErrClosed) || response != nil || !receipt.NotSent || receipt.Reason != UnknownLimit || received.Load() != 3 ||
		e.Snapshot().Counts.Unknown != 3 {
		t.Fatal("unknown ceiling did not stop the call's next target request")
	}
	// 상한은 그 call만 막는다. 실행과 다른 call은 계속 보낼 수 있다(C01).
	if view := e.Snapshot(); view.State != "active" || view.Reason != Complete {
		t.Fatalf("call unknown ceiling stopped the whole execution: %+v", view)
	}
	next, err := e.BeginCall(t.Context(), "after-unknown-ceiling")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = next.Close(t.Context()) }()
	if response, receipt, err = next.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed")); err == nil || response != nil ||
		receipt.Reason != Unknown || received.Load() != 4 {
		t.Fatal("new call was not admitted after another call's unknown ceiling")
	}
}

func TestConfiguredCredentialTrailerNameIsWithheld(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trailer", "X-Session")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "public response")
		w.Header().Set("X-Session", "")
	}))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 1)
	cfg.HeaderMode = HeaderProtectedSessionProposal
	cfg.Rules[0].CredentialHeaders = http.Header{"X-Session": {"synthetic-protected-value"}}
	_, call := coreOpen(t, cfg)
	response, receipt, err := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err == nil || response != nil || receipt.Decision != "allowed" ||
		!receipt.ResponseReceived || !receipt.ResponseComplete || receipt.Reason != Unknown {
		t.Fatal("credential trailer name was ignored or confused with incomplete transport")
	}
}
