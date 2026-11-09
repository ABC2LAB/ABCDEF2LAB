//go:build linux

package server

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/internal/diagnosticlog"
)

func TestDiagnosticApprovalDecisionIsNotTransmission(t *testing.T) {
	for _, scenario := range []string{"allow", "deny", "cancelled", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			l, err := diagnosticlog.Create(t.TempDir(), "run", 1, "run")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(diagnosticlog.With(t.Context(), l))
			defer cancel()
			store := &approvalReviewStore{}
			s, p := approvalReviewFixture(t, store)
			p.Receipt.IntentID = 7
			p.Receipt.CallRef = "actual-call"
			s.mu.Lock()
			binding := s.bindings[1]
			binding.ctx = ctx
			s.bindings[1] = binding
			s.mu.Unlock()
			if scenario == "expired" {
				p.ExpiresAt = time.Now().Add(50 * time.Millisecond)
			}
			inserted := make(chan struct{})
			store.insert = func(context.Context) error { close(inserted); return nil }
			type result struct {
				allowed bool
				err     error
			}
			done := make(chan result, 1)
			go func() { a, e := s.AwaitRequestApproval(ctx, p); done <- result{a, e} }()
			<-inserted
			if scenario == "allow" || scenario == "deny" {
				s.mu.Lock()
				digest := store.row.RequestSHA256
				s.mu.Unlock()
				if _, err := s.decide(t.Context(), 1, "ARTEX", p.Receipt.AttemptID, digest, scenario); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "cancelled" {
				cancel()
			}
			select {
			case r := <-done:
				if r.err != nil || r.allowed != (scenario == "allow") {
					t.Fatalf("result=%+v", r)
				}
			case <-time.After(time.Second):
				t.Fatal("approval deadlock")
			}
			if err = l.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(l.Path())
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "example.invalid") || strings.Contains(string(raw), "private-fixture") {
				t.Fatal("private request leaked")
			}
			statuses := map[string]string{}
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				var e diagnosticlog.Event
				if json.Unmarshal([]byte(line), &e) != nil {
					t.Fatal("invalid JSONL")
				}
				statuses[e.Event] = e.Status
				if strings.HasPrefix(e.Event, "http_") {
					t.Fatal("approval became transmission")
				}
				if e.TaskID != 1 || e.IntentID != 7 || len(e.RunRef) != 64 || len(e.CallRef) != 64 || len(e.AttemptRef) != 64 {
					t.Fatal("lost attribution")
				}
			}
			want := scenario
			if scenario == "allow" {
				want = "allowed"
			}
			if scenario == "deny" {
				want = "denied"
			}
			if statuses["request_approval_pending"] != "pending" || statuses["request_approval_resolved"] != want {
				t.Fatalf("wrong status: %+v", statuses)
			}
			if scenario == "allow" || scenario == "deny" {
				if statuses["request_approval_decided"] != want {
					t.Fatal("durable decision absent")
				}
			}
		})
	}
}
