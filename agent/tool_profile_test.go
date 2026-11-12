package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"iter"
	"reflect"
)

func profileToolNames(tools []actool.CoreTool) string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name())
	}
	return strings.Join(names, ",")
}

// bound 항목은 원본 목록에 같은 이름이 있어도 그쪽을 고르지 않는다. 호스트 Bash가 진단 도구로 새면 E②를 우회한다.
func TestToolProfileBoundEntriesNeverComeFromBase(t *testing.T) {
	hostCalled := false
	host := e2WorkerFixtureTool("Bash", func(context.Context, json.RawMessage) (actool.Result, error) {
		hostCalled = true
		return actool.Text("host"), nil
	})
	hostCandidate := e2WorkerFixtureTool("submit_diagnostic_candidate", nil)
	e2Bash := e2WorkerFixtureTool("Bash", nil)
	base := []actool.CoreTool{host, hostCandidate, e2WorkerFixtureTool("record_fact", nil)}
	tools, err := diagnosisWorkerProfile.selectTools(base, map[string]actool.CoreTool{"Bash": e2Bash}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if profileToolNames(tools) != "Bash,record_fact" || tools[0] != e2Bash {
		t.Fatalf("tools=%s", profileToolNames(tools))
	}
	if _, err := tools[0].Call(context.Background(), json.RawMessage(`{}`), nil); err != nil || hostCalled {
		t.Fatal("host Bash selected")
	}
	// 원본 목록에 같은 이름이 둘이면 첫 도구만 고른다(원본 필터와 같다).
	first := e2WorkerFixtureTool("record_fact", nil)
	dup, err := diagnosisWorkerProfile.selectTools([]actool.CoreTool{nil, first, e2WorkerFixtureTool("record_fact", nil)}, map[string]actool.CoreTool{"Bash": e2Bash}, nil)
	if err != nil || profileToolNames(dup) != "Bash,record_fact" || dup[1] != first {
		t.Fatalf("duplicate base tools=%s err=%v", profileToolNames(dup), err)
	}
	// 필수 bound 항목이 비면 원본 목록에 Bash가 있어도 시작하지 않는다.
	if _, err := diagnosisWorkerProfile.selectTools(base, nil, nil); !errors.Is(err, ErrE2HTTPBinding) {
		t.Fatalf("missing E2 Bash accepted: %v", err)
	}
}

func TestToolProfileRejectsInvalidBindings(t *testing.T) {
	e2Bash := e2WorkerFixtureTool("Bash", nil)
	var typedNil *e2HTTPGraphTool
	for name, bindings := range map[string]map[string]actool.CoreTool{
		"not in profile":   {"Bash": e2Bash, "WebSearch": e2WorkerFixtureTool("WebSearch", nil)},
		"base entry bound": {"Bash": e2Bash, "record_fact": e2WorkerFixtureTool("record_fact", nil)},
		"name mismatch":    {"Bash": e2WorkerFixtureTool("Read", nil)},
		"typed nil":        {"Bash": e2Bash, "submit_diagnostic_candidate": typedNil},
		"untyped nil":      {"Bash": e2Bash, "submit_diagnostic_candidate": nil},
	} {
		if _, err := diagnosisWorkerProfile.selectTools(nil, bindings, nil); !errors.Is(err, ErrE2HTTPBinding) {
			t.Fatalf("%s: accepted %v", name, err)
		}
	}
}

func TestToolProfileCheck(t *testing.T) {
	bash, fact := e2WorkerFixtureTool("Bash", nil), e2WorkerFixtureTool("record_fact", nil)
	if err := diagnosisWorkerProfile.check([]actool.CoreTool{bash, fact}); err != nil {
		t.Fatal(err)
	}
	for name, tools := range map[string][]actool.CoreTool{
		"outside profile": {bash, e2WorkerFixtureTool("WebSearch", nil)},
		"duplicate":       {bash, fact, e2WorkerFixtureTool("record_fact", nil)},
		"mcp":             {bash, e2WorkerFixtureTool("mcp__browser__browser_navigate", nil)},
		"nil":             {bash, nil},
	} {
		if err := diagnosisWorkerProfile.check(tools); !errors.Is(err, ErrE2HTTPBinding) {
			t.Fatalf("%s: accepted %v", name, err)
		}
	}
	if err := diagnosisPlannerProfile.check([]actool.CoreTool{bash}); !errors.Is(err, ErrE2HTTPBinding) {
		t.Fatal("planner accepted Bash")
	}
}

// 실제 원본 도구 목록에서 고른 결과. Planner는 프로필 도입 전 E② 목록·순서와 같다(아직 복원 전).
func TestToolProfileMatchesOriginalToolLists(t *testing.T) {
	ts := NewToolSet(nil, "")
	e2Bash := e2WorkerFixtureTool("Bash", nil)
	candidate := e2WorkerFixtureTool("submit_diagnostic_candidate", nil)
	base := append(ts.WorkerTools(), defaultToolsExcept("MultiEdit", "Glob", "Grep")...)
	worker, err := diagnosisWorkerProfile.selectTools(base, map[string]actool.CoreTool{"Bash": e2Bash, "submit_diagnostic_candidate": candidate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 그래프 도구는 원본 WorkerTools 순서 그대로이고, 확정 기록(report_finding)과 자산 쓰기(insert_assets)만 빠진다.
	var graph []actool.CoreTool
	for _, tool := range ts.WorkerTools() {
		if tool.Name() != "report_finding" && tool.Name() != "insert_assets" {
			graph = append(graph, tool)
		}
	}
	want := "Bash," + profileToolNames(graph) + ",submit_diagnostic_candidate"
	if got := profileToolNames(worker); got != want || len(graph) != 6 || worker[0] != e2Bash || worker[len(worker)-1] != candidate {
		t.Fatalf("worker tools=%s want=%s", got, want)
	}
	// 프로필 도입 전 Planner 필터의 13개 이름에 C26 목표 도구(prove_goal·goal_met)와 add_task_scope를 더한 목록이다.
	// 순서는 PlannerTools 순서다. kill_work·steer_work·report_finding·add_company_scope는 아직 없다.
	previous := map[string]bool{
		"graph_overview": true, "list_findings": true, "list_facts": true, "node_detail": true,
		"expand_digest": true, "get_worker_output": true, "get_worker_trace": true,
		"search_all_worker_traces": true, "list_goals": true, "add_intent": true,
		"prove_goal": true, "goal_met": true,
		"list_companies": true, "list_assets": true, "add_task_scope": true, "list_untested_assets": true,
	}
	for _, coverage := range []bool{true, false} {
		ts.SetCoverageEnabled(coverage)
		plannerBase := ts.DropCoverageTools(ts.PlannerTools())
		var want []actool.CoreTool
		for _, tool := range plannerBase {
			if previous[tool.Name()] {
				want = append(want, tool)
			}
		}
		planner, err := diagnosisPlannerProfile.selectTools(plannerBase, nil, nil)
		if err != nil || profileToolNames(planner) != profileToolNames(want) || len(want) < 12 {
			t.Fatalf("coverage=%v planner=%s want=%s err=%v", coverage, profileToolNames(planner), profileToolNames(want), err)
		}
	}
}

// 진단 Worker의 그래프 도구 상태에는 대상으로 보내거나 확정을 기록하는 연결이 없다.
func TestE2HTTPWorkerToolSetHasNoSendOrConfirmHooks(t *testing.T) {
	toolset := newE2HTTPWorkerToolSet(nil, nil, "worker-1", 7, 22)
	if toolset.enrich != nil || toolset.notifyFinding != nil || toolset.findingRecorder != nil || toolset.killWork != nil || toolset.steerWork != nil || toolset.notify != nil {
		t.Fatal("diagnosis worker tool set has send or confirm hooks")
	}
	if toolset.taskID != 7 || toolset.ownerNode != 22 || toolset.worker != "worker-1" || toolset.CoverageDisabled() {
		t.Fatalf("tool set binding: task=%d owner=%d", toolset.taskID, toolset.ownerNode)
	}
}

// 진단 Planner의 add_task_scope는 원본 동작·스키마 그대로이고, 모델에 보이는 설명만 "송신 범위는 그대로"로 바뀐다.
// 원본 설명("authorization boundary"·"deliberately widening")이 남으면 모델이 승인 밖 호스트로 intent를 만들어
// 거부 상한에 닿을 수 있다.
func TestDiagnosisPlannerTaskScopeDescriptionKeepsBehavior(t *testing.T) {
	ts := NewToolSet(nil, "")
	var original actool.CoreTool
	for _, tool := range ts.PlannerTools() {
		if tool.Name() == "add_task_scope" {
			original = tool
		}
	}
	if original == nil || !strings.Contains(original.Description(), "authorization boundary") {
		t.Fatal("원본 add_task_scope 설명 전제가 바뀜")
	}
	planner, err := diagnosisPlannerProfile.selectTools(ts.PlannerTools(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var scoped actool.CoreTool
	for _, tool := range planner {
		if tool.Name() == "add_task_scope" {
			scoped = tool
		}
	}
	if scoped == nil || scoped.Description() != diagnosisTaskScopeDescription || strings.Contains(scoped.Description(), "authorization boundary") ||
		!strings.Contains(scoped.Description(), "does NOT change") || !reflect.DeepEqual(scoped.InputSchema(), original.InputSchema()) || scoped.Prompt() != original.Prompt() {
		t.Fatalf("진단 판 add_task_scope 설명·스키마가 틀림: %q", scoped.Description())
	}
	// 동작은 원본 그대로다: 자산 저장소가 없는 ToolSet이면 원본과 같은 오류 결과를 낸다.
	input := json.RawMessage(`{"kind":"ip","value":"192.0.2.1","reason":"fixture"}`)
	got, gotErr := scoped.Call(context.Background(), input, nil)
	want, wantErr := original.Call(context.Background(), input, nil)
	if (gotErr == nil) != (wantErr == nil) || !reflect.DeepEqual(got, want) {
		t.Fatalf("진단 판 add_task_scope 동작이 원본과 다름: %+v / %+v", got, want)
	}
	// 다른 Planner 도구는 원본 설명 그대로다.
	for _, tool := range planner {
		if tool.Name() == "add_task_scope" {
			continue
		}
		for _, base := range ts.PlannerTools() {
			if base.Name() == tool.Name() && base.Description() != tool.Description() {
				t.Fatalf("%s 설명이 바뀜", tool.Name())
			}
		}
	}
}

// toolSchemaProvider는 모델 요청에 실린 도구 스키마를 남기고 바로 끝낸다.
type toolSchemaProvider struct{ schemas map[string]llm.ToolSchema }

func (p *toolSchemaProvider) Complete(_ context.Context, request llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	for _, schema := range request.Tools {
		p.schemas[schema.Name] = schema
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("done")}}, "end_turn", llm.Usage{}, nil
}

func (*toolSchemaProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("unexpected streaming"))
	}
}

// 진단 Planner 옵션 조립(constrainE2HTTPPlannerOptions)을 거쳐 실제로 모델에 가는 도구 스키마에서도 add_task_scope는
// 진단 판 설명이고, 어떤 도구 설명도 "인가 경계"를 약속하지 않는다.
func TestDiagnosisPlannerTaskScopeDescriptionReachesProvider(t *testing.T) {
	ts := NewToolSet(nil, "")
	provider := &toolSchemaProvider{schemas: map[string]llm.ToolSchema{}}
	opts := agentcore.Options{Provider: provider, Tools: ts.PlannerTools(), PermissionMode: permission.ModeBypass, NonStreaming: true}
	if err := constrainE2HTTPPlannerOptions(context.Background(), &opts); err != nil {
		t.Fatal(err)
	}
	session := agentcore.NewSession(opts)
	defer session.Close()
	for range session.Prompt(context.Background(), "synthetic planning") {
	}
	scope, ok := provider.schemas["add_task_scope"]
	if !ok || scope.Description != diagnosisTaskScopeDescription {
		t.Fatalf("provider add_task_scope description: %q", scope.Description)
	}
	for name, schema := range provider.schemas {
		if strings.Contains(schema.Description, "authorization boundary") {
			t.Fatalf("%s promises an authorization boundary to the model", name)
		}
	}
}
