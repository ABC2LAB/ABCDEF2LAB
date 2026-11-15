package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

// 보호 값이 요약 자르는 자리에 걸쳐 있어도 활동 기록에 그 앞부분이 남지 않아야 한다. 요약을 먼저 자르고 가리면 잘린 앞부분이
// 정확한 값 가림을 지나간다(2026-11-14 점검 회차의 TOKEN="…" 요약).
func TestCaptureRunWithRedactsBeforeSummaryTruncation(t *testing.T) {
	const secret = "eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiJ9.eyJzdGF0dXMiOiJzdWNjZXNzIn0.c2lnbmF0dXJl"
	prefix := secret[:16]
	// 보호 값이 200자(도구 입력·결과·모델 글 요약)와 400자(최종 답 요약) 경계에 걸치도록 앞을 채운다.
	pad := func(n int) string { return strings.Repeat("a", n) }
	command, _ := json.Marshal(map[string]string{"command": pad(160) + ` TOKEN="` + secret + `"`})
	thought := pad(170) + " " + secret
	answer := pad(370) + " " + secret
	turn := 0
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		turn++
		events := []llm.StreamEvent{{Type: llm.SETextDelta, Text: answer}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
		if turn == 1 {
			events = []llm.StreamEvent{
				{Type: llm.SETextDelta, Text: thought},
				{Type: llm.SEToolUseStart, ToolID: "echo-call", ToolName: "Echo"},
				{Type: llm.SEToolInputJSON, Text: string(command)}, {Type: llm.SEMessageDelta, StopReason: "tool_use"},
			}
		}
		for _, event := range events {
			if !yield(event, nil) {
				return
			}
		}
	}}
	echo := tool.Build(tool.Spec{Name: "Echo", Schema: map[string]any{"type": "object"},
		Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
			return tool.Text(pad(170) + " " + secret), nil
		},
	})
	redact := func(text string) string { return strings.ReplaceAll(text, secret, "[REDACTED]") }
	var activities []db.Activity
	final, _, err := captureRunWith(t.Context(), agentcore.Options{Provider: provider, Tools: []tool.CoreTool{echo},
		PermissionMode: permission.ModeBypass, WorkingDir: t.TempDir(), MaxTurns: 2}, "run", redact, func(a db.Activity) {
		activities = append(activities, a)
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, a := range activities {
		if strings.Contains(a.Summary, prefix) || strings.Contains(a.Detail, prefix) || strings.Contains(string(a.Metadata), prefix) {
			t.Errorf("%s activity keeps protected prefix: summary=%q", a.Kind, a.Summary)
		}
		if strings.Contains(a.Summary, "[REDACTED]") {
			seen[a.Kind] = true
		}
	}
	// 네 종류 모두 보호 값이 요약 안에 들어온 경우를 실제로 거쳤는지 본다(채움 길이가 바뀌어 경계를 벗어나면 이 시험은 의미가 없다).
	for _, kind := range []string{"text", "tool_use", "tool_result", "result"} {
		if !seen[kind] {
			t.Errorf("%s summary did not cover the protected value: %v", kind, seen)
		}
	}
	// 반환하는 최종 답은 호출자 몫이라 원문 그대로다.
	if !strings.Contains(final, secret) {
		t.Fatal("final answer changed")
	}
}

// E② Worker 세션은 실행의 RedactOutput으로 요약 전에 가린다(배선 시험). 결과 요약(400자) 경계에 시험 계정 세션 값을 둔다.
func TestE2HTTPSessionSummaryHidesTruncatedSessionValue(t *testing.T) {
	const session = "eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiJ9.eyJkYXRhIjoidGVzdGVyLWEifQ.c2lnbmF0dXJl"
	execution, _, _ := e2WorkerFixture(t)
	if err := execution.AddRedactValues([]byte(session)); err != nil {
		t.Fatal(err)
	}
	answer := strings.Repeat("a", 370) + " " + session
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		for _, event := range []llm.StreamEvent{{Type: llm.SETextDelta, Text: answer}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}} {
			if !yield(event, nil) {
				return
			}
		}
	}}
	worker := NewWorker(provider, "synthetic", t.TempDir(), nil, 10000, 1)
	opts := agentcore.Options{Provider: provider, DisableBackgroundTasks: true, PermissionMode: permission.ModeBypass, MaxTurns: 1, MaxDuration: time.Second, NonStreaming: true}
	var activities []db.Activity
	if _, _, err := worker.runE2HTTPSession(t.Context(), "worker", &db.Node{ID: 22, Kind: db.KindIntent}, func(a db.Activity) { activities = append(activities, a) }, opts, "assigned intent", execution, &e2HTTPToolState{}, nil); err != nil && !errors.Is(err, ErrE2HTTPNotExecuted) {
		// 명령 없이 끝난 세션은 미실행으로 돌아온다. 이 시험은 활동 기록만 본다.
		t.Fatal(err)
	}
	covered := false
	for _, a := range activities {
		if strings.Contains(a.Summary+a.Detail+string(a.Metadata), session[:16]) {
			t.Fatalf("%s activity keeps session prefix: summary=%q", a.Kind, a.Summary)
		}
		covered = covered || a.Kind == "result" && strings.Contains(a.Summary, "[REDACTED]")
	}
	if !covered {
		t.Fatal("result summary did not cover the session value")
	}
}

// 탐색 실행도 실행 가림(o.Redact)으로 요약 전에 가린다(배선 시험). 결과 요약(400자) 경계에 보호 표식을 둔다.
func TestExplorationSummaryHidesTruncatedProtectedValue(t *testing.T) {
	provider := &explorationProvider{answer: strings.Repeat("a", 385) + " seeded-private-marker"}
	w := NewWorker(provider, "owned", t.TempDir(), nil, 0, 10)
	w.SetNonStreaming(func() bool { return true })
	var activities []db.Activity
	if _, err := w.ExecuteExploration(t.Context(), explorationOptions(&recordingDiscoveryReporter{n: 1}), func(a db.Activity) { activities = append(activities, a) }); err != nil {
		t.Fatal(err)
	}
	covered := false
	for _, a := range activities {
		if strings.Contains(a.Summary+a.Detail+string(a.Metadata), "seeded-pri") {
			t.Fatalf("%s activity keeps marker prefix: summary=%q", a.Kind, a.Summary)
		}
		covered = covered || a.Kind == "result" && strings.Contains(a.Summary, "[REDACTED]")
	}
	if !covered {
		t.Fatal("result summary did not cover the marker")
	}
}

// 결과 행의 화면용 글(metadata → ForDisplay)에도 보호 값이 남지 않아야 한다. 제공자 오류 글과 중단 직전 모델 글은 terminalText가
// 화면용 한국어로 다시 쓰는데, 그 글을 가리지 않으면 Summary·Detail을 가려도 화면에는 원문이 나간다(#573 리뷰).
func TestCaptureRunWithResultDisplayHidesProtectedError(t *testing.T) {
	const secret = "eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiJ9.eyJzdGF0dXMiOiJzdWNjZXNzIn0.c2lnbmF0dXJl"
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("upstream rejected TOKEN="+secret))
	}}
	redact := func(text string) string { return strings.ReplaceAll(text, secret, "[REDACTED]") }
	var activities []db.Activity
	_, _, err := captureRunWith(t.Context(), agentcore.Options{Provider: provider, PermissionMode: permission.ModeBypass,
		WorkingDir: t.TempDir(), MaxTurns: 1}, "run", redact, func(a db.Activity) { activities = append(activities, a) })
	// 반환 오류는 호출자 몫이라 원문 그대로다.
	if err == nil || !strings.Contains(err.Error(), secret) {
		t.Fatalf("returned error changed: %v", err)
	}
	results := 0
	for _, a := range activities {
		if a.Kind != "result" {
			continue
		}
		results++
		if len(a.Metadata) == 0 {
			t.Fatalf("result row has no display text: %+v", a)
		}
		shown := a.ForDisplay()
		if strings.Contains(a.Summary+a.Detail+string(a.Metadata)+shown.Summary+shown.Detail, secret[:16]) {
			t.Fatalf("result row keeps protected error: display=%q", shown.Detail)
		}
		if !strings.Contains(shown.Detail, "[REDACTED]") {
			t.Fatalf("display text did not carry the redacted error: %q", shown.Detail)
		}
	}
	if results == 0 {
		t.Fatal("no result row")
	}
}

// 중단된 실행(스트리밍 중 취소)의 결과 행은 중단 직전 모델 글과 오류를 화면용 글에 넣는다. 그 사본을 가린 뒤 만든다.
// 지금 SDK는 취소 때 Terminal.Text를 비워 실제 세션으로는 재현되지 않아, 결과 행을 만드는 경로를 직접 거친다.
func TestRedactedTerminalHidesPartialTextOnDisplay(t *testing.T) {
	const secret = "eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiJ9.eyJzdGF0dXMiOiJzdWNjZXNzIn0.c2lnbmF0dXJl"
	redact := func(text string) string { return strings.ReplaceAll(text, secret, "[REDACTED]") }
	original := errors.New("stream closed after TOKEN=" + secret)
	term := &harness.Terminal{Reason: harness.ReasonAbortedStreaming, Text: `TOKEN="` + secret + `"`, Err: original, Turns: 2}
	shownTerm := redactedTerminal(term, redact)
	row := terminalText(t.Context(), shownTerm, &runTrace{startedAt: time.Now()}).activity(db.Activity{Kind: "result", IsError: true})
	shown := row.ForDisplay()
	if strings.Contains(row.Summary+row.Detail+string(row.Metadata)+shown.Summary+shown.Detail, secret[:16]) {
		t.Fatalf("aborted result keeps protected value: display=%q", shown.Detail)
	}
	if strings.Count(shown.Detail, "[REDACTED]") < 2 {
		t.Fatalf("display text lost the partial text or error line: %q", shown.Detail)
	}
	// 원래 사건은 바꾸지 않는다(반환·판정은 원문 오류를 쓴다).
	if term.Text == shownTerm.Text || term.Err != original || shownTerm.Turns != 2 || shownTerm.Reason != term.Reason {
		t.Fatal("terminal copy changed the original or dropped fields")
	}
	if redactedTerminal(term, nil) != term || redactedTerminal(nil, redact) != nil || redactedError(nil, redact) != nil {
		t.Fatal("nil redaction must keep the terminal")
	}
}

// E② Worker의 실패 결과 행은 화면에도 고정 문구만 보인다. metadata를 남기면 ForDisplay가 고정 문구를 원문 오류로 덮는다.
func TestE2HTTPSessionErrorResultDisplayIsFixed(t *testing.T) {
	const session = "eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiJ9.eyJkYXRhIjoidGVzdGVyLWEifQ.c2lnbmF0dXJl"
	execution, _, _ := e2WorkerFixture(t)
	if err := execution.AddRedactValues([]byte(session)); err != nil {
		t.Fatal(err)
	}
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("provider echoed Authorization: Bearer "+session))
	}}
	worker := NewWorker(provider, "synthetic", t.TempDir(), nil, 10000, 1)
	opts := agentcore.Options{Provider: provider, DisableBackgroundTasks: true, PermissionMode: permission.ModeBypass, MaxTurns: 1, MaxDuration: time.Second, NonStreaming: true}
	var activities []db.Activity
	_, _, _ = worker.runE2HTTPSession(t.Context(), "worker", &db.Node{ID: 22, Kind: db.KindIntent}, func(a db.Activity) { activities = append(activities, a) }, opts, "assigned intent", execution, &e2HTTPToolState{}, nil)
	errorResults := 0
	for _, a := range activities {
		shown := a.ForDisplay()
		if strings.Contains(a.Summary+a.Detail+string(a.Metadata)+shown.Summary+shown.Detail, session[:16]) {
			t.Fatalf("%s activity keeps session value on display: %q", a.Kind, shown.Detail)
		}
		if a.Kind == "result" && a.IsError {
			errorResults++
			if shown.Summary != ErrE2HTTPWorker.Error() || shown.Detail != ErrE2HTTPWorker.Error() {
				t.Fatalf("error result display is not the fixed text: %q / %q", shown.Summary, shown.Detail)
			}
		}
	}
	if errorResults == 0 {
		t.Fatal("no error result row")
	}
}
