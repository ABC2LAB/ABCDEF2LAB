package llmrec

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"github.com/Autumn-27/norma/llm"
)

func diagnosticRole(role string) string {
	switch role {
	case "goals", "planner", "worker", "mainagent", "judge", "reporter":
		return role
	}
	return "unknown"
}

// transcript가 없을 때만 호스트 runtime이 제공한 유한 역할을 사용한다.
func diagnosticCallRole(ctx context.Context, worker string) string {
	if worker != "" {
		return diagnosticRole(worker)
	}
	return diagnosticRole(diagnosticlog.RoleFrom(ctx))
}

// 실제 task 문맥만 사용한다. exploration 세션 ID를 task ID로 추정하지 않는다.
func (r *Recorder) startDiagnostic(ctx context.Context, capture *Capture) func(llm.Usage, error, bool, string) {
	event := diagnosticlog.Event{Component: "llmrec", Event: "model_call_started", Stage: "model_call", Status: "started", Role: diagnosticlog.RoleFrom(ctx), ModelRef: r.model}
	if r.model == "qwen/qwen3.6-35b-a3b" {
		event.Model = r.model
	}
	if task := TaskIDFrom(ctx); task != "" {
		if id, err := strconv.ParseInt(task, 10, 64); err == nil && id > 0 && strconv.FormatInt(id, 10) == task {
			event.TaskID = id
		}
	}
	start := time.Now()
	_ = diagnosticlog.Emit(ctx, event)
	return func(usage llm.Usage, err error, complete bool, stop string) {
		event.Event = "model_call_finished"
		event.DurationMS = time.Since(start).Milliseconds()
		event.Status = "succeeded"
		switch {
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
			event.Status, event.ReasonCode = "timeout", "timeout"
		case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
			event.Status, event.ReasonCode = "cancelled", "cancelled"
		case err != nil:
			event.Status, event.ReasonCode = "failed", "model_error"
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				event.Status, event.ReasonCode = "timeout", "timeout"
			}
		case !complete:
			event.Status, event.ReasonCode = "incomplete", "consumer_stopped"
		case stop == "max_tokens" || stop == "length":
			event.ReasonCode = "token_limit"
		}
		// 기존 metadata-only capture의 상태만 재사용한다. 본문은 읽지 않는다.
		attempts := capture.Metadata()
		if len(attempts) > 0 {
			status := attempts[len(attempts)-1].Status
			if status >= 100 && status <= 599 {
				event.HTTPStatus = status
			}
			if event.ReasonCode == "model_error" {
				switch {
				case status == 401:
					event.ReasonCode = "http_unauthorized"
				case status == 429:
					event.ReasonCode = "http_rate_limited"
				case status >= 500 && status <= 599:
					event.ReasonCode = "http_server_error"
				case status >= 400 && status <= 499:
					event.ReasonCode = "http_client_error"
				}
			}
		}
		if usage.InputTokens >= 0 {
			event.InputTokens = int64(usage.InputTokens)
		}
		if usage.OutputTokens >= 0 {
			event.OutputTokens = int64(usage.OutputTokens)
		}
		if usage.InputTokens < 0 || usage.OutputTokens < 0 {
			event.Status, event.ReasonCode = "failed", "response_invalid"
		}
		_ = diagnosticlog.Emit(ctx, event)
	}
}
