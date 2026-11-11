package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// namedTool은 이름만 쓰는 시험용 도구다. 다른 메서드는 이 시험에서 부르지 않는다.
type namedTool struct {
	actool.CoreTool
	name string
}

func (t namedTool) Name() string { return t.name }

func toolsOf(server string, bases ...string) []actool.CoreTool {
	out := make([]actool.CoreTool, 0, len(bases))
	for _, b := range bases {
		out = append(out, namedTool{name: "mcp__" + server + "__" + b})
	}
	return out
}

func baseNames(server string, tools []actool.CoreTool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, strings.TrimPrefix(t.Name(), "mcp__"+server+"__"))
	}
	sort.Strings(out)
	return out
}

// 0.0.84 README의 기본 묶음(Core automation 24개 + Tab management 1개)과 --caps 묶음 일부, 모르는 새 도구.
var playwright084Tools = []string{
	"browser_click", "browser_close", "browser_console_messages", "browser_drag", "browser_drop",
	"browser_emulate_media", "browser_evaluate", "browser_file_upload", "browser_fill_form", "browser_find",
	"browser_handle_dialog", "browser_hover", "browser_navigate", "browser_navigate_back",
	"browser_network_request", "browser_network_requests", "browser_press_key", "browser_resize",
	"browser_run_code_unsafe", "browser_select_option", "browser_snapshot", "browser_take_screenshot",
	"browser_type", "browser_wait_for", "browser_tabs",
	"browser_route", "browser_cookie_set", "browser_get_config", "page_registered_webmcp_tool",
}

func TestFilterMCPToolsKeepsOnlyAllowedPlaywrightTools(t *testing.T) {
	m := &db.MCPServer{Name: "browser", Transport: "stdio", Command: "npx", Args: json.RawMessage(`["@playwright/mcp","--headless"]`)}
	kept, hidden := filterMCPTools(m, toolsOf(m.Name, playwright084Tools...))
	got := baseNames(m.Name, kept)
	want := make([]string, 0, len(playwrightMCPAllowedTools))
	for name := range playwrightMCPAllowedTools {
		want = append(want, name)
	}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("노출 도구 = %v, 기대 %v", got, want)
	}
	if hidden != len(playwright084Tools)-len(want) {
		t.Fatalf("숨긴 수 = %d, 기대 %d", hidden, len(playwright084Tools)-len(want))
	}
	for _, name := range got {
		switch name {
		case "browser_run_code_unsafe", "browser_file_upload", "browser_drop", "browser_network_request", "browser_route", "browser_cookie_set":
			t.Fatalf("제외해야 할 도구 %s가 노출됨", name)
		}
	}
}

// 등록 이름을 바꾸고 HTTP로 붙여도(실행 명령이 없어도) 도구 이름으로 Playwright MCP를 알아본다.
func TestFilterMCPToolsDetectsRenamedHTTPPlaywright(t *testing.T) {
	m := &db.MCPServer{Name: "explore", Transport: "http", URL: "http://explore-browser:8931/mcp"}
	kept, hidden := filterMCPTools(m, toolsOf(m.Name, "browser_run_code_unsafe", "browser_navigate"))
	if got := baseNames(m.Name, kept); len(got) != 1 || got[0] != "browser_navigate" || hidden != 1 {
		t.Fatalf("노출 %v 숨김 %d, 기대 [browser_navigate] 1", got, hidden)
	}
	// 표식 도구 하나(run_code_unsafe)만 내놓아도 Playwright로 보고 숨긴다.
	kept, hidden = filterMCPTools(m, toolsOf(m.Name, "browser_run_code_unsafe"))
	if len(kept) != 0 || hidden != 1 {
		t.Fatalf("run_code_unsafe만 있는 서버: 노출 %d 숨김 %d", len(kept), hidden)
	}
}

// 실행 명령에 playwright가 들어 있으면 표식 도구가 없어도 허용 목록을 적용한다(새 버전이 이름을 바꿔도 기본 거부).
func TestFilterMCPToolsDetectsPlaywrightByLaunchCommand(t *testing.T) {
	m := &db.MCPServer{Name: "web", Transport: "stdio", Command: "npx", Args: json.RawMessage(`["@playwright/mcp@latest"]`)}
	kept, hidden := filterMCPTools(m, toolsOf(m.Name, "browser_renamed_navigate", "browser_click"))
	if got := baseNames(m.Name, kept); len(got) != 1 || got[0] != "browser_click" || hidden != 1 {
		t.Fatalf("노출 %v 숨김 %d, 기대 [browser_click] 1", got, hidden)
	}
}

// Playwright MCP에서 이름 앞부분이 mcp__<서버>__가 아닌 도구는 허용 이름과 같아도 노출하지 않는다.
func TestFilterMCPToolsDropsForeignPrefixOnPlaywright(t *testing.T) {
	m := &db.MCPServer{Name: "browser", Transport: "stdio", Command: "npx"}
	tools := append(toolsOf(m.Name, "browser_click"), namedTool{name: "mcp__other__browser_navigate"}, namedTool{name: "browser_navigate"})
	kept, hidden := filterMCPTools(m, tools)
	if got := baseNames(m.Name, kept); len(got) != 1 || got[0] != "browser_click" || hidden != 2 {
		t.Fatalf("노출 %v 숨김 %d, 기대 [browser_click] 2", got, hidden)
	}
}

// Playwright가 아닌 MCP 서버는 원본처럼 그대로 둔다.
func TestFilterMCPToolsLeavesOtherServers(t *testing.T) {
	m := &db.MCPServer{Name: "calc", Transport: "stdio", Command: "calc-mcp"}
	tools := toolsOf(m.Name, "add", "run_code", "browser_like")
	kept, hidden := filterMCPTools(m, tools)
	if len(kept) != len(tools) || hidden != 0 {
		t.Fatalf("다른 서버 도구가 바뀜: 노출 %d 숨김 %d", len(kept), hidden)
	}
}

// 코드의 허용 목록과 계약 §5.1의 허용 목록이 같아야 한다(한쪽만 고치면 실패).
func TestPlaywrightAllowListMatchesContract(t *testing.T) {
	raw, err := os.ReadFile("../docs/contracts/exploration-boundary.md")
	if err != nil {
		t.Fatalf("계약 문서를 읽지 못함: %v", err)
	}
	var line string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "- **허용**(기본 묶음 중):") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatal("계약 §5.1 허용 줄을 찾지 못함")
	}
	var doc []string
	for _, m := range regexp.MustCompile("`([a-z_]+)`").FindAllStringSubmatch(line, -1) {
		doc = append(doc, m[1])
	}
	sort.Strings(doc)
	code := make([]string, 0, len(playwrightMCPAllowedTools))
	for name := range playwrightMCPAllowedTools {
		code = append(code, name)
	}
	sort.Strings(code)
	if strings.Join(doc, ",") != strings.Join(code, ",") {
		t.Fatalf("계약 허용 목록 %v와 코드 %v가 다름", doc, code)
	}
}

type fakeMCPClient struct {
	tools []actool.CoreTool
	err   error
}

func (c fakeMCPClient) Tools(context.Context) ([]actool.CoreTool, error) { return c.tools, c.err }
func (c fakeMCPClient) Close() error                                     { return nil }

// 에이전트 조립 경로가 쓰는 agentMCPTools가 실제로 거른다(조립 쪽 호출은 이 함수 하나).
func TestAgentMCPToolsFiltersAndPassesErrors(t *testing.T) {
	m := &db.MCPServer{Name: "browser", Transport: "stdio", Command: "npx"}
	got, err := agentMCPTools(context.Background(), m, fakeMCPClient{tools: toolsOf(m.Name, "browser_run_code_unsafe", "browser_click")})
	if err != nil || len(got) != 1 || got[0].Name() != "mcp__browser__browser_click" {
		t.Fatalf("agentMCPTools = %v, %v", baseNames(m.Name, got), err)
	}
	listErr := errors.New("tools/list failed")
	if _, err := agentMCPTools(context.Background(), m, fakeMCPClient{err: listErr}); !errors.Is(err, listErr) {
		t.Fatalf("목록 오류를 그대로 돌려줘야 함: %v", err)
	}
}

// 에이전트 도구 조립이 거르지 않은 cl.Tools를 직접 부르지 않는다(필터를 빼면 실패).
func TestAgentAssemblyUsesFilteredMCPTools(t *testing.T) {
	src, err := os.ReadFile("assembly.go")
	if err != nil {
		t.Fatalf("assembly.go를 읽지 못함: %v", err)
	}
	if !strings.Contains(string(src), "agentMCPTools(ctx, m, cl)") || strings.Contains(string(src), "cl.Tools(") {
		t.Fatal("wireAgentAugment는 MCP 도구를 agentMCPTools로만 받아야 함")
	}
}
