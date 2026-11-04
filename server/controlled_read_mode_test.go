package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/taskmode"
)

func configureModeDBFixture(t *testing.T, f *controlledReadDBFixture, check func(context.Context) error, quiescence ...func(context.Context, int64) error) {
	t.Helper()
	anchor, err := os.Open(f.store.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = anchor.Close() })
	// Explicitly owned temporary storage is the trust input of this synthetic
	// fixture. This does not certify a restored installation's state directory.
	quiescent := func(ctx context.Context, taskID int64) error {
		// The fixture has no general Worker/process execution: only its owned
		// fake provider and the fixed registered HTTP tool are ever started.
		if itoa(taskID) != f.task.ID {
			return errControlledReadUnavailable
		}
		return ctx.Err()
	}
	if len(quiescence) == 1 {
		quiescent = quiescence[0]
	}
	if err := f.s.ConfigureControlledTaskMode(ControlledTaskModeHost{RootPath: f.store.stateDir, RootAnchor: anchor, CheckRoot: check, CheckTaskQuiescent: quiescent}); err != nil {
		t.Fatal(err)
	}
}

func modeRegisterHTTP(f *controlledReadDBFixture, snapshot string) int {
	request := httptest.NewRequest(http.MethodPost, "/api/tasks/"+f.task.ID+"/intents/"+itoa(f.intent.ID)+"/controlled-read", strings.NewReader("{\"snapshot_ref\":\""+snapshot+"\"}"))
	request.SetPathValue("id", f.task.ID)
	request.SetPathValue("iid", itoa(f.intent.ID))
	response := httptest.NewRecorder()
	f.s.registerControlledReadHTTP(response, request)
	return response.Code
}

func TestControlledTaskModeDBFenceAdmissionAndRestart(t *testing.T) {
	var hits atomic.Int64
	target := controlledReadTarget(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	f := newControlledReadDBFixture(t, target.URL)
	configureModeDBFixture(t, f, func(context.Context) error { return nil })
	if !f.e.controlledTask(f.task.ID) {
		t.Fatal("missing canonical mode fell back to ordinary planning")
	}
	if _, _, err := f.e.prepareControlledRead(context.Background(), f.task, f.intent); err == nil {
		t.Fatal("unknown mode admitted a Worker")
	}
	f.registerAndResume(t)
	var mode string
	var version int64
	if err := f.m.pg.QueryRow("SELECT mode,version FROM task_control_modes WHERE task_id=$1", f.task.ID).Scan(&mode, &version); err != nil ||
		mode != string(taskmode.ModeControlled) || version != 1 {
		t.Fatalf("durable controlled fence absent: %s/%d %v", mode, version, err)
	}
	if err := f.intentState("running"); err != nil {
		t.Fatal(err)
	}
	provider := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: "{\"account_ref\":\"owner\",\"resource_ref\":\"doc:one\"}"}
	if !f.e.runIntent(context.Background(), f.task, "worker", runtimeWorker(t, provider), f.intent, "", "") || hits.Load() != 1 {
		t.Fatal("registered controlled execution did not reach its actual HTTP path")
	}
	f.assertAudit(t)
	// A restart has the persisted mode+marker, but never the one-use capability.
	f.e.controlledReads = newControlledReadRuntime(f.store)
	configureModeDBFixture(t, f, func(context.Context) error { return nil })
	if !f.e.controlledTask(f.task.ID) {
		t.Fatal("restart lost the canonical controlled route")
	}
	if _, _, err := f.e.prepareControlledRead(context.Background(), f.task, f.intent); err == nil {
		t.Fatal("restart reconstructed a consumed approval")
	}
	if hits.Load() != 1 {
		t.Fatal("restart sent another request")
	}
}

func TestControlledTaskModeDBFailedRegistrationKeepsFenceAndCanRetry(t *testing.T) {
	f := newControlledReadDBFixture(t, "http://127.0.0.1:1")
	configureModeDBFixture(t, f, func(context.Context) error { return nil })
	if status := modeRegisterHTTP(f, "missing"); status != http.StatusConflict {
		t.Fatalf("missing snapshot status=%d", status)
	}
	var mode string
	if err := f.m.pg.QueryRow("SELECT mode FROM task_control_modes WHERE task_id=$1", f.task.ID).Scan(&mode); err != nil || mode != string(taskmode.ModeControlled) {
		t.Fatalf("fence was not committed before snapshot load: %s %v", mode, err)
	}
	if len(f.e.controlledReads.pending) != 0 || f.e.beginTaskOperation(f.task.ID) {
		t.Fatal("failed transition reopened ordinary task admission")
	}
	if status := modeRegisterHTTP(f, "approved"); status != http.StatusCreated || len(f.e.controlledReads.pending) != 1 {
		t.Fatalf("explicit checked registration could not retry: %d", status)
	}
	if !f.e.beginTaskOperation(f.task.ID) {
		t.Fatal("successful registration failed to release its transition gate")
	}
	f.e.decInflight(f.task.ID)
}

func TestControlledTaskModeDBRejectsUndrainedOperationsAndLostRoot(t *testing.T) {
	f := newControlledReadDBFixture(t, "http://127.0.0.1:1")
	var trusted atomic.Bool
	trusted.Store(true)
	configureModeDBFixture(t, f, func(context.Context) error {
		if !trusted.Load() {
			return errors.New("test root generation lost")
		}
		return nil
	})
	if !f.e.beginTaskOperation(f.task.ID) {
		t.Fatal("could not reserve a pre-transition task operation")
	}
	if status := modeRegisterHTTP(f, "approved"); status != http.StatusConflict {
		t.Fatalf("undrained task operation was ignored: %d", status)
	}
	f.e.decInflight(f.task.ID)
	var count int
	if err := f.m.pg.QueryRow("SELECT count(*) FROM task_control_modes WHERE task_id=$1", f.task.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("undrained task changed its mode fence: %d %v", count, err)
	}
	if status := modeRegisterHTTP(f, "approved"); status != http.StatusCreated {
		t.Fatalf("drained registration failed: %d", status)
	}
	trusted.Store(false)
	if _, _, err := f.e.prepareControlledRead(context.Background(), f.task, f.intent); err == nil {
		t.Fatal("lost root trust admitted the registered execution")
	}
}

func TestControlledTaskModeDBTransitionClosesAdmissionAndHonorsCancellationToken(t *testing.T) {
	f := newControlledReadDBFixture(t, "http://127.0.0.1:1")
	var block atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	var released atomic.Bool
	unblock := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer unblock()
	configureModeDBFixture(t, f, func(context.Context) error {
		// A stalled infrastructure check is fault injection. Production checks
		// are required to be quick and not wait for database/registry locks.
		if block.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return nil
	})
	block.Store(true)
	result := make(chan int, 1)
	go func() { result <- modeRegisterHTTP(f, "approved") }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("registration did not enter the post-gate fault barrier")
	}
	admission := make(chan bool, 1)
	go func() { admission <- f.e.beginTaskOperation(f.task.ID) }()
	select {
	case admitted := <-admission:
		if admitted {
			f.e.decInflight(f.task.ID)
			t.Fatal("ordinary/main-chat admission crossed the transition gate")
		}
	case <-time.After(time.Second):
		unblock()
		t.Fatal("registration held a global admission/registry lock across an external check")
	}
	key := controlledWorkKey{f.task.ID, f.intent.ID}
	failure := errors.New("synthetic cancellation DB failure")
	if err := f.e.controlledReads.cancelPendingWork(key, func() error { return failure }); !errors.Is(err, failure) {
		t.Fatal("cancellation failure was swallowed")
	}
	unblock()
	select {
	case status := <-result:
		if status != http.StatusConflict || len(f.e.controlledReads.pending) != 0 {
			t.Fatalf("cancelled token published a late pending registration: status=%d", status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("registration did not terminate after token invalidation")
	}
}

func TestControlledTaskModeDBRequiresIndependentChildDrainEvidence(t *testing.T) {
	f := newControlledReadDBFixture(t, "http://127.0.0.1:1")
	var confirmed atomic.Bool
	configureModeDBFixture(t, f, func(context.Context) error { return nil }, func(context.Context, int64) error {
		if !confirmed.Load() {
			return errors.New("synthetic child drain not established")
		}
		return nil
	})
	if status := modeRegisterHTTP(f, "approved"); status != http.StatusConflict || len(f.e.controlledReads.pending) != 0 {
		t.Fatalf("inflight counter was substituted for independent child drain: %d", status)
	}
	var count int
	if err := f.m.pg.QueryRow("SELECT count(*) FROM task_control_modes WHERE task_id=$1", f.task.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unproven child drain advanced the mode fence: %d %v", count, err)
	}
	confirmed.Store(true)
	if status := modeRegisterHTTP(f, "approved"); status != http.StatusCreated {
		t.Fatalf("verified owned-fixture drain could not retry: %d", status)
	}
}
