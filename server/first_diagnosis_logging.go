package server

import (
	"context"
	"errors"
	"time"

	"github.com/Autumn-27/artex/internal/diagnosticlog"
)

// 원문 오류 대신 호출 지점과 문맥에서 확인한 고정 사유만 기록한다.
func diagnosisLogOutcome(ctx context.Context, err error, fallback string) (string, string) {
	if err == nil {
		return "succeeded", ""
	}
	if errors.Is(err, context.DeadlineExceeded) || ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout", "timeout"
	}
	if errors.Is(err, context.Canceled) || ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return "cancelled", "user_cancelled"
	}
	if err != nil {
		return "failed", fallback
	}
	return "succeeded", ""
}

func diagnosisStage(ctx context.Context, task int64, stage string, err error, fallback string, started time.Time) {
	status, reason := diagnosisLogOutcome(ctx, err, fallback)
	_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "supervisor", Event: "stage_finished", TaskID: task, Stage: stage, Status: status, ReasonCode: reason, DurationMS: time.Since(started).Milliseconds()})
}

// 추가 조회는 로거가 연결된 첫 진단에만 수행하며 실행 결과를 바꾸지 않는다.
func (s *FirstDiagnosisSupervisor) plannerLogStart(ctx context.Context, task *Task) (int64, bool) {
	return s.server.engine.plannerLogStart(ctx, task)
}
func (e *Engine) plannerLogStart(ctx context.Context, task *Task) (int64, bool) {
	if diagnosticlog.From(ctx) == nil {
		return 0, false
	}
	_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "planner", Event: "planner_started", TaskID: mustFirstDiagnosisTaskNumber(task.ID), Stage: "planning", Status: "started"})
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var last int64
	err := e.m.pg.QueryRowContext(call, "SELECT COALESCE(max(id),0) FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent'", task.ExpID).Scan(&last)
	if err != nil {
		_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "planner", Event: "stage_finished", TaskID: mustFirstDiagnosisTaskNumber(task.ID), Stage: "planning", Status: "incomplete", ReasonCode: "intent_storage_failed"})
	}
	return last, err == nil
}
func (e *Engine) plannerLogFinish(ctx context.Context, task *Task, last int64, observed bool, operationErr error, started time.Time) {
	if diagnosticlog.From(ctx) == nil {
		return
	}
	status, reason := diagnosisLogOutcome(ctx, operationErr, "planner_failed")
	event := diagnosticlog.Event{Component: "planner", Event: "planner_finished", TaskID: mustFirstDiagnosisTaskNumber(task.ID), Stage: "planning", Status: status, ReasonCode: reason, DurationMS: time.Since(started).Milliseconds()}
	if observed {
		call, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		var count int
		err := e.m.pg.QueryRowContext(call, "SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent' AND id>$2", task.ExpID, last).Scan(&count)
		if err == nil {
			event.Count, event.CountKnown = count, true
		} else {
			event.Status, event.ReasonCode = "incomplete", "intent_storage_failed"
		}
	} else {
		event.Status, event.ReasonCode = "incomplete", "intent_storage_failed"
	}
	_ = diagnosticlog.Emit(ctx, event)
}

func diagnosisLogStage(stage string) string {
	switch stage {
	case "admission", "quiescence", "execution", "fence", "drain":
		return stage
	case "task", "intent-binding":
		return "task_binding"
	case "e2-policy":
		return "e2_policy"
	case "model":
		return "model_profile"
	case "registered-result":
		return "result_storage"
	case "audit-close":
		return "audit_close"
	case "user-stop":
		return "shutdown"
	default:
		return "execution"
	}
}
func diagnosisLogStageReason(stage string) string {
	switch stage {
	case "task", "quiescence", "intent-binding":
		return "task_binding_failed"
	case "e2-policy":
		return "e2_policy_failed"
	case "model":
		return "model_profile_invalid"
	case "drain", "user-stop":
		return "drain_failed"
	case "audit-close":
		return "audit_failed"
	case "registered-result":
		return "result_storage_failed"
	default:
		return "execution_failed"
	}
}

// 마감 호출자가 별도 문맥을 쓰더라도 같은 첫 진단의 관측기만 보존한다.
func diagnosisLogContext(ctx, parent context.Context) context.Context {
	if ctx != nil && diagnosticlog.From(ctx) == nil && diagnosticlog.From(parent) != nil {
		return diagnosticlog.With(ctx, diagnosticlog.From(parent))
	}
	return ctx
}

func (s *FirstDiagnosisSupervisor) logRunSummary(ctx context.Context, task int64, state requestApprovalRunState, stored bool) {
	if diagnosticlog.From(ctx) == nil {
		return
	}
	observationComplete := !diagnosticlog.From(ctx).Incomplete() && diagnosticlog.From(ctx).Err() == nil
	event := diagnosticlog.Event{Component: "supervisor", Event: "run_summary", TaskID: task, RunRef: state.RunRef, Stage: "result_storage", Status: "succeeded", ResultStored: &stored, ExecutionClosed: &state.DrainComplete}
	if state.Outcome == "cancelled" {
		event.Status, event.ReasonCode = "cancelled", "user_cancelled"
	}
	if state.Outcome == "failed" {
		event.Status, event.ReasonCode = "failed", "execution_failed"
		s.mu.Lock()
		observedErr := s.runLogErr
		s.mu.Unlock()
		if errors.Is(observedErr, context.DeadlineExceeded) {
			event.Status, event.ReasonCode = "timeout", "timeout"
		}
	}
	if state.ClosureFailed {
		event.Status, event.ReasonCode = "failed", "drain_failed"
	}
	if !stored {
		event.Status, event.ReasonCode = "failed", "result_storage_failed"
	}
	if state.Execution != nil {
		view := state.Execution
		event.AuditComplete = &view.AuditComplete
		event.TargetRequests, event.Approved, event.Denied, event.Blocked = int(view.Counts.ForwardAttempted), int(view.Counts.Allowed), int(view.Counts.Denied), int(view.Counts.Unsupported)
	}
	if model := s.ModelUsage(); model != nil {
		event.Count, event.CountKnown = int(model.Requests), true
		event.InputTokens, event.OutputTokens = model.InputTokens, model.OutputTokens
		event.ReservedInputTokens, event.ReservedOutputTokens, event.ReservedCostMicros, event.CostMicros = model.ReservedInputTokens, model.ReservedOutputTokens, model.ReservedCostMicros, model.CostMicros
		event.CostUncertain = model.Errors > 0
	}
	call, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := s.m.pg.QueryRowContext(call, `SELECT count(*) FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id WHERE t.id=$1 AND n.origin='e2_diagnostic_candidate_v1' AND n.payload->'diagnostic_candidate_v1'->>'run_ref'=$2`, task, state.RunRef).Scan(&event.Candidates); err != nil {
		observationComplete = false
		event.Status, event.ReasonCode = "incomplete", "intent_storage_failed"
	}
	event.ObservationComplete = &observationComplete
	_ = diagnosticlog.Emit(ctx, event)
}

// cancel 정리보다 먼저 defer하여 정리 자체의 취소를 실행 실패로 오인하지 않는다.
func captureDiagnosisContextError(ctx context.Context, into *error) { *into = ctx.Err() }

// 저장된 실행 상태의 timeout은 일반 오류 반환과 별도로 관측에서 보존한다.
func diagnosisRunObservedError(err error, taskStatus, intentState string) error {
	if taskStatus == "timeout" || intentState == "timeout" {
		return context.DeadlineExceeded
	}
	return err
}
