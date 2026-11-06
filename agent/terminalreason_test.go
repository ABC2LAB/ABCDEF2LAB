package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

func TestReasonHintCoversEveryTerminalReason(t *testing.T) {
	all := []harness.TerminalReason{
		harness.ReasonCompleted, harness.ReasonBlockingLimit, harness.ReasonImageError,
		harness.ReasonModelError, harness.ReasonAbortedStreaming, harness.ReasonAbortedTools,
		harness.ReasonPromptTooLong, harness.ReasonStopHookPrevented, harness.ReasonHookStopped,
		harness.ReasonMaxTurns, harness.ReasonTimeout,
	}
	for _, reason := range all {
		if strings.TrimSpace(terminalEnglish.hints[reason]) == "" || strings.TrimSpace(terminalKorean.hints[reason]) == "" {
			t.Errorf("terminal reason %q has no explanation", reason)
		}
	}
}

func TestAbortCausePropagatesThroughRunContextChain(t *testing.T) {
	type key struct{}
	execCtx, cancelExec := context.WithCancelCause(context.Background())
	workCtx, cancelWork := context.WithCancelCause(execCtx)
	defer cancelWork(nil)
	valued := context.WithValue(workCtx, key{}, "task-1")
	runCtx, cancelRun := context.WithTimeoutCause(valued, time.Hour, AbortRunHardTimeout)
	defer cancelRun()

	cancelExec(AbortPausedByUser)
	code, _, text, ok := AbortReason(runCtx)
	if !ok || code != "paused_by_user" {
		t.Fatalf("code=%q ok=%v, want paused_by_user", code, ok)
	}
	if !strings.Contains(text, "frontier") {
		t.Fatalf("cause detail did not propagate: %q", text)
	}
}

func TestAbortReasonFallbacks(t *testing.T) {
	if _, _, _, ok := AbortReason(context.Background()); ok {
		t.Fatal("live context must not report an abort reason")
	}
	plain, cancel := context.WithCancel(context.Background())
	cancel()
	if code, _, _, ok := AbortReason(plain); !ok || code != "canceled_no_cause" {
		t.Fatalf("code=%q ok=%v, want canceled_no_cause", code, ok)
	}
	timed, cancelTimed := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancelTimed()
	<-timed.Done()
	if code, _, _, _ := AbortReason(timed); code != "deadline_exceeded" {
		t.Fatalf("code=%q, want deadline_exceeded", code)
	}
}

func TestTerminalTextAbortedNamesCauseAndHangingTool(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(AbortKilledByPlanner)
	trace := &runTrace{startedAt: time.Now().Add(-90 * time.Second)}
	trace.start("tu_1", "Bash", `{"command":"nmap -p- 10.0.0.1"}`)
	term := &harness.Terminal{
		Reason: harness.ReasonAbortedTools,
		Err:    context.Canceled,
		Turns:  7,
		Usage:  llm.Usage{InputTokens: 1200, OutputTokens: 340},
	}
	record := terminalText(ctx, term, trace)
	if !strings.Contains(record.Summary, "killed_by_planner") || strings.Contains(record.Summary, "\n") {
		t.Fatalf("unexpected summary: %q", record.Summary)
	}
	for _, want := range []string{"killed_by_planner", "aborted_tools", "7 model turns", "Bash", "no result returned", "1200"} {
		if !strings.Contains(record.Detail, want) {
			t.Errorf("detail missing %q:\n%s", want, record.Detail)
		}
	}
	if !strings.Contains(record.DisplaySummary, "Planner가 이 의도를 종료함") || strings.Contains(record.DisplaySummary, "\n") {
		t.Fatalf("unexpected display summary: %q", record.DisplaySummary)
	}
	for _, want := range []string{"killed_by_planner", "aborted_tools", "7", "Bash", "결과 못 받음", "1200"} {
		if !strings.Contains(record.DisplayDetail, want) {
			t.Errorf("display detail missing %q:\n%s", want, record.DisplayDetail)
		}
	}
}

func TestTerminalTextDirectContextCancellationKeepsCause(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(AbortChatStoppedByUser)
	record := terminalText(ctx, &harness.Terminal{Err: context.Canceled}, &runTrace{startedAt: time.Now()})
	if !strings.Contains(record.DisplaySummary, "사용자가 이번 대화를 멈춤") || !strings.Contains(record.Detail, "chat_stopped_by_user") {
		t.Fatalf("direct context cancellation lost cause: %q\n%s", record.DisplaySummary, record.Detail)
	}
	if !strings.Contains(record.Detail, "context_canceled") {
		t.Fatalf("missing synthesized terminal label: %s", record.Detail)
	}
}

func TestTerminalTextAbortedPreservesPartialOutput(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(AbortPausedByUser)
	const partial = "already generated partial answer"
	record := terminalText(ctx, &harness.Terminal{
		Reason: harness.ReasonAbortedStreaming,
		Text:   partial,
	}, &runTrace{startedAt: time.Now()})
	if !strings.Contains(record.DisplaySummary, "사용자가 작업을 일시정지함") {
		t.Fatalf("abort summary lost cause: %q", record.DisplaySummary)
	}
	if !strings.Contains(record.Detail, partial) || !strings.Contains(record.DisplayDetail, partial) {
		t.Fatalf("abort detail lost partial output: %s", record.Detail)
	}
}

func TestTerminalTextCompletedToolNotBlamed(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(AbortShutdown)
	trace := &runTrace{startedAt: time.Now()}
	trace.start("tu_1", "Read", `{"path":"/etc/hosts"}`)
	trace.done("tu_1")
	record := terminalText(ctx, &harness.Terminal{Reason: harness.ReasonAbortedStreaming}, trace)
	if strings.Contains(record.Detail, "no result returned") || !strings.Contains(record.Detail, "returned normally") {
		t.Fatalf("completed tool was blamed:\n%s", record.Detail)
	}
	if strings.Contains(record.DisplayDetail, "결과 못 받음") || !strings.Contains(record.DisplayDetail, "정상 반환") {
		t.Fatalf("completed tool was blamed on screen:\n%s", record.DisplayDetail)
	}
}

func TestTerminalTextNonAbortReasons(t *testing.T) {
	trace := &runTrace{startedAt: time.Now()}
	record := terminalText(context.Background(), &harness.Terminal{Reason: harness.ReasonMaxTurns}, trace)
	if !strings.Contains(record.Summary, "run budget limit") || strings.Contains(record.Summary, "interrupted") {
		t.Fatalf("unexpected max_turns summary: %q", record.Summary)
	}
	if !strings.Contains(record.DisplaySummary, "실행 예산 상한") || strings.Contains(record.DisplaySummary, "중단") {
		t.Fatalf("unexpected max_turns display summary: %q", record.DisplaySummary)
	}
	record = terminalText(context.Background(), &harness.Terminal{
		Reason: harness.ReasonModelError, Err: errors.New("429 rate limited"),
	}, trace)
	if !strings.Contains(record.Summary, "model_error") || !strings.Contains(record.Detail, "429 rate limited") {
		t.Fatalf("model_error detail incomplete: %q\n%s", record.Summary, record.Detail)
	}
}

// 모델이 읽는 글은 영어, 사람이 보는 글은 한국어이고, 코드와 enum 값은 어느 쪽에서도 그대로여야 한다.
func TestTerminalTextLanguagePerReader(t *testing.T) {
	for _, reason := range []harness.TerminalReason{
		harness.ReasonCompleted, harness.ReasonMaxTurns, harness.ReasonTimeout, harness.ReasonModelError,
		harness.ReasonBlockingLimit, harness.ReasonPromptTooLong, harness.ReasonImageError,
		harness.ReasonStopHookPrevented, harness.ReasonHookStopped, harness.ReasonAbortedStreaming, harness.ReasonAbortedTools,
	} {
		for _, words := range []terminalWords{terminalEnglish, terminalKorean} {
			if words.hints[reason] == "" {
				t.Errorf("%s: missing hint", reason)
			}
		}
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(AbortGoalMet)
		trace := &runTrace{startedAt: time.Now()}
		trace.start("tu_1", "Bash", "{}")
		record := terminalText(ctx, &harness.Terminal{Reason: reason, Turns: 2, Usage: llm.Usage{InputTokens: 1}}, trace)
		english := record.Summary + record.Detail
		if strings.IndexFunc(english, func(r rune) bool { return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hangul, r) }) >= 0 {
			t.Errorf("%s: model text must be English:\n%s", reason, english)
		}
		display := record.DisplaySummary + record.DisplayDetail
		if strings.IndexFunc(display, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 ||
			strings.IndexFunc(display, func(r rune) bool { return unicode.Is(unicode.Hangul, r) }) < 0 {
			t.Errorf("%s: display text must be Korean:\n%s", reason, display)
		}
		if !strings.Contains(record.Detail, string(reason)) || !strings.Contains(record.DisplayDetail, string(reason)) {
			t.Errorf("%s: terminal label must stay in both languages", reason)
		}
	}
}

func TestTerminalRecordActivityCarriesDisplayText(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(AbortGoalMet)
	got := terminalText(ctx, &harness.Terminal{Reason: harness.ReasonAbortedTools}, &runTrace{startedAt: time.Now()}).
		activity(db.Activity{Kind: "result", IsError: true})
	if !got.IsError || got.Kind != "result" {
		t.Fatalf("base fields lost: %+v", got)
	}
	if !strings.Contains(got.Summary, "goal_met") || strings.Contains(got.Detail, "Planner가") {
		t.Fatalf("stored text must be the model text: %+v", got)
	}
	shown := got.ForDisplay()
	if !strings.Contains(shown.Summary, "Planner가 작업 목표 달성으로 판정함") || !strings.Contains(shown.Detail, "Planner가") || shown.Metadata != nil {
		t.Fatalf("display text not applied: %+v", shown)
	}
}

func TestAbortCausesAreWellFormed(t *testing.T) {
	all := []*AbortCause{
		AbortPausedByUser, AbortPausedByOrchestrator, AbortTaskDeleted, AbortPausedOnReload,
		AbortGoalMet, AbortSettleDrainTimeout, AbortKilledByPlanner, AbortWorkPausedByUser,
		AbortWorkCancelledByUser, AbortWorkFinished, AbortPausedRaceGuard,
		AbortChatStoppedByUser, AbortChatPausedWithTask, AbortChatTurnFinished,
		AbortShutdown, AbortRunHardTimeout,
	}
	seen := map[string]bool{}
	for _, abort := range all {
		switch {
		case abort.Code == "" || seen[abort.Code]:
			t.Errorf("missing or duplicate code: %q", abort.Code)
		case abort.Short == "" || utf8.RuneCountInString(abort.Short) > 40:
			t.Errorf("%s has invalid short text: %q", abort.Code, abort.Short)
		case utf8.RuneCountInString(abort.Text) <= utf8.RuneCountInString(abort.Short):
			t.Errorf("%s detail must be longer than short text", abort.Code)
		case abort.ModelText == "" || strings.IndexFunc(abort.ModelText, func(r rune) bool { return r > unicode.MaxASCII }) >= 0:
			t.Errorf("%s model text must be non-empty ASCII: %q", abort.Code, abort.ModelText)
		case strings.IndexFunc(abort.Short+abort.Text, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0:
			t.Errorf("%s display text must not contain Han", abort.Code)
		}
		seen[abort.Code] = true
	}
}

func TestFirstLineCapsByRuneNotByte(t *testing.T) {
	if got := firstLine(strings.Repeat("恢复", 10), 5); got != "恢复恢复恢…" {
		t.Fatalf("firstLine split a Unicode character: %q", got)
	}
	if got := firstLine("头\n尾", 100); got != "头" {
		t.Fatalf("firstLine did not stop at newline: %q", got)
	}
}
