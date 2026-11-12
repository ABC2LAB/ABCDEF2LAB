package server

import (
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"testing"
)

func TestFirstDiagnosisClosedToolOutcome(t *testing.T) {
	base := e2runner.TrustedResult{Classification: "completed", Execution: e2proxy.ExecutionView{AuditComplete: true}, Call: e2proxy.CallResult{Reason: e2proxy.Complete}}
	cases := []struct {
		name   string
		change func(*e2runner.TrustedResult)
		want   bool
	}{
		{"completed", func(r *e2runner.TrustedResult) {}, true},
		{"audited-tool-failure", func(r *e2runner.TrustedResult) { r.Classification = "tool_failure"; r.ExitCode = 1 }, true},
		{"policy-not-sent", func(r *e2runner.TrustedResult) {
			r.Classification = "policy_not_executed"
			r.Call.Counts.Denied = 1
			r.Call.Reason = e2proxy.PolicyDenied
		}, true},
		{"partial-policy", func(r *e2runner.TrustedResult) {
			r.Classification = "partial_policy_denial"
			r.Call.Counts.Denied = 1
			r.Call.Counts.ForwardAttempted = 1
		}, true},
		{"unsupported-not-sent", func(r *e2runner.TrustedResult) {
			r.Classification = "unsupported"
			r.Call.Counts.Unsupported = 1
			r.Call.Reason = e2proxy.Unsupported
		}, true},
		{"partial-unsupported", func(r *e2runner.TrustedResult) {
			r.Classification = "partial_unsupported"
			r.Call.Counts.Unsupported = 1
			r.Call.Counts.ForwardAttempted = 1
		}, true},
		{"unknown-count", func(r *e2runner.TrustedResult) { r.Call.Counts.Unknown = 1 }, false},
		// 실행 전체 누계의 결과 불명은 다른 intent의 것이다. 이 호출의 마감을 막지 않는다(C03).
		{"other-intent-unknown", func(r *e2runner.TrustedResult) { r.Execution.Counts.Unknown = 1 }, true},
		// 같은 intent의 앞선 결과 불명 뒤에는 더 보내지 않는다(C01). 그 뒤 결과 불명 아닌 호출은 받지 않는다.
		{"same-intent-unknown-before", func(r *e2runner.TrustedResult) { r.Execution.IntentCounts.Unknown = 1 }, false},
		{"result-unknown", func(r *e2runner.TrustedResult) {
			r.Classification = "result_unknown"
			r.Call.Counts.Unknown = 1
			r.Call.Counts.ForwardAttempted = 1
			r.Call.Reason = e2proxy.Unknown
			r.Execution.IntentCounts.Unknown = 1
		}, true},
		{"result-unknown-then-complete", func(r *e2runner.TrustedResult) {
			r.Classification = "result_unknown"
			r.Call.Counts.Unknown = 1
			r.Call.Counts.ForwardAttempted = 2
		}, true},
		{"result-unknown-then-connect-failure", func(r *e2runner.TrustedResult) {
			r.Classification = "result_unknown"
			r.Call.Counts.Unknown = 1
			r.Call.Counts.Transient = 1
			r.Call.Reason = e2proxy.TransportFailed
		}, true},
		{"result-unknown-audit-incomplete", func(r *e2runner.TrustedResult) {
			r.Classification = "result_unknown"
			r.Call.Counts.Unknown = 1
			r.Execution.AuditComplete = false
		}, false},
		{"result-unknown-audit-failed", func(r *e2runner.TrustedResult) {
			r.Classification = "result_unknown"
			r.Call.Counts.Unknown = 1
			r.Call.Reason = e2proxy.AuditFailed
		}, false},
		{"result-unknown-revoked", func(r *e2runner.TrustedResult) {
			r.Classification = "result_unknown"
			r.Call.Counts.Unknown = 1
			r.Call.Reason = e2proxy.Revoked
		}, false},
		{"connect-failure", func(r *e2runner.TrustedResult) {
			r.Classification = "transient_failure"
			r.Call.Counts.Transient = 1
			r.Call.Counts.ForwardAttempted = 1
			r.Call.Reason = e2proxy.TransportFailed
			r.ExitCode = 7
		}, true},
		{"connect-failure-then-complete", func(r *e2runner.TrustedResult) {
			r.Classification = "transient_failure"
			r.Call.Counts.Transient = 1
		}, true},
		// 송신 실패 수가 없는 transient_failure는 프록시 장애·명령 시간 초과라 원인을 나눌 수 없다.
		{"transient-without-connect-failure", func(r *e2runner.TrustedResult) {
			r.Classification = "transient_failure"
			r.Call.Reason = e2proxy.TransportFailed
		}, false},
		{"transient-with-unknown", func(r *e2runner.TrustedResult) {
			r.Classification = "transient_failure"
			r.Call.Counts.Transient = 1
			r.Call.Counts.Unknown = 1
		}, false},
		{"transient-after-same-intent-unknown", func(r *e2runner.TrustedResult) {
			r.Classification = "transient_failure"
			r.Call.Counts.Transient = 1
			r.Execution.IntentCounts.Unknown = 1
		}, false},
		{"transient-expired", func(r *e2runner.TrustedResult) {
			r.Classification = "transient_failure"
			r.Call.Counts.Transient = 1
			r.Call.Reason = e2proxy.Expired
		}, false},
		{"transient-audit-failed", func(r *e2runner.TrustedResult) {
			r.Classification = "transient_failure"
			r.Call.Counts.Transient = 1
			r.Call.Reason = e2proxy.AuditFailed
		}, false},
		{"audit-incomplete", func(r *e2runner.TrustedResult) { r.Execution.AuditComplete = false }, false},
		{"audit-failed", func(r *e2runner.TrustedResult) { r.Call.Reason = e2proxy.AuditFailed }, false},
		{"unknown-class", func(r *e2runner.TrustedResult) { r.Classification = "future-success" }, false},
		{"unknown-result", func(r *e2runner.TrustedResult) { r.Classification = "result_unknown" }, false},
		{"terminated", func(r *e2runner.TrustedResult) { r.Classification = "terminated" }, false},
		{"unsupported-without-receipt", func(r *e2runner.TrustedResult) { r.Classification = "unsupported" }, false},
		{"policy-without-receipt", func(r *e2runner.TrustedResult) { r.Classification = "policy_not_executed" }, false},
		{"hidden-denial", func(r *e2runner.TrustedResult) { r.Call.Counts.Denied = 1 }, false},
		{"false-not-sent", func(r *e2runner.TrustedResult) {
			r.Classification = "policy_not_executed"
			r.Call.Counts.Denied = 1
			r.Call.Counts.ForwardAttempted = 1
		}, false},
		{"false-partial", func(r *e2runner.TrustedResult) { r.Classification = "partial_policy_denial"; r.Call.Counts.Denied = 1 }, false},
		{"false-completed-exit", func(r *e2runner.TrustedResult) { r.ExitCode = 1 }, false},
		{"false-failed-exit", func(r *e2runner.TrustedResult) { r.Classification = "tool_failure" }, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r := base
			tt.change(&r)
			if got := firstDiagnosisClosedToolOutcome(r); got != tt.want {
				t.Fatalf("closed outcome = %v, want %v", got, tt.want)
			}
		})
	}
}
