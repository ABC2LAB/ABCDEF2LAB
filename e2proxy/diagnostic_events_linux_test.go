//go:build linux

package e2proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"github.com/Autumn-27/artex/transmit/budget"
)

func readDiagnosticEvents(t *testing.T, l *diagnosticlog.Logger) []diagnosticlog.Event {
	t.Helper()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-query", "secret-body", "secret-run", "secret-command"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("untrusted value leaked")
		}
	}
	scan := bufio.NewScanner(strings.NewReader(string(raw)))
	var out []diagnosticlog.Event
	for scan.Scan() {
		var e diagnosticlog.Event
		if json.Unmarshal(scan.Bytes(), &e) != nil {
			t.Fatal("invalid JSONL")
		}
		out = append(out, e)
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
func diagnosticExecution(t *testing.T, l *diagnosticlog.Logger, origin string, rec Recorder) (*Execution, *Call) {
	t.Helper()
	cfg := coreConfig(t, origin, rec, 10)
	cfg.Scope.TaskRef = "42"
	cfg.Scope.RunRef = "secret-run"
	cfg.Budget.Key.TaskRef = "42"
	if err := cfg.Budget.Ledger.Register(budget.Grant{Key: cfg.Budget.Key, Binding: cfg.Budget.Binding, MaxRequests: 10, ValidFrom: time.Now().Add(-time.Minute), ValidUntil: time.Now().Add(time.Hour)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	ctx := diagnosticlog.With(context.Background(), l)
	e, err := NewExecution(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.BeginCall(ctx, "secret-command")
	if err != nil {
		t.Fatal(err)
	}
	return e, c
}
func TestDiagnosticHTTPEventsReflectActualTransmission(t *testing.T) {
	var sent atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		if r.URL.Path == "/allowed/drop" {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		w.WriteHeader(503)
		_, _ = w.Write([]byte("secret-body"))
	}))
	defer target.Close()
	l, err := diagnosticlog.Create(t.TempDir(), "run", 42, "secret-run")
	if err != nil {
		t.Fatal(err)
	}
	e, c := diagnosticExecution(t, l, target.URL, &coreRecorder{})
	for _, path := range []string{"/allowed?token=secret-query", "/denied", "/allowed/drop"} {
		resp, _, _ := c.Do(context.Background(), coreRequest(t, "GET", target.URL+path))
		if resp != nil {
			resp.Body.Close()
		}
	}
	_, _ = c.Close(context.Background())
	_ = e.Stop(context.Background())
	events := readDiagnosticEvents(t, l)
	if sent.Load() != 2 {
		t.Fatalf("actual requests=%d", sent.Load())
	}
	counts := map[string]int{}
	var finished []diagnosticlog.Event
	for _, event := range events {
		counts[event.Event]++
		if event.TaskID != 42 || len(event.RunRef) != 64 {
			t.Fatal("lost task/run correlation")
		}
		if event.Event == "http_finished" {
			finished = append(finished, event)
			if event.IntentID != 1 || len(event.CallRef) != 64 || len(event.AttemptRef) != 64 {
				t.Fatal("lost intent/call/attempt correlation")
			}
		}
	}
	if counts["http_forward_attempted"] != 2 || counts["http_dispatch_authorized"] != 2 || counts["http_response_received"] != 1 || len(finished) != 3 {
		t.Fatalf("wrong events: %+v", counts)
	}
	if finished[0].Status != "completed" || finished[0].HTTPStatus != 503 || finished[1].Status != "denied" || finished[1].ReasonCode != "policy_denied" || finished[2].Status != "unknown" {
		t.Fatalf("wrong result semantics: %+v", finished)
	}
}
func TestDiagnosticFailureDoesNotGrantOrAlterAudit(t *testing.T) {
	var sent atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sent.Add(1) }))
	defer target.Close()
	l, err := diagnosticlog.Create(t.TempDir(), "run", 42, "secret-run")
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Record(diagnosticlog.Event{Component: "e2proxy", Event: "raw-secret-invalid"}); !errors.Is(err, diagnosticlog.ErrEvent) {
		t.Fatal(err)
	}
	_, c := diagnosticExecution(t, l, target.URL, &coreRecorder{})
	resp, r, err := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
	if resp != nil {
		resp.Body.Close()
	}
	if err != nil || !r.ResponseComplete || !r.AuditComplete || sent.Load() != 1 {
		t.Fatalf("logging changed allowed execution: %v %+v", err, r)
	}
	_, r, err = c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/denied"))
	if err == nil || r.ForwardAttempted || sent.Load() != 1 {
		t.Fatal("logging granted disallowed request")
	}
	_, _ = c.Close(context.Background())
	if !errors.Is(l.Close(), diagnosticlog.ErrEvent) {
		t.Fatal("logging failure disappeared")
	}
}
func TestDiagnosticAuditFailureRemainsSeparate(t *testing.T) {
	var sent atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sent.Add(1) }))
	defer target.Close()
	l, err := diagnosticlog.Create(t.TempDir(), "run", 42, "secret-run")
	if err != nil {
		t.Fatal(err)
	}
	_, c := diagnosticExecution(t, l, target.URL, &coreRecorder{failStage: "intent"})
	_, r, err := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
	if !errors.Is(err, ErrAudit) || r.ForwardAttempted || sent.Load() != 0 {
		t.Fatal("audit failure did not block")
	}
	events := readDiagnosticEvents(t, l)
	found := false
	for _, e := range events {
		if e.Event == "audit_event_failed" && e.ReasonCode == "audit_failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing audit failure")
	}
}

func TestDiagnosticCancelledDialReceiptRace(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(time.Millisecond) }))
	defer target.Close()
	l, err := diagnosticlog.Create(t.TempDir(), "run", 42, "secret-run")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 32; i++ {
		_, c := diagnosticExecution(t, l, target.URL, &coreRecorder{})
		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(time.Microsecond, cancel)
		resp, _, _ := c.Do(ctx, coreRequest(t, "GET", target.URL+"/allowed"))
		if resp != nil {
			resp.Body.Close()
		}
		timer.Stop()
		cancel()
		_, _ = c.Close(context.Background())
	}
	readDiagnosticEvents(t, l)
}
