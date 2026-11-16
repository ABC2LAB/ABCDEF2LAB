package server

import (
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

// 이 파일은 멈춘 명령(Planner kill_work, 목표 달성 뒤 실행 취소, 시간 상한 정리, 호출 시간 상한 call_stopped 등)과
// 마지막 요청 사유가 닫힘(closed)·호출 상한(denial/transient/unknown_limit)인 명령의 마감 판정이다. 기존 분류 검사
// (firstDiagnosisClosedToolOutcome)는 이런 명령을 받지 않아 실행 전체가 closure_tool_outcome_invalid로 실패했다.
// 집계 수로는 "대상에 전달됐는데 응답을 끝까지 받지 못한 시도"를 다른 시도와 나눌 수 없으므로 E② 감사 기록의 시도
// 하나하나(결과 단계 영수증)를 본다. 모든 시도가 대상에 전달되지 않았거나(보내지 않음, 연결 전 송신 실패) 응답을 끝까지
// 받았으면 명령이 왜 멈췄는지와 관계없이 대상 쪽에 남은 모르는 결과가 없다. 그런 시도가 하나라도 있으면 그 intent는
// 결과 불명이다(C01·C02·C08, docs/contracts/run-outcome-states.md).

// firstDiagnosisCallAttempts는 실행 감사 기록에서 호출별 결과 단계 영수증을 모은다. 시도 하나는 결과 단계 기록을
// 정확히 하나 남긴다(e2proxy Call.finish). 송신 직전 단계(intent)는 같은 시도의 중간 기록이라 세지 않는다.
func firstDiagnosisCallAttempts(execution *e2proxy.Execution) map[string][]e2proxy.Receipt {
	out := map[string][]e2proxy.Receipt{}
	if execution == nil {
		return out
	}
	var after uint64
	for {
		page := execution.Events(after, 0)
		if len(page) == 0 {
			return out
		}
		for _, event := range page {
			after = event.Index
			if event.Stage == "result" && event.CallRef != "" {
				out[event.CallRef] = append(out[event.CallRef], event.Receipt)
			}
		}
	}
}

// firstDiagnosisAttemptReason은 마감할 수 있는 시도·호출 사유다. 감사 실패·만료·철회·잘못된 실행은 집행 장애라
// 지금처럼 마감 실패다.
func firstDiagnosisAttemptReason(reason e2proxy.Reason) bool {
	switch reason {
	case e2proxy.Complete, e2proxy.Withheld, e2proxy.PolicyDenied, e2proxy.Unsupported, e2proxy.TransportFailed, e2proxy.Unknown,
		e2proxy.BudgetExhausted, e2proxy.AttemptLimit, e2proxy.Closed, e2proxy.DenialLimit, e2proxy.TransientLimit, e2proxy.UnknownLimit:
		return true
	}
	return false
}

// firstDiagnosisRunnerClassification은 감사가 닫힌 최종 호출의 분류를 실행기(e2runner classification)와 같은 순서로
// 다시 계산한다. 신뢰 결과의 분류가 E② 수·종료 상태와 맞는지 대조하는 데 쓴다.
func firstDiagnosisRunnerClassification(result e2runner.TrustedResult) string {
	c := result.Call.Counts
	switch {
	case c.Unknown > 0:
		return "result_unknown"
	case c.Allowed > c.ForwardAttempted:
		return e2runner.ClassificationForwardNotAttempted
	case c.ForwardAttempted == 0 && c.Denied > 0:
		return "policy_not_executed"
	case c.ForwardAttempted == 0 && c.Unsupported > 0:
		return "unsupported"
	case c.Transient > 0, result.Termination == e2runner.TerminationProxyUnavailable, result.Termination == e2runner.TerminationTimeout:
		return "transient_failure"
	case !result.ProcessBashExited:
		return "terminated"
	case result.ExitCode != 0:
		return "tool_failure"
	case c.Denied > 0:
		return "partial_policy_denial"
	case c.Unsupported > 0:
		return "partial_unsupported"
	}
	return "completed"
}

// firstDiagnosisAttemptUnknown은 시도 하나가 대상에 전달됐는데 응답을 끝까지 받지 못했는지 본다(감독자 마감 판정과
// E② 런타임의 intent 마감이 같이 쓰는 한 기준이다). ok=false는 영수증이 E② 송신 규칙과 맞지 않는다는 뜻이다(마감 실패).
// 기준은 E②의 송신 분류와 같은 의미다(C02). E②는 연결(TLS 포함)이 맺어진 뒤의 송신 오류만 결과 불명(unknown)으로,
// 그 전의 TCP 연결·TLS 검증 실패는 전달하지 않은 송신 실패(transport_failed)로 기록한다. 연결을 시도했으면
// forward_attempted가 켜져 있어도 마찬가지다(e2proxy Call.do 송신 오류 분류, 도구 결과 헤더 proxyOutcomeOf도
// forward이면서 transport_failed가 아닐 때만 outcome_unknown). 그래서 안전한 시도는 셋이다: 보내지 않음
// (forward_attempted=false), 연결 전 송신 실패(transport_failed), 응답 완료(complete·withheld). 그 밖의 전달된 시도
// (연결 뒤 응답 미완료 unknown, 전달된 뒤 닫힘·호출 상한 사유 등)는 결과 불명이다.
func firstDiagnosisAttemptUnknown(attempt e2proxy.Receipt) (unknown, ok bool) {
	// 보내지 않은 시도는 쓰거나 받은 것이 없어야 하고, 결과 영수증의 not_sent는 forward_attempted의 반대다.
	if attempt.NotSent == attempt.ForwardAttempted || !attempt.ForwardAttempted && (attempt.RequestWritten || attempt.ResponseReceived || attempt.ResponseComplete) {
		return false, false
	}
	switch attempt.Reason {
	case e2proxy.Complete:
		// 완료는 보내고 응답을 끝까지 읽은 시도에만 붙는다.
		return false, attempt.ForwardAttempted && attempt.ResponseComplete
	case e2proxy.Withheld:
		// 보류는 대상 응답을 받았지만 표현만 주지 않은 것이다(C16). 대상이 처리한 것은 안다.
		return false, attempt.ForwardAttempted && attempt.ResponseReceived
	case e2proxy.TransportFailed:
		// 연결 전 송신 실패다. 연결이 없었으니 요청을 쓰거나 응답을 받았을 수 없다.
		return false, !attempt.RequestWritten && !attempt.ResponseReceived && !attempt.ResponseComplete
	case e2proxy.Unknown:
		return true, true
	}
	return attempt.ForwardAttempted, true
}

// firstDiagnosisStoppedToolOutcome은 기존 분류 검사가 받지 않은 호출 결과를 시도 단위 기록으로 다시 본다.
// accepted면 마감할 수 있고, unknown이면 그 호출의 intent는 결과 불명이어야 한다(정책 제한·미완료로 마감하지 않는다).
// 종료 원인(kill·취소·시간 상한·호출 시간 상한)은 보지 않는다. 시도 단위로 미전송(연결 전 송신 실패 포함)·응답 완료를
// 확인하므로 원인과 무관하게 안전하다. 감사·집행 장애와 대조 불일치는 지금처럼 받지 않는다.
func firstDiagnosisStoppedToolOutcome(result e2runner.TrustedResult, attempts []e2proxy.Receipt) (accepted, unknown bool) {
	call := result.Call
	if !result.Final || !result.ProcessDrained || !result.ProxyDrained || !call.Final || !call.AuditComplete || !result.Execution.AuditComplete {
		return false, false
	}
	switch result.Execution.Reason {
	case e2proxy.Complete, e2proxy.Closed, e2proxy.PolicyDenied:
		// 실행이 살아 있거나 운영 중지·마감으로 닫혔다. 정책 갱신 기록 중(PolicyDenied)은 새 송신만 막는 상태다.
	default:
		return false, false
	}
	// 격리 실패는 init이 멈춘 것인지 helper 장애인지 아직 나눌 수 없고, 프록시 장애는 집행 장애다.
	if result.Classification == "isolation_unavailable" || result.Termination == e2runner.TerminationProxyUnavailable || !firstDiagnosisAttemptReason(call.Reason) {
		return false, false
	}
	// 이 intent의 앞선 호출에 결과 불명이 있었다면 그 intent는 더 보내지 않으므로 이 호출도 결과 불명이어야 한다(C01).
	if result.Execution.IntentCounts.Unknown != 0 && call.Counts.Unknown == 0 {
		return false, false
	}
	if firstDiagnosisRunnerClassification(result) != result.Classification {
		return false, false
	}
	// 시도 기록에서 호출 수를 다시 세어 신뢰 결과의 수와 정확히 같아야 한다. 마지막 사유도 그 호출의 시도 중 하나여야 한다
	// (시도가 없으면 처음 값 complete).
	var counts e2proxy.Counts
	reasonSeen := len(attempts) == 0 && call.Reason == e2proxy.Complete
	for _, attempt := range attempts {
		if attempt.CallRef != call.CallRef || attempt.IntentID != call.IntentID || attempt.AttemptID == "" || !attempt.AuditComplete || !firstDiagnosisAttemptReason(attempt.Reason) {
			return false, false
		}
		attemptUnknown, ok := firstDiagnosisAttemptUnknown(attempt)
		if !ok {
			return false, false
		}
		unknown = unknown || attemptUnknown
		reasonSeen = reasonSeen || attempt.Reason == call.Reason
		counts.Attempts++
		switch attempt.Decision {
		case "allowed":
			counts.Allowed++
		case "denied":
			counts.Denied++
		case "unsupported":
			counts.Unsupported++
		}
		if attempt.ForwardAttempted {
			counts.ForwardAttempted++
		}
		if attempt.ResponseReceived {
			counts.Responses++
		}
		switch attempt.Reason {
		case e2proxy.Unknown:
			counts.Unknown++
		case e2proxy.TransportFailed:
			counts.Transient++
		case e2proxy.Withheld:
			counts.Withheld++
		}
	}
	if counts != call.Counts || !reasonSeen {
		return false, false
	}
	return true, unknown
}

// firstDiagnosisToolOutcome은 호출 결과 하나의 마감 판정이다. 기존 분류 검사가 받는 결과는 지금 그대로 둔다(결과 불명
// 송신 수가 있으면 결과 불명, 그 밖에는 intent 그래프 상태대로). 받지 않는 결과만 시도 단위 검사로 다시 본다.
func firstDiagnosisToolOutcome(result e2runner.TrustedResult, attempts []e2proxy.Receipt) (accepted, attemptUnknown bool) {
	if firstDiagnosisClosedToolOutcome(result) {
		return true, false
	}
	return firstDiagnosisStoppedToolOutcome(result, attempts)
}
