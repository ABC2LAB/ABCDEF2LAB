package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
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
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit/auditlog"
	"github.com/Autumn-27/norma/llm"
)

const runtimeSecret = "PRIVATE-RUNTIME-SECRET-DO-NOT-EXPOSE"

type runtimeReadProvider struct {
	t     *testing.T
	calls int
	name  string
	input string
}

func (p *runtimeReadProvider) Complete(_ context.Context, request llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls++
	encoded, err := json.Marshal(request)
	if err != nil {
		p.t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(runtimeSecret)) {
		p.t.Error("private credential/body reached model request")
	}
	if len(request.Tools) != 1 || request.Tools[0].Name != agent.ControlledHTTPToolName {
		p.t.Error("uncontrolled tool exposed to model")
	}
	if p.calls%2 == 1 {
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "call", Name: p.name, Input: json.RawMessage(p.input)}}}, "tool_use", llm.Usage{}, nil
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("read finished")}}, "end_turn", llm.Usage{}, nil
}

func (p *runtimeReadProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("unexpected stream"))
	}
}

func runtimeReadFixture(t *testing.T, target string) (*Engine, *Task, *db.Node, *controlledReadStore) {
	t.Helper()
	root := t.TempDir()
	store, err := openControlledReadStore(filepath.Join(root, "snapshots"), filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour)
	snapshot := snapshotJSON{TaskID: "17", IntentID: 22, CaseRef: "read-case", ResetRef: "reset-1", PolicyRef: "policy-1",
		Origin: target, ConnectIP: "127.0.0.1", ExpiresAt: expires, MaxRequests: 1, RequestTimeoutMS: 1000, MaxBodyBytes: 1024,
		KeyRef: "key-1", KeyFile: "audit.key",
		Sessions:  []sessionJSON{{Label: "owner", Handle: "session-1", Status: oracle.SubjectVerified, ExpiresAt: expires, HeaderFiles: map[string]string{"X-Session": "owner.secret"}}},
		Resources: []resourceJSON{{Ref: "doc:one", URL: target + "/document", ApprovalRef: "read-approval", ReadOnly: true, Accounts: []string{"owner"}}},
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"approved.json": body, "audit.key": bytes.Repeat([]byte{0x71}, 32), "owner.secret": []byte(runtimeSecret)} {
		if err := os.WriteFile(filepath.Join(store.snapshotDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	engine := NewEngine(nil)
	engine.controlledReads = newControlledReadRuntime(store)
	task := &Task{ID: "17", Paused: true, Description: runtimeSecret}
	engine.paused.Store(task.ID, true)
	intent := &db.Node{ID: 22, Kind: "intent"}
	if err := engine.registerControlledRead(context.Background(), task, intent.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	engine.paused.Delete(task.ID)
	task.updateLifecycle(func(state *taskLifecycleState) { state.Paused = false })
	return engine, task, intent, store
}

func runtimeWorker(t *testing.T, provider *runtimeReadProvider) *agent.Worker {
	t.Helper()
	w := agent.NewWorker(provider, "fake", t.TempDir(), nil, 12000, 4)
	w.SetNonStreaming(func() bool { return true })
	return w
}

func TestControlledReadRuntimeRealWorkerAndAudit(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != "GET" || r.Header.Get("X-Session") != runtimeSecret {
			t.Error("registered method/session not used")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"private":"` + runtimeSecret + `"}`))
	}))
	defer target.Close()
	e, task, intent, store := runtimeReadFixture(t, target.URL)
	ctx, finish, err := e.prepareControlledRead(e.execContextFor(context.Background(), task.ID), task, intent)
	if err != nil || finish == nil {
		t.Fatalf("prepare: %v", err)
	}
	p := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"owner","resource_ref":"doc:one"}`}
	if _, writes, err := runtimeWorker(t, p).Execute(ctx, "worker", 17, nil, nil, intent, nil, nil, nil, nil); err != nil || writes != (agent.WriteCounts{}) {
		t.Fatalf("worker: writes=%+v error=%v", writes, err)
	}
	if err := finish(); err != nil || hits.Load() != 1 {
		t.Fatalf("finish=%v target=%d", err, hits.Load())
	}
	journals, _ := filepath.Glob(filepath.Join(store.journalDirectory(), "*.jsonl"))
	checkpoints, _ := filepath.Glob(filepath.Join(store.journalDirectory(), "*.checkpoint"))
	if len(journals) != 1 || len(checkpoints) != 1 {
		t.Fatalf("journal/checkpoint count=%d/%d", len(journals), len(checkpoints))
	}
	body, err := os.ReadFile(checkpoints[0])
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint auditlog.Checkpoint
	if err := json.Unmarshal(body, &checkpoint); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(journals[0])
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if summary, err := auditlog.Verify(context.Background(), file, bytes.Repeat([]byte{0x71}, 32), checkpoint); err != nil || summary.Intents != 1 || summary.Results != 1 || checkpoint.Scope.TaskRef != task.ID {
		t.Fatalf("execution evidence mismatch: %+v %v", summary, err)
	}
	if _, _, err := e.prepareControlledRead(context.Background(), task, intent); err == nil {
		t.Fatal("consumed registration replayed")
	}
}

func TestControlledReadRuntimeNoBypassAndRetryBudget(t *testing.T) {
	for _, mode := range []string{"budget", "Bash", "WebFetch", "mcp__escape", "raw-url", "duplicate", "unknown-account", "unknown-resource"} {
		t.Run(mode, func(t *testing.T) {
			var hits atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
			defer target.Close()
			e, task, intent, _ := runtimeReadFixture(t, target.URL)
			ctx, finish, err := e.prepareControlledRead(context.Background(), task, intent)
			if err != nil {
				t.Fatal(err)
			}
			p := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"owner","resource_ref":"doc:one"}`}
			switch mode {
			case "Bash", "WebFetch", "mcp__escape":
				p.name = mode
			case "raw-url":
				p.input = `{"account_ref":"owner","resource_ref":"doc:one","url":"` + target.URL + `"}`
			case "duplicate":
				p.input = `{"account_ref":"owner","account_ref":"owner","resource_ref":"doc:one"}`
			case "unknown-account":
				p.input = `{"account_ref":"other","resource_ref":"doc:one"}`
			case "unknown-resource":
				p.input = `{"account_ref":"owner","resource_ref":"doc:other"}`
			}
			worker := runtimeWorker(t, p)
			_, _, _ = worker.Execute(ctx, "worker", 17, nil, nil, intent, nil, nil, nil, nil)
			want := int64(0)
			if mode == "budget" {
				want = 1
				// The Engine prepares once outside retries. A second Worker call on
				// that work cannot replace the transmitter or refill its request budget.
				_, _, _ = worker.Execute(ctx, "worker", 17, nil, nil, intent, nil, nil, nil, nil)
			}
			if err := finish(); err == nil || hits.Load() != want {
				t.Fatalf("denied/empty work became success: finish=%v hits=%d want=%d", err, hits.Load(), want)
			}
		})
	}
}

func TestControlledReadRuntimeRestartAndUnregisteredIntentFailClosed(t *testing.T) {
	e, task, intent, store := runtimeReadFixture(t, "http://127.0.0.1:1")
	for _, id := range []int64{intent.ID, intent.ID + 1} {
		restarted := NewEngine(nil)
		restarted.controlledReads = newControlledReadRuntime(store)
		if !restarted.controlledTask(task.ID) {
			t.Fatal("restart lost controlled task marker")
		}
		if _, _, err := restarted.prepareControlledRead(context.Background(), task, &db.Node{ID: id, Kind: "intent"}); err == nil {
			t.Fatal("missing approval fell back to normal worker")
		}
	}
	if _, _, err := e.prepareControlledRead(context.Background(), task, &db.Node{ID: intent.ID + 1, Kind: "intent"}); err == nil {
		t.Fatal("new unapproved intent bypassed task-wide control")
	}
}

func TestControlledReadRuntimeCheckpointFailurePreventsSend(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	e, task, intent, store := runtimeReadFixture(t, target.URL)
	ctx, finish, err := e.prepareControlledRead(context.Background(), task, intent)
	if err != nil {
		t.Fatal(err)
	}
	checkpoints, _ := filepath.Glob(filepath.Join(store.journalDirectory(), "*.checkpoint"))
	if len(checkpoints) != 1 {
		t.Fatal("header checkpoint not acknowledged before tool")
	}
	if err := os.Remove(checkpoints[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(checkpoints[0], 0o700); err != nil {
		t.Fatal(err)
	}
	p := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"owner","resource_ref":"doc:one"}`}
	_, _, _ = runtimeWorker(t, p).Execute(ctx, "worker", 17, nil, nil, intent, nil, nil, nil, nil)
	if err := finish(); err == nil || hits.Load() != 0 || strings.Contains(err.Error(), store.stateDir) {
		t.Fatalf("checkpoint failure failed open: error=%v hits=%d", err, hits.Load())
	}
}

func TestControlledReadRuntimePauseCancelsRegisteredRead(t *testing.T) {
	started := make(chan struct{})
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer target.Close()
	e, task, intent, store := runtimeReadFixture(t, target.URL)
	ctx, finish, err := e.prepareControlledRead(e.execContextFor(context.Background(), task.ID), task, intent)
	if err != nil {
		t.Fatal(err)
	}
	p := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"owner","resource_ref":"doc:one"}`}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = runtimeWorker(t, p).Execute(ctx, "worker", 17, nil, nil, intent, nil, nil, nil, nil)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("registered request did not start")
	}
	e.Pause(task.ID, agent.AbortPausedByUser)
	// Close the lease before the Worker returns. The sent request still needs a
	// terminal result and checkpoint before the journal can be closed.
	if err := finish(); err == nil {
		t.Fatal("interrupted read reported completion")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pause did not cancel controlled worker")
	}
	journals, _ := filepath.Glob(filepath.Join(store.journalDirectory(), "*.jsonl"))
	checkpoints, _ := filepath.Glob(filepath.Join(store.journalDirectory(), "*.checkpoint"))
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
	if summary, err := auditlog.Verify(context.Background(), journal, bytes.Repeat([]byte{0x71}, 32), checkpoint); err != nil || summary.Intents != 1 || summary.Results != 1 {
		t.Fatalf("paused request audit incomplete: summary=%+v err=%v", summary, err)
	}
}

func TestControlledReadAPIRejectsRawAndAmbiguousInput(t *testing.T) {
	s := &Server{}
	for _, body := range []string{`{}`, `{"snapshot_ref":"approved","url":"http://example.invalid"}`, `{"snapshot_ref":"one","snapshot_ref":"two"}`, `{"snapshot_ref":"approved"} garbage`, `{"snapshot_ref":"../secret"}`} {
		request := httptest.NewRequest("POST", "/api/tasks/17/intents/22/controlled-read", strings.NewReader(body))
		response := httptest.NewRecorder()
		s.registerControlledReadHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("raw/ambiguous input accepted: %d", response.Code)
		}
	}
	request := httptest.NewRequest("POST", "/api/tasks/17/intents/22/controlled-read", strings.NewReader(`{"snapshot_ref":"approved"}`))
	response := httptest.NewRecorder()
	s.requireAuth(http.HandlerFunc(s.registerControlledReadHTTP)).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("snapshot registration lacks JWT protection: %d", response.Code)
	}
}

func TestControlledReadStopRevokesPendingApproval(t *testing.T) {
	e, task, intent, _ := runtimeReadFixture(t, "http://127.0.0.1:1")
	key := e.controlledReads.pending[controlledWorkKey{task.ID, intent.ID}].Key
	e.StopTask(task.ID)
	if len(e.controlledReads.pending) != 0 || len(e.controlledReads.active) != 0 || !bytes.Equal(key, make([]byte, len(key))) {
		t.Fatal("Stop retained a pending capability or key")
	}
	// Archive/restore can reuse this task ID; the disk marker must still refuse
	// the old approval rather than constructing a fresh sender with a new budget.
	if _, _, err := e.prepareControlledRead(context.Background(), task, intent); err == nil {
		t.Fatal("restored task reused pre-Stop approval")
	}
}

func TestControlledReadRegistrationRejectsTerminalTask(t *testing.T) {
	for _, status := range []string{"done", "failed", "timeout"} {
		t.Run(status, func(t *testing.T) {
			e, task, intent, _ := runtimeReadFixture(t, "http://127.0.0.1:1")
			e.Pause(task.ID, agent.AbortPausedByUser)
			task.updateLifecycle(func(state *taskLifecycleState) {
				state.Paused, state.Status = true, status
			})
			if err := e.registerControlledRead(context.Background(), task, intent.ID, "approved"); err == nil || len(e.controlledReads.pending) != 0 {
				t.Fatal("terminal task received an unusable approval")
			}
		})
	}
}

func TestControlledReadInitializationRejectsUnavailableStore(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTEX_E2_SNAPSHOT_DIR", blocked)
	t.Setenv("ARTEX_E2_STATE_DIR", filepath.Join(root, "state"))
	s := &Server{engine: NewEngine(nil), ctx: context.Background()}
	if err := s.initControlledReads(root, root); err == nil || s.engine.controlledReads != nil {
		t.Fatal("unavailable authority was installed as a silent task-blocking runtime")
	}
}

func TestControlledReadDefaultsStayInsidePersistentDataDirectory(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	t.Setenv("ARTEX_E2_SNAPSHOT_DIR", "")
	t.Setenv("ARTEX_E2_STATE_DIR", "")
	s := &Server{engine: NewEngine(nil), ctx: context.Background()}
	if err := s.initControlledReads(root, dataDir); err != nil {
		t.Fatal(err)
	}
	store := s.engine.controlledReads.store
	if store.snapshotDir != filepath.Join(dataDir, "e2-snapshots") || store.stateDir != filepath.Join(dataDir, "e2-state") {
		t.Fatalf("private store escaped data directory: snapshots=%q state=%q", store.snapshotDir, store.stateDir)
	}
	for _, name := range []string{"e2-snapshots", "e2-state"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("private store created in key directory: %s: %v", name, err)
		}
	}
}

func TestControlledReadCompletedOrBlockedWorkReleasesSlot(t *testing.T) {
	for _, resource := range []string{"doc:one", "doc:unregistered"} {
		t.Run(resource, func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			defer target.Close()
			e, task, intent, _ := runtimeReadFixture(t, target.URL)
			task.updateLifecycle(func(state *taskLifecycleState) { state.Status = "running" })
			e.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return &agent.Planner{}, &agent.Worker{} })
			s := &Server{engine: e}
			if !s.occupiesConcurrencySlot(task) {
				t.Fatal("fixture did not occupy a slot")
			}
			ctx, finish, err := e.prepareControlledRead(context.Background(), task, intent)
			if err != nil {
				t.Fatal(err)
			}
			p := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"owner","resource_ref":"` + resource + `"}`}
			_, _, _ = runtimeWorker(t, p).Execute(ctx, "worker", 17, nil, nil, intent, nil, nil, nil, nil)
			finishErr := finish()
			if resource == "doc:one" && finishErr != nil || resource != "doc:one" && finishErr == nil {
				t.Fatalf("unexpected read outcome: %v", finishErr)
			}
			e.controlledIdle = func(task *Task) {
				e.Pause(task.ID, agent.AbortPausedByOrchestrator)
				task.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
			}
			e.maybeParkControlledTask(task)
			if !task.lifecycleSnapshot().Paused || task.lifecycleSnapshot().Status == "done" || s.occupiesConcurrencySlot(task) {
				t.Fatal("controlled work retained slot or became diagnosis-complete")
			}
		})
	}
}

func TestControlledReadSnapshotRejectsAmbiguousAuthorityAndUnsafeLimits(t *testing.T) {
	for _, change := range []string{"canonical-header", "audit-key-header", "audit-key-copy", "body", "requests", "timeout"} {
		t.Run(change, func(t *testing.T) {
			_, task, intent, store := runtimeReadFixture(t, "http://127.0.0.1:1")
			path := filepath.Join(store.snapshotDir, "approved.json")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var snapshot snapshotJSON
			if err := json.Unmarshal(body, &snapshot); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "canonical-header":
				snapshot.Sessions[0].HeaderFiles["x-session"] = "owner.secret"
			case "audit-key-header":
				snapshot.Sessions[0].HeaderFiles["X-Session"] = snapshot.KeyFile
			case "audit-key-copy":
				if err := os.WriteFile(filepath.Join(store.snapshotDir, "copy.secret"), bytes.Repeat([]byte{0x71}, 32), 0o600); err != nil {
					t.Fatal(err)
				}
				snapshot.Sessions[0].HeaderFiles["X-Session"] = "copy.secret"
			case "body":
				snapshot.MaxBodyBytes = maxControlledBodyBytes + 1
			case "requests":
				snapshot.MaxRequests = maxControlledRequests + 1
			case "timeout":
				snapshot.RequestTimeoutMS = maxControlledTimeoutMS + 1
			}
			body, err = json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.load(task.ID, intent.ID, "approved"); err == nil {
				t.Fatal("ambiguous credentials or unsafe limit accepted")
			}
		})
	}
}
