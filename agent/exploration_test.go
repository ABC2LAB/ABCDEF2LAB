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
	answer   string // 비어 있지 않으면 마지막 답을 이것으로 바꾼다.
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
	answer := "탐색 발견 보고 완료"
	if p.answer != "" {
		answer = p.answer
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(answer)}}, "end_turn", llm.Usage{}, nil
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
	}, func(o *ExplorationRunOptions) { o.Redact = nil }, func(o *ExplorationRunOptions) { o.Check = nil }, func(o *ExplorationRunOptions) { o.APIRecon = actool.Build(actool.Spec{Name: "WebFetch"}) }, func(o *ExplorationRunOptions) { o.MaxDuration = time.Hour }} {
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
		// 직접 Bash 칸은 typed 격리 실행만 받으므로 이름만 Bash인 CoreTool은 일반 칸(API Recon)으로만 들어올 수 있다.
		options.APIRecon = tool
		if _, err := worker.ExecuteExploration(t.Context(), options, nil); !errors.Is(err, ErrExplorationProfile) {
			t.Fatal("generic direct sandbox capability accepted")
		}
	}
	if provider.calls != 0 || called != 0 {
		t.Fatal("unsupported direct capability activated model or script")
	}
}

// 승인 경로가 있으면 탐색 에이전트의 첫 입력이 승인 경로 전체와 같은 origin의 시작점(정확 일치 포함)을 주고, 점검 주소
// 하나가 아니라 시작점마다 탐색하라고 지시한다(VAmPI 2차 회차: 점검 주소 경로에서만 탐색). 잘못된 승인 경로는 거부한다.
func TestExplorationStartsFromEveryApprovedPath(t *testing.T) {
	provider := &explorationProvider{}
	w := NewWorker(provider, "owned", t.TempDir(), nil, 0, 10)
	w.SetNonStreaming(func() bool { return true })
	o := explorationOptions(&recordingDiscoveryReporter{n: 1})
	o.TargetURL = "https://owned.test:8443/api/v1"
	o.ApprovedPaths = []string{"/api/v1", "/openapi.json", "=/"}
	if _, err := w.ExecuteExploration(t.Context(), o, nil); err != nil || len(provider.requests) == 0 {
		t.Fatal("exploration run failed", err)
	}
	first := ""
	for _, msg := range provider.requests[0].Messages {
		for _, block := range msg.Content {
			first += block.Text
		}
	}
	for _, w := range []string{"starting from every start point, not only target_url", `"approved_paths":["/api/v1","/openapi.json","=/"]`, `"start_points":["https://owned.test:8443/api/v1","https://owned.test:8443/openapi.json","https://owned.test:8443/"]`} {
		if !strings.Contains(first, w) {
			t.Fatalf("first exploration input missing %q", w)
		}
	}
	o.ApprovedPaths = []string{"/ok", "relative"}
	if _, err := NewWorker(&explorationProvider{}, "owned", t.TempDir(), nil, 0, 10).ExecuteExploration(t.Context(), o, nil); !errors.Is(err, ErrExplorationProfile) {
		t.Fatal("invalid approved path accepted", err)
	}
	o.ApprovedPaths = nil
	plain := &explorationProvider{}
	pw := NewWorker(plain, "owned", t.TempDir(), nil, 0, 10)
	pw.SetNonStreaming(func() bool { return true })
	if _, err := pw.ExecuteExploration(t.Context(), o, nil); err != nil {
		t.Fatal(err)
	}
	for _, msg := range plain.requests[0].Messages {
		for _, block := range msg.Content {
			if strings.Contains(block.Text, "approved_paths") || strings.Contains(block.Text, "start point") {
				t.Fatal("no approved paths must keep the original single-target input")
			}
		}
	}
}

// 저장 기록 전용 가림(RedactRecord): 대상이 돌려준 시험 계정 비밀번호는 모델 입력에는 그대로 가고(11/23 규칙), 활동 기록에서만
// 가려진다(2026-11-24 사용자 결정, #875 발견 3). RedactRecord가 없으면 지금처럼 Redact만 쓴다.
func TestExplorationRecordRedactionIsRecordOnly(t *testing.T) {
	const reflected = "reflected-tester-password"
	run := func(recordOnly func([]byte) []byte) (*explorationProvider, []db.Activity) {
		provider := &explorationProvider{}
		w := NewWorker(provider, "owned", t.TempDir(), nil, 0, 10)
		w.SetNonStreaming(func() bool { return true })
		o := explorationOptions(&recordingDiscoveryReporter{n: 1})
		for i, tool := range o.BrowserTools {
			o.BrowserTools[i] = actool.Build(actool.Spec{Name: tool.Name(), Description: "Owned browser fixture", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
				return actool.Text(`{"users":[{"username":"tester","password":"` + reflected + `"}]}`), nil
			}})
		}
		o.RedactRecord = recordOnly
		var activity []db.Activity
		if _, err := w.ExecuteExploration(t.Context(), o, func(a db.Activity) { activity = append(activity, a) }); err != nil {
			t.Fatal(err)
		}
		return provider, activity
	}
	modelSaw := func(p *explorationProvider) bool {
		for _, r := range p.requests {
			for _, msg := range r.Messages {
				for _, block := range msg.Content {
					for _, v := range block.Content {
						if strings.Contains(v.Text, reflected) {
							return true
						}
					}
				}
			}
		}
		return false
	}
	recorded := func(activity []db.Activity) bool {
		for _, a := range activity {
			if strings.Contains(a.Summary, reflected) || strings.Contains(a.Detail, reflected) || strings.Contains(string(a.Metadata), reflected) {
				return true
			}
		}
		return false
	}
	provider, activity := run(func(b []byte) []byte { return []byte(strings.ReplaceAll(string(b), reflected, "[REDACTED]")) })
	if !modelSaw(provider) {
		t.Fatal("record-only redaction hid the target response from the model")
	}
	if len(activity) == 0 || recorded(activity) {
		t.Fatalf("reflected password stored in activity (n=%d)", len(activity))
	}
	// 가림 목록이 없는 실행은 지금처럼 기록에 남는다(이 시험이 가림이 없는 경우를 실제로 구별하는지).
	provider, activity = run(nil)
	if !modelSaw(provider) || !recorded(activity) {
		t.Fatal("fixture does not exercise the record path")
	}
}
