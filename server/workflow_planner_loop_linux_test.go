//go:build linux

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/norma/llm"
)

type workflowLoopStore struct{}

func (workflowLoopStore) Create(context.Context, diagnosisflow.Record) error       { return nil }
func (workflowLoopStore) Save(context.Context, uint64, diagnosisflow.Record) error { return nil }
func (workflowLoopStore) Read(context.Context, string, string) (diagnosisflow.Record, error) {
	return diagnosisflow.Record{}, diagnosisflow.ErrUnavailable
}

type workflowLoopProvider struct{ requests chan []byte }

func (p *workflowLoopProvider) Complete(_ context.Context, request llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return llm.Message{}, "", llm.Usage{}, err
	}
	select {
	case p.requests <- raw:
	default:
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("Owned synthetic planning observation.")}}, "end_turn", llm.Usage{InputTokens: 10, OutputTokens: 10}, nil
}
func (*workflowLoopProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("non-streaming fixture required"))
	}
}

// Exercise plannerLoop -> runtime lease -> real Planner -> provider request.
// Discovery and the model are synthetic; no target HTTP or sandbox process runs.
func TestPlannerLoopRedactsSelectedPathsBeforeProviderOwnedPostgres(t *testing.T) {
	manager, task, intent := firstDiagnosisActualTask(t)
	if _, err := task.Store.AddGoal(map[string]any{"text": "Observe owned selected endpoints."}, "human"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	const origin = "https://owned.test"
	flow, err := diagnosisflow.New(ctx, diagnosisflow.Binding{JobID: "owned-planner-loop", TaskID: task.ID, RunRef: firstDiagnosisExpectedE2().RunRef, SourceDigest: strings.Repeat("a", 64), Owner: "ARTEX", Mode: diagnosisflow.Auto, TargetURL: origin + "/allowed/" + firstDiagnosisE2Secret, PathPrefixes: []string{"/allowed"}, ReadonlyPrefixes: []string{"/allowed"}, Methods: []string{"GET"}, Limits: diagnosisflow.Limits{Format: diagnosisflow.Version, MaxPages: 1, MaxEndpoints: 32, MaxResponseBytes: 4096}, Current: func() error { return nil }}, workflowLoopStore{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = flow.Crawl(ctx, func(context.Context, string) (e2runner.Result, error) {
		return e2runner.Result{ToolOutput: "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n<a href='/allowed/public'>owned</a>", Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Final: true, ProcessDrained: true, ProxyDrained: true, Call: e2proxy.CallResult{Final: true, AuditComplete: true, Counts: e2proxy.Counts{ForwardAttempted: 1}}}}, nil
	})
	if err != nil || !strings.Contains(flow.SelectedData(), firstDiagnosisE2Secret) {
		t.Fatal("sensitive selected-path fixture not established", err)
	}
	runtime, _ := e2HTTPRuntimeFixture(t, func(o *E2HTTPRuntimeOptions) { o.Workflow = flow; o.Expected = firstDiagnosisExpectedE2() })
	if err = runtime.AdmitTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(firstDiagnosisPrivateTemp(t), "never-run-helper")
	if err = os.WriteFile(helper, []byte("#!/bin/sh\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runner := firstDiagnosisAcquiredRunner(t, ctx, E2HTTPRequest{TaskID: task.ID, TaskIDNumber: mustFirstDiagnosisTaskNumber(task.ID), ExplorationID: task.ExpID, IntentID: intent.ID}, helper, origin, firstDiagnosisExpectedE2().RunRef)
	runtime.task.lease = &E2HTTPRunOptions{Execution: runner.Execution(), Runner: newLocalE2HTTPRun(runner), Release: func(context.Context) error { return nil }}
	runtime.task.validLease = true
	provider := &workflowLoopProvider{requests: make(chan []byte, 1)}
	planner := agent.NewPlanner(provider, "owned-fixture", firstDiagnosisPrivateTemp(t), nil, 8192, 1)
	planner.SetNonStreaming(func() bool { return true })
	engine := NewEngine(manager)
	engine.e2HTTP = runtime
	engine.debounce = time.Millisecond
	engine.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return planner, &agent.Worker{} })
	done := make(chan struct{})
	go func() { defer close(done); engine.plannerLoop(ctx, task) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("owned planner loop did not stop")
		}
	})
	task.Notify()
	var raw []byte
	select {
	case raw = <-provider.requests:
	case <-ctx.Done():
		t.Fatal("real planner loop never reached provider")
	}
	cancel()
	<-done
	if bytes.Contains(raw, []byte(firstDiagnosisE2Secret)) {
		t.Fatal("planner loop sent protected credential in selected path")
	}
	var request llm.CompletionRequest
	if err = json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(request.System, "\n"), "/allowed/public") {
		t.Fatal("selected target path promoted to system prompt")
	}
	userData := false
	for _, message := range request.Messages {
		if message.Role == llm.RoleUser && strings.Contains(message.Text(), "/allowed/public") && strings.Contains(message.Text(), "[REDACTED]") {
			userData = true
		}
	}
	if !userData {
		t.Fatal("planner loop omitted selected data instead of redacting it")
	}
}
