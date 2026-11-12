package agent

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

type explorationProvider struct {
	failure  error
	calls    int
	requests []llm.CompletionRequest
}

func (p *explorationProvider) Complete(_ context.Context, r llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls++
	if p.failure != nil {
		return llm.Message{}, "", llm.Usage{}, p.failure
	}
	p.requests = append(p.requests, r)
	if p.calls <= 2 {
		name := "mcp__explore_browser__browser_navigate"
		input := `{"url":"https://owned.test/"}`
		if p.calls == 2 {
			name = "report_discovery"
			input = `{"discoveries":[{"method":"GET","path":"/read/api"}]}`
		}
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "owned-call", Name: name, Input: json.RawMessage(input)}}}, "tool_use", llm.Usage{}, nil
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("탐색 발견 보고 완료")}}, "end_turn", llm.Usage{}, nil
}
func (p *explorationProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) { yield(llm.StreamEvent{}, errors.New("stream not used")) }
}
func explorationOptions(reporter ExplorationReporter) ExplorationRunOptions {
	b := exploration.Binding{SessionRef: "hidden-session", RunRef: "hidden-run", JobID: "hidden-job", TaskID: "7", TargetRef: "hidden-target", TargetRevision: 1, WriteMode: exploration.ReadOnly, CommMode: exploration.FullLocal, Limits: exploration.Limits{TimeBudgetSeconds: 10, MaxRequests: 10, MaxConcurrency: 1, MaxResponseBytes: 4096}}
	tools := []actool.CoreTool{}
	for _, name := range exploration.BrowserToolNames() {
		tools = append(tools, actool.Build(actool.Spec{Name: "mcp__explore_browser__" + name, Description: "Owned browser fixture", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			return actool.Text("seeded-private-marker"), nil
		}}))
	}
	return ExplorationRunOptions{Binding: b, TargetURL: "https://owned.test/", BrowserTools: tools, Reporter: reporter, Redact: func(b []byte) []byte {
		return []byte(strings.ReplaceAll(string(b), "seeded-private-marker", "[REDACTED]"))
	}, Check: func(context.Context) error { return nil }, MaxTurns: 5, MaxDuration: 5 * time.Second}
}
func TestExplorationUsesWorkerSessionWithoutGraphAugmentOrNativeNetworkTools(t *testing.T) {
	provider := &explorationProvider{}
	w := NewWorker(provider, "owned", t.TempDir(), nil, 0, 10)
	w.SetNonStreaming(func() bool { return true })
	reporter := &recordingDiscoveryReporter{n: 1}
	o := explorationOptions(reporter)
	augment := 0
	ctx := WithRuntimeHooks(t.Context(), RuntimeHooks{ToolAugment: func(context.Context, string) ([]actool.CoreTool, DeferredInfo, func()) {
		augment++
		return nil, DeferredInfo{}, func() {}
	}})
	var activity []db.Activity
	result, err := w.ExecuteExploration(ctx, o, func(a db.Activity) { activity = append(activity, a) })
	if err != nil || result.Reason != harness.ReasonCompleted || provider.calls != 3 || len(reporter.calls) != 1 || augment != 0 {
		t.Fatal("exploration execution not isolated", err)
	}
	if len(result.Tools) != 22 {
		t.Fatal("unexpected tool surface")
	}
	for _, r := range provider.requests {
		for _, tool := range r.Tools {
			if !strings.HasPrefix(tool.Name, "mcp__explore_browser__") && tool.Name != "report_discovery" {
				t.Fatal("native or graph tool exposed", tool.Name)
			}
		}
		for _, sys := range r.System {
			if strings.Contains(sys, o.Binding.SessionRef) || strings.Contains(sys, o.Binding.TargetRef) {
				t.Fatal("private control refs exposed")
			}
		}
		for _, msg := range r.Messages {
			for _, block := range msg.Content {
				for _, v := range block.Content {
					if strings.Contains(v.Text, "seeded-private-marker") {
						t.Fatal("S09 marker reached model")
					}
				}
			}
		}
	}
	for _, a := range activity {
		if strings.Contains(a.Detail, "seeded-private-marker") {
			t.Fatal("S09 marker reached activity")
		}
	}
}
func TestExplorationRejectsForeignOrIncompleteProfilesBeforeProvider(t *testing.T) {
	provider := &explorationProvider{}
	w := NewWorker(provider, "owned", t.TempDir(), nil, 0, 10)
	reporter := &recordingDiscoveryReporter{n: 1}
	for _, change := range []func(*ExplorationRunOptions){func(o *ExplorationRunOptions) { o.BrowserTools = o.BrowserTools[:1] }, func(o *ExplorationRunOptions) { o.BrowserTools = append(o.BrowserTools, o.BrowserTools[0]) }, func(o *ExplorationRunOptions) {
		o.BrowserTools = append(o.BrowserTools, actool.Build(actool.Spec{Name: "record_fact"}))
	}, func(o *ExplorationRunOptions) { o.Redact = nil }, func(o *ExplorationRunOptions) { o.Check = nil }, func(o *ExplorationRunOptions) { o.DirectBash = actool.Build(actool.Spec{Name: "WebFetch"}) }, func(o *ExplorationRunOptions) { o.MaxDuration = time.Hour }} {
		o := explorationOptions(reporter)
		change(&o)
		_, err := w.ExecuteExploration(t.Context(), o, nil)
		if !errors.Is(err, ErrExplorationProfile) {
			t.Fatal("invalid profile accepted", err)
		}
	}
	o := explorationOptions(reporter)
	_, err := w.ExecuteExploration(WithE2HTTPExecution(t.Context(), E2HTTPExecution{}), o, nil)
	if !errors.Is(err, ErrExplorationProfile) || provider.calls != 0 {
		t.Fatal("diagnosis/exploration capabilities mixed")
	}
}
func TestExplorationToolChecksLeaseBeforeCallingAndRedactsFailures(t *testing.T) {
	called := 0
	tool := &explorationTool{CoreTool: actool.Build(actool.Spec{Name: "Bash", Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
		called++
		return actool.Text("seeded-private-marker"), nil
	}}), check: func(context.Context) error { return exploration.ErrClosed }, redact: func(b []byte) []byte { return b }}
	result, err := tool.Call(t.Context(), json.RawMessage(`{}`), nil)
	if err != nil || !result.IsError || called != 0 {
		t.Fatal("closed lease called tool")
	}
}

func TestExplorationRedactsStructuredErrorActivityAndPreservesErrorIdentity(t *testing.T) {
	privateErr := errors.New("seeded-private-marker")
	provider := &explorationProvider{failure: privateErr}
	worker := NewWorker(provider, "owned", t.TempDir(), nil, 0, 10)
	worker.SetNonStreaming(func() bool { return true })
	var records []db.Activity
	_, err := worker.ExecuteExploration(t.Context(), explorationOptions(&recordingDiscoveryReporter{}), func(a db.Activity) { records = append(records, a) })
	if err == nil || !errors.Is(err, privateErr) || strings.Contains(err.Error(), "seeded-private-marker") {
		t.Fatal("model error was replaced or exposed")
	}
	for _, a := range records {
		if strings.Contains(a.Summary+a.Detail+string(a.Metadata), "seeded-private-marker") {
			t.Fatal("private model failure entered activity")
		}
	}
}
func TestExplorationToolNeverSpillsUnredactedOutput(t *testing.T) {
	dir := t.TempDir()
	core := actool.Build(actool.Spec{Name: "Bash", Run: func(_ context.Context, _ json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
		return actool.Text(actool.Capture(tc, strings.Repeat("seeded-private-marker", 10))), nil
	}})
	tool := &explorationTool{CoreTool: core, check: func(context.Context) error { return nil }, redact: func(b []byte) []byte {
		return []byte(strings.ReplaceAll(string(b), "seeded-private-marker", "[REDACTED]"))
	}}
	_, err := tool.Call(t.Context(), json.RawMessage(`{}`), &actool.ToolContext{OutputDir: dir, MaxOutputChars: 8})
	files, readErr := os.ReadDir(dir)
	if err != nil || readErr != nil || len(files) != 0 {
		t.Fatal("raw tool output spilled before redaction")
	}
}

func TestExplorationGuardsRejectUnsafeSameSizeProfileControlledContextAndURLShapes(t *testing.T) {
	provider := &explorationProvider{}
	worker := NewWorker(provider, "owned", t.TempDir(), nil, 0, 10)
	options := explorationOptions(&recordingDiscoveryReporter{})
	options.BrowserTools[0] = actool.Build(actool.Spec{Name: "mcp__explore_browser__browser_run_code_unsafe"})
	if _, err := worker.ExecuteExploration(t.Context(), options, nil); !errors.Is(err, ErrExplorationProfile) {
		t.Fatal("same-size unsafe catalog accepted")
	}
	options = explorationOptions(&recordingDiscoveryReporter{})
	if _, err := worker.ExecuteExploration(WithControlledReadExecution(t.Context(), ControlledReadExecution{}), options, nil); !errors.Is(err, ErrExplorationProfile) {
		t.Fatal("registered read/exploration contexts mixed")
	}
	for _, target := range []string{"ftp://owned.test/", "https://u:p@owned.test/", "https://owned.test/?token=seeded", "https://owned.test/#fragment"} {
		options.TargetURL = target
		if _, err := worker.ExecuteExploration(t.Context(), options, nil); !errors.Is(err, ErrExplorationProfile) {
			t.Fatal("unsafe URL shape accepted")
		}
	}
	if provider.calls != 0 {
		t.Fatal("guard rejection called provider")
	}
}

func TestExplorationRejectsGenericBashAndSkillUntilTypedSandboxExists(t *testing.T) {
	provider := &explorationProvider{}
	worker := NewWorker(provider, "owned", t.TempDir(), nil, 0, 10)
	called := 0
	for _, name := range []string{"Bash", "Skill"} {
		options := explorationOptions(&recordingDiscoveryReporter{})
		tool := actool.Build(actool.Spec{Name: name, Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			called++
			return actool.Text("unsafe fixture execution"), nil
		}})
		if name == "Bash" {
			options.DirectBash = tool
		} else {
			options.APIRecon = tool
		}
		if _, err := worker.ExecuteExploration(t.Context(), options, nil); !errors.Is(err, ErrExplorationProfile) {
			t.Fatal("generic direct sandbox capability accepted")
		}
	}
	if provider.calls != 0 || called != 0 {
		t.Fatal("unsupported direct capability activated model or script")
	}
}
