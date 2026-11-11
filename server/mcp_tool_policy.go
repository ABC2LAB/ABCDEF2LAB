package server

import (
	"context"
	"log"
	"strings"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// playwrightMCPAllowedTools는 탐색 계약 §5.1의 고정 목록이다(docs/contracts/exploration-boundary.md,
// 제품 핀 @playwright/mcp@0.0.84). 이 목록 밖의 도구는 이름이 무엇이든 모델에 노출하지 않는다.
// 특히 browser_run_code_unsafe는 0.0.84 기본 묶음에 들어 있고 끄는 CLI 옵션이 없어서, MCP 서버
// 프로세스에서 임의 JS를 실행하는 길이 여기서만 막힌다.
var playwrightMCPAllowedTools = map[string]bool{
	"browser_navigate":         true,
	"browser_navigate_back":    true,
	"browser_tabs":             true,
	"browser_click":            true,
	"browser_hover":            true,
	"browser_drag":             true,
	"browser_type":             true,
	"browser_fill_form":        true,
	"browser_select_option":    true,
	"browser_press_key":        true,
	"browser_handle_dialog":    true,
	"browser_wait_for":         true,
	"browser_snapshot":         true,
	"browser_find":             true,
	"browser_take_screenshot":  true,
	"browser_resize":           true,
	"browser_emulate_media":    true,
	"browser_console_messages": true,
	"browser_network_requests": true,
	"browser_evaluate":         true,
	"browser_close":            true,
}

// playwrightMCPMarkerTools 중 하나라도 내놓는 서버는 등록 이름·전송 방식과 상관없이 Playwright MCP로 본다.
// 다른 이름으로 등록하거나 HTTP 전송으로 붙여도 허용 목록을 건너뛰지 못하게 하려는 것이다.
var playwrightMCPMarkerTools = []string{"browser_run_code_unsafe", "browser_navigate", "browser_snapshot"}

// seededBrowserMCPName은 db 시드가 만드는 Playwright MCP 행 이름이다(db/db.go).
const seededBrowserMCPName = "browser"

// agentMCPTools는 연결된 MCP 서버 m의 도구 목록을 받아 모델에 노출할 것만 돌려준다. 에이전트 도구를
// 조립하는 곳(wireAgentAugment)은 cl.Tools 대신 이 함수를 쓴다. 도구 목록은 실행마다 한 번만 받으므로,
// 그 뒤 서버가 새로 등록한 도구(WebMCP 등)는 모델에 노출되지 않는다.
func agentMCPTools(ctx context.Context, m *db.MCPServer, cl mcpClient) ([]actool.CoreTool, error) {
	tools, err := cl.Tools(ctx)
	if err != nil {
		return nil, err
	}
	kept, hidden := filterMCPTools(m, tools)
	if hidden > 0 {
		log.Printf("[mcp] %s: 탐색 계약 허용 목록 밖 도구 %d개를 모델에 노출하지 않음", m.Name, hidden)
	}
	return kept, nil
}

// filterMCPTools는 MCP 서버 m이 내놓은 도구 중 모델에 노출할 것만 돌려준다. Playwright MCP가 아니면
// 그대로 두고, Playwright MCP면 허용 목록에 있는 도구만 남긴다. 이름 앞부분이 mcp__<서버>__가 아닌
// 도구도 뺀다(무엇인지 모르는 도구를 열어 두지 않음). 두 번째 값은 뺀 도구 수다.
func filterMCPTools(m *db.MCPServer, tools []actool.CoreTool) ([]actool.CoreTool, int) {
	prefix := "mcp__" + m.Name + "__"
	if !isPlaywrightMCP(m, tools, prefix) {
		return tools, 0
	}
	kept := make([]actool.CoreTool, 0, len(tools))
	for _, t := range tools {
		base, ok := strings.CutPrefix(t.Name(), prefix)
		if ok && playwrightMCPAllowedTools[base] {
			kept = append(kept, t)
		}
	}
	return kept, len(tools) - len(kept)
}

func isPlaywrightMCP(m *db.MCPServer, tools []actool.CoreTool, prefix string) bool {
	if m.Name == seededBrowserMCPName {
		return true
	}
	launch := strings.ToLower(m.Command + " " + strings.Join(jsonStrSlice(m.Args), " "))
	if strings.Contains(launch, "playwright") {
		return true
	}
	for _, t := range tools {
		base, ok := strings.CutPrefix(t.Name(), prefix)
		if !ok {
			continue
		}
		for _, marker := range playwrightMCPMarkerTools {
			if base == marker {
				return true
			}
		}
	}
	return false
}
