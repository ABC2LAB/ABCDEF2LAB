package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
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
		"not in profile":   {"Bash": e2Bash, "WebFetch": e2WorkerFixtureTool("WebFetch", nil)},
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
		"outside profile": {bash, e2WorkerFixtureTool("WebFetch", nil)},
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

// 1단계(골격)는 동작 차이가 없어야 한다. 실제 원본 도구 목록에서 고른 결과가 프로필 도입 전 E② 도구 목록·순서와 같다.
func TestToolProfileMatchesPreviousE2ToolLists(t *testing.T) {
	ts := NewToolSet(nil, "")
	e2Bash := e2WorkerFixtureTool("Bash", nil)
	candidate := e2WorkerFixtureTool("submit_diagnostic_candidate", nil)
	base := append(ts.WorkerTools(), defaultToolsExcept("MultiEdit", "Glob", "Grep")...)
	worker, err := diagnosisWorkerProfile.selectTools(base, map[string]actool.CoreTool{"Bash": e2Bash, "submit_diagnostic_candidate": candidate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := profileToolNames(worker); got != "Bash,record_fact,node_detail,submit_diagnostic_candidate" || worker[0] != e2Bash || worker[3] != candidate {
		t.Fatalf("worker tools=%s", got)
	}
	// 프로필 도입 전 Planner 필터: PlannerTools를 순서대로 보며 이 13개 이름만 남겼다.
	previous := map[string]bool{
		"graph_overview": true, "list_findings": true, "list_facts": true, "node_detail": true,
		"expand_digest": true, "get_worker_output": true, "get_worker_trace": true,
		"search_all_worker_traces": true, "list_goals": true, "add_intent": true,
		"list_companies": true, "list_assets": true, "list_untested_assets": true,
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
