package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"github.com/Autumn-27/norma/harness"
	actool "github.com/Autumn-27/norma/tool"
)

type diagnosticToolsKey struct{}
type diagnosticTools struct {
	role  string
	tools map[string]actool.CoreTool
}

// 등록된 도구만 이름을 기록한다. 모델이 제안한 미등록 이름은 남기지 않는다.
func withDiagnosticTools(ctx context.Context, tools []actool.CoreTool, role string) (context.Context, []actool.CoreTool) {
	if diagnosticlog.From(ctx) == nil {
		return ctx, tools
	}
	d := diagnosticTools{role: role, tools: map[string]actool.CoreTool{}}
	wrapped := make([]actool.CoreTool, 0, len(tools))
	for _, t := range tools {
		d.tools[t.Name()] = t
		wrapped = append(wrapped, &diagnosticTool{CoreTool: t})
	}
	return context.WithValue(ctx, diagnosticToolsKey{}, d), wrapped
}
func diagnosticAgentEvent(ctx context.Context, e diagnosticlog.Event) {
	ri := RunInfo{}
	if ctx != nil {
		ri = RunInfoFrom(ctx)
	}
	e.Component = "agent"
	if e.TaskID == 0 {
		e.TaskID = ri.TaskID
	}
	if e.IntentID == 0 {
		e.IntentID = ri.IntentID
	}
	_ = diagnosticlog.Emit(ctx, e) // 실패는 logger에 고정되고 CLI가 성공과 분리해 보고한다.
}
func observeDiagnosticToolUse(ctx context.Context, name string, input json.RawMessage) {
	d, ok := ctx.Value(diagnosticToolsKey{}).(diagnosticTools)
	if !ok {
		return
	}
	t, ok := d.tools[name]
	if !ok {
		diagnosticAgentEvent(ctx, diagnosticlog.Event{Event: "tool_selected", Stage: "tool_execution", Status: "failed", ReasonCode: "unregistered_tool", Role: d.role})
		return
	}
	diagnosticAgentEvent(ctx, diagnosticlog.Event{Event: "tool_selected", Stage: "tool_execution", Status: "succeeded", Tool: t.Name(), Role: d.role})
	if raw, ok := t.(interface{ AcceptsRawInput() bool }); ok && raw.AcceptsRawInput() {
		return
	}
	status, reason := "succeeded", ""
	if actool.ValidateInput(t.InputSchema(), input) != nil {
		status, reason = "failed", "schema_invalid"
	}
	diagnosticAgentEvent(ctx, diagnosticlog.Event{Event: "tool_schema_checked", Stage: "tool_execution", Status: status, ReasonCode: reason, Tool: t.Name(), Role: d.role})
}

type diagnosticTool struct{ actool.CoreTool }

func (t *diagnosticTool) AcceptsRawInput() bool {
	raw, ok := t.CoreTool.(interface{ AcceptsRawInput() bool })
	return ok && raw.AcceptsRawInput()
}
func (t *diagnosticTool) Call(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	start := time.Now()
	diagnosticAgentEvent(ctx, diagnosticlog.Event{Event: "tool_started", Stage: "tool_execution", Status: "started", Tool: t.Name()})
	result, err := t.CoreTool.Call(ctx, input, tc)
	status, reason := "succeeded", ""
	if err != nil || result.IsError {
		status, reason = "failed", "tool_failed"
	}
	diagnosticAgentEvent(ctx, diagnosticlog.Event{Event: "tool_finished", Stage: "tool_execution", Status: status, ReasonCode: reason, Tool: t.Name(), DurationMS: time.Since(start).Milliseconds()})
	return result, err
}
func diagnosticTerminal(reason harness.TerminalReason) string {
	switch reason {
	case harness.ReasonCompleted, harness.ReasonBlockingLimit, harness.ReasonImageError, harness.ReasonModelError, harness.ReasonAbortedStreaming, harness.ReasonAbortedTools, harness.ReasonPromptTooLong, harness.ReasonStopHookPrevented, harness.ReasonHookStopped, harness.ReasonMaxTurns, harness.ReasonTimeout:
		return string(reason)
	}
	return "terminal_unknown"
}
func observeDiagnosticTerminal(ctx context.Context, t *harness.Terminal) {
	d, ok := ctx.Value(diagnosticToolsKey{}).(diagnosticTools)
	if !ok || t == nil {
		return
	}
	event, stage := "worker_session_finished", "worker"
	if d.role == "planner" {
		event, stage = "planner_session_finished", "planning"
	}
	status := "failed"
	if t.Reason == harness.ReasonCompleted && t.Err == nil {
		status = "succeeded"
	}
	if t.Reason == harness.ReasonTimeout {
		status = "timeout"
	}
	if errors.Is(t.Err, context.Canceled) {
		status = "cancelled"
	}
	diagnosticAgentEvent(ctx, diagnosticlog.Event{Event: event, Stage: stage, Status: status, ReasonCode: diagnosticTerminal(t.Reason), Role: d.role, Count: t.Turns})
}
func diagnosticWorkerReason(err error) string {
	if err == nil {
		return "complete"
	}
	for _, c := range []struct {
		err  error
		code string
	}{{context.Canceled, "cancelled"}, {context.DeadlineExceeded, "timeout"}, {ErrE2HTTPBinding, "e2_http_worker_binding_invalid"}, {ErrE2HTTPUnknown, "e2_http_execution_result_unknown"}, {ErrE2HTTPIncomplete, "e2_http_execution_closure_incomplete"}, {ErrE2HTTPBlocked, "e2_http_execution_blocked"}, {ErrE2HTTPPolicyLimited, "e2_http_policy_limited"}, {ErrE2HTTPUnsupported, "e2_http_unsupported"}, {ErrE2HTTPTransient, "e2_http_transient_failure"}, {ErrE2HTTPUnavailable, "e2_http_isolation_unavailable"}, {ErrE2HTTPNotExecuted, "e2_http_not_executed"}} {
		if errors.Is(err, c.err) {
			return c.code
		}
	}
	return "e2_http_worker_failed"
}

func diagnosticToolClassification(class string) string {
	switch class {
	case "completed", "tool_failure", "policy_not_executed", "partial_policy_denial", "unsupported", "partial_unsupported", "result_unknown", "transient_failure", "forward_not_attempted", "isolation_unavailable", "terminated":
		return class
	}
	return "invalid"
}

func observeDiagnosticWorkerFinished(ctx context.Context, binding E2HTTPExecution, terminal harness.TerminalReason, outcome error, duration time.Duration) {
	status := "succeeded"
	if outcome != nil {
		status = "failed"
	}
	if errors.Is(outcome, context.Canceled) {
		status = "cancelled"
	}
	if errors.Is(outcome, context.DeadlineExceeded) || terminal == harness.ReasonTimeout {
		status = "timeout"
	}
	reason := diagnosticWorkerReason(outcome)
	if terminal == harness.ReasonTimeout {
		reason = "timeout"
	}
	if outcome == nil && terminal != "" && terminal != harness.ReasonCompleted {
		reason = diagnosticTerminal(terminal)
		if status == "succeeded" {
			status = "incomplete"
		}
	}
	diagnosticAgentEvent(ctx, diagnosticlog.Event{Event: "worker_finished", Stage: "worker", Status: status, ReasonCode: reason, TaskID: binding.TaskID, IntentID: binding.IntentID, RunRef: binding.RunRef, DurationMS: duration.Milliseconds()})
}
