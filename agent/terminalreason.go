package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/harness"
)

// resultSummaryRunes 는 결과 행 요약 칸의 글자 상한이다.
const resultSummaryRunes = 400

// runTrace retains the latest tool call so an interrupted run can identify the
// operation that was still in flight.
type runTrace struct {
	startedAt time.Time
	id        string
	name      string
	input     string
	at        time.Time
	pending   bool
}

func (t *runTrace) start(id, name, input string) {
	t.id, t.name, t.input, t.at, t.pending = id, name, input, time.Now(), true
}

func (t *runTrace) done(id string) {
	if id == t.id {
		t.pending = false
	}
}

// terminalWords 는 종료 결과 한 벌의 문구다. 모델은 영어로, 사람은 한국어로 읽는다.
type terminalWords struct {
	hints           map[harness.TerminalReason]string
	hintRunes       int // 요약에 넣는 설명의 글자 상한. 같은 내용이어도 언어마다 글자 수가 다르다
	hintNoTerminal  string
	hintUnknown     string
	stageDefault    string
	stageStreaming  string
	stageTools      string
	abortedSummary  string // 원인, 단계, 진행
	budgetSummary   string // 종료 상태, 진행
	noTextSummary   string // 종료 상태, 설명
	reasonLine      string
	abortLine       string
	noAbortLine     string
	errLine         string
	partialLine     string
	turnsLine       string
	elapsedLine     string
	tokensLine      string
	noToolLine      string
	pendingToolLine string
	lastToolLine    string
	turnsUnit       string
	progressPrefix  string
	unknownAbort    string
	abortName       func(*AbortCause) string
	abortText       func(*AbortCause) string
}

var terminalEnglish = terminalWords{
	hints: map[harness.TerminalReason]string{
		harness.ReasonCompleted:         "The model finished this round normally but left no text summary; rely on this round's tool call records for facts and assets",
		harness.ReasonMaxTurns:          "Reached the step limit (MaxTurns): the SDK ran the wrap-up and wrote back facts and assets; the intent is marked exhausted so the planner can change direction, and it isn't treated as a failure",
		harness.ReasonTimeout:           "Reached the wall-clock budget of this run (MaxDuration): at that point the running tool is interrupted and wrap-up starts on the spot, writing back the facts and assets identified; the intent is marked exhausted",
		harness.ReasonModelError:        "The model or API call failed (network, auth, rate limit, provider 5xx and so on); after retries ran out the intent is marked blocked. A transport failure means this intent was barely explored; check its execution with get_worker_trace before deciding to dispatch it again or change approach",
		harness.ReasonBlockingLimit:     "Context length reached the hard limit and the request was blocked before sending; narrow the intent or compress tool output",
		harness.ReasonPromptTooLong:     "The prompt is too long and the context compaction retries ran out, so the run can't continue",
		harness.ReasonImageError:        "The current model doesn't support this round's multimodal content; switch to a model that supports vision or avoid tools that return images",
		harness.ReasonStopHookPrevented: "A Stop hook prevented this round from ending and the run then couldn't continue; check whether the task Guard rules are too strict",
		harness.ReasonHookStopped:       "A tool or hook actively stopped further execution, for example an out-of-scope target or a prohibited command; check the block note in the last tool_result",
		harness.ReasonAbortedStreaming:  "The run was cancelled while the model output was streaming",
		harness.ReasonAbortedTools:      "The run was cancelled during tool execution",
	},
	hintRunes:       200,
	hintNoTerminal:  "The run's context was cancelled, but the lower layer produced no Terminal event",
	hintUnknown:     "Unknown terminal state; the harness may have added a TerminalReason, so reasonHint needs an entry",
	stageDefault:    "execution",
	stageStreaming:  "model output",
	stageTools:      "tool execution",
	abortedSummary:  "(Run interrupted: %s; stopped during %s%s; not finished)",
	budgetSummary:   "(Reached the run budget limit (%s); wrapped up and wrote back facts%s; no text summary this time)",
	noTextSummary:   "(No text summary, terminal state %s: %s)",
	reasonLine:      "- **Terminal state**: `%s` - %s\n",
	abortLine:       "- **Interruption cause** (`%s`): %s\n",
	noAbortLine:     "- **Interruption cause**: unavailable; the cancelling side may not have attached a named cause through context.WithCancelCause\n",
	errLine:         "- **Underlying error**: `%v`\n",
	partialLine:     "- **Partial output generated before the cancellation**:\n\n",
	turnsLine:       "- **Executed**: %d model turns\n",
	elapsedLine:     "- **Run time**: %s\n",
	tokensLine:      "- **Cumulative tokens**: input %d / output %d / cache read %d / cache write %d\n",
	noToolLine:      "- **Tool calls**: the run ended before making any tool call\n",
	pendingToolLine: "- **Tool running at the interruption**: `%s` (running for %s, **no result returned**)\n\n  ```json\n  %s\n  ```\n",
	lastToolLine:    "- **Last tool before the interruption**: `%s` (returned normally)\n",
	turnsUnit:       "%d turns",
	progressPrefix:  ", ran for ",
	unknownAbort:    "cause unavailable",
	abortName:       func(c *AbortCause) string { return c.Code },
	abortText:       func(c *AbortCause) string { return c.ModelText },
}

var terminalKorean = terminalWords{
	hints: map[harness.TerminalReason]string{
		harness.ReasonCompleted:         "모델이 이번 라운드를 정상 종료했지만 글 요약을 남기지 않았다. 사실과 자산은 이번 도구 호출 기록을 기준으로 본다",
		harness.ReasonMaxTurns:          "단계 상한(MaxTurns)에 도달했다. SDK가 마무리해 사실과 자산을 기록했고, 의도는 exhausted로 표시해 Planner가 방향을 바꾸게 한다. 실패로 다루지 않는다",
		harness.ReasonTimeout:           "이번 실행의 벽시계 예산(MaxDuration)에 도달했다. 실행 중인 도구를 끊고 바로 마무리해 찾은 사실과 자산을 기록했으며, 의도는 exhausted로 표시한다",
		harness.ReasonModelError:        "모델이나 API 호출이 실패했다(네트워크, 인증, 호출 제한, 제공자 5xx 등). 재시도를 다 써서 의도를 blocked로 표시했고, 전송 장애라 이 의도는 거의 탐색되지 않았다. 실행 과정(get_worker_trace)을 본 뒤 다시 보낼지 방법을 바꿀지 정한다",
		harness.ReasonBlockingLimit:     "컨텍스트 길이가 하드 상한에 도달해 요청을 보내기 전에 막았다. 의도 범위를 좁히거나 도구 출력을 줄여야 한다",
		harness.ReasonPromptTooLong:     "프롬프트가 너무 길고 컨텍스트 압축 재시도도 다 써서 계속할 수 없다",
		harness.ReasonImageError:        "현재 모델은 이번 라운드의 멀티모달 내용을 지원하지 않는다. 시각을 지원하는 모델로 바꾸거나 이미지를 돌려주는 도구를 피한다",
		harness.ReasonStopHookPrevented: "Stop 훅이 이번 라운드 종료를 막았고 그 뒤 계속하지 못했다. 작업 Guard 규칙이 너무 엄격한지 확인한다",
		harness.ReasonHookStopped:       "도구나 훅이 실행을 멈췄다. 범위 밖 대상이나 금지 명령이 그런 경우다. 마지막 tool_result의 차단 설명을 확인한다",
		harness.ReasonAbortedStreaming:  "모델 출력을 받는 중에 실행이 취소됐다",
		harness.ReasonAbortedTools:      "도구 실행 중에 실행이 취소됐다",
	},
	hintRunes:       120,
	hintNoTerminal:  "실행 context가 취소됐지만 하위 계층이 Terminal 이벤트를 만들지 않았다",
	hintUnknown:     "알 수 없는 종료 상태다. harness에 TerminalReason이 추가됐을 수 있으니 reasonHint를 보충한다",
	stageDefault:    "실행 중",
	stageStreaming:  "모델 출력 단계",
	stageTools:      "도구 실행 단계",
	abortedSummary:  "(실행 중단: %s. %s에 멈춤%s, 끝나지 않음)",
	budgetSummary:   "(실행 예산 상한 도달(%s). 마무리해 사실을 기록함%s. 이번에는 글 요약 없음)",
	noTextSummary:   "(글 요약 없음, 종료 상태 %s: %s)",
	reasonLine:      "- **종료 상태**: `%s` - %s\n",
	abortLine:       "- **중단 원인** (`%s`): %s\n",
	noAbortLine:     "- **중단 원인**: 알 수 없다. 취소한 쪽이 context.WithCancelCause로 원인을 붙이지 않았을 수 있다\n",
	errLine:         "- **내부 오류**: `%v`\n",
	partialLine:     "- **취소 전에 만든 일부 출력**:\n\n",
	turnsLine:       "- **실행한 모델 턴**: %d\n",
	elapsedLine:     "- **이번 실행 시간**: %s\n",
	tokensLine:      "- **누적 토큰**: 입력 %d / 출력 %d / 캐시 읽기 %d / 캐시 쓰기 %d\n",
	noToolLine:      "- **도구 호출**: 도구를 호출하기 전에 실행이 끝났다\n",
	pendingToolLine: "- **중단 때 실행 중이던 도구**: `%s` (%s 실행, **결과 못 받음**)\n\n  ```json\n  %s\n  ```\n",
	lastToolLine:    "- **중단 전 마지막 도구**: `%s` (정상 반환)\n",
	turnsUnit:       "%d턴",
	progressPrefix:  ", 실행 ",
	unknownAbort:    "취소 원인을 알 수 없음",
	abortName:       func(c *AbortCause) string { return c.Short },
	abortText:       func(c *AbortCause) string { return c.Text },
}

// terminalRecord 는 글 요약 없이 끝난 실행의 결과 행이다. Summary·Detail 은 모델이 읽는 영어로 저장하고,
// 화면용 한국어는 metadata 에 둔다. 모델이 읽는 경로는 여러 곳이지만 화면 경계는 db.Activity.ForDisplay 하나다.
type terminalRecord struct {
	Summary, Detail, DisplaySummary, DisplayDetail string
}

// terminalText renders a terminal event with no final text into a compact summary
// and a Markdown detail block, in both languages.
func terminalText(ctx context.Context, term *harness.Terminal, tr *runTrace) terminalRecord {
	summary, detail := renderTerminal(terminalEnglish, ctx, term, tr)
	displaySummary, displayDetail := renderTerminal(terminalKorean, ctx, term, tr)
	return terminalRecord{Summary: summary, Detail: detail, DisplaySummary: displaySummary, DisplayDetail: displayDetail}
}

// activity 는 결과 행의 글과 metadata 를 채운다. 목록 칸에 맞게 요약 길이는 호출 쪽 규칙과 같게 자른다.
func (r terminalRecord) activity(base db.Activity) db.Activity {
	base.Summary = firstLine(r.Summary, resultSummaryRunes)
	base.Detail = r.Detail
	base.Metadata = db.DisplayMetadata(firstLine(r.DisplaySummary, resultSummaryRunes), r.DisplayDetail)
	return base
}

func renderTerminal(w terminalWords, ctx context.Context, term *harness.Terminal, tr *runTrace) (string, string) {
	reason := term.Reason
	aborted := reason == harness.ReasonAbortedStreaming || reason == harness.ReasonAbortedTools
	// Prompt may return ctx.Err directly without a terminal event. Preserve the
	// cancellation cause instead of falling back to an empty/unknown terminal reason.
	if reason == "" && ctx.Err() != nil {
		aborted = true
	}
	abort := abortCauseOf(ctx)

	var sum string
	if aborted {
		name := w.unknownAbort
		if abort != nil {
			name = w.abortName(abort)
		}
		stage := w.stageDefault
		switch reason {
		case harness.ReasonAbortedStreaming:
			stage = w.stageStreaming
		case harness.ReasonAbortedTools:
			stage = w.stageTools
		}
		sum = fmt.Sprintf(w.abortedSummary, name, stage, progressSuffix(w, term, tr))
	} else if reason == harness.ReasonMaxTurns || reason == harness.ReasonTimeout {
		sum = fmt.Sprintf(w.budgetSummary, string(reason), progressSuffix(w, term, tr))
	} else {
		sum = fmt.Sprintf(w.noTextSummary, terminalReasonLabel(reason), firstLine(terminalReasonHint(w, reason), w.hintRunes))
	}

	var b strings.Builder
	b.WriteString(sum)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, w.reasonLine, terminalReasonLabel(reason), terminalReasonHint(w, reason))
	if aborted {
		if abort != nil {
			fmt.Fprintf(&b, w.abortLine, abort.Code, w.abortText(abort))
		} else {
			b.WriteString(w.noAbortLine)
		}
	}
	if term.Err != nil {
		fmt.Fprintf(&b, w.errLine, term.Err)
	}
	if aborted && strings.TrimSpace(term.Text) != "" {
		b.WriteString(w.partialLine)
		b.WriteString(term.Text)
		b.WriteString("\n\n")
	}
	if term.Turns > 0 {
		fmt.Fprintf(&b, w.turnsLine, term.Turns)
	}
	if !tr.startedAt.IsZero() {
		fmt.Fprintf(&b, w.elapsedLine, roundDur(time.Since(tr.startedAt)))
	}
	if u := term.Usage; u.InputTokens+u.OutputTokens+u.CacheReadTokens+u.CacheWriteTokens > 0 {
		fmt.Fprintf(&b, w.tokensLine, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)
	}
	if tr.name == "" {
		b.WriteString(w.noToolLine)
	} else if tr.pending {
		fmt.Fprintf(&b, w.pendingToolLine, tr.name, roundDur(time.Since(tr.at)), firstLine(tr.input, 300))
	} else {
		fmt.Fprintf(&b, w.lastToolLine, tr.name)
	}
	return sum, b.String()
}

func terminalReasonLabel(reason harness.TerminalReason) string {
	if reason == "" {
		return "context_canceled"
	}
	return string(reason)
}

func terminalReasonHint(w terminalWords, reason harness.TerminalReason) string {
	if hint := w.hints[reason]; hint != "" {
		return hint
	}
	if reason == "" {
		return w.hintNoTerminal
	}
	return w.hintUnknown
}

func progressSuffix(w terminalWords, term *harness.Terminal, tr *runTrace) string {
	var parts []string
	if term.Turns > 0 {
		parts = append(parts, fmt.Sprintf(w.turnsUnit, term.Turns))
	}
	if !tr.startedAt.IsZero() {
		parts = append(parts, roundDur(time.Since(tr.startedAt)))
	}
	if len(parts) == 0 {
		return ""
	}
	return w.progressPrefix + strings.Join(parts, " / ")
}

func roundDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	case d < time.Hour:
		return d.Round(time.Second).String()
	default:
		return d.Round(time.Minute).String()
	}
}
