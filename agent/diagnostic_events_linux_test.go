//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/permission"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"github.com/Autumn-27/norma/harness"
	actool "github.com/Autumn-27/norma/tool"
)

func TestDiagnosticRegisteredToolsAndSchema(t *testing.T) {
	l, err := diagnosticlog.Create(t.TempDir(), "run", 42, "run")
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRunInfo(diagnosticlog.With(context.Background(), l), RunInfo{TaskID: 42, IntentID: 7})
	executed := 0
	tool := actool.Build(actool.Spec{Name: "Bash", Schema: map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}, "required": []string{"command"}}, Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
		executed++
		return actool.Text("secret-output"), nil
	}})
	ctx, tools := withDiagnosticTools(ctx, []actool.CoreTool{tool}, "worker")
	observeDiagnosticToolUse(ctx, "secret-unregistered-tool", json.RawMessage(`{"secret":"secret-input"}`))
	observeDiagnosticToolUse(ctx, "Bash", json.RawMessage(`{"command":42}`))
	observeDiagnosticToolUse(ctx, "Bash", json.RawMessage(`{"command":"secret-command"}`))
	if _, err := tools[0].Call(ctx, json.RawMessage(`{"command":"secret-command"}`), nil); err != nil {
		t.Fatal(err)
	}
	observeDiagnosticTerminal(ctx, &harness.Terminal{Reason: harness.ReasonMaxTurns, Turns: 3})
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-") {
		t.Fatal("raw tool data leaked")
	}
	var events []diagnosticlog.Event
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var e diagnosticlog.Event
		if json.Unmarshal([]byte(line), &e) != nil {
			t.Fatal("invalid event")
		}
		events = append(events, e)
		if e.TaskID != 42 || e.IntentID != 7 {
			t.Fatal("attribution missing")
		}
	}
	if executed != 1 || len(events) != 8 || events[0].ReasonCode != "unregistered_tool" || events[2].ReasonCode != "schema_invalid" || events[7].ReasonCode != "max_turns" {
		t.Fatalf("events disagree with execution: %d %+v", executed, events)
	}
}

type diagnosticFailureStore struct{}

func (diagnosticFailureStore) RecordDiagnosticCandidate(context.Context, db.DiagnosticCandidate) (int64, error) {
	return 0, errors.New("secret-database-dsn")
}
func TestDiagnosticCandidateEventsMatchStorage(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved", true: "failed"}[fail], func(t *testing.T) {
			execution, _, binding := e2WorkerFixture(t)
			l, err := diagnosticlog.Create(t.TempDir(), "run", binding.TaskID, binding.RunRef)
			if err != nil {
				t.Fatal(err)
			}
			ctx := diagnosticlog.With(t.Context(), l)
			state := &e2HTTPToolState{}
			state.observeDiagnostic(execution, binding.IntentID, "secret-command", e2runner.Result{ToolOutput: "secret-output", Trusted: e2runner.TrustedResult{Execution: execution.SnapshotForIntent(binding.IntentID), Call: e2proxy.CallResult{IntentID: binding.IntentID, CallRef: "actual-call"}, Classification: "completed"}})
			memory := &candidateMemoryStore{}
			var store diagnosticCandidateStore = memory
			if fail {
				store = diagnosticFailureStore{}
			}
			tool := newE2DiagnosticCandidateTool(store, binding, state)
			result, err := tool.Call(ctx, json.RawMessage(`{"call_ref":"invented-secret-ref","vulnclass":"XSS","name":"candidate","summary":"secret-summary"}`), nil)
			if err != nil || !result.IsError {
				t.Fatal("invalid evidence accepted")
			}
			result, err = tool.Call(ctx, json.RawMessage(`{"call_ref":"actual-call","vulnclass":"XSS","name":"candidate","summary":"secret-summary"}`), nil)
			if err != nil || result.IsError != fail {
				t.Fatalf("unexpected storage outcome: %v", err)
			}
			if err = l.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(l.Path())
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "secret-") || strings.Contains(string(raw), "invented-secret-ref") {
				t.Fatal("raw candidate or error leaked")
			}
			var saved, invalid, failed int
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				var e diagnosticlog.Event
				if json.Unmarshal([]byte(line), &e) != nil {
					t.Fatal("invalid JSONL")
				}
				if e.ReasonCode == "candidate_observation_not_found" {
					invalid++
				}
				if e.Event == "candidate_saved" {
					if e.Status == "manual_review_required" {
						saved++
					}
					if e.ReasonCode == "candidate_store_failed" {
						failed++
					}
				}
			}
			if invalid != 1 || saved != len(memory.candidates) || fail && failed != 1 || !fail && saved != 1 {
				t.Fatalf("events disagree with stored rows: saved=%d invalid=%d failed=%d actual=%d", saved, invalid, failed, len(memory.candidates))
			}
		})
	}
}

func TestDiagnosticSuccessfulToolThenMaxTurnsIsIncomplete(t *testing.T) {
	execution, _, binding := e2WorkerFixture(t)
	l, err := diagnosticlog.Create(t.TempDir(), "run", binding.TaskID, binding.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRunInfo(diagnosticlog.With(t.Context(), l), RunInfo{TaskID: binding.TaskID, IntentID: binding.IntentID})
	provider := &e2WorkerProvider{forever: true}
	worker := NewWorker(provider, "synthetic", t.TempDir(), nil, 10000, 2)
	state := &e2HTTPToolState{}
	calls := 0
	tool := e2WorkerBashTool(t, execution, func(context.Context, string) (e2runner.Result, error) { calls++; return completedE2WorkerResult(), nil }, time.Second, state)
	ctx, tools := withDiagnosticTools(ctx, []actool.CoreTool{tool}, "worker")
	opts := agentcore.Options{Provider: provider, Tools: tools, DisableBackgroundTasks: true, PermissionMode: permission.ModeBypass, MaxTurns: 2, MaxDuration: time.Second, NonStreaming: true}
	terminal, _, outcome := worker.runE2HTTPSession(ctx, "worker", &db.Node{ID: binding.IntentID, Kind: db.KindIntent}, nil, opts, "assigned intent", execution, state, nil)
	if calls == 0 || terminal != harness.ReasonMaxTurns || outcome != nil {
		t.Fatalf("invalid bounded fixture terminal=%s calls=%d err=%v", terminal, calls, outcome)
	}
	observeDiagnosticWorkerFinished(ctx, binding, terminal, outcome, time.Second)
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	var final diagnosticlog.Event
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if json.Unmarshal([]byte(lines[len(lines)-1]), &final) != nil {
		t.Fatal("invalid JSONL")
	}
	if final.Event != "worker_finished" || final.Status != "incomplete" || final.ReasonCode != "max_turns" {
		t.Fatalf("bounded stop became success: %+v", final)
	}
}
