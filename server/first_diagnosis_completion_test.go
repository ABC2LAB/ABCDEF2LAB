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
		{"execution-unknown", func(r *e2runner.TrustedResult) { r.Execution.Counts.Unknown = 1 }, false},
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
