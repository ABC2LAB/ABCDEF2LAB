package transmit_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit"
)

func TestHTTPTransmitterAdmittedFailureConsumesBudget(t *testing.T) {
	for _, budget := range []uint64{1, 2} {
		t.Run(fmt.Sprintf("max_requests_%d", budget), func(t *testing.T) {
			var hits atomic.Int32
			closed := make(chan error, budget+1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					closed <- errors.New("fixture does not support connection hijacking")
					return
				}
				conn, _, err := hijacker.Hijack()
				if err != nil {
					closed <- err
					return
				}
				// Keep the listener alive: only this received request loses its response.
				closed <- conn.Close()
			}))
			t.Cleanup(server.Close)

			cfg := transportConfig(server.URL, &recorder{})
			cfg.MaxRequests = budget
			cfg.Health = nil
			tx, err := transmit.NewHTTP(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tx.StopAndWait)

			// Budget two is a positive control: a transport error must leave the
			// fixture and transmitter usable while another admission is available.
			for attempt := uint64(1); attempt <= budget; attempt++ {
				_, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
				if ok || !receipt.Admitted || receipt.Reason != transmit.TransportError || receipt.ResponseReceived {
					t.Fatalf("attempt %d: want admitted transport failure, got ok=%v receipt=%+v", attempt, ok, receipt)
				}
				select {
				case err := <-closed:
					if err != nil {
						t.Fatalf("attempt %d: fixture connection close failed: %v", attempt, err)
					}
				case <-time.After(cfg.RequestTimeout):
					t.Fatalf("attempt %d: fixture did not finish handling the request", attempt)
				}
				if got := hits.Load(); got != int32(attempt) {
					t.Fatalf("attempt %d: target hits=%d, want %d", attempt, got, attempt)
				}
			}

			// No Stop, cancellation, health trip or audit error may mask exhaustion.
			_, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
			if ok || receipt.Admitted || receipt.Reason != transmit.BudgetExhausted || receipt.RequestWritten || receipt.ResponseReceived {
				t.Fatalf("after %d failed admissions: want unsent budget refusal, got ok=%v receipt=%+v", budget, ok, receipt)
			}
			if got := hits.Load(); got != int32(budget) {
				t.Fatalf("exhausted budget sent an extra request: target hits=%d, want %d", got, budget)
			}
		})
	}
}
