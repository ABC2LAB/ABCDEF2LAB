package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// 실제 runtime의 once gate 안에서도 일시정지 동안 host·브라우저·모델 I/O가 없어야 한다.
func TestE2HTTPExplorationPausedPreparationWaitsBeforeHostIO(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "nil-host"
		if enabled {
			name = "fixture-host"
		}
		t.Run(name, func(t *testing.T) {
			runtime, task := e2HTTPRuntimeFixture(t, nil)
			server, _, o, b, _, http, p := pipelineFixtureWithStore(t, nil, task.ID, runtime.options.Expected.RunRef)
			if !enabled {
				o.OpenHost = nil
			}
			runtime.options.Workflow = o.Workflow
			var calls atomic.Int32
			runtime.options.Exploration = func(ctx context.Context, actual *Task) error {
				calls.Add(1)
				_, err := server.RunExplorationPipeline(ctx, actual, o)
				return err
			}
			snapshot, _ := o.Workflow.Snapshot()
			if _, err := o.Workflow.Command(t.Context(), "ARTEX", "pause", "pause-before-first-plan", snapshot.Revision, nil); err != nil {
				t.Fatal("pause fixture failed")
			}
			if err := runtime.AdmitTask(t.Context(), task); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := runtime.PreparePlanning(t.Context(), task); done <- err }()
			select {
			case <-done:
				t.Fatal("paused preparation finished before resume")
			case <-time.After(25 * time.Millisecond):
			}
			if calls.Load() != 1 || b.opens.Load() != 0 || http.requests.Load() != 0 || p.calls.Load() != 0 || runtime.task.lease != nil {
				t.Fatal("paused preparation performed I/O or acquired diagnosis authority")
			}
			snapshot, _ = o.Workflow.Snapshot()
			if _, err := o.Workflow.Command(t.Context(), "ARTEX", "resume", "resume-before-first-plan", snapshot.Revision, nil); err != nil {
				t.Fatal("resume fixture failed")
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal("resumed stage failed", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("resumed preparation did not finish")
			}
			if _, err := runtime.PreparePlanning(t.Context(), task); err != nil || calls.Load() != 1 {
				t.Fatal("resumed successful stage was cached as failure or replayed")
			}
			snapshot, _ = o.Workflow.Snapshot()
			if enabled {
				if snapshot.ExplorationStatus != "COMPLETED" || p.calls.Load() != 3 || b.opens.Load() != 1 {
					t.Fatal("resumed fixture host did not execute actual exploration Worker")
				}
			} else if snapshot.ExplorationStatus != "NOT_RUN" || b.opens.Load() != 0 || p.calls.Load() != 0 {
				t.Fatal("nil host did not preserve explicit NOT_RUN")
			}
		})
	}
}

func TestExplorationPausedPreparationCancellationHasNoIO(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "nil-host"
		if enabled {
			name = "fixture-host"
		}
		t.Run(name, func(t *testing.T) {
			server, task, o, b, _, http, p := pipelineFixture(t)
			if !enabled {
				o.OpenHost = nil
			}
			snapshot, _ := o.Workflow.Snapshot()
			if _, err := o.Workflow.Command(t.Context(), "ARTEX", "pause", "pause-cancelled-preparation", snapshot.Revision, nil); err != nil {
				t.Fatal("pause fixture failed")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := server.RunExplorationPipeline(ctx, task, o); done <- err }()
			select {
			case <-done:
				t.Fatal("paused pipeline finished before cancellation")
			case <-time.After(25 * time.Millisecond):
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("pause lost cancellation identity")
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled pause did not finish")
			}
			snapshot, _ = o.Workflow.Snapshot()
			if b.opens.Load() != 0 || http.requests.Load() != 0 || p.calls.Load() != 0 || snapshot.ExplorationStatus != "" || !snapshot.Paused {
				t.Fatal("cancelled preparation created host I/O or fabricated exploration status")
			}
		})
	}
}
