package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
)

// 내부 추론은 활동 기록 어디에도 남지 않아야 한다. 추론 앞뒤의 글은 메시지 경계를 유지하고, 사용량은 토큰 수로 남는다.
func TestCaptureRunDoesNotRecordThinking(t *testing.T) {
	const secret = "PRIVATE-REASONING-SENTINEL"
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		events := []llm.StreamEvent{
			{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 5}},
			{Type: llm.SETextDelta, Text: "before"},
			{Type: llm.SEThinkingDelta, Text: secret},
			{Type: llm.SETextDelta, Text: "after"},
			{Type: llm.SEMessageDelta, StopReason: "end_turn", Usage: llm.Usage{OutputTokens: 3}},
			{Type: llm.SEMessageStop},
		}
		for _, event := range events {
			if !yield(event, nil) {
				return
			}
		}
	}}

	var activities []db.Activity
	_, _, err := captureRun(context.Background(), agentcore.Options{Provider: provider, MaxTurns: 1}, "test", func(a db.Activity) {
		activities = append(activities, a)
	})
	if err != nil {
		t.Fatal(err)
	}
	sawUsage, sawBefore := false, false
	for _, a := range activities {
		if a.Kind == "thinking" {
			t.Fatalf("thinking activity recorded: %+v", a)
		}
		if strings.Contains(a.Summary, secret) || strings.Contains(a.Detail, secret) {
			t.Fatalf("reasoning text leaked into %s activity", a.Kind)
		}
		sawUsage = sawUsage || a.Kind == "usage"
		sawBefore = sawBefore || (a.Kind == "text" && a.Detail == "before")
	}
	if !sawUsage {
		t.Fatal("usage activity must stay")
	}
	if !sawBefore {
		t.Fatalf("text before the reasoning must stay a separate record: %+v", activities)
	}
}
