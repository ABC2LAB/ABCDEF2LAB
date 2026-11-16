package server

import (
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"testing"
)

func TestFirstDiagnosisClosedToolOutcome(t *testing.T) {
	// 마감 대조가 보는 닫힘 표시(최종·드레인·감사 완료)는 채워 둔다. 그래야 기존 분류 검사가 받지 않는 결과가 시도 단위
	// 검사(firstDiagnosisStoppedToolOutcome)로 넘어가도 시도 기록 없이는 받지 않는지까지 본다.
	base := e2runner.TrustedResult{Classification: "completed", ProcessBashExited: true, Final: true, ProcessDrained: true, ProxyDrained: true,
		Execution: e2proxy.ExecutionView{AuditComplete: true, Reason: e2proxy.Complete},
		Call:      e2proxy.CallResult{CallRef: stoppedFixtureCall, IntentID: stoppedFixtureIntent, Final: true, AuditComplete: true, Reason: e2proxy.Complete}}
	// 시도 기록이 있는 경우만 이름으로 준다. 나머지는 시도 기록 없이 본다.
	attemptsFor := map[string][]e2proxy.Receipt{
		"denial-without-bash-exit": {stoppedDenied(1, e2proxy.PolicyDenied)},
		"policy-without-bash-exit": {stoppedDenied(1, e2proxy.PolicyDenied)},
	}
	cases := []struct {
		name   string
		change func(*e2runner.TrustedResult)
		want   bool
	}{
		{"completed", func(r *e2runner.TrustedResult) {}, true},
		{"audited-tool-failure", func(r *e2runner.TrustedResult) { r.Classification = "tool_failure"; r.ExitCode = 1 }, true},
		// 실행기는 종료 코드를 거부보다 먼저 본다. 거부·미지원이 섞인 명령도 종료 코드가 0이 아니면 tool_failure이고, 그
		// 거부 수가 E② 근거라 마감한다. 마지막 요청 사유가 거부·미지원이면 그 수가 있어야 한다.
		{"tool-failure-after-denial", func(r *e2runner.TrustedResult) {
			r.Classification, r.ExitCode = "tool_failure", 22
			r.Call.Counts.Denied, r.Call.Counts.ForwardAttempted = 1, 1
			r.Call.Reason = e2proxy.PolicyDenied
		}, true},
		{"tool-failure-denial-not-sent", func(r *e2runner.TrustedResult) {
			r.Classification, r.ExitCode = "tool_failure", 1
			r.Call.Counts.Denied = 1
			r.Call.Reason = e2proxy.PolicyDenied
		}, true},
		{"tool-failure-answer-after-denial", func(r *e2runner.TrustedResult) {
			r.Classification, r.ExitCode = "tool_failure", 1
			r.Call.Counts.Denied, r.Call.Counts.ForwardAttempted = 1, 1
		}, true},
		{"tool-failure-after-unsupported", func(r *e2runner.TrustedResult) {
			r.Classification, r.ExitCode = "tool_failure", 1
			r.Call.Counts.Unsupported = 1
			r.Call.Reason = e2proxy.Unsupported
		}, true},
		{"tool-failure-after-budget-exhausted", func(r *e2runner.TrustedResult) {
			r.Classification, r.ExitCode = "tool_failure", 1
			r.Call.Counts.Denied, r.Call.Counts.ForwardAttempted = 1, 1
			r.Call.Reason = e2proxy.BudgetExhausted
		}, true},
		{"tool-failure-denial-reason-without-count", func(r *e2runner.TrustedResult) {
			r.Classification, r.ExitCode = "tool_failure", 1
			r.Call.Reason = e2proxy.PolicyDenied
		}, false},
		{"tool-failure-unsupported-reason-without-count", func(r *e2runner.TrustedResult) {
			r.Classification, r.ExitCode = "tool_failure", 1
			r.Call.Counts.Denied = 1
			r.Call.Reason = e2proxy.Unsupported
		}, false},
		{"tool-failure-denial-zero-exit", func(r *e2runner.TrustedResult) {
			r.Classification = "tool_failure"
			r.Call.Counts.Denied = 1
			r.Call.Reason = e2proxy.PolicyDenied
		}, false},
		{"tool-failure-expired-denial", func(r *e2runner.TrustedResult) {
			r.Classification, r.ExitCode = "tool_failure", 1
			r.Call.Counts.Denied = 1
			r.Call.Reason = e2proxy.Expired
		}, false},
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
		// 송신 실패 수도 종료 원인도 없는 transient_failure는 원인을 알 수 없다.
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
		// 명령 시간 상한에 닿아 호스트가 멈춘 명령(계약 열린 항목 2): 결과 불명 송신이 없으면 그 intent의 미완료다.
		// 프록시 장애는 원인이 따로 남아도 집행 장애라 마감 실패다.
		{"timeout-no-send", func(r *e2runner.TrustedResult) {
			r.Classification = "transient_failure"
			r.Termination = e2runner.TerminationTimeout
			r.ProcessBashExited = false
		}, true},
		{"timeout-after-answered", func(r *e2runner.TrustedResult) {
			r.Classification = "transient_failure"
			r.Termination = e2runner.TerminationTimeout
			r.ProcessBashExited = false
			r.Call.Counts.ForwardAttempted, r.Call.Counts.Responses = 1, 1
		}, true},
		{"timeout-with-unknown", func(r *e2runner.TrustedResult) {
			r.Classification = "transient_failure"
			r.Termination = e2runner.TerminationTimeout
			r.ProcessBashExited = false
			r.Call.Counts.Unknown = 1
		}, false},
		{"timeout-expired", func(r *e2runner.TrustedResult) {
			r.Classification = "transient_failure"
			r.Termination = e2runner.TerminationTimeout
			r.ProcessBashExited = false
			r.Call.Reason = e2proxy.Expired
		}, false},
		{"timeout-audit-open", func(r *e2runner.TrustedResult) {
			r.Classification = "transient_failure"
			r.Termination = e2runner.TerminationTimeout
			r.ProcessBashExited = false
			r.Execution.AuditComplete = false
		}, false},
		// 정책 거부·미지원 뒤 시간 상한: 실행기는 거부·미지원 분류를 먼저 붙인다. E② 수로 그 intent의 결과다(#578 리뷰).
		{"timeout-after-denial", func(r *e2runner.TrustedResult) {
			r.Classification = "policy_not_executed"
			r.Termination = e2runner.TerminationTimeout
			r.ProcessBashExited = false
			r.Call.Counts.Denied = 1
			r.Call.Reason = e2proxy.PolicyDenied
		}, true},
		{"timeout-after-unsupported", func(r *e2runner.TrustedResult) {
			r.Classification = "unsupported"
			r.Termination = e2runner.TerminationTimeout
			r.ProcessBashExited = false
			r.Call.Counts.Unsupported = 1
			r.Call.Reason = e2proxy.Unsupported
		}, true},
		// 시간 상한이 아닌데 bash가 끝나지 않은 거부 분류(#578에서는 받지 않음): 시도 단위로 미전송·응답 완료를 확인하므로
		// 종료 원인과 무관하게 안전하다. 거부 하나가 보내지 않은 시도로 기록돼 있으면 받는다(Planner kill_work 등).
		{"denial-without-bash-exit", func(r *e2runner.TrustedResult) {
			r.Classification = "policy_not_executed"
			r.ProcessBashExited = false
			r.Call.Counts.Attempts = 1
			r.Call.Counts.Denied = 1
			r.Call.Reason = e2proxy.PolicyDenied
		}, true},
		// 시간 상한인데 완료 분류는 지금처럼 받지 않는다.
		{"timeout-completed", func(r *e2runner.TrustedResult) {
			r.Termination = e2runner.TerminationTimeout
			r.ProcessBashExited = false
		}, false},
		{"proxy-unavailable", func(r *e2runner.TrustedResult) {
			r.Classification = "transient_failure"
			r.Termination = e2runner.TerminationProxyUnavailable
			r.ProcessBashExited = false
		}, false},
		// 호스트가 멈춘 명령(C27 kill_work): bash가 끝나기 전에 끊겼다. 결과 불명 송신이 있으면 결과 불명, 없으면 terminated.
		{"killed-mid-request", func(r *e2runner.TrustedResult) {
			r.Classification = "result_unknown"
			r.ProcessBashExited = false
			r.ExitCode = -1
			r.Call.Counts.Unknown = 1
			r.Call.Reason = e2proxy.Unknown
		}, true},
		{"killed-between-requests", func(r *e2runner.TrustedResult) {
			r.Classification = "terminated"
			r.ProcessBashExited = false
			r.ExitCode = -1
		}, true},
		{"terminated-after-budget-exhausted", func(r *e2runner.TrustedResult) {
			r.Classification = "terminated"
			r.ProcessBashExited = false
			r.Call.Counts.Denied = 1
			r.Call.Reason = e2proxy.BudgetExhausted
		}, true},
		{"terminated-after-attempt-limit", func(r *e2runner.TrustedResult) {
			r.Classification = "terminated"
			r.ProcessBashExited = false
			r.Call.Counts.Denied = 1
			r.Call.Reason = e2proxy.AttemptLimit
		}, true},
		{"terminated-but-bash-exited", func(r *e2runner.TrustedResult) { r.Classification = "terminated" }, false},
		{"terminated-audit-failed", func(r *e2runner.TrustedResult) {
			r.Classification = "terminated"
			r.ProcessBashExited = false
			r.Call.Reason = e2proxy.AuditFailed
		}, false},
		{"completed-without-bash-exit", func(r *e2runner.TrustedResult) { r.ProcessBashExited = false }, false},
		{"tool-failure-without-bash-exit", func(r *e2runner.TrustedResult) {
			r.Classification = "tool_failure"
			r.ProcessBashExited = false
			r.ExitCode = -1
		}, false},
		// #578에서는 받지 않던 조합이다. 시도 단위로 미전송·응답 완료를 확인하므로 종료 원인과 무관하게 안전하다.
		{"policy-without-bash-exit", func(r *e2runner.TrustedResult) {
			r.Classification = "policy_not_executed"
			r.Call.Counts.Attempts = 1
			r.Call.Counts.Denied = 1
			r.Call.Reason = e2proxy.PolicyDenied
			r.ProcessBashExited = false
		}, true},
		// 같은 명령이라도 시도 기록이 없으면(E② 감사와 대조할 수 없으면) 받지 않는다.
		{"policy-without-bash-exit-no-attempts", func(r *e2runner.TrustedResult) {
			r.Classification = "policy_not_executed"
			r.Call.Counts.Attempts = 1
			r.Call.Counts.Denied = 1
			r.Call.Reason = e2proxy.PolicyDenied
			r.ProcessBashExited = false
		}, false},
		// 예산 소진·송신 시도 상한은 그 요청만 보내지 않은 거부다(C01). 거부 수가 있을 때 정책 제한으로만 받는다.
		{"budget-exhausted-not-sent", func(r *e2runner.TrustedResult) {
			r.Classification = "policy_not_executed"
			r.Call.Counts.Denied = 1
			r.Call.Reason = e2proxy.BudgetExhausted
		}, true},
		{"budget-exhausted-after-send", func(r *e2runner.TrustedResult) {
			r.Classification = "partial_policy_denial"
			r.Call.Counts.Denied = 1
			r.Call.Counts.ForwardAttempted = 1
			r.Call.Reason = e2proxy.BudgetExhausted
		}, true},
		{"attempt-limit-after-send", func(r *e2runner.TrustedResult) {
			r.Classification = "partial_policy_denial"
			r.Call.Counts.Denied = 1
			r.Call.Counts.ForwardAttempted = 1
			r.Call.Reason = e2proxy.AttemptLimit
		}, true},
		{"budget-exhausted-without-denial", func(r *e2runner.TrustedResult) { r.Call.Reason = e2proxy.BudgetExhausted }, false},
		{"budget-exhausted-as-unsupported", func(r *e2runner.TrustedResult) {
			r.Classification = "unsupported"
			r.Call.Counts.Unsupported = 1
			r.Call.Reason = e2proxy.BudgetExhausted
		}, false},
		{"result-unknown-then-budget-exhausted", func(r *e2runner.TrustedResult) {
			r.Classification = "result_unknown"
			r.Call.Counts.Unknown = 1
			r.Call.Counts.Denied = 1
			r.Call.Reason = e2proxy.BudgetExhausted
		}, true},
		{"expired-denial", func(r *e2runner.TrustedResult) {
			r.Classification = "policy_not_executed"
			r.Call.Counts.Denied = 1
			r.Call.Reason = e2proxy.Expired
		}, false},
		// 보류(C16)는 대상이 답한 응답이라 완료와 같이 마감한다. 결과 불명 분류가 아니다.
		{"withheld-completed", func(r *e2runner.TrustedResult) { r.Call.Reason = e2proxy.Withheld; r.Call.Counts.Withheld = 1 }, true},
		{"withheld-tool-failure", func(r *e2runner.TrustedResult) {
			r.Classification, r.ExitCode = "tool_failure", 22
			r.Call.Reason = e2proxy.Withheld
		}, true},
		{"withheld-terminated", func(r *e2runner.TrustedResult) {
			r.Classification, r.ProcessBashExited = "terminated", false
			r.Call.Reason = e2proxy.Withheld
		}, true},
		{"withheld-after-unknown", func(r *e2runner.TrustedResult) {
			r.Classification = "result_unknown"
			r.Call.Counts.Unknown = 1
			r.Call.Reason = e2proxy.Withheld
		}, true},
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
			// 이 표의 시도 기록에는 보낸 뒤 응답을 끝까지 받지 못한 시도가 없다. 받아도 시도 단위 결과 불명은 아니다.
			if got, unknown := firstDiagnosisToolOutcome(r, attemptsFor[tt.name]); got != tt.want || unknown {
				t.Fatalf("closed outcome = %v (attempt unknown %v), want %v", got, unknown, tt.want)
			}
		})
	}
}

// 시도 단위 마감 시험의 호출·intent다.
const (
	stoppedFixtureCall   = "call_stopped_fixture"
	stoppedFixtureIntent = int64(7)
)

// stoppedAttempt는 E② 결과 단계 영수증 하나다(e2proxy Call.finish가 남기는 모양). not_sent는 forward_attempted의 반대다.
func stoppedAttempt(seq uint64, decision string, reason e2proxy.Reason, forward, written, received, complete bool) e2proxy.Receipt {
	return e2proxy.Receipt{IntentID: stoppedFixtureIntent, CallRef: stoppedFixtureCall, AttemptID: "attempt-" + string(rune('a'+seq)), Sequence: seq,
		Decision: decision, Reason: reason, ForwardAttempted: forward, RequestWritten: written, ResponseReceived: received, ResponseComplete: complete,
		NotSent: !forward, AuditComplete: true}
}

// 보내지 않은 거부(정책 거부·자리 대기 취소 closed·호출 상한·예산 소진).
func stoppedDenied(seq uint64, reason e2proxy.Reason) e2proxy.Receipt {
	return stoppedAttempt(seq, "denied", reason, false, false, false, false)
}

// 대상이 끝까지 답한 시도.
func stoppedAnswered(seq uint64) e2proxy.Receipt {
	return stoppedAttempt(seq, "allowed", e2proxy.Complete, true, true, true, true)
}

// 연결을 시도했지만 거부·실패한 송신(transport_failed, forward_attempted).
func stoppedConnectFailed(seq uint64) e2proxy.Receipt {
	return stoppedAttempt(seq, "allowed", e2proxy.TransportFailed, true, false, false, false)
}

// 응답을 받는 도중 끊긴 시도(결과 불명).
func stoppedMidResponse(seq uint64) e2proxy.Receipt {
	return stoppedAttempt(seq, "allowed", e2proxy.Unknown, true, true, true, false)
}

// 허용됐지만 송신 전에 닫힌 시도(감사 intent 기록 뒤 취소, 연결 시도 전 검사 거부).
func stoppedAllowedNotSent(seq uint64, reason e2proxy.Reason) e2proxy.Receipt {
	return stoppedAttempt(seq, "allowed", reason, false, false, false, false)
}

// stoppedResult는 시도 기록으로 호출 수를 채운 감사 완료·드레인 완료 신뢰 결과다. 분류·종료 상태는 시험이 정한다.
func stoppedResult(class string, bashExited bool, exitCode int, termination string, attempts ...e2proxy.Receipt) e2runner.TrustedResult {
	var c e2proxy.Counts
	reason := e2proxy.Complete
	for _, a := range attempts {
		c.Attempts++
		switch a.Decision {
		case "allowed":
			c.Allowed++
		case "denied":
			c.Denied++
		case "unsupported":
			c.Unsupported++
		}
		if a.ForwardAttempted {
			c.ForwardAttempted++
		}
		if a.ResponseReceived {
			c.Responses++
		}
		switch a.Reason {
		case e2proxy.Unknown:
			c.Unknown++
		case e2proxy.TransportFailed:
			c.Transient++
		case e2proxy.Withheld:
			c.Withheld++
		}
		reason = a.Reason
	}
	r := e2runner.TrustedResult{Classification: class, ProcessBashExited: bashExited, ExitCode: exitCode, Termination: termination, Final: true, ProcessDrained: true, ProxyDrained: true,
		Execution: e2proxy.ExecutionView{AuditComplete: true, Reason: e2proxy.Complete},
		Call:      e2proxy.CallResult{CallRef: stoppedFixtureCall, IntentID: stoppedFixtureIntent, Final: true, AuditComplete: true, Reason: reason, Counts: c}}
	if c.Unknown > 0 {
		r.Execution.IntentCounts.Unknown = c.Unknown
	}
	return r
}

// 멈춘 명령(bash 미종료)과 닫힘·호출 상한 사유는 시도 단위로만 받는다. 모든 시도가 보내지 않음·응답 완료면 받고(거부가
// 있으면 정책 제한 근거, 없으면 미완료), 보낸 뒤 응답을 끝까지 받지 못한 시도가 하나라도 있으면 그 intent는 결과
// 불명이어야 한다. 감사·집행 장애·대조 불일치는 지금처럼 받지 않는다.
func TestFirstDiagnosisStoppedToolOutcome(t *testing.T) {
	type verdict struct{ accepted, unknown bool }
	accept, unknown, reject := verdict{true, false}, verdict{true, true}, verdict{false, false}
	cases := []struct {
		name     string
		result   e2runner.TrustedResult
		attempts []e2proxy.Receipt
		change   func(*e2runner.TrustedResult, []e2proxy.Receipt) []e2proxy.Receipt
		want     verdict
	}{
		// 보내지 않음만: 받는다.
		{name: "killed-after-denial", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied)}, result: stoppedResult("policy_not_executed", false, -1, ""), want: accept},
		{name: "killed-after-unsupported", attempts: []e2proxy.Receipt{stoppedAttempt(1, "unsupported", e2proxy.Unsupported, false, false, false, false)}, result: stoppedResult("unsupported", false, -1, ""), want: accept},
		{name: "forward-not-attempted-closed", attempts: []e2proxy.Receipt{stoppedAllowedNotSent(1, e2proxy.Closed)}, result: stoppedResult(e2runner.ClassificationForwardNotAttempted, false, -1, ""), want: accept},
		{name: "forward-not-attempted-dial-refused-before-connect", attempts: []e2proxy.Receipt{stoppedAnswered(1), stoppedAllowedNotSent(2, e2proxy.TransportFailed)}, result: stoppedResult(e2runner.ClassificationForwardNotAttempted, true, 0, ""), want: accept},
		{name: "slot-wait-closed-bash-exited", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.Closed)}, result: stoppedResult("policy_not_executed", true, 0, ""), want: accept},
		{name: "terminated-closed", attempts: []e2proxy.Receipt{stoppedAnswered(1), stoppedDenied(2, e2proxy.Closed)}, result: stoppedResult("terminated", false, -1, ""), want: accept},
		{name: "timeout-closed", attempts: []e2proxy.Receipt{stoppedAnswered(1), stoppedDenied(2, e2proxy.Closed)}, result: stoppedResult("transient_failure", false, -1, e2runner.TerminationTimeout), want: accept},
		{name: "denial-limit-after-answer", attempts: []e2proxy.Receipt{stoppedAnswered(1), stoppedDenied(2, e2proxy.PolicyDenied), stoppedDenied(3, e2proxy.DenialLimit)}, result: stoppedResult("partial_policy_denial", true, 0, ""), want: accept},
		{name: "transient-limit-not-sent", attempts: []e2proxy.Receipt{stoppedAllowedNotSent(1, e2proxy.TransportFailed), stoppedDenied(2, e2proxy.TransientLimit)}, result: stoppedResult(e2runner.ClassificationForwardNotAttempted, true, 7, ""), want: accept},
		{name: "withheld-then-closed", attempts: []e2proxy.Receipt{stoppedAttempt(1, "allowed", e2proxy.Withheld, true, true, true, false), stoppedDenied(2, e2proxy.Closed)}, result: stoppedResult("terminated", false, -1, ""), want: accept},
		// 같은 조합에 보낸 뒤 응답을 끝까지 받지 못한 시도가 하나 섞이면 정책 제한·미완료가 아니라 결과 불명이다.
		{name: "killed-after-denial-and-connect-failure", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied), stoppedConnectFailed(2)}, result: stoppedResult("transient_failure", false, -1, ""), want: unknown},
		{name: "killed-after-connect-failure", attempts: []e2proxy.Receipt{stoppedConnectFailed(1)}, result: stoppedResult("transient_failure", false, -1, ""), want: unknown},
		{name: "forward-not-attempted-after-connect-failure", attempts: []e2proxy.Receipt{stoppedConnectFailed(1), stoppedAllowedNotSent(2, e2proxy.Closed)}, result: stoppedResult(e2runner.ClassificationForwardNotAttempted, false, -1, ""), want: unknown},
		{name: "slot-wait-closed-after-connect-failure", attempts: []e2proxy.Receipt{stoppedConnectFailed(1), stoppedDenied(2, e2proxy.Closed)}, result: stoppedResult("transient_failure", true, 0, ""), want: unknown},
		{name: "mid-response-then-closed", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied), stoppedMidResponse(2), stoppedDenied(3, e2proxy.Closed)}, result: stoppedResult("result_unknown", false, -1, ""), want: unknown},
		{name: "unknown-limit", attempts: []e2proxy.Receipt{stoppedMidResponse(1), stoppedDenied(2, e2proxy.UnknownLimit)}, result: stoppedResult("result_unknown", true, 0, ""), want: unknown},
		// E② 코드에서는 생기지 않지만, 보낸 시도에 닫힘·호출 상한 사유가 붙어도 보내지 않은 것으로 보지 않는다.
		{name: "forwarded-closed", attempts: []e2proxy.Receipt{stoppedAttempt(1, "allowed", e2proxy.Closed, true, true, false, false)}, result: stoppedResult("terminated", false, -1, ""), want: unknown},
		{name: "forwarded-denial-limit", attempts: []e2proxy.Receipt{stoppedAnswered(1), stoppedAttempt(2, "allowed", e2proxy.DenialLimit, true, false, false, false)}, result: stoppedResult("terminated", false, -1, ""), want: unknown},
		// 집행 장애·감사 미완료는 지금처럼 받지 않는다.
		{name: "execution-audit-incomplete", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied)}, result: stoppedResult("policy_not_executed", false, -1, ""),
			change: func(r *e2runner.TrustedResult, a []e2proxy.Receipt) []e2proxy.Receipt {
				r.Execution.AuditComplete = false
				return a
			}, want: reject},
		{name: "call-audit-incomplete", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied)}, result: stoppedResult("policy_not_executed", false, -1, ""),
			change: func(r *e2runner.TrustedResult, a []e2proxy.Receipt) []e2proxy.Receipt {
				r.Call.AuditComplete = false
				return a
			}, want: reject},
		{name: "not-drained", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied)}, result: stoppedResult("policy_not_executed", false, -1, ""),
			change: func(r *e2runner.TrustedResult, a []e2proxy.Receipt) []e2proxy.Receipt {
				r.ProxyDrained = false
				return a
			}, want: reject},
		{name: "execution-revoked", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied)}, result: stoppedResult("policy_not_executed", false, -1, ""),
			change: func(r *e2runner.TrustedResult, a []e2proxy.Receipt) []e2proxy.Receipt {
				r.Execution.Reason = e2proxy.Revoked
				return a
			}, want: reject},
		{name: "execution-expired", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied)}, result: stoppedResult("policy_not_executed", false, -1, ""),
			change: func(r *e2runner.TrustedResult, a []e2proxy.Receipt) []e2proxy.Receipt {
				r.Execution.Reason = e2proxy.Expired
				return a
			}, want: reject},
		{name: "execution-audit-failed", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied)}, result: stoppedResult("policy_not_executed", false, -1, ""),
			change: func(r *e2runner.TrustedResult, a []e2proxy.Receipt) []e2proxy.Receipt {
				r.Execution.Reason = e2proxy.AuditFailed
				return a
			}, want: reject},
		{name: "execution-invalid", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied)}, result: stoppedResult("policy_not_executed", false, -1, ""),
			change: func(r *e2runner.TrustedResult, a []e2proxy.Receipt) []e2proxy.Receipt {
				r.Execution.Reason = e2proxy.Invalid
				return a
			}, want: reject},
		{name: "proxy-unavailable", attempts: []e2proxy.Receipt{stoppedAnswered(1)}, result: stoppedResult("transient_failure", false, -1, e2runner.TerminationProxyUnavailable), want: reject},
		{name: "isolation-unavailable", attempts: nil, result: stoppedResult("isolation_unavailable", false, -1, ""), want: reject},
		{name: "attempt-revoked", attempts: []e2proxy.Receipt{stoppedAllowedNotSent(1, e2proxy.Revoked)}, result: stoppedResult(e2runner.ClassificationForwardNotAttempted, false, -1, ""), want: reject},
		{name: "attempt-expired", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.Expired)}, result: stoppedResult("policy_not_executed", false, -1, ""), want: reject},
		// 마지막 사유가 닫힘이어도 앞선 시도가 권한 철회로 끝났으면 받지 않는다(마지막 사유만 보지 않는다).
		{name: "earlier-attempt-revoked", attempts: []e2proxy.Receipt{stoppedAllowedNotSent(1, e2proxy.Revoked), stoppedDenied(2, e2proxy.Closed)}, result: stoppedResult(e2runner.ClassificationForwardNotAttempted, false, -1, ""), want: reject},
		// 대조 불일치는 받지 않는다.
		{name: "same-intent-unknown-before", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied)}, result: stoppedResult("policy_not_executed", false, -1, ""),
			change: func(r *e2runner.TrustedResult, a []e2proxy.Receipt) []e2proxy.Receipt {
				r.Execution.IntentCounts.Unknown = 1
				return a
			}, want: reject},
		{name: "classification-mismatch", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied)}, result: stoppedResult("unsupported", false, -1, ""), want: reject},
		{name: "count-mismatch", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied)}, result: stoppedResult("policy_not_executed", false, -1, ""),
			change: func(r *e2runner.TrustedResult, a []e2proxy.Receipt) []e2proxy.Receipt {
				r.Call.Counts.Denied++
				return a
			}, want: reject},
		{name: "missing-attempt", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied), stoppedConnectFailed(2)}, result: stoppedResult("transient_failure", false, -1, ""),
			change: func(_ *e2runner.TrustedResult, a []e2proxy.Receipt) []e2proxy.Receipt { return a[:1] }, want: reject},
		{name: "foreign-call-attempt", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied)}, result: stoppedResult("policy_not_executed", false, -1, ""),
			change: func(_ *e2runner.TrustedResult, a []e2proxy.Receipt) []e2proxy.Receipt {
				a[0].CallRef = "call_other"
				return a
			}, want: reject},
		{name: "foreign-intent-attempt", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied)}, result: stoppedResult("policy_not_executed", false, -1, ""),
			change: func(_ *e2runner.TrustedResult, a []e2proxy.Receipt) []e2proxy.Receipt { a[0].IntentID = 8; return a }, want: reject},
		{name: "attempt-audit-incomplete", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied)}, result: stoppedResult("policy_not_executed", false, -1, ""),
			change: func(_ *e2runner.TrustedResult, a []e2proxy.Receipt) []e2proxy.Receipt {
				a[0].AuditComplete = false
				return a
			}, want: reject},
		{name: "not-sent-but-written", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied)}, result: stoppedResult("policy_not_executed", false, -1, ""),
			change: func(_ *e2runner.TrustedResult, a []e2proxy.Receipt) []e2proxy.Receipt {
				a[0].RequestWritten = true
				return a
			}, want: reject},
		{name: "not-sent-flag-contradicts-forward", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied)}, result: stoppedResult("policy_not_executed", false, -1, ""),
			change: func(_ *e2runner.TrustedResult, a []e2proxy.Receipt) []e2proxy.Receipt { a[0].NotSent = false; return a }, want: reject},
		{name: "complete-without-response", attempts: []e2proxy.Receipt{stoppedAttempt(1, "allowed", e2proxy.Complete, true, true, true, false), stoppedDenied(2, e2proxy.Closed)}, result: stoppedResult("terminated", false, -1, ""), want: reject},
		{name: "complete-without-forward", attempts: []e2proxy.Receipt{stoppedAttempt(1, "allowed", e2proxy.Complete, false, false, false, false)}, result: stoppedResult(e2runner.ClassificationForwardNotAttempted, false, -1, ""), want: reject},
		{name: "last-reason-not-recorded", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied)}, result: stoppedResult("policy_not_executed", false, -1, ""),
			change: func(r *e2runner.TrustedResult, a []e2proxy.Receipt) []e2proxy.Receipt {
				r.Call.Reason = e2proxy.Closed
				return a
			}, want: reject},
		{name: "attempt-id-missing", attempts: []e2proxy.Receipt{stoppedDenied(1, e2proxy.PolicyDenied)}, result: stoppedResult("policy_not_executed", false, -1, ""),
			change: func(_ *e2runner.TrustedResult, a []e2proxy.Receipt) []e2proxy.Receipt { a[0].AttemptID = ""; return a }, want: reject},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			attempts := append([]e2proxy.Receipt(nil), tt.attempts...)
			// 신뢰 결과의 호출 수는 고치기 전 시도 기록으로 채운다. 시험은 그 뒤 한쪽만 바꿔 대조 불일치를 만든다.
			r := stoppedResult(tt.result.Classification, tt.result.ProcessBashExited, tt.result.ExitCode, tt.result.Termination, attempts...)
			if tt.change != nil {
				attempts = tt.change(&r, attempts)
			}
			accepted, attemptUnknown := firstDiagnosisToolOutcome(r, attempts)
			if (verdict{accepted, attemptUnknown}) != tt.want {
				t.Fatalf("accepted=%v unknown=%v, want %+v", accepted, attemptUnknown, tt.want)
			}
			// 결과 불명 송신 수가 없는 조합은 기존 분류 검사가 받지 않던 것이다(이 판정은 새 검사에서 나온다).
			if tt.want.accepted && r.Call.Counts.Unknown == 0 && firstDiagnosisClosedToolOutcome(r) {
				t.Fatal("기존 분류 검사가 이미 받는 조합이라 시도 단위 검사를 시험하지 않음")
			}
		})
	}
}
