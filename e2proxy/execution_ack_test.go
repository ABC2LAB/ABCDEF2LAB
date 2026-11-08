package e2proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type failOnceRecorder struct{ calls atomic.Int64 }

func (r *failOnceRecorder) Record(context.Context, Event) error {
	if r.calls.Add(1) == 1 {
		return errors.New("owned recorder failure")
	}
	return nil
}

func TestFailedACKDoesNotCommitIndexOrCallRecorderAgain(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	recorder := &failOnceRecorder{}
	e, call := coreOpen(t, coreConfig(t, target.URL, recorder, 2))
	response, receipt, err := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err == nil || response != nil || receipt.AuditComplete || received.Load() != 0 || e.eventIndex != 0 || len(e.Events(0, 200)) != 0 || recorder.calls.Load() != 1 {
		t.Fatal("failed ACK consumed a normal event ordinal or retried persistence")
	}
	if err := e.record(t.Context(), Event{Stage: "control"}); !errors.Is(err, ErrAudit) || recorder.calls.Load() != 1 {
		t.Fatal("audit latch called recorder after failure")
	}
}

func TestSuccessfulRecorderACKProjectionHasContinuousIndices(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "normal") }))
	defer target.Close()
	e, call := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 2))
	response, _, err := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if _, err := call.Reject(t.Context(), "CONNECT", Unsupported); err != nil {
		t.Fatal(err)
	}
	events := e.Events(0, 200)
	if len(events) != 3 {
		t.Fatal("successful memory recorder ACKs were not projected")
	}
	for index, event := range events {
		if event.Index != uint64(index+1) {
			t.Fatal("successful projection contains an ordinal gap")
		}
	}
}

type cancellationRecorder struct {
	cancel context.CancelFunc
	bad    atomic.Bool
	rows   atomic.Int64
}

func (r *cancellationRecorder) Record(ctx context.Context, event Event) error {
	if event.Stage == "intent" {
		r.cancel()
	}
	if _, ok := ctx.Deadline(); !ok || ctx.Err() != nil {
		r.bad.Store(true)
		return context.Canceled
	}
	r.rows.Add(1)
	return nil
}

type cancelAtStageRecorder struct {
	cancel context.CancelFunc
	stage  string
	bad    atomic.Bool
	rows   atomic.Int64
}

func (r *cancelAtStageRecorder) Record(ctx context.Context, event Event) error {
	if _, ok := ctx.Deadline(); !ok || ctx.Err() != nil {
		r.bad.Store(true)
		return context.Canceled
	}
	if event.Stage == r.stage {
		r.cancel()
	}
	r.rows.Add(1)
	return nil
}

func TestControlAndCloseAuditSurviveCallerCancellation(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer target.Close()
	for _, operation := range []string{"register_intent", "stop", "restrict", "call_close"} {
		t.Run(operation, func(t *testing.T) {
			caller, cancel := context.WithCancel(context.Background())
			defer cancel()
			recorder := &cancelAtStageRecorder{cancel: cancel, stage: "control"}
			if operation == "call_close" {
				recorder.stage = "call_closed"
			}
			e, call := coreOpen(t, coreConfig(t, target.URL, recorder, 2))
			var err error
			switch operation {
			case "register_intent":
				err = e.RegisterIntent(caller, 2)
				if !errors.Is(err, ErrClosed) || e.HasIntent(2) {
					t.Fatal("canceled intent binding was published")
				}
			case "stop":
				err = e.Stop(caller)
			case "restrict":
				err = e.Restrict(caller, Restriction{PolicyRevision: "revision2", ExpiresAt: e.cfg.ExpiresAt.Add(-time.Second), RuleRefs: []string{"rule"}, Methods: []string{"GET"}})
				if err == nil && e.Snapshot().PolicyRevision != "revision2" {
					t.Fatal("acknowledged restriction was not committed")
				}
			case "call_close":
				response, _, requestErr := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
				if requestErr != nil || response == nil {
					t.Fatalf("setup request failed: %v", requestErr)
				}
				_ = response.Body.Close()
				_, err = call.Close(caller)
			}
			if (err != nil && operation != "register_intent") || recorder.bad.Load() || e.Snapshot().Reason == AuditFailed {
				t.Fatalf("%s audit was poisoned by caller cancellation: error=%v bad_context=%t reason=%s", operation, err, recorder.bad.Load(), e.Snapshot().Reason)
			}
		})
	}
}

func TestIntentPersistenceDetachesCallerCancellationAndStillClosesResult(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	caller, cancel := context.WithCancel(t.Context())
	defer cancel()
	recorder := &cancellationRecorder{cancel: cancel}
	e, call := coreOpen(t, coreConfig(t, target.URL, recorder, 2))
	response, receipt, err := call.Do(caller, coreRequest(t, "GET", target.URL+"/allowed"))
	if err == nil || response != nil || received.Load() != 0 || recorder.bad.Load() || recorder.rows.Load() != 2 || !receipt.AuditComplete || e.eventIndex != 2 || e.Snapshot().Reason == AuditFailed {
		t.Fatal("caller cancellation poisoned or abandoned a bounded audit operation")
	}
}
