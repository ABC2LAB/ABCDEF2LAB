package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

type controlledReadDBFixture struct {
	m      *Manager
	s      *Server
	e      *Engine
	task   *Task
	intent *db.Node
	store  *controlledReadStore
}

func controlledReadTarget(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.Start()
	t.Cleanup(server.Close)
	return server
}

func newControlledReadDBFixture(t *testing.T, target string) *controlledReadDBFixture {
	t.Helper()
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	concurrencyEnabled, concurrencyLimit := m.ConcurrencyLimit()
	if concurrencyLimit < 1 {
		concurrencyLimit = defaultConcurrencyLimit
	}
	if err := m.SetConcurrency(false, concurrencyLimit); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.SetConcurrency(concurrencyEnabled, concurrencyLimit) })

	task, err := m.CreateTask("controlled read integration", "execute one approved read", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) })
	intentID, err := task.Store.AddIntent(map[string]any{"summary": "approved read"}, 10, nil, "human")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := task.Store.GetNode(intentID)
	if err != nil || intent == nil {
		t.Fatalf("intent: node=%v err=%v", intent, err)
	}

	root := t.TempDir()
	store, err := openControlledReadStore(filepath.Join(root, "snapshots"), filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour)
	snapshot := snapshotJSON{
		TaskID: task.ID, IntentID: intent.ID, CaseRef: "db-read-case", ResetRef: "reset-1", PolicyRef: "policy-1",
		Origin: target, ConnectIP: "127.0.0.1", ExpiresAt: expires, MaxRequests: 1, RequestTimeoutMS: 1000, MaxBodyBytes: 1024,
		KeyRef: "key-1", KeyFile: "audit.key",
		Sessions:  []sessionJSON{{Label: "owner", Handle: "session-1", Status: oracle.SubjectVerified, ExpiresAt: expires, HeaderFiles: map[string]string{"X-Session": "owner.secret"}}},
		Resources: []resourceJSON{{Ref: "doc:one", URL: target + "/document", ApprovalRef: "read-approval", ReadOnly: true, Accounts: []string{"owner"}}},
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"approved.json": body, "audit.key": bytes.Repeat([]byte{0x71}, 32), "owner.secret": []byte(runtimeSecret),
	} {
		if err := os.WriteFile(filepath.Join(store.snapshotDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	e := NewEngine(m)
	e.controlledReads = newControlledReadRuntime(store)
	// Mark the loops as already admitted so the resume API exercises admission and
	// barrier release without starting an unrelated planner goroutine.
	e.started.Store(task.ID, true)
	e.Pause(task.ID, agent.AbortPausedByUser)
	if err := e.m.ApplyTaskPause(task.ID); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: m, engine: e, ctx: context.Background(), chatBusy: map[string]bool{}, chatCancel: map[string]context.CancelCauseFunc{}}
	e.controlledIdle = s.parkControlledReadTask
	return &controlledReadDBFixture{m: m, s: s, e: e, task: task, intent: intent, store: store}
}

func (f *controlledReadDBFixture) registerAndResume(t *testing.T) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/tasks/"+f.task.ID+"/intents/"+itoa(f.intent.ID)+"/controlled-read", strings.NewReader(`{"snapshot_ref":"approved"}`))
	request.SetPathValue("id", f.task.ID)
	request.SetPathValue("iid", itoa(f.intent.ID))
	response := httptest.NewRecorder()
	f.s.registerControlledReadHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("controlled-read registration status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/tasks/"+f.task.ID+"/control", strings.NewReader(`{"action":"resume"}`))
	request.SetPathValue("id", f.task.ID)
	response = httptest.NewRecorder()
	f.s.control(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", response.Code, response.Body.String())
	}
}

func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}

func TestControlledReadEngineDBResumeRunsWorkerAndSettlesIntent(t *testing.T) {
	var hits atomic.Int64
	target := controlledReadTarget(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("X-Session") != runtimeSecret {
			t.Errorf("registered method/session not used")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"private":"` + runtimeSecret + `"}`))
	}))
	f := newControlledReadDBFixture(t, target.URL)
	f.registerAndResume(t)
	if len(f.e.controlledReads.pending) != 1 {
		t.Fatalf("resume discarded registered capability: pending=%d", len(f.e.controlledReads.pending))
	}
	if err := f.intentState("running"); err != nil {
		t.Fatal(err)
	}

	p := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"owner","resource_ref":"doc:one"}`}
	if ok := f.e.runIntent(context.Background(), f.task, "worker", runtimeWorker(t, p), f.intent, "", ""); !ok {
		t.Fatal("runIntent did not settle controlled work")
	}
	intent, err := f.task.Store.GetNode(f.intent.ID)
	if err != nil || intent == nil || intent.State != "done" {
		t.Fatalf("intent state=%+v err=%v", intent, err)
	}
	if hits.Load() != 1 {
		t.Fatalf("target hits=%d, want 1", hits.Load())
	}
	if f.task.lifecycleSnapshot().Status == "done" {
		t.Fatal("one intent settlement incorrectly marked diagnosis task done")
	}
	f.assertAudit(t)
}

func (f *controlledReadDBFixture) intentState(state string) error {
	return f.task.Store.SetIntentState(f.intent.ID, state)
}

func (f *controlledReadDBFixture) assertAudit(t *testing.T) {
	t.Helper()
	journals, _ := filepath.Glob(filepath.Join(f.store.journalDirectory(), "*.jsonl"))
	checkpoints, _ := filepath.Glob(filepath.Join(f.store.journalDirectory(), "*.checkpoint"))
	if len(journals) != 1 || len(checkpoints) != 1 {
		t.Fatalf("journal/checkpoint count=%d/%d", len(journals), len(checkpoints))
	}
	checkpointBody, err := os.ReadFile(checkpoints[0])
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint auditlog.Checkpoint
	if err := json.Unmarshal(checkpointBody, &checkpoint); err != nil {
		t.Fatal(err)
	}
	journal, err := os.Open(journals[0])
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	summary, err := auditlog.Verify(context.Background(), journal, bytes.Repeat([]byte{0x71}, 32), checkpoint)
	if err != nil || summary.Intents != 1 || summary.Results != 1 || checkpoint.Scope.TaskRef != f.task.ID {
		t.Fatalf("audit evidence mismatch: summary=%+v err=%v", summary, err)
	}
}

func TestControlledReadEngineDBInvalidInputBlocksWithoutTargetHit(t *testing.T) {
	var hits atomic.Int64
	target := controlledReadTarget(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	f := newControlledReadDBFixture(t, target.URL)
	f.registerAndResume(t)
	if err := f.intentState("running"); err != nil {
		t.Fatal(err)
	}
	p := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"owner","resource_ref":"doc:one","url":"http://escape.invalid"}`}
	if ok := f.e.runIntent(context.Background(), f.task, "worker", runtimeWorker(t, p), f.intent, "", ""); !ok {
		t.Fatal("runIntent did not settle blocked work")
	}
	intent, err := f.task.Store.GetNode(f.intent.ID)
	if err != nil || intent == nil || intent.State != "blocked" {
		t.Fatalf("invalid input state=%+v err=%v, want blocked", intent, err)
	}
	if hits.Load() != 0 {
		t.Fatalf("blocked input reached target: hits=%d", hits.Load())
	}
}

func TestControlledReadEngineDBCancelRevokesPendingAndParksTask(t *testing.T) {
	for _, mode := range []string{"soft", "hard"} {
		t.Run(mode, func(t *testing.T) {
			var hits atomic.Int64
			target := controlledReadTarget(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
			f := newControlledReadDBFixture(t, target.URL)
			f.registerAndResume(t)
			key := controlledWorkKey{task: f.task.ID, intent: f.intent.ID}
			registeredKey := f.e.controlledReads.pending[key].Key
			if len(registeredKey) == 0 {
				t.Fatal("registration did not retain an audit key")
			}

			request := httptest.NewRequest(http.MethodPost, "/api/tasks/"+f.task.ID+"/intents/"+itoa(f.intent.ID)+"/control", strings.NewReader(`{"action":"cancel","reason":"approval revoked","mode":"`+mode+`"}`))
			request.SetPathValue("id", f.task.ID)
			request.SetPathValue("iid", itoa(f.intent.ID))
			response := httptest.NewRecorder()
			f.s.controlIntent(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("cancel status=%d body=%s", response.Code, response.Body.String())
			}
			if len(f.e.controlledReads.pending) != 0 {
				t.Fatalf("cancel retained pending capability: %d", len(f.e.controlledReads.pending))
			}
			if !bytes.Equal(registeredKey, make([]byte, len(registeredKey))) {
				t.Fatal("cancel did not zero the revoked audit key")
			}
			if mode == "soft" {
				node, err := f.task.Store.GetNode(f.intent.ID)
				if err != nil || node == nil || node.State != db.StateIntentDeleted {
					t.Fatalf("soft cancel state=%+v err=%v", node, err)
				}
			} else if node, err := f.task.Store.GetNode(f.intent.ID); err != nil || node != nil {
				t.Fatalf("hard cancel retained intent: node=%+v err=%v", node, err)
			}

			// Cancel revokes only the intent capability. The controlled task remains
			// resumable; with no pending work, its idle transition must park it.
			f.e.maybeParkControlledTask(f.task)
			if !f.task.lifecycleSnapshot().Paused || f.s.occupiesConcurrencySlot(f.task) {
				t.Fatalf("cancelled controlled task was not parked: lifecycle=%+v slot=%v", f.task.lifecycleSnapshot(), f.s.occupiesConcurrencySlot(f.task))
			}
			if hits.Load() != 0 {
				t.Fatalf("cancelled pending read reached target: hits=%d", hits.Load())
			}
		})
	}
}

func TestControlledReadEngineDBControlRunningReadCompletesCleanup(t *testing.T) {
	for _, action := range []string{"pause", "cancel"} {
		t.Run(action, func(t *testing.T) {
			started := make(chan struct{})
			target := controlledReadTarget(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				close(started)
				<-r.Context().Done()
			}))
			f := newControlledReadDBFixture(t, target.URL)
			f.registerAndResume(t)
			if err := f.intentState("running"); err != nil {
				t.Fatal(err)
			}
			workerDone := make(chan struct{})
			go func() {
				defer close(workerDone)
				p := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"owner","resource_ref":"doc:one"}`}
				f.e.runIntent(context.Background(), f.task, "worker", runtimeWorker(t, p), f.intent, "", "")
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("registered request did not start")
			}
			body := `{"action":"` + action + `"}`
			if action == "cancel" {
				body = `{"action":"cancel","reason":"approval revoked","mode":"soft"}`
			}
			request := httptest.NewRequest(http.MethodPost, "/api/tasks/"+f.task.ID+"/intents/"+itoa(f.intent.ID)+"/control", strings.NewReader(body))
			request.SetPathValue("id", f.task.ID)
			request.SetPathValue("iid", itoa(f.intent.ID))
			response := httptest.NewRecorder()
			f.s.controlIntent(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("%s status=%d body=%s", action, response.Code, response.Body.String())
			}
			select {
			case <-workerDone:
			case <-time.After(2 * time.Second):
				t.Fatal("interrupted worker did not settle")
			}
			intent, err := f.task.Store.GetNode(f.intent.ID)
			want := "paused"
			if action == "cancel" {
				want = db.StateIntentDeleted
			}
			if err != nil || intent == nil || intent.State != want {
				t.Fatalf("%s state=%+v err=%v, want %s", action, intent, err, want)
			}
			f.assertAudit(t)
		})
	}
}
