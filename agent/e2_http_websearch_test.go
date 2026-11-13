package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// 설정은 끔이거나 백엔드에 맞는 키만 받는다. 모델 호출로 검색하는 deepseek·모르는 백엔드·다른 백엔드 키 섞임은 거부한다.
// 출력 형식으로 키가 새지 않는다.
func TestE2HTTPWebSearchConfig(t *testing.T) {
	for _, tc := range []struct {
		cfg            E2HTTPWebSearch
		valid, enabled bool
	}{
		{E2HTTPWebSearch{}, true, false},
		{E2HTTPWebSearch{Backend: "ddgs"}, true, true},
		{E2HTTPWebSearch{Backend: "brave-free", BraveKey: "synthetic-brave-key"}, true, true},
		{E2HTTPWebSearch{Backend: "tavily", TavilyKey: "synthetic-tavily-key"}, true, true},
		{E2HTTPWebSearch{Backend: "brave-free"}, false, false},
		{E2HTTPWebSearch{Backend: "tavily", BraveKey: "synthetic-brave-key"}, false, false},
		{E2HTTPWebSearch{Backend: "brave-free", BraveKey: "synthetic-brave-key", TavilyKey: "synthetic-tavily-key"}, false, false},
		{E2HTTPWebSearch{Backend: "tavily", TavilyKey: "synthetic-tavily-key", BraveKey: "synthetic-brave-key"}, false, false},
		{E2HTTPWebSearch{Backend: "ddgs", TavilyKey: "synthetic-tavily-key"}, false, false},
		{E2HTTPWebSearch{BraveKey: "synthetic-brave-key"}, false, false},
		{E2HTTPWebSearch{Backend: "deepseek"}, false, false},
		{E2HTTPWebSearch{Backend: "DDGS"}, false, false},
	} {
		if tc.cfg.Valid() != tc.valid || tc.cfg.Enabled() != tc.enabled {
			t.Fatalf("%q: valid=%v enabled=%v", tc.cfg.Backend, tc.cfg.Valid(), tc.cfg.Enabled())
		}
		tool, err := newE2HTTPWebSearchTool(tc.cfg, nil)
		if tc.enabled != (tool != nil) || (err != nil) == tc.valid {
			t.Fatalf("%q: tool=%v err=%v", tc.cfg.Backend, tool != nil, err)
		}
		if tool != nil && tool.Name() != e2HTTPWebSearchToolName {
			t.Fatal(tool.Name())
		}
	}
	cfg := E2HTTPWebSearch{Backend: "tavily", TavilyKey: "synthetic-tavily-key"}
	if out := fmt.Sprintf("%v %+v %#v %s", cfg, cfg, cfg, cfg); strings.Contains(out, "synthetic-tavily-key") {
		t.Fatal("search key printed", out)
	}
}

// 진단 Worker 판은 검색어에 이 실행의 보호 값이 있으면 검색 제공자로 보내지 않고, 결과 글에도 실행 가림을 건다.
func TestE2HTTPWebSearchGuardKeepsProtectedValuesOut(t *testing.T) {
	redact := func(b []byte) []byte {
		return []byte(strings.ReplaceAll(string(b), "synthetic-session-value", "[REDACTED]"))
	}
	calls := 0
	inner := e2WorkerFixtureTool(e2HTTPWebSearchToolName, func(_ context.Context, input json.RawMessage) (actool.Result, error) {
		calls++
		return actool.Text("result mentions synthetic-session-value for " + string(input)), nil
	})
	tool := &e2HTTPWebSearchTool{CoreTool: inner, redact: redact}
	// 평문·JSON 이스케이프(\u0073 = s)·중복 키(원본처럼 마지막 값)·대소문자 다른 키(Go JSON은 이름을 대소문자 없이 맞춤)
	// 어느 쪽으로 써도 원본 도구가 풀어 보낼 검색어에 보호 값이 있으면 보내지 않는다. 깨진 입력도 보내지 않는다.
	for _, input := range []string{
		`{"query":"owned app synthetic-session-value"}`,
		`{"query":"owned app \u0073ynthetic-session-value"}`,
		`{"query":"juice shop","query":"synthetic-\u0073ession-value"}`,
		`{"QUERY":"synthetic-session-value"}`,
		`{"query":`,
	} {
		blocked, err := tool.Call(t.Context(), json.RawMessage(input), nil)
		if err != nil || !blocked.IsError || calls != 0 {
			t.Fatalf("%s: protected or invalid query sent: calls=%d err=%v result=%+v", input, calls, err, blocked)
		}
	}
	result, err := tool.Call(t.Context(), json.RawMessage(`{"query":"juice shop ftp"}`), nil)
	if err != nil || result.IsError || calls != 1 || len(result.Content) != 1 || strings.Contains(result.Content[0].Text, "synthetic-session-value") || !strings.Contains(result.Content[0].Text, "[REDACTED]") {
		t.Fatalf("search result not redacted: calls=%d err=%v result=%+v", calls, err, result)
	}
}

// Worker 프로필은 설치가 켠 검색 도구를 WebFetch 다음에 둔다. 원본 목록에 같은 이름이 있어도 bound로 넘긴 것만 쓴다.
func TestDiagnosisWorkerProfilePlacesBoundWebSearch(t *testing.T) {
	host := e2WorkerFixtureTool(e2HTTPWebSearchToolName, nil)
	search := e2WorkerFixtureTool(e2HTTPWebSearchToolName, nil)
	bash, fetch := e2WorkerFixtureTool("Bash", nil), e2WorkerFixtureTool("WebFetch", nil)
	tools, err := diagnosisWorkerProfile.selectTools([]actool.CoreTool{host}, map[string]actool.CoreTool{"Bash": bash, "WebFetch": fetch, e2HTTPWebSearchToolName: search}, nil)
	if err != nil || profileToolNames(tools) != "Bash,WebFetch,web_search" || tools[2] != search {
		t.Fatalf("worker tools=%s err=%v", profileToolNames(tools), err)
	}
	tools, err = diagnosisWorkerProfile.selectTools([]actool.CoreTool{host}, map[string]actool.CoreTool{"Bash": bash}, nil)
	if err != nil || profileToolNames(tools) != "Bash" {
		t.Fatalf("host web_search leaked into the worker profile: %s %v", profileToolNames(tools), err)
	}
}

// 진단 Planner는 설치가 검색을 켠 실행에서만 web_search를 모델에 보인다. 켜지 않았으면 같은 이름 도구가 섞여도 뺀다.
func TestDiagnosisPlannerWebSearchFollowsInstall(t *testing.T) {
	ts := NewToolSet(nil, "")
	for _, enabled := range []bool{false, true} {
		ctx := context.Background()
		search := e2WorkerFixtureTool(e2HTTPWebSearchToolName, nil)
		if enabled {
			ctx = WithE2HTTPWebSearch(ctx, E2HTTPWebSearch{Backend: "ddgs"})
		}
		selected, err := diagnosisPlannerProfile.selectTools(ts.PlannerTools(), map[string]actool.CoreTool{e2HTTPWebSearchToolName: search}, nil)
		if err != nil {
			t.Fatal(err)
		}
		provider := &toolSchemaProvider{schemas: map[string]llm.ToolSchema{}}
		opts := agentcore.Options{Provider: provider, Tools: selected, PermissionMode: permission.ModeBypass, NonStreaming: true}
		if err := constrainE2HTTPPlannerOptions(ctx, &opts); err != nil {
			t.Fatal(err)
		}
		session := agentcore.NewSession(opts)
		for range session.Prompt(context.Background(), "synthetic planning") {
		}
		session.Close()
		if _, ok := provider.schemas[e2HTTPWebSearchToolName]; ok != enabled {
			t.Fatalf("enabled=%v: planner exposes web_search=%v", enabled, ok)
		}
	}
}

// 실제 Worker 조립: 설치가 검색을 켠 실행이면 모델 요청에 원본 web_search가 실리고, 아니면 실리지 않는다. 잘못된 설정은
// 모델을 부르기 전에 막는다.
func TestE2HTTPWorkerAttachesWebSearchOnlyWhenInstalled(t *testing.T) {
	for _, tc := range []struct {
		cfg    E2HTTPWebSearch
		expose bool
		called bool
	}{
		{E2HTTPWebSearch{}, false, true},
		{E2HTTPWebSearch{Backend: "ddgs"}, true, true},
		{E2HTTPWebSearch{Backend: "brave-free"}, false, false},
	} {
		_, _, binding := e2WorkerFixture(t)
		provider := &e2SettlementProvider{}
		worker := NewWorker(provider, "synthetic", t.TempDir(), nil, 10000, 2)
		worker.SetNonStreaming(func() bool { return true })
		ctx := WithE2HTTPWebSearch(WithE2HTTPExecution(context.Background(), binding), tc.cfg)
		_, _, _ = worker.Execute(ctx, "worker", 7, nil, nil, &db.Node{ID: 22, Kind: db.KindIntent, Payload: json.RawMessage("{}")}, nil, nil, nil, nil)
		if (len(provider.requests) > 0) != tc.called {
			t.Fatalf("%q: model called=%v", tc.cfg.Backend, len(provider.requests) > 0)
		}
		if !tc.called {
			continue
		}
		exposed := false
		for _, tool := range provider.requests[0].Tools {
			exposed = exposed || tool.Name == e2HTTPWebSearchToolName
		}
		if exposed != tc.expose {
			t.Fatalf("%q: web_search exposed=%v", tc.cfg.Backend, exposed)
		}
	}
}
