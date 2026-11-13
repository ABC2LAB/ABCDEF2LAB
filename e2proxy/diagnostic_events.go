package e2proxy

import (
	"context"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"strconv"
)

func diagnosticReceipt(ctx context.Context, r Receipt, event, status, reason string) {
	task, _ := strconv.ParseInt(r.Scope.TaskRef, 10, 64)
	if task < 0 {
		task = 0
	}
	_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "e2proxy", Event: event, Stage: "http_execution", Status: status, ReasonCode: reason, TaskID: task, RunRef: r.Scope.RunRef, IntentID: r.IntentID, CallRef: r.CallRef, AttemptRef: r.AttemptID, HTTPStatus: r.StatusCode})
}
func diagnosticReason(reason Reason) string {
	switch reason {
	case Complete, Withheld, Unsupported, PolicyDenied, Closed, AuditFailed, Expired, Revoked, BudgetExhausted, TransportFailed, Unknown, AttemptLimit, DenialLimit, TransientLimit, UnknownLimit, Invalid:
		return string(reason)
	}
	return "invalid"
}

// 감사 기록 성공은 전송 성공이 아니다. 여기서는 정본의 안전한 상태만 별도 투영한다.
func diagnosticAudit(ctx context.Context, ev Event, err error) {
	if err != nil {
		diagnosticReceipt(ctx, ev.Receipt, "audit_event_failed", "failed", "audit_failed")
		return
	}
	switch ev.Stage {
	case "intent":
		diagnosticReceipt(ctx, ev.Receipt, "http_dispatch_authorized", "allowed", diagnosticReason(ev.Reason))
	case "result":
		status := "not_sent"
		switch {
		case ev.ResponseComplete, ev.Reason == Withheld:
			// 보류(C16)는 대상이 답한 요청이다. 표현을 주지 않았거나 잘랐을 뿐 결과 불명이 아니다(사유 withheld).
			status = "completed"
		case ev.ForwardAttempted:
			status = "unknown"
		case ev.Decision == "unsupported":
			status = "unsupported"
		case ev.Decision == "denied":
			status = "denied"
		}
		diagnosticReceipt(ctx, ev.Receipt, "http_finished", status, diagnosticReason(ev.Reason))
	default:
		diagnosticReceipt(ctx, ev.Receipt, "audit_event_recorded", "succeeded", diagnosticReason(ev.Reason))
	}
}
