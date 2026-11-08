package agent

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"strings"

	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

var ErrOpenRouterProvider = errors.New("llm: OpenRouter 요청이 차단되었거나 실패했습니다")

// NewOpenRouterProvider는 로컬 팩터리와 분리된 명시적 외부 송신 경로다.
func (c Config) NewOpenRouterProvider(policy *llmpolicy.OpenRouterPolicy) (llm.Provider, error) {
	if policy == nil || c.Format != llm.FormatOpenAI || c.BaseURL != llmpolicy.OpenRouterBaseURL || c.Model != llmpolicy.OpenRouterModel ||
		c.Proxy != "" || c.Stream || strings.TrimSpace(c.APIKey) == "" || c.SessionHeaderKey != "" || c.ThinkingType != "" || c.ReasoningEffort != "" ||
		(c.MaxTokensField != "" && c.MaxTokensField != "max_tokens") {
		return nil, ErrOpenRouterProvider
	}
	if policy.ValidateCallLimits(c.MaxTokens, c.ContextWindowK) != nil {
		return nil, ErrOpenRouterProvider
	}
	target := policy.Target()
	if target.Format != string(c.Format) || target.BaseURL != c.BaseURL || target.Model != c.Model {
		return nil, ErrOpenRouterProvider
	}
	client, err := llmpolicy.NewOpenRouterClient(policy)
	if err != nil {
		return nil, ErrOpenRouterProvider
	}
	return c.newOpenRouterProviderWithClient(client)
}

func (c Config) newOpenRouterProviderWithClient(client *http.Client) (llm.Provider, error) {
	c.Retry.ConnectAttempts = -1
	c.Retry.EmptyAttempts = -1
	inner, err := c.newProviderWithClient(client)
	if err != nil {
		return nil, ErrOpenRouterProvider
	}
	return openRouterProvider{inner: inner}, nil
}

type openRouterProvider struct{ inner llm.Provider }

func (p openRouterProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) { yield(llm.StreamEvent{}, ErrOpenRouterProvider) }
}
func (p openRouterProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	if !validOpenRouterHistory(req) {
		return llm.Message{}, "", llm.Usage{}, ErrOpenRouterProvider
	}
	msg, stop, usage, err := p.inner.Complete(ctx, req)
	if ctx.Err() != nil {
		return llm.Message{}, "", llm.Usage{}, ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return llm.Message{}, "", llm.Usage{}, err
	}
	if err != nil || !validOpenRouterTools(msg, req.Tools) {
		return llm.Message{}, "", llm.Usage{}, ErrOpenRouterProvider
	}
	return msg, stop, usage, nil
}
func validOpenRouterObject(raw json.RawMessage) bool {
	return llmpolicy.ValidOpenRouterToolArguments(raw)
}

func validOpenRouterTools(msg llm.Message, tools []llm.ToolSchema) bool {
	names := map[string]map[string]any{}
	for _, t := range tools {
		names[t.Name] = t.InputSchema
		if names[t.Name] == nil {
			names[t.Name] = map[string]any{}
		}
	}
	ids := map[string]bool{}
	meaningful := false
	for _, b := range msg.Content {
		if b.Type == llm.BlockText && strings.TrimSpace(b.Text) != "" {
			meaningful = true
		}
		if b.Type != llm.BlockToolUse {
			continue
		}
		if b.ID == "" || ids[b.ID] || names[b.Name] == nil || !validOpenRouterObject(b.Input) || !validOpenRouterSchemaInput(names[b.Name], b.Input) {
			return false
		}
		ids[b.ID] = true
		meaningful = true
	}
	return meaningful
}
func validOpenRouterHistory(req llm.CompletionRequest) bool {
	names := map[string]map[string]any{}
	for _, t := range req.Tools {
		if t.Name == "" || names[t.Name] != nil {
			return false
		}
		names[t.Name] = t.InputSchema
		if names[t.Name] == nil {
			names[t.Name] = map[string]any{}
		}
	}
	pending := map[string]bool{}
	seen := map[string]bool{}
	for _, msg := range req.Messages {
		for _, b := range msg.Content {
			switch b.Type {
			case llm.BlockToolUse:
				if msg.Role != llm.RoleAssistant || b.ID == "" || seen[b.ID] || names[b.Name] == nil || !validOpenRouterObject(b.Input) || !validOpenRouterSchemaInput(names[b.Name], b.Input) {
					return false
				}
				seen[b.ID] = true
				pending[b.ID] = true
			case llm.BlockToolResult:
				if msg.Role != llm.RoleUser || !pending[b.ToolUseID] {
					return false
				}
				delete(pending, b.ToolUseID)
				for _, part := range b.Content {
					if part.Type != llm.BlockText {
						return false
					}
				}
			case llm.BlockText:
			default:
				return false
			}
		}
	}
	return len(pending) == 0
}

// SDK 검증기는 JSON 디코딩된 required 배열을 기대한다. 프로그램의 []string도 같은 의미로 정규화한다.
func validOpenRouterSchemaInput(schema map[string]any, raw json.RawMessage) bool {
	encoded, err := json.Marshal(schema)
	if err != nil {
		return false
	}
	var canonical map[string]any
	if json.Unmarshal(encoded, &canonical) != nil {
		return false
	}
	return actool.ValidateInput(canonical, raw) == nil
}
