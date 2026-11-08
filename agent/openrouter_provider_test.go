package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/norma/llm"
)

type openRouterSDKRT func(*http.Request) (*http.Response, error)

func (f openRouterSDKRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func openRouterSDKConfig() Config {
	return Config{Format: llm.FormatOpenAI, BaseURL: llmpolicy.OpenRouterBaseURL, Model: llmpolicy.OpenRouterModel, APIKey: "synthetic-not-a-key", MaxTokens: 100, ContextWindowK: 262}
}
func TestOpenRouterSDKToolRoundTrip(t *testing.T) {
	calls := 0
	c := &http.Client{Transport: openRouterSDKRT(func(r *http.Request) (*http.Response, error) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		if calls == 2 {
			var wire struct {
				Messages []struct {
					Role       string `json:"role"`
					ToolCallID string `json:"tool_call_id"`
					Content    string `json:"content"`
				} `json:"messages"`
			}
			if json.Unmarshal(raw, &wire) != nil {
				t.Fatal("invalid wire JSON")
			}
			bound := false
			for _, m := range wire.Messages {
				if m.Role == "tool" && m.ToolCallID == "call-7" && m.Content == `{"ok":true}` {
					bound = true
				}
			}
			if !bound {
				t.Fatal("tool result did not bind exact call id and JSON content")
			}
		}
		body := `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-7","type":"function","function":{"name":"fixture","arguments":"{\"value\":7}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
		if calls == 2 {
			body = `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	provider, err := openRouterSDKConfig().newOpenRouterProviderWithClient(c)
	if err != nil {
		t.Fatal(err)
	}
	req := llm.CompletionRequest{MaxTokens: 100, Tools: []llm.ToolSchema{{Name: "fixture", InputSchema: map[string]any{"type": "object"}}}, Messages: []llm.Message{llm.UserText("synthetic")}}
	reply, _, _, err := provider.Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	uses := reply.ToolUses()
	if len(uses) != 1 || uses[0].ID != "call-7" || uses[0].Name != "fixture" || string(uses[0].Input) != `{"value":7}` {
		t.Fatal("SDK tool fields changed")
	}
	req.Messages = append(req.Messages, reply, llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("call-7", `{"ok":true}`, false)}})
	result, _, _, err := provider.Complete(context.Background(), req)
	if err != nil || result.Text() != "done" || calls != 2 {
		t.Fatalf("roundtrip err=%v calls=%d", err, calls)
	}
	req.Messages[len(req.Messages)-1].Content[0].ToolUseID = "wrong-id"
	if _, _, _, err := provider.Complete(context.Background(), req); err == nil || calls != 2 {
		t.Fatal("unbound result reached SDK")
	}
}
func TestOpenRouterSDKRejectsToolResponses(t *testing.T) {
	for _, tool := range []string{
		`{"id":"","type":"function","function":{"name":"fixture","arguments":"{}"}}`,
		`{"id":"call-1","type":"function","function":{"name":"unknown","arguments":"{}"}}`,
		`{"id":"call-1","type":"function","function":{"name":"fixture","arguments":"[]"}}`,
		`{"id":"call-1","type":"function","function":{"name":"fixture","arguments":"invalid"}}`,
	} {
		calls := 0
		c := &http.Client{Transport: openRouterSDKRT(func(*http.Request) (*http.Response, error) {
			calls++
			body := `{"choices":[{"message":{"tool_calls":[` + tool + `]},"finish_reason":"tool_calls"}]}`
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})}
		provider, err := openRouterSDKConfig().newOpenRouterProviderWithClient(c)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, err = provider.Complete(context.Background(), llm.CompletionRequest{MaxTokens: 10, Tools: []llm.ToolSchema{{Name: "fixture"}}, Messages: []llm.Message{llm.UserText("synthetic")}})
		if err != ErrOpenRouterProvider || calls != 1 {
			t.Fatalf("bad tool allowed or retried: err=%v calls=%d", err, calls)
		}
	}
}
func TestOpenRouterSDKRetryAndStreamDisabled(t *testing.T) {
	calls := 0
	c := &http.Client{Transport: openRouterSDKRT(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("synthetic sensitive body")), Header: make(http.Header)}, nil
	})}
	cfg := openRouterSDKConfig()
	cfg.Retry.ConnectAttempts = 10
	cfg.Retry.EmptyAttempts = 10
	p, err := cfg.newOpenRouterProviderWithClient(c)
	if err != nil {
		t.Fatal(err)
	}
	req := llm.CompletionRequest{MaxTokens: 1, Messages: []llm.Message{llm.UserText("synthetic")}}
	if _, _, _, err = p.Complete(context.Background(), req); err != ErrOpenRouterProvider || calls != 1 {
		t.Fatal("SDK error leaked or retries escaped", err, calls)
	}
	for _, err := range p.Stream(context.Background(), req) {
		if err != ErrOpenRouterProvider {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("stream sent request")
	}
}

func TestOpenRouterRejectsEmptyCompletion(t *testing.T) {
	for _, msg := range []llm.Message{{}, {Content: []llm.ContentBlock{llm.TextBlock("   ")}}} {
		if validOpenRouterTools(msg, nil) {
			t.Fatal("empty completion treated as successful model response")
		}
	}
	if !validOpenRouterTools(llm.Message{Content: []llm.ContentBlock{llm.TextBlock("No further authorized actions.")}}, nil) {
		t.Fatal("terminal no-tool response rejected")
	}
}

func TestOpenRouterValidatesRegisteredToolSchema(t *testing.T) {
	tools := []llm.ToolSchema{{Name: "fixture", InputSchema: map[string]any{"type": "object", "required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": "integer"}}}}}
	for _, raw := range []string{`{}`, `{"value":"not-an-integer"}`} {
		msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "call-1", Name: "fixture", Input: json.RawMessage(raw)}}}
		if validOpenRouterTools(msg, tools) {
			t.Fatal("tool input mismatched registered schema")
		}
	}
	msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "call-1", Name: "fixture", Input: json.RawMessage(`{"value":7}`)}}}
	if !validOpenRouterTools(msg, tools) {
		t.Fatal("valid registered schema input rejected")
	}
}
