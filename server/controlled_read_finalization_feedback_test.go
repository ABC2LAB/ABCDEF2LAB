package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

func TestControlledFinalizationSafeReconciliationDiagnostic(t *testing.T) {
	const privateCause = "SYNTHETIC-PRIVATE-SQL-URL-COOKIE-CAUSE"
	operation := sha256.Sum256([]byte("synthetic-stable-operation"))
	for _, tc := range []struct {
		name, reason string
		cause        error
		typed        bool
	}{
		{"unknown", "storage_outcome_unknown", errors.New(privateCause), true},
		{"conflict", "storage_conflict", fmt.Errorf("%w: %s", db.ErrControlledFindingConflict, privateCause), true},
		{"rejected", "storage_rejected", fmt.Errorf("%w: %s", db.ErrControlledFindingMode, privateCause), true},
		{"callback", "finalization_rejected", errors.New(privateCause), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "safe.log")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			previous := log.Writer()
			log.SetOutput(file)
			defer log.SetOutput(previous)
			failure := tc.cause
			if tc.typed {
				failure = controlledFindingPersistenceError(failure, operation)
			}
			calls := 0
			state := &controlledReadVerificationState{
				host: &controlledReadVerificationConfig{lifecycle: context.Background()}, runCtx: context.Background(), selected: true,
				plan: ControlledReadVerificationPlan{Finalize: func(context.Context, ControlledReadVerificationEvidence) error { calls++; return failure }},
			}
			lease := &controlledReadLease{timeout: time.Second}
			if got := state.finish(lease, true, auditlog.Checkpoint{}); got != errControlledReadUnavailable {
				t.Fatal("public finalization error exposed an internal storage outcome")
			}
			_ = state.finish(lease, true, auditlog.Checkpoint{})
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			wantKey := "unavailable"
			if tc.typed {
				wantKey = fmt.Sprintf("%x", operation)
			}
			if calls != 1 || strings.Count(string(data), "finalization_failed") != 1 ||
				!strings.Contains(string(data), "reason="+tc.reason+" operation_key="+wantKey) || strings.Contains(string(data), privateCause) {
				t.Fatal("safe fixed classification/key was missing, leaked a cause, or caused a second finalize")
			}
		})
	}
}

// The Worker has returned before finish starts. A test-owned barrier surrounds
// the real atomic claim after StopAndWait, Journal.Close and checkpoint fsync.
// Independent flag/context cases ensure one guard cannot hide another's removal.
func TestControlledCompletionControlsAfterWorkerBeforeClaim(t *testing.T) {
	for _, mode := range []string{"complete", "work-cancel", "task-cancel", "pause", "delete", "action", "missing", "replacement", "already-claimed"} {
		t.Run(mode, func(t *testing.T) {
			var hits, saves atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"status":"ok","data":{"note":%q}}`, verificationTestMarker)
			}))
			defer target.Close()
			e, task, intent, _ := runtimeReadFixture(t, target.URL)
			key := controlledWorkKey{task.ID, intent.ID}
			spec := e.controlledReads.pending[key]
			spec.HTTP.MaxRequests = 2
			spec.HTTP.Sessions = append(spec.HTTP.Sessions, transmit.Session{
				Account: idor.AccountRef{Label: "peer", Handle: "session-2", Status: oracle.SubjectVerified}, ExpiresAt: time.Now().Add(time.Hour),
			})
			spec.HTTP.Resources[0].Accounts = []string{"owner", "peer"}
			spec.HTTP.Resources[0].View = &transmit.JSONViewRule{StatusPath: []string{"status"}, NormalValue: "ok", DataPath: []string{"data"}, Markers: []idor.Marker{{Field: "note", Value: verificationTestMarker}}}
			e.controlledReads.pending[key] = spec
			e.controlledReads.verification = &controlledReadVerificationConfig{lifecycle: context.Background(), resolve: func(context.Context, ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
				return verificationFixturePlan(nil, func(context.Context, ControlledReadVerificationEvidence) error { saves.Add(1); return nil }), nil
			}}
			workCtx, cancelWork := context.WithCancelCause(context.Background())
			defer cancelWork(nil)
			taskCtx, cancelTask := context.WithCancel(context.Background())
			defer cancelTask()
			run := e.registerWork(intent.ID, cancelWork)
			defer func() { _, complete := e.detachWork(intent.ID); complete(nil) }()
			entered, release := make(chan struct{}), make(chan struct{})
			runCtx := context.WithValue(workCtx, controlledReadCompletionClaimKey{}, func() bool {
				close(entered)
				<-release
				return e.claimControlledReadCompletion(task.ID, intent.ID, run, workCtx, taskCtx)
			})
			ctx, finish, err := e.prepareControlledRead(runCtx, task, intent)
			if err != nil {
				t.Fatal(err)
			}
			provider := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"peer","resource_ref":"doc:one"}`}
			if _, _, err := runtimeWorker(t, provider).Execute(ctx, "worker", 17, nil, nil, intent, nil, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			if hits.Load() != 2 || saves.Load() != 0 {
				t.Fatal("Worker did not finish two staged reads before finalization")
			}
			done := make(chan error, 1)
			go func() { done <- finish(true) }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				close(release)
				t.Fatal("did not reach the post-audit pre-claim barrier")
			}
			switch mode {
			case "work-cancel":
				cancelWork(errors.New("synthetic cancellation after Worker return"))
			case "task-cancel":
				cancelTask()
			case "pause":
				e.paused.Store(task.ID, true)
			case "delete":
				e.deleting.Store(task.ID, true)
			case "action":
				e.workMu.Lock()
				run.action = "cancel"
				e.workMu.Unlock()
			case "missing":
				e.workMu.Lock()
				delete(e.work, intent.ID)
				e.workMu.Unlock()
			case "replacement":
				e.registerWork(intent.ID, cancelWork)
			case "already-claimed":
				e.workMu.Lock()
				run.completionClaimed = true
				e.workMu.Unlock()
			}
			close(release)
			select {
			case err := <-done:
				if (mode == "complete") != (err == nil) {
					t.Fatalf("completion=%s err=%v", mode, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("post-Worker finalization did not settle")
			}
			_ = finish(true)
			want := int64(0)
			if mode == "complete" {
				want = 1
			}
			if saves.Load() != want || hits.Load() != 2 {
				t.Fatalf("post-Worker control saved or resent: saves=%d hits=%d", saves.Load(), hits.Load())
			}
		})
	}
}
