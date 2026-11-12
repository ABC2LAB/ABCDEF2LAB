package diagnosisflow

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

func finishedExplorationTransitionFixture(t *testing.T, mode, reason string) (*Controller, *memoryStore, ExplorationBinding) {
	t.Helper()
	c, store, r, b := explorationFixture(t, mode)
	_, err := c.CrawlExploration(t.Context(), b, func(_ context.Context, target string) (ExplorationFetchResult, error) {
		body := []byte("<p>owned page</p>")
		o := observationFor(b, 1, strings.TrimPrefix(target, c.origin))
		o.Source, o.Kind, o.ResponseContentType, o.ResponseSHA256 = SourceBrowserNavigation, "page", "text/html", sha256.Sum256(body)
		r.rows[1] = o
		return ExplorationFetchResult{Seq: 1, Body: body, Complete: true}, nil
	})
	if err != nil {
		t.Fatal("owned exploration crawl failed", err)
	}
	if _, err := c.ReportStatic(t.Context(), []Discovery{{Method: "GET", Path: "/read/unselected"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.FinishExploration(t.Context(), b, reason); err != nil {
		t.Fatal(err)
	}
	return c, store, b
}

func TestFinishedExplorationSkipsLegacyCrawlWithoutTransmission(t *testing.T) {
	for _, mode := range []string{Auto, Manual} {
		for _, reason := range []string{"completed", "cap_exhausted", "time_budget", "page_limit", "model_error", "runtime_unavailable", "boundary_unavailable"} {
			t.Run(mode+"_"+reason, func(t *testing.T) {
				c, store, _ := finishedExplorationTransitionFixture(t, mode, reason)
				before, _ := c.Snapshot()
				claimed := c.claimed
				calls := 0
				handled, err := c.Crawl(t.Context(), func(context.Context, string) (e2runner.Result, error) {
					calls++
					return e2runner.Result{}, errors.New("legacy fetch must not run")
				})
				if handled || err != nil || calls != 0 {
					t.Fatal("closed exploration did not skip duplicate static fetch", handled, err, calls)
				}
				if handled, err := c.Crawl(t.Context(), nil); handled || err != nil {
					t.Fatal("skipped crawl unexpectedly required a sender")
				}
				after, _ := c.Snapshot()
				if after.Revision != before.Revision || after.DeepRuns != before.DeepRuns || after.DeepClosed != before.DeepClosed || after.Counts != before.Counts || c.claimed != claimed || store.records["prep-owned"].Snapshot.Revision != before.Revision {
					t.Fatal("skip fabricated diagnosis activity or changed workflow")
				}
			})
		}
	}
}

func TestExplorationTransitionKeepsManualSelectionAndRequestGates(t *testing.T) {
	c, _, _ := finishedExplorationTransitionFixture(t, Manual, "completed")
	calls := 0
	if handled, err := c.Crawl(t.Context(), func(context.Context, string) (e2runner.Result, error) { calls++; return e2runner.Result{}, nil }); handled || err != nil || calls != 0 {
		t.Fatal("manual selection phase did not skip static crawl")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() { close(started); done <- c.StartDeepWork(ctx) }()
	<-started
	select {
	case err := <-done:
		t.Fatal("manual deep work bypassed selection", err)
	default:
	}
	if err := c.Before(t.Context(), e2proxy.RequestView{Method: "GET", URL: c.binding.TargetURL}); !errors.Is(err, ErrDenied) {
		t.Fatal("unselected manual request was allowed")
	}
	s, _ := c.Snapshot()
	var selected string
	for _, e := range s.Endpoints {
		if e.Path == "/read" && e.Method == "GET" {
			selected = e.ID
		}
	}
	if selected == "" {
		t.Fatal("owned selected endpoint missing")
	}
	if _, err := c.Command(t.Context(), "ARTEX", "selection", "owned-transition-selection", s.Revision, []string{selected}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("selected deep work could not continue", err)
		}
	case <-ctx.Done():
		t.Fatal("selected deep work remained blocked")
	}
	if err := c.Before(t.Context(), e2proxy.RequestView{Method: "GET", URL: c.binding.TargetURL}); err != nil {
		t.Fatal("approved selected read was rejected", err)
	}
	for _, request := range []e2proxy.RequestView{{Method: "GET", URL: c.origin + "/read/unselected"}, {Method: "GET", URL: c.origin + "/outside"}, {Method: "GET", URL: "https://foreign.test/read"}, {Method: "DELETE", URL: c.binding.TargetURL}} {
		if err := c.Before(t.Context(), request); !errors.Is(err, ErrDenied) {
			t.Fatal("transition expanded selected scope")
		}
	}
	// 다음 Worker도 정적 크롤만 생략하며 DeepRuns는 실제 StartDeepWork만 세야 한다.
	if handled, err := c.Crawl(t.Context(), nil); handled || err != nil {
		t.Fatal("subsequent diagnostic worker repeated exploration")
	}
	s, _ = c.Snapshot()
	if s.DeepRuns != 1 || s.DeepClosed != 0 || s.Counts.Attempted != 0 || s.Counts.Sent != 0 {
		t.Fatal("transition synthesized request or deep completion")
	}
}

func TestExplorationTransitionPreservesPausedDeepAdmission(t *testing.T) {
	c, _, _ := finishedExplorationTransitionFixture(t, Auto, "completed")
	s, _ := c.Snapshot()
	if _, err := c.Command(t.Context(), "ARTEX", "pause", "owned-transition-pause", s.Revision, nil); err != nil {
		t.Fatal(err)
	}
	if handled, err := c.Crawl(t.Context(), nil); handled || err != nil {
		t.Fatal("paused closed exploration repeated or failed static crawl")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.StartDeepWork(ctx) }()
	select {
	case err := <-done:
		t.Fatal("paused deep work advanced", err)
	default:
	}
	s, _ = c.Snapshot()
	if !s.Paused || s.DeepRuns != 0 {
		t.Fatal("skip cleared pause or fabricated deep work")
	}
	if _, err := c.Command(t.Context(), "ARTEX", "resume", "owned-transition-resume", s.Revision, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("resumed deep work remained blocked")
	}
}

func TestExplorationTransitionRejectsIncompleteForeignAndCorruptState(t *testing.T) {
	changes := map[string]func(*Controller){
		"private_session":        func(c *Controller) { c.record.Exploration.Binding.SessionRef = "foreign" },
		"private_run":            func(c *Controller) { c.record.Exploration.Binding.RunRef = "foreign" },
		"private_job":            func(c *Controller) { c.record.Exploration.Binding.JobID = "foreign" },
		"private_task":           func(c *Controller) { c.record.Exploration.Binding.TaskID = "foreign" },
		"private_target":         func(c *Controller) { c.record.Exploration.Binding.TargetRef = "foreign" },
		"private_revision":       func(c *Controller) { c.record.Exploration.Binding.TargetRevision++ },
		"private_digest":         func(c *Controller) { c.record.Exploration.Binding.SourceDigest = strings.Repeat("b", 64) },
		"protected_revision":     func(c *Controller) { c.binding.TargetRevision++ },
		"snapshot_job":           func(c *Controller) { c.record.Snapshot.JobID = "foreign" },
		"snapshot_task":          func(c *Controller) { c.record.Snapshot.TaskID = "foreign" },
		"snapshot_run":           func(c *Controller) { c.record.Snapshot.RunRef = "foreign" },
		"snapshot_digest":        func(c *Controller) { c.record.Snapshot.SourceDigest = strings.Repeat("b", 64) },
		"snapshot_mode":          func(c *Controller) { c.record.Snapshot.Mode = Manual },
		"format":                 func(c *Controller) { c.record.Snapshot.Format = "invalid" },
		"not_completed":          func(c *Controller) { c.record.Snapshot.InitialCompleted = false },
		"still_crawling":         func(c *Controller) { c.record.Snapshot.Stage = "crawling" },
		"stopped_stage":          func(c *Controller) { c.record.Snapshot.Stage = "stopped" },
		"invalid_stage":          func(c *Controller) { c.record.Snapshot.Stage = "completed" },
		"unfinished_private":     func(c *Controller) { c.record.Exploration.ClosedReason = "" },
		"unknown_private_reason": func(c *Controller) { c.record.Exploration.ClosedReason = "unknown" },
		"reason_mismatch":        func(c *Controller) { c.record.Snapshot.ExplorationReason = "cap_exhausted" },
		"completed_with_partial": func(c *Controller) { c.record.Snapshot.ExplorationPartial = true },
		"partial_without_flag":   func(c *Controller) { c.record.Snapshot.ExplorationStatus = "PARTIAL" },
		"unknown_status":         func(c *Controller) { c.record.Snapshot.ExplorationStatus = "RUNNING" },
		"poisoned":               func(c *Controller) { c.poisoned = true },
		"failure":                func(c *Controller) { c.record.Snapshot.Failure = "projection_failed" },
		"reader_missing":         func(c *Controller) { c.explorationReader = nil },
		"inventory_foreign":      func(c *Controller) { c.record.DiscoveryInventory[0].SessionRef = "foreign" },
		"inventory_zero_seq":     func(c *Controller) { c.record.DiscoveryInventory[0].Seq = 0 },
		"inventory_future_seq":   func(c *Controller) { c.record.DiscoveryInventory[0].Seq++ },
		"revoked":                func(c *Controller) { c.binding.Current = func() error { return errors.New("revoked") } },
		"current_missing":        func(c *Controller) { c.binding.Current = nil },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			c, store, _ := finishedExplorationTransitionFixture(t, Auto, "completed")
			change(c)
			before := store.records["prep-owned"].Snapshot.Revision
			var calls atomic.Int64
			handled, err := c.Crawl(t.Context(), func(context.Context, string) (e2runner.Result, error) { calls.Add(1); return e2runner.Result{}, nil })
			if !handled || !errors.Is(err, ErrExplorationBinding) || calls.Load() != 0 {
				t.Fatal("invalid transition was accepted")
			}
			if store.records["prep-owned"].Snapshot.Revision != before {
				t.Fatal("rejected transition changed persistence")
			}
		})
	}
	for _, reason := range []string{"cancelled", "target_ended"} {
		c, _, _ := finishedExplorationTransitionFixture(t, Auto, reason)
		if handled, err := c.Crawl(t.Context(), nil); !handled || !errors.Is(err, ErrExplorationBinding) {
			t.Fatal("stopped exploration entered diagnosis")
		}
	}
	active, _, _, _ := explorationFixture(t, Auto)
	if handled, err := active.Crawl(t.Context(), nil); !handled || !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("active exploration opened another static crawl")
	}
}

func TestExplorationTransitionRejectsFailedSaveAndCancelledContext(t *testing.T) {
	c, store, _, b := explorationFixture(t, Auto)
	store.fail = true
	if err := c.FinishExploration(t.Context(), b, "cap_exhausted"); !errors.Is(err, ErrProjection) {
		t.Fatal("fixture did not fail save")
	}
	if handled, err := c.Crawl(t.Context(), nil); !handled || !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("unpersisted exploration entered diagnosis")
	}
	valid, _, _ := finishedExplorationTransitionFixture(t, Auto, "completed")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if handled, err := valid.Crawl(ctx, nil); !handled || !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("cancelled transition entered diagnosis")
	}
}
