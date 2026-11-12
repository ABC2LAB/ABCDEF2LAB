package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

// webFetchBash는 E② Bash 자리에 넣는 기록용 도구다. 받은 명령을 남기고 정해 둔 결과 JSON을 돌려준다.
func webFetchBash(t *testing.T, output string, isError bool, commands *[]string) actool.CoreTool {
	t.Helper()
	return e2WorkerFixtureTool("Bash", func(_ context.Context, input json.RawMessage) (actool.Result, error) {
		var in struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			t.Fatal(err)
		}
		*commands = append(*commands, in.Command)
		raw, _ := json.Marshal(map[string]any{"tool_output": output, "trusted": map[string]any{"classification": "completed", "call": map[string]any{"call_ref": "call-7"}}})
		out := actool.Text(string(raw))
		out.IsError = isError
		return out, nil
	})
}

func TestE2HTTPWebFetchRejectsUnsafeURLsBeforeSending(t *testing.T) {
	for _, bad := range []string{"", "/relative", "ftp://owned.example/a", "https://owned.example/a b", "https://owned.example/a\nb",
		"https://user:pass@owned.example/", "https://owned.example/app.js", "https://owned.example/style.CSS?v=1", "javascript:alert(1)", "https:///nohost"} {
		var commands []string
		tool := newE2HTTPWebFetchTool(webFetchBash(t, "", false, &commands))
		raw, _ := json.Marshal(map[string]any{"url": bad})
		result, err := tool.Call(context.Background(), raw, nil)
		if err != nil || !result.IsError || len(commands) != 0 {
			t.Fatalf("%q: sent=%d result=%q", bad, len(commands), result.Flatten())
		}
	}
}

// 가져오기는 E② Bash 한 번이다. URL은 셸 인용되고, 리다이렉트를 따라가지 않는다.
func TestE2HTTPWebFetchSendsOneQuotedBashCall(t *testing.T) {
	var commands []string
	tool := newE2HTTPWebFetchTool(webFetchBash(t, "HTTP/1.1 204 No Content\r\n\r\n", false, &commands))
	raw, _ := json.Marshal(map[string]any{"url": "https://owned.example/search?q=it's"})
	if _, err := tool.Call(context.Background(), raw, nil); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || !strings.HasPrefix(commands[0], "curl ") || !strings.Contains(commands[0], "--max-redirs 0") ||
		!strings.Contains(commands[0], `--url 'https://owned.example/search?q=it'"'"'s'`) {
		t.Fatalf("commands=%q", commands)
	}
}

// HTML은 마크다운으로 바꾸고 extract면 정찰 단서를 붙인다. trusted 관찰(call_ref)은 그대로 남는다.
func TestE2HTTPWebFetchRendersHTMLAndKeepsTrusted(t *testing.T) {
	page := "HTTP/1.1 100 Continue\r\n\r\nHTTP/1.1 200 OK\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		`<html><head><title>T</title><meta name="generator" content="fixture"><script src="/main.js"></script></head><body>` +
		`<h2>Orders</h2><p>See <a href="/orders/7">order 7</a></p><ul><li>one</li></ul>` +
		`<!-- legacy /admin/debug --><script>fetch('/api/orders/7')</script>` +
		`<form action="/login" method="post"><input type="hidden" name="csrf"><input name="email"></form></body></html>`
	var commands []string
	tool := newE2HTTPWebFetchTool(webFetchBash(t, page, false, &commands))
	raw, _ := json.Marshal(map[string]any{"url": "https://owned.example/orders", "extract": true})
	result, err := tool.Call(context.Background(), raw, nil)
	if err != nil || result.IsError {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result.Flatten()), &fields); err != nil {
		t.Fatal(err)
	}
	var output string
	_ = json.Unmarshal(fields["tool_output"], &output)
	for _, want := range []string{"HTTP/1.1 200 OK", "## Orders", "[order 7](/orders/7)", "- one", "## RECON", "legacy /admin/debug", "/main.js", "/api/orders/7", "POST /login [csrf:hidden, email:text]", "generator=fixture"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in %q", want, output)
		}
	}
	if strings.Contains(output, "fetch(") || strings.Contains(output, "<h2>") {
		t.Fatalf("script or markup leaked into markdown: %q", output)
	}
	if !strings.Contains(string(fields["trusted"]), `"call_ref":"call-7"`) {
		t.Fatal("trusted observation changed")
	}
}

// HTML이 아닌 응답은 그대로 두고, E②가 거부한 결과(오류)는 바꾸지 않는다.
func TestE2HTTPWebFetchLeavesNonHTMLAndRefusalsUnchanged(t *testing.T) {
	var commands []string
	api := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"id\":7}"
	result, _ := newE2HTTPWebFetchTool(webFetchBash(t, api, false, &commands)).Call(context.Background(), json.RawMessage(`{"url":"https://owned.example/api/7"}`), nil)
	if !strings.Contains(result.Flatten(), `{\"id\":7}`) {
		t.Fatalf("api body changed: %q", result.Flatten())
	}
	// 오류 결과는 출력이 HTML처럼 보여도 바꾸지 않는다. 모델은 E② 결과를 그대로 받는다.
	refusalOutput := "HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/html\r\n\r\n<html><h1>E2 refused (sequence=1)</h1></html>"
	refusal := webFetchBash(t, refusalOutput, true, &commands)
	result, _ = newE2HTTPWebFetchTool(refusal).Call(context.Background(), json.RawMessage(`{"url":"https://owned.example/admin"}`), nil)
	var refused struct {
		ToolOutput string `json:"tool_output"`
	}
	if !result.IsError || json.Unmarshal([]byte(result.Flatten()), &refused) != nil || refused.ToolOutput != refusalOutput {
		t.Fatalf("refusal changed: %q", result.Flatten())
	}
}
