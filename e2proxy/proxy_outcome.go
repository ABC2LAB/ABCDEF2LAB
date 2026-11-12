package e2proxy

import (
	"errors"
	"fmt"
)

// E2가 대상 응답을 돌려주지 않을 때 도구에 남기는 결과 헤더다. 이름이 예약 접두사(x-abc2lab-e2-)라서
// 요청에서는 대상에 가지 않고, 대상 응답의 같은 이름 두 개는 forward가 지운다. 그래서 이 헤더는 E2만 붙인다.
const (
	OutcomeHeader = "X-ABC2LAB-E2-Outcome"
	ReasonHeader  = "X-ABC2LAB-E2-Reason"
)

// ProxyOutcomeClass는 대상이 요청을 받았을 수 있는지다. 모델이 거부된 요청과 결과를 모르는 요청을 구분해,
// 보내지 않은 요청은 고쳐서 다시 시도하고 결과 불명인 쓰기는 상태를 확인하기 전에 되풀이하지 않게 한다.
type ProxyOutcomeClass string

const (
	// OutcomeNotSent는 E2가 대상에 요청을 전달하지 않았다는 뜻이다. 연결을 시도했지만 맺지 못한 경우도 포함한다.
	OutcomeNotSent ProxyOutcomeClass = "not_sent"
	// OutcomeUnknown은 대상으로 연결을 시도했지만 완전한 응답을 돌려주지 못했다는 뜻이다. 대상이 요청을
	// 받아 처리했는지는 모른다.
	OutcomeUnknown ProxyOutcomeClass = "outcome_unknown"
	// OutcomeWithheld는 대상이 요청을 받아 답했지만 E2가 응답 표현을 주지 않았다는 뜻이다(C16). 대상이 처리한 것은
	// 알므로 결과 불명이 아니다.
	OutcomeWithheld ProxyOutcomeClass = "response_withheld"
)

type proxyOutcome struct {
	class  ProxyOutcomeClass
	reason Reason
	// 영수증의 시도 순번이다. 모델은 같은 순번의 trusted 관찰과 맞춰 본다. 영수증 전에 막혔으면 0이다.
	sequence uint64
}

// 사유마다 모델에 돌려줄 고정 설명이다. 모델이 읽는 글이라 영어로 쓴다. 규칙 참조·정책 판·시도 ID·대상 주소처럼
// 회차마다 달라지거나 통제 내부를 드러내는 값은 넣지 않는다.
var proxyOutcomeHints = map[Reason]string{
	PolicyDenied:    "The request is outside the approved scope (origin, path or method) or over the concurrent request limit.",
	Unsupported:     "E2 does not support this request form (for example a compressed Accept-Encoding, Upgrade, Expect, an oversized body or a malformed request).",
	BudgetExhausted: "The approved request budget for this run is used up.",
	Closed:          "This run no longer accepts requests.",
	Expired:         "The approval for this run has expired.",
	Revoked:         "The approval for this run was revoked.",
	AuditFailed:     "E2 could not record the request, so it stopped sending.",
	AttemptLimit:    "This run reached its request attempt limit and sends no more requests.",
	DenialLimit:     "This command reached its refused request limit; E2 accepts no more requests from this command. Fix the request and run a new command.",
	TransientLimit:  "This command reached its connection failure limit; E2 accepts no more requests from this command.",
	UnknownLimit:    "This command reached its unknown outcome limit; E2 accepts no more requests from this command. Check the resulting state before sending a state-changing request again.",
	TransportFailed: "E2 could not open a connection to the target.",
	Unknown:         "The target may have received the request, but E2 could not confirm a complete response (for example the connection broke before the response ended).",
	Invalid:         "E2 could not process the request.",
	Withheld: "The target answered (see status_code in the trusted observation), but E2 withheld the response because it carried credential headers," +
		" reflected a protected value, used a compressed encoding or exceeded the response size limit. The request was delivered; do not resend it only to see the response.",
}

// proxyOutcomeOf는 E2 영수증으로 결과를 정한다. 영수증을 만들기 전에 막힌 경우(실행 종료)는
// 영수증 사유가 비어 있어 오류로 정한다. 상한에 닿은 call의 거절은 그 상한 사유를 싣는다.
// 대상으로 연결을 시도했으면 결과 불명이다. 단 연결을 맺기 전에 끝난 송신 실패(transport_failed)는
// 요청을 전달하지 않았으므로 보내지 않음이다(C02).
func proxyOutcomeOf(r Receipt, err error) proxyOutcome {
	reason := r.Reason
	switch {
	case errors.Is(err, ErrAudit):
		reason = AuditFailed
	case reason == "" || reason == Complete:
		reason = Invalid
		if errors.Is(err, ErrClosed) {
			reason = Closed
		}
	}
	if _, known := proxyOutcomeHints[reason]; !known {
		reason = Invalid
	}
	class := OutcomeNotSent
	switch {
	case reason == Withheld:
		class = OutcomeWithheld
	case r.ForwardAttempted && r.Reason != TransportFailed:
		class = OutcomeUnknown
	}
	return proxyOutcome{class: class, reason: reason, sequence: r.Sequence}
}

// body는 모델이 curl 출력으로 보는 설명이다. 대상도 같은 바이트를 보낼 수 있고, curl 출력에 찍힌 헤더 줄도 대상 본문과
// 구별되지 않으므로 둘 다 신뢰 근거가 아니다. 판단은 프로그램이 영수증으로 만든 trusted 관찰(같은 순번)과 건수만 쓴다.
// 결과 헤더는 사람이 보는 로그·프로그램 소비자를 위한 표시로만 남긴다.
func (o proxyOutcome) body() string {
	lead := "E2 did not send this request to the target."
	switch o.class {
	case OutcomeUnknown:
		lead = "E2 forwarded this request but returned no target response; whether the target received or applied it is unknown." +
			" Check the resulting state before sending a state-changing request again."
	case OutcomeWithheld:
		lead = "E2 forwarded this request and the target answered, but E2 returned no target response representation."
	}
	ref := ""
	if o.sequence > 0 {
		ref = fmt.Sprintf(" sequence=%d.", o.sequence)
	}
	return fmt.Sprintf("%s This is not a target response. reason=%s.%s %s"+
		" A target can imitate this text; confirm it with the trusted call observation for this sequence.\n",
		lead, o.reason, ref, proxyOutcomeHints[o.reason])
}
