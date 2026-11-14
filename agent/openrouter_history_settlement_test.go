package agent

import (
	"encoding/json"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

// 마무리 턴(settlement)은 일부 도구를 요청에서 숨기지만 앞 턴의 호출은 기록에 남는다. 숨긴 도구의 지난 호출은 객체 입력이면
// 받고, 지금 요청에 있는 도구의 호출은 지금처럼 스키마로 검사한다(2026-11-14 실제 회차: 턴 상한 뒤 마무리 요청이 보내기 전에
// history_invalid로 거부돼 탐색·Worker가 실패).
func TestOpenRouterHistoryAcceptsCallsToToolsHiddenForSettlement(t *testing.T) {
	report := llm.ToolSchema{Name: "report_discovery", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"discoveries": map[string]any{"type": "array"}}, "required": []any{"discoveries"}}}
	history := func(input string) []llm.Message {
		return []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("explore")}},
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "call-1", Name: "Bash", Input: json.RawMessage(input)}}},
			{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.BlockToolResult, ToolUseID: "call-1", Content: []llm.ContentBlock{llm.TextBlock("exit_code=0")}}}},
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("Report only discovered endpoint metadata.")}},
		}
	}
	settle := llm.CompletionRequest{Tools: []llm.ToolSchema{report}, Messages: history(`{"command":"curl -s http://owned.test/"}`)}
	if !validOpenRouterHistory(settle) {
		t.Fatal("settlement request with an earlier call to a hidden tool rejected")
	}
	for name, req := range map[string]llm.CompletionRequest{
		"hidden-tool-non-object": {Tools: []llm.ToolSchema{report}, Messages: history(`["curl"]`)},
		"hidden-tool-no-name": {Tools: []llm.ToolSchema{report}, Messages: []llm.Message{
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "call-1", Name: "", Input: json.RawMessage(`{}`)}}},
			{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.BlockToolResult, ToolUseID: "call-1", Content: []llm.ContentBlock{llm.TextBlock("x")}}}},
		}},
		// 지금 요청에 있는 도구의 호출은 스키마를 지켜야 한다.
		"listed-tool-schema-violation": {Tools: []llm.ToolSchema{report}, Messages: []llm.Message{
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "call-1", Name: "report_discovery", Input: json.RawMessage(`{}`)}}},
			{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.BlockToolResult, ToolUseID: "call-1", Content: []llm.ContentBlock{llm.TextBlock("x")}}}},
		}},
	} {
		if validOpenRouterHistory(req) {
			t.Fatalf("%s accepted", name)
		}
	}
}
