package server

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControlledReadRegistrationLogsSafeFailureCategories(t *testing.T) {
	engine, task, intent, store := runtimeReadFixture(t, "http://127.0.0.1")
	path := filepath.Join(t.TempDir(), "registration.log")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	previous := log.Writer()
	log.SetOutput(file)
	defer log.SetOutput(previous)
	const privateRef = "private-snapshot-reference-log-canary"
	if err := engine.registerControlledRead(context.Background(), task, intent.ID, privateRef); !errors.Is(err, errControlledReadNotPaused) {
		t.Fatal("running task did not reject registration")
	}
	engine.paused.Store(task.ID, true)
	task.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
	if err := engine.registerControlledRead(context.Background(), task, intent.ID, privateRef); !errors.Is(err, errControlledReadUnavailable) {
		t.Fatal("missing snapshot did not reject registration")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{"reason=requires_paused_task", "reason=runtime_unavailable", "reason=snapshot_load_failed"} {
		if !strings.Contains(string(data), reason) {
			t.Fatalf("missing safe diagnostic %s", reason)
		}
	}
	for _, private := range []string{privateRef, store.snapshotDir, runtimeSecret} {
		if strings.Contains(string(data), private) {
			t.Fatal("registration diagnostic included private input or paths")
		}
	}
}
