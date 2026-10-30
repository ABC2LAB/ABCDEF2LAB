package server

import (
	"path/filepath"
	"testing"
)

func parkingRuntime(t *testing.T) (*Engine, *Task, *controlledReadRuntime) {
	t.Helper()
	root := t.TempDir()
	store, err := openControlledReadStore(filepath.Join(root, "snapshots"), filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.mark("17", 22); err != nil {
		t.Fatal(err)
	}
	r := newControlledReadRuntime(store)
	e := NewEngine(nil)
	e.controlledReads = r
	task := &Task{ID: "17", Store: nil}
	return e, task, r
}

func TestMaybeParkControlledTaskAfterDrain(t *testing.T) {
	e, task, _ := parkingRuntime(t)
	var parked int
	e.controlledIdle = func(*Task) { parked++ }
	e.maybeParkControlledTask(task)
	if parked != 1 {
		t.Fatalf("park callback count = %d, want 1", parked)
	}
	if task.Status == "done" {
		t.Fatal("controlled idle task must not be marked done")
	}
}

func TestMaybeParkControlledTaskWaitsForRegisteredWork(t *testing.T) {
	e, task, r := parkingRuntime(t)
	var parked int
	e.controlledIdle = func(*Task) { parked++ }
	r.pending[controlledWorkKey{task: task.ID, intent: 22}] = controlledReadSpec{TaskID: task.ID, IntentID: 22}
	e.maybeParkControlledTask(task)
	if parked != 0 {
		t.Fatal("pending controlled work was parked")
	}
	delete(r.pending, controlledWorkKey{task: task.ID, intent: 22})
	r.active[controlledWorkKey{task: task.ID, intent: 22}] = true
	e.maybeParkControlledTask(task)
	if parked != 0 {
		t.Fatal("active controlled work was parked")
	}
}

func TestMaybeParkControlledTaskIgnoresTerminalTask(t *testing.T) {
	e, task, _ := parkingRuntime(t)
	var parked int
	e.controlledIdle = func(*Task) { parked++ }
	task.updateLifecycle(func(state *taskLifecycleState) { state.Status = "done" })
	e.maybeParkControlledTask(task)
	if parked != 0 {
		t.Fatal("terminal controlled task was parked")
	}
}

func TestMaybeParkControlledTaskUnavailableStore(t *testing.T) {
	e := NewEngine(nil)
	e.controlledReads = newControlledReadRuntime(nil)
	task := &Task{ID: "17"}
	var parked int
	e.controlledIdle = func(*Task) { parked++ }
	e.maybeParkControlledTask(task)
	if parked != 1 {
		t.Fatal("unavailable authority must release its idle execution slot")
	}
}
