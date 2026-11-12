package diagnosisflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
)

func closedBeforeDiagnosisFixture(t *testing.T, mode string, observed bool) (*Controller, *memoryStore, ExplorationBinding) {
	t.Helper()
	c, store, reader, b := explorationFixture(t, mode)
	if observed {
		reader.rows[1] = observationFor(b, 1, "/read/observed")
		if _, err := c.ReportObserved(t.Context(), b, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.FinishExploration(t.Context(), b, "cap_exhausted"); err != nil {
		t.Fatal(err)
	}
	return c, store, b
}

func TestExplorationClosedBeforeDiagnosisReadOnlyAndZeroObservations(t *testing.T) {
	for _, mode := range []string{Manual, Auto} {
		for _, observed := range []bool{false, true} {
			c, store, b := closedBeforeDiagnosisFixture(t, mode, observed)
			before, _ := c.Snapshot()
			if err := c.CheckExplorationClosedBeforeDiagnosis(t.Context(), b, c.record.Snapshot.Revision); err != nil {
				t.Fatal("closed pre-diagnosis state rejected", mode, observed)
			}
			after, _ := c.Snapshot()
			if before.Revision != after.Revision || store.records["prep-owned"].Snapshot.Revision != before.Revision || before.DeepRuns != after.DeepRuns || before.Counts != after.Counts {
				t.Fatal("readonly closure check changed execution state")
			}
		}
	}
}

func TestExplorationClosedBeforeDiagnosisRejectsBindingAndStateTampering(t *testing.T) {
	cases := map[string]func(*Controller, *ExplorationBinding){
		"caller_session":   func(_ *Controller, b *ExplorationBinding) { b.SessionRef = "foreign" },
		"caller_run":       func(_ *Controller, b *ExplorationBinding) { b.RunRef = "foreign" },
		"caller_job":       func(_ *Controller, b *ExplorationBinding) { b.JobID = "foreign" },
		"caller_task":      func(_ *Controller, b *ExplorationBinding) { b.TaskID = "foreign" },
		"caller_target":    func(_ *Controller, b *ExplorationBinding) { b.TargetRef = "foreign" },
		"caller_revision":  func(_ *Controller, b *ExplorationBinding) { b.TargetRevision++ },
		"caller_digest":    func(_ *Controller, b *ExplorationBinding) { b.SourceDigest = strings.Repeat("b", 64) },
		"private_session":  func(c *Controller, _ *ExplorationBinding) { c.record.Exploration.Binding.SessionRef = "foreign" },
		"private_target":   func(c *Controller, _ *ExplorationBinding) { c.record.Exploration.Binding.TargetRef = "foreign" },
		"private_revision": func(c *Controller, _ *ExplorationBinding) { c.record.Exploration.Binding.TargetRevision++ },
		"private_digest": func(c *Controller, _ *ExplorationBinding) {
			c.record.Exploration.Binding.SourceDigest = strings.Repeat("b", 64)
		},
		"protected_revision":   func(c *Controller, _ *ExplorationBinding) { c.binding.TargetRevision++ },
		"snapshot_job":         func(c *Controller, _ *ExplorationBinding) { c.record.Snapshot.JobID = "foreign" },
		"snapshot_task":        func(c *Controller, _ *ExplorationBinding) { c.record.Snapshot.TaskID = "foreign" },
		"snapshot_run":         func(c *Controller, _ *ExplorationBinding) { c.record.Snapshot.RunRef = "foreign" },
		"snapshot_digest":      func(c *Controller, _ *ExplorationBinding) { c.record.Snapshot.SourceDigest = strings.Repeat("b", 64) },
		"mode":                 func(c *Controller, _ *ExplorationBinding) { c.record.Snapshot.Mode = Manual },
		"stage":                func(c *Controller, _ *ExplorationBinding) { c.record.Snapshot.Stage = "awaiting_selection" },
		"partial_flag":         func(c *Controller, _ *ExplorationBinding) { c.record.Snapshot.ExplorationPartial = false },
		"status":               func(c *Controller, _ *ExplorationBinding) { c.record.Snapshot.ExplorationStatus = "COMPLETED" },
		"private_reason":       func(c *Controller, _ *ExplorationBinding) { c.record.Exploration.ClosedReason = "model_error" },
		"public_reason":        func(c *Controller, _ *ExplorationBinding) { c.record.Snapshot.ExplorationReason = "model_error" },
		"incomplete":           func(c *Controller, _ *ExplorationBinding) { c.record.Snapshot.InitialCompleted = false },
		"paused":               func(c *Controller, _ *ExplorationBinding) { c.record.Snapshot.Paused = true },
		"deep_started":         func(c *Controller, _ *ExplorationBinding) { c.record.Snapshot.DeepRuns = 1 },
		"deep_closed":          func(c *Controller, _ *ExplorationBinding) { c.record.Snapshot.DeepClosed = 1 },
		"sent":                 func(c *Controller, _ *ExplorationBinding) { c.record.Snapshot.Counts.Sent = 1 },
		"attempted":            func(c *Controller, _ *ExplorationBinding) { c.record.Snapshot.Counts.Attempted = 1 },
		"unknown":              func(c *Controller, _ *ExplorationBinding) { c.record.Snapshot.Counts.Unknown = 1 },
		"blocked":              func(c *Controller, _ *ExplorationBinding) { c.record.Snapshot.Counts.Blocked = 1 },
		"held":                 func(c *Controller, _ *ExplorationBinding) { c.record.Snapshot.Counts.Held = 1 },
		"inventory_session":    func(c *Controller, _ *ExplorationBinding) { c.record.DiscoveryInventory[0].SessionRef = "foreign" },
		"inventory_zero_seq":   func(c *Controller, _ *ExplorationBinding) { c.record.DiscoveryInventory[0].Seq = 0 },
		"inventory_future_seq": func(c *Controller, _ *ExplorationBinding) { c.record.DiscoveryInventory[0].Seq++ },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			c, _, b := closedBeforeDiagnosisFixture(t, Auto, true)
			before, _ := c.Snapshot()
			change(c, &b)
			if err := c.CheckExplorationClosedBeforeDiagnosis(t.Context(), b, before.Revision); !errors.Is(err, ErrExplorationBinding) {
				t.Fatal("tampered closure accepted")
			}
		})
	}
}

func TestExplorationClosedBeforeDiagnosisRejectsUnfinishedOrOtherClosures(t *testing.T) {
	for _, reason := range []string{"", "completed", "model_error", "runtime_unavailable", "boundary_unavailable", "cancelled", "target_ended", "time_budget", "page_limit"} {
		t.Run("reason_"+reason, func(t *testing.T) {
			c, _, _, b := explorationFixture(t, Auto)
			if reason != "" {
				if err := c.FinishExploration(t.Context(), b, reason); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.CheckExplorationClosedBeforeDiagnosis(t.Context(), b, c.record.Snapshot.Revision); !errors.Is(err, ErrExplorationBinding) {
				t.Fatal("non-budget or unfinished exploration accepted")
			}
		})
	}
	c, _ := flowFixture(t, Auto)
	if err := c.CheckExplorationClosedBeforeDiagnosis(t.Context(), ExplorationBinding{}, c.record.Snapshot.Revision); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("unbound workflow accepted")
	}
}

func TestExplorationClosedBeforeDiagnosisRejectsRevocationAndFailedPersistence(t *testing.T) {
	c, _, b := closedBeforeDiagnosisFixture(t, Auto, false)
	c.binding.Current = func() error { return errors.New("authority revoked") }
	if err := c.CheckExplorationClosedBeforeDiagnosis(t.Context(), b, c.record.Snapshot.Revision); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("revoked closure accepted")
	}
	c.binding.Current = func() error { return nil }
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.CheckExplorationClosedBeforeDiagnosis(cancelled, b, c.record.Snapshot.Revision); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("cancelled proof context accepted")
	}
	failed, store, _, bound := explorationFixture(t, Auto)
	store.fail = true
	if err := failed.FinishExploration(t.Context(), bound, "cap_exhausted"); !errors.Is(err, ErrProjection) {
		t.Fatal("fixture persistence did not fail")
	}
	if err := failed.CheckExplorationClosedBeforeDiagnosis(t.Context(), bound, failed.record.Snapshot.Revision); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("unpersisted closure accepted")
	}
}

func TestExplorationClosedBeforeDiagnosisRejectsActualDeepStartAndReceipt(t *testing.T) {
	c, _, b := closedBeforeDiagnosisFixture(t, Auto, false)
	if err := c.StartDeepWork(t.Context()); err != nil {
		t.Fatal("owned deep start failed")
	}
	if err := c.CheckExplorationClosedBeforeDiagnosis(t.Context(), b, c.record.Snapshot.Revision); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("actual diagnosis start accepted")
	}
	other, _, bound := closedBeforeDiagnosisFixture(t, Auto, false)
	request := e2proxy.RequestView{Method: "GET", URL: other.binding.TargetURL}
	if err := other.Before(t.Context(), request); err != nil {
		t.Fatal("owned diagnosis request rejected")
	}
	receipt := e2proxy.Receipt{AttemptID: "owned-closure-attempt", CallRef: "owned-closure-call", Decision: "allowed", ForwardAttempted: true, RequestWritten: true, ResponseReceived: true, ResponseComplete: true, StatusCode: 200}
	if err := other.After(t.Context(), request, receipt); err != nil {
		t.Fatal("owned diagnosis receipt failed")
	}
	if err := other.CheckExplorationClosedBeforeDiagnosis(t.Context(), bound, other.record.Snapshot.Revision); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("actual diagnosis receipt accepted")
	}
}

func TestExplorationClosedBeforeDiagnosisRejectsStaleRevisionAtomically(t *testing.T) {
	c, _, b := closedBeforeDiagnosisFixture(t, Auto, false)
	before, _ := c.Snapshot()
	if err := c.CheckExplorationClosedBeforeDiagnosis(t.Context(), b, before.Revision); err != nil {
		t.Fatal("current proof rejected")
	}
	c.mu.Lock()
	c.record.Snapshot.Revision++
	c.mu.Unlock()
	if err := c.CheckExplorationClosedBeforeDiagnosis(t.Context(), b, before.Revision); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("stale private closure revision accepted")
	}
	if err := c.CheckExplorationClosedBeforeDiagnosis(t.Context(), b, 0); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("zero private closure revision accepted")
	}
}
