package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
	"github.com/Autumn-27/norma/llm"
)

func healthSnapshotPayload() map[string]any {
	return map[string]any{
		"task_id": "1", "intent_id": 2, "case_ref": "case", "reset_ref": "reset", "policy_ref": "policy",
		"origin": "http://127.0.0.1", "connect_ip": "127.0.0.1",
		"expires_at": time.Now().Add(time.Hour), "max_requests": 4, "request_timeout_ms": 1000, "max_body_bytes": 1024,
		"key_ref": "key-ref", "key_file": "audit.key",
		"sessions":  []map[string]any{{"label": "owner", "handle": "session", "status": "verified", "expires_at": time.Now().Add(time.Hour), "header_files": map[string]string{"X-Test": "header.txt"}}},
		"resources": []map[string]any{{"ref": "item:1", "url": "http://127.0.0.1/item/1", "approval_ref": "approval", "read_only": true, "accounts": []string{"owner"}}},
	}
}

func writeHealthSnapshot(t *testing.T, store *controlledReadStore, health any) {
	t.Helper()
	payload := healthSnapshotPayload()
	payload["health"] = health
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x31}, 32)
	for name, contents := range map[string][]byte{
		"approved.json": body, "audit.key": key, "header.txt": []byte("synthetic-header"),
	} {
		if err := os.WriteFile(filepath.Join(store.snapshotDir, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestControlledReadHealthSnapshotLoadBindsExplicitPolicy(t *testing.T) {
	root := t.TempDir()
	store, err := openControlledReadStore(filepath.Join(root, "snapshots"), filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	writeHealthSnapshot(t, store, map[string]any{
		"window_ms": 5000, "max_samples": 8, "min_samples": 2, "failure_limit": 2,
		"slow_after_ms": 0, "slow_limit": 0,
		"network_timeout_ms": 500,
	})
	spec, err := store.load("1", 2, "approved")
	if err != nil {
		t.Fatal(err)
	}
	if spec.HTTP.Health == nil {
		t.Fatal("explicit health policy was discarded")
	}
	if spec.HTTP.Health.Policy.Window != 5*time.Second || spec.HTTP.Health.Policy.MaxSamples != 8 || spec.HTTP.Health.Policy.MinSamples != 2 || spec.HTTP.Health.Policy.FailureLimit != 2 || spec.HTTP.Health.NetworkTimeout != 500*time.Millisecond {
		t.Fatalf("health policy was not converted exactly: %+v", spec.HTTP.Health)
	}
	if spec.HTTP.Health.Policy.ScopeRef != "validation" {
		t.Fatalf("snapshot policy was not kept in validation scope: %q", spec.HTTP.Health.Policy.ScopeRef)
	}
	cfg := controlledReadHTTPConfig(spec, transmit.Scope{RunRef: "run-1", TaskRef: "1", CaseRef: "case", ResetRef: "reset", PolicyRef: "policy"}, nil)
	if cfg.Health == nil || cfg.Health.Policy.ScopeRef != "run-1" {
		t.Fatalf("execution scope was not rebound: %+v", cfg.Health)
	}
}

func TestControlledReadHealthSnapshotAbsentKeepsTransmitterDisabled(t *testing.T) {
	root := t.TempDir()
	store, err := openControlledReadStore(filepath.Join(root, "snapshots"), filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	payload := healthSnapshotPayload()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string][]byte{
		"approved.json": body, "audit.key": bytes.Repeat([]byte{0x31}, 32), "header.txt": []byte("synthetic-header"),
	} {
		if err := os.WriteFile(filepath.Join(store.snapshotDir, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	spec, err := store.load("1", 2, "approved")
	if err != nil {
		t.Fatal(err)
	}
	if spec.HTTP.Health != nil {
		t.Fatalf("omitted health policy enabled transmitter: %+v", spec.HTTP.Health)
	}
}

func TestControlledReadHealthSnapshotNullOrMalformedFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		health any
	}{
		{name: "null", health: nil},
		{name: "zero", health: map[string]any{}},
		{name: "negative window", health: map[string]any{"window_ms": -1, "max_samples": 8, "min_samples": 2, "failure_limit": 2, "slow_after_ms": 0, "slow_limit": 0, "network_timeout_ms": 500}},
		{name: "network timeout too large", health: map[string]any{"window_ms": 5000, "max_samples": 8, "min_samples": 2, "failure_limit": 2, "slow_after_ms": 0, "slow_limit": 0, "network_timeout_ms": 1000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := openControlledReadStore(filepath.Join(root, "snapshots"), filepath.Join(root, "state"))
			if err != nil {
				t.Fatal(err)
			}
			writeHealthSnapshot(t, store, tc.health)
			if _, err := store.load("1", 2, "approved"); err == nil {
				t.Fatal("invalid health policy accepted")
			}
		})
	}
}

func registerRuntimeHealthSnapshot(t *testing.T, engine *Engine, task *Task, intentID int64, enabled bool) {
	t.Helper()
	path := filepath.Join(engine.controlledReads.store.snapshotDir, "approved.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot snapshotJSON
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.MaxRequests = 4
	if enabled {
		snapshot.Health, err = json.Marshal(controlledHealthFields())
		if err != nil {
			t.Fatal(err)
		}
	} else {
		snapshot.Health = nil
	}
	body, err = json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	engine.paused.Store(task.ID, true)
	task.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
	if err := engine.registerControlledRead(context.Background(), task, intentID, "approved"); err != nil {
		t.Fatal(err)
	}
	engine.paused.Delete(task.ID)
	task.updateLifecycle(func(state *taskLifecycleState) { state.Paused = false })
}

type healthResultProvider struct {
	*runtimeReadProvider
	enabled bool
	outputs int
}

func (p *healthResultProvider) Complete(ctx context.Context, request llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	for _, message := range request.Messages {
		for _, block := range message.Content {
			if block.Type != llm.BlockToolResult {
				continue
			}
			for _, part := range block.Content {
				if part.Type != llm.BlockText {
					continue
				}
				var output struct {
					OK      bool             `json:"ok"`
					Receipt transmit.Receipt `json:"receipt"`
					Health  json.RawMessage  `json:"health"`
				}
				if err := json.Unmarshal([]byte(part.Text), &output); err != nil {
					p.t.Fatal("tool result is not structured JSON")
				}
				p.outputs++
				if !p.enabled {
					if len(output.Health) != 0 || !output.OK || block.IsError {
						p.t.Fatal("omitted health changed tool output")
					}
					continue
				}
				var health transmit.HealthStatus
				if err := json.Unmarshal(output.Health, &health); err != nil || !health.Enabled || !health.InputValid {
					p.t.Fatal("safe health state missing from tool result")
				}
				if p.outputs == 1 {
					if health.Halted || !output.OK || block.IsError || health.Decision.Counts.Samples != 1 {
						p.t.Fatal("first observation changed completion semantics")
					}
				} else {
					if !health.Halted || !block.IsError || health.Decision.Counts.Failures != 2 {
						p.t.Fatal("health halt not propagated to tool error")
					}
					if p.outputs == 2 && (!output.OK || output.Receipt.Reason != transmit.Completed) {
						p.t.Fatal("threshold response receipt was overwritten")
					}
					if p.outputs == 3 && (output.OK || output.Receipt.Reason != transmit.Stopped || output.Receipt.Admitted) {
						p.t.Fatal("request after health halt was admitted")
					}
				}
			}
		}
	}
	return p.runtimeReadProvider.Complete(ctx, request)
}

func runControlledHealthWorker(t *testing.T, targetStatus int, enabled bool, calls int) (hits int, finishErr error, summary auditlog.Summary) {
	t.Helper()
	var hitCount atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitCount.Add(1)
		w.WriteHeader(targetStatus)
	}))
	defer target.Close()
	engine, task, intent, store := runtimeReadFixture(t, target.URL)
	registerRuntimeHealthSnapshot(t, engine, task, intent.ID, enabled)
	ctx, finish, err := engine.prepareControlledRead(context.Background(), task, intent)
	if err != nil {
		t.Fatal(err)
	}
	provider := &healthResultProvider{runtimeReadProvider: &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"owner","resource_ref":"doc:one"}`}, enabled: enabled}
	worker := agent.NewWorker(provider, "fake", t.TempDir(), nil, 12000, 4)
	worker.SetNonStreaming(func() bool { return true })
	for range calls {
		_, _, _ = worker.Execute(ctx, "worker", 17, nil, nil, intent, nil, nil, nil, nil)
	}
	if provider.outputs != calls {
		t.Fatalf("model observed %d tool results, want %d", provider.outputs, calls)
	}
	finishErr = finish()
	journals, err := filepath.Glob(filepath.Join(store.journalDirectory(), "*.jsonl"))
	if err != nil || len(journals) != 1 {
		t.Fatalf("journal count=%d err=%v", len(journals), err)
	}
	checkpoints, err := filepath.Glob(filepath.Join(store.journalDirectory(), "*.checkpoint"))
	if err != nil || len(checkpoints) != 1 {
		t.Fatalf("checkpoint count=%d err=%v", len(checkpoints), err)
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
	summary, err = auditlog.Verify(context.Background(), journal, bytes.Repeat([]byte{0x71}, 32), checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	return int(hitCount.Load()), finishErr, summary
}

func TestControlledReadHealthStopsRuntimeAndPreservesJournal(t *testing.T) {
	hits, finishErr, summary := runControlledHealthWorker(t, http.StatusServiceUnavailable, true, 3)
	if hits != 2 {
		t.Fatalf("health stop allowed target requests after threshold: %d", hits)
	}
	if finishErr == nil {
		t.Fatal("health halt was reported as successful controlled read")
	}
	if summary.Intents != 2 || summary.Results != 3 || summary.Checkpoint.Sequence != 5 {
		t.Fatalf("health stop journal summary=%+v", summary)
	}
	if summary.Checkpoint.Scope.RunRef == "" || summary.Checkpoint.Scope.RunRef == "validation" {
		t.Fatalf("journal kept validation scope instead of execution scope: %+v", summary.Checkpoint.Scope)
	}
}

func TestControlledReadHealthHaltStillFailsWithOnlyTwoRequests(t *testing.T) {
	hits, finishErr, summary := runControlledHealthWorker(t, http.StatusTooManyRequests, true, 2)
	if hits != 2 || finishErr == nil {
		t.Fatalf("health halt was not retained after final observed request: hits=%d finish=%v", hits, finishErr)
	}
	if summary.Intents != 2 || summary.Results != 2 || summary.Checkpoint.Sequence != 4 {
		t.Fatalf("two-request health journal summary=%+v", summary)
	}
}

func TestControlledReadHealthOmittedPreservesExistingRuntime(t *testing.T) {
	hits, finishErr, summary := runControlledHealthWorker(t, http.StatusServiceUnavailable, false, 3)
	if hits != 3 || finishErr != nil {
		t.Fatalf("omitted health changed existing runtime: hits=%d finish=%v", hits, finishErr)
	}
	if summary.Intents != 3 || summary.Results != 3 || summary.Checkpoint.Sequence != 6 {
		t.Fatalf("disabled-health journal summary=%+v", summary)
	}
}

func TestControlledReadHealthInvalidRegistrationCannotDowngrade(t *testing.T) {
	for _, name := range []string{"null", "foreign scope", "unreachable threshold"} {
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
			defer target.Close()
			engine, task, intent, store := runtimeReadFixture(t, target.URL)
			path := filepath.Join(store.snapshotDir, "approved.json")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var snapshot snapshotJSON
			if err := json.Unmarshal(body, &snapshot); err != nil {
				t.Fatal(err)
			}
			snapshot.MaxRequests = 4
			fields := controlledHealthFields()
			switch name {
			case "null":
				snapshot.Health = json.RawMessage("null")
			case "foreign scope":
				fields["scope_ref"] = "foreign-run"
			case "unreachable threshold":
				fields["failure_limit"] = 5
			}
			if name != "null" {
				snapshot.Health, err = json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
			}
			body, err = json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			engine.paused.Store(task.ID, true)
			task.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
			if err := engine.registerControlledRead(context.Background(), task, intent.ID, "approved"); err != errControlledReadUnavailable {
				t.Fatal("invalid health registration accepted or raw error escaped")
			}
			_, finish, err := engine.prepareControlledRead(context.Background(), task, intent)
			if finish != nil {
				_ = finish()
			}
			if err != errControlledReadUnavailable || hits.Load() != 0 {
				t.Fatal("invalid health registration fell back to unrestricted execution")
			}
		})
	}
}
