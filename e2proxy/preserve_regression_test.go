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

// 자격 응답 보류는 결과 불명 상한과 따로 센다(C16, e2-http-v2). 결과 불명 상한이 1이어도 같은 call이 보류 응답을 계속
// 받고, 실행·다른 call도 그대로다.
func TestPreserveWithheldResponsesDoNotUseUnknownCeiling(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.Header().Set("Set-Cookie", "fixture=public")
		_, _ = io.WriteString(w, "public response")
	}))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 4)
	cfg.HeaderMode = HeaderPreserve
	cfg.Limits.MaxUnknown = 1
	e, call := coreOpen(t, cfg)
	for i := uint64(1); i <= 4; i++ {
		response, receipt, err := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
		view := e.Snapshot()
		if !errors.Is(err, ErrWithheld) || response != nil || receipt.Decision != "allowed" || receipt.Reason != Withheld ||
			!receipt.ResponseReceived || receipt.ResponseComplete || !receipt.AuditComplete ||
			view.Counts.Unknown != 0 || view.Counts.Withheld != i || view.Counts.Responses != i || received.Load() != int64(i) {
			t.Fatalf("withheld response %d was counted as unknown or exposed: %+v %+v", i, receipt, view.Counts)
		}
	}
	if view := e.Snapshot(); view.State != "active" || view.Reason != Complete {
		t.Fatalf("withheld responses stopped the execution: %+v", view)
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
	if !errors.Is(err, ErrWithheld) || response != nil || receipt.Decision != "allowed" ||
		!receipt.ResponseReceived || receipt.ResponseComplete || receipt.Reason != Withheld {
		t.Fatal("credential trailer name was ignored or confused with incomplete transport")
	}
}
