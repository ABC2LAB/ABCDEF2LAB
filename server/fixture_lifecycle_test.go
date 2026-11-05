package server

import (
	"context"
	"testing"
	"time"

	"github.com/Autumn-27/artex/sidequestion"
)

const testServerDrainTimeout = 10 * time.Second

// newTestServer owns the cancellation side of a Server fixture.  New starts
// task runtimes and several background workers, so closing Manager first can
// race those goroutines with the database and temporary-directory cleanup.
// Callers must register the Manager cleanup before calling this helper; test
// cleanup is LIFO, which then makes this drain run first.
func newTestServer(t *testing.T, parent context.Context, m *Manager, skillDir, dataDir, keyDir string) *Server {
	t.Helper()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s := New(ctx, m, skillDir, dataDir, keyDir)
	t.Cleanup(func() {
		cancel()
		drainTestServer(t, s)
	})
	return s
}

// drainTestServer is also callable by a regression test that needs to prove
// the cleanup boundary before testing.T runs its remaining cleanups.
func drainTestServer(t *testing.T, s *Server) {
	t.Helper()
	if s == nil {
		return
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), testServerDrainTimeout)
	defer cancel()
	deadline := time.NewTimer(testServerDrainTimeout)
	defer deadline.Stop()

	// Close background admission and join reconciliation before stopping tasks:
	// otherwise a queued startup job can recreate runtime state after StopTask.
	if err := s.WaitBackground(drainCtx); err != nil {
		t.Errorf("server background workers did not drain within %s: %v", testServerDrainTimeout, err)
		return
	}

	// StopTask waits for every planner/worker/deadline routine and removes the
	// runtime state.  Run the loop in one goroutine so a broken fixture cannot
	// hold testing.T forever; normal cancellation completes well before the cap.
	runtimesDone := make(chan struct{})
	go func() {
		if s.m != nil && s.engine != nil {
			for _, task := range s.m.List() {
				if task != nil {
					s.engine.StopTask(task.ID)
				}
			}
		}
		close(runtimesDone)
	}()
	if !waitTestServerDrain(deadline, runtimesDone) {
		t.Errorf("server task runtimes did not drain within %s", testServerDrainTimeout)
		return
	}

	if s.side != nil {
		s.side.commands.Lock()
		runsDone := s.cancelSideWhere(func(sidequestion.Parent) bool { return true })
		s.side.commands.Unlock()
		for _, done := range runsDone {
			if !waitTestServerDrain(deadline, done) {
				t.Errorf("side-question run did not drain within %s", testServerDrainTimeout)
				return
			}
		}
	}

	archiveDone := make(chan struct{})
	go func() {
		s.archiveWG.Wait()
		close(archiveDone)
	}()
	if !waitTestServerDrain(deadline, archiveDone) {
		t.Errorf("task archive worker did not drain within %s", testServerDrainTimeout)
		return
	}

	if s.side != nil && !waitTestServerDrain(deadline, s.side.done) {
		t.Errorf("side-question snapshot worker did not drain within %s", testServerDrainTimeout)
	}
}

func waitTestServerDrain(deadline *time.Timer, done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	case <-deadline.C:
		return false
	}
}

func TestNewTestServerDrainsRestoredTaskRuntime(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	task, err := m.CreateTask("fixture lifecycle", "drain", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) })

	ctx, cancel := context.WithCancel(context.Background())
	s := newTestServer(t, ctx, m, t.TempDir(), t.TempDir(), t.TempDir())
	if !s.engine.Started(task.ID) {
		t.Fatalf("restored task runtime did not start for task %s", task.ID)
	}
	// Exercise the same cancellation/drain boundary used by the fixture cleanup.
	// No provider is configured, so this test cannot make an external HTTP call.
	cancel()
	drainTestServer(t, s)
	if s.engine.Started(task.ID) {
		t.Fatalf("task runtime still started after fixture drain")
	}
}
