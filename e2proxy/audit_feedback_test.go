package e2proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestIntentACKThenCallerCancellationIsAllowedButUnobserved(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
	}))
	defer target.Close()
	caller, cancel := context.WithCancel(t.Context())
	defer cancel()
	recorder := &cancellationRecorder{cancel: cancel}
	e, call := coreOpen(t, coreConfig(t, target.URL, recorder, 2))
	response, receipt, err := call.Do(caller, coreRequest(t, "GET", target.URL+"/allowed"))
	counts := e.Snapshot().Counts
	// Admission succeeded before the caller canceled. It is allowed/unobserved,
	// not a target response or an ambiguous transport outcome.
	if !errors.Is(err, ErrClosed) || response != nil || receipt.Reason != Closed ||
		receipt.Decision != "allowed" || !receipt.NotSent || receipt.ForwardAttempted ||
		receipt.ResponseReceived || !receipt.AuditComplete || received.Load() != 0 ||
		recorder.bad.Load() || recorder.rows.Load() != 2 || e.eventIndex != 2 ||
		counts.Allowed != 1 || counts.ForwardAttempted != 0 || counts.Responses != 0 || counts.Unknown != 0 {
		t.Fatal("ACKed caller cancellation lost its allowed/unobserved classification")
	}
}
