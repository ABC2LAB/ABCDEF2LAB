package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
)

func lastResult(t *testing.T, activities []db.Activity) db.Activity {
	t.Helper()
	for i := len(activities) - 1; i >= 0; i-- {
		if activities[i].Kind == "result" {
			return activities[i]
		}
	}
	t.Fatalf("no result activity: %+v", activities)
	return db.Activity{}
}

func captureEvents(events []llm.StreamEvent, last error) captureUsageProvider {
	return captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		for _, event := range events {
			if !yield(event, nil) {
				return
			}
		}
		if last != nil {
			yield(llm.StreamEvent{}, last)
		}
	}}
}

func TestExecutionErrorActivityLanguagePerReader(t *testing.T) {
	got := executionErrorActivity(errors.New("boom"))
	shown := got.ForDisplay()
	if !got.IsError || got.Summary != "Execution error: boom" || shown.Summary != "실행 오류: boom" || shown.Detail != "boom" {
		t.Fatalf("stored=%+v shown=%+v", got, shown)
	}
}

// 실행 경로마다 결과 행은 모델용 영어로 저장되고 화면 경계에서 한국어로 바뀌어야 한다. 모델의 최종 답은 그대로다.
func TestCaptureRunResultLanguagePerReader(t *testing.T) {
	endTurn := []llm.StreamEvent{
		{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 5}},
		{Type: llm.SEMessageDelta, StopReason: "end_turn", Usage: llm.Usage{OutputTokens: 3}},
		{Type: llm.SEMessageStop},
	}
	run := func(t *testing.T, ctx context.Context, provider captureUsageProvider) db.Activity {
		t.Helper()
		var activities []db.Activity
		_, _, _ = captureRun(ctx, agentcore.Options{Provider: provider, MaxTurns: 1}, "test", func(a db.Activity) {
			activities = append(activities, a)
		})
		return lastResult(t, activities)
	}

	t.Run("no text", func(t *testing.T) {
		got := run(t, context.Background(), captureEvents(endTurn, nil))
		shown := got.ForDisplay()
		if !strings.Contains(got.Summary, "No text summary") || !strings.Contains(shown.Summary, "글 요약 없음") {
			t.Fatalf("stored=%q shown=%q", got.Summary, shown.Summary)
		}
		if got.InputTokens == nil || got.OutputTokens == nil {
			t.Fatalf("usage lost: %+v", got)
		}
	})
	t.Run("provider error", func(t *testing.T) {
		got := run(t, context.Background(), captureEvents(endTurn[:1], errors.New("boom")))
		shown := got.ForDisplay()
		if !got.IsError || !strings.Contains(got.Summary, "model_error") || !strings.Contains(shown.Summary, "모델이나 API 호출이 실패했다") {
			t.Fatalf("stored=%q shown=%q", got.Summary, shown.Summary)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(AbortGoalMet)
		got := run(t, ctx, captureEvents(nil, context.Canceled))
		shown := got.ForDisplay()
		if !strings.Contains(got.Summary, "goal_met") || !strings.Contains(shown.Summary, "Planner가 작업 목표 달성으로 판정함") {
			t.Fatalf("stored=%q shown=%q", got.Summary, shown.Summary)
		}
	})
	t.Run("final answer", func(t *testing.T) {
		const answer = "최종 답변"
		events := []llm.StreamEvent{endTurn[0], {Type: llm.SETextDelta, Text: answer}, endTurn[1], endTurn[2]}
		got := run(t, context.Background(), captureEvents(events, nil))
		if got.Summary != answer || got.Detail != answer || len(got.Metadata) != 0 {
			t.Fatalf("final answer changed: %+v", got)
		}
	})
}
