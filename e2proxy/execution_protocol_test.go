package e2proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDirectUnsupportedProtocolsDoNotReserveOrSend(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	for _, kind := range []string{"upgrade_only", "connection_only", "h2c", "http2", "http12", "expect"} {
		t.Run(kind, func(t *testing.T) {
			e, call := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 2))
			req := coreRequest(t, "GET", target.URL+"/allowed")
			switch kind {
			case "upgrade_only":
				req.Header["upgrade"] = []string{"websocket"}
			case "connection_only":
				req.Header["connection"] = []string{"upgrade"}
			case "h2c":
				req.Header.Set("Upgrade", "h2c")
			case "http2":
				req.ProtoMajor = 2
				req.ProtoMinor = 0
				req.Proto = "HTTP/2.0"
			case "http12":
				req.ProtoMajor = 1
				req.ProtoMinor = 2
				req.Proto = "HTTP/1.2"
			case "expect":
				req.Header["expect"] = []string{"100-continue"}
			}
			response, receipt, err := call.Do(t.Context(), req)
			remaining, budgetErr := e.BudgetRemaining()
			if err == nil || response != nil || receipt.Decision != "unsupported" || receipt.Reason != Unsupported || !receipt.NotSent || receipt.ForwardAttempted || received.Load() != 0 || remaining != 2 || budgetErr != nil || e.Snapshot().Counts.Unsupported != 1 {
				t.Fatal("unsupported direct request was downgraded, reserved or sent")
			}
		})
	}
}

func TestEmptyPostRetainsZeroLengthInsteadOfChunking(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		body, _ := io.ReadAll(r.Body)
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.Header.Get("Content-Length") != "0" || len(body) != 0 {
			t.Error("empty POST framing changed")
		}
		io.WriteString(w, "normal")
	}))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
	cfg.Rules[0].Methods = []string{"POST"}
	_, call := coreOpen(t, cfg)
	req := coreRequest(t, "POST", target.URL+"/allowed")
	req.Header.Set("Content-Length", "0")
	response, receipt, err := call.Do(t.Context(), req)
	if err != nil || response == nil || receipt.Reason != Complete || received.Load() != 1 {
		t.Fatal("empty POST positive failed")
	}
	response.Body.Close()
}
