package agent

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

type controlledReadProvider struct {
	calls int
	tools []string
}

func (p *controlledReadProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls++
	for _, tool := range req.Tools {
		p.tools = append(p.tools, tool.Name)
	}
	if p.calls == 1 {
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "read-1", Name: ControlledHTTPToolName, Input: json.RawMessage(`{"ref":"registered"}`)}}}, "tool_use", llm.Usage{}, nil
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("done")}}, "end_turn", llm.Usage{}, nil
}

func (p *controlledReadProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		msg, stop, _, err := p.Complete(ctx, req)
		if err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		for _, block := range msg.Content {
			if block.Type == llm.BlockToolUse {
				if !yield(llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: block.ID, ToolName: block.Name}, nil) ||
					!yield(llm.StreamEvent{Type: llm.SEToolInputJSON, Text: string(block.Input)}, nil) {
					return
				}
			} else if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: block.Text}, nil) {
				return
			}
		}
		yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: stop}, nil)
	}
}

func controlledReadFixtureTool(run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: ControlledHTTPToolName, Description: "fixture", Schema: map[string]any{"type": "object"},
		ReadOnly: func(json.RawMessage) bool { return true }, Concurrent: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, input)
		},
	})
}

func controlledReadIntent() *db.Node { return &db.Node{ID: 22, Kind: "intent"} }

func TestWorkerControlledReadExposesOnlyBoundTool(t *testing.T) {
	called := false
	tool := controlledReadFixtureTool(func(_ context.Context, input json.RawMessage) (actool.Result, error) {
		called = true
		if string(input) != `{"ref":"registered"}` {
			t.Fatalf("input=%s", input)
		}
		return actool.Text("sanitized"), nil
	})
	provider := &controlledReadProvider{}
	worker := NewWorker(provider, "fixture", t.TempDir(), nil, 10000, 2)
	ctx := WithControlledReadExecution(context.Background(), ControlledReadExecution{TaskID: 7, IntentID: 22, RunRef: "run_1", Tool: tool})
	_, writes, err := worker.Execute(ctx, "worker", 7, nil, nil, controlledReadIntent(), nil, nil, nil, nil)
	if err != nil || writes != (WriteCounts{}) {
		t.Fatalf("result err=%v writes=%+v", err, writes)
	}
	if !called || provider.calls != 2 {
		t.Fatalf("tool called=%v provider calls=%d", called, provider.calls)
	}
	if strings.Join(provider.tools, ",") != ControlledHTTPToolName+","+ControlledHTTPToolName {
		t.Fatalf("tools=%v", provider.tools)
	}
}

func TestWorkerControlledReadRejectsMalformedBindingBeforeProvider(t *testing.T) {
	provider := &controlledReadProvider{}
	worker := NewWorker(provider, "fixture", t.TempDir(), nil, 10000, 1)
	ctx := WithControlledReadExecution(context.Background(), ControlledReadExecution{TaskID: 8, IntentID: 22, RunRef: "../escape", Tool: controlledReadFixtureTool(nil)})
	_, _, err := worker.Execute(ctx, "worker", 7, nil, nil, controlledReadIntent(), nil, nil, nil, nil)
	if err == nil || provider.calls != 0 {
		t.Fatalf("err=%v provider calls=%d", err, provider.calls)
	}
}

func TestWorkerControlledReadCancellationReachesTool(t *testing.T) {
	started := make(chan struct{})
	tool := controlledReadFixtureTool(func(ctx context.Context, _ json.RawMessage) (actool.Result, error) {
		close(started)
		<-ctx.Done()
		return actool.Result{}, ctx.Err()
	})
	provider := &controlledReadProvider{}
	worker := NewWorker(provider, "fixture", t.TempDir(), nil, 10000, 2)
	ctx, cancel := context.WithCancel(WithControlledReadExecution(context.Background(), ControlledReadExecution{TaskID: 7, IntentID: 22, RunRef: "run_cancel", Tool: tool}))
	done := make(chan error, 1)
	go func() {
		_, _, err := worker.Execute(ctx, "worker", 7, nil, nil, controlledReadIntent(), nil, nil, nil, nil)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}
