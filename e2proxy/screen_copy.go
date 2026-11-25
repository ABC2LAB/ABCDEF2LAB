package e2proxy

// E3ScreenProjection은 확정 단계(E③ 증거 판정의 프로그램 요청) 사건을 화면 사본(CreateScreenJournal)에 싣는 모양이다.
// 정본은 Host v3 저널이고, 사본은 작업대 e2-flow 총계·요청 목록이 확정 요청을 한 번씩 세고 보이게 하려는 것이다
// (2026-11-25 화면 회차: 확정 요청 5건이 총계·목록에서 빠짐). 확정 전용 칸(E3Audit: 주체·대상·표식 관련 digest 등)과
// 메서드·명령 참조는 싣지 않는다. 남기는 것은 호출·시도 식별자, 프로그램 종류, 결정, 송신 단계 칸(request_written과,
// 사본 검증이 정합을 보는 forward_attempted·response_received·response_complete·not_sent), 상태 코드, 그리고 모든 행이
// 가지는 범위·정책 판·규칙 참조·순번·사유·시각이다. 사본 순번(Index)은 기록하는 쪽이 정한다.
func E3ScreenProjection(e Event) Event {
	return Event{Index: e.Index, Stage: e.Stage, Receipt: Receipt{
		Program: e.Program, Version: e.Version, Scope: e.Scope, CallRef: e.CallRef, AttemptID: e.AttemptID,
		PolicyRevision: e.PolicyRevision, RuleRef: e.RuleRef, Sequence: e.Sequence, Decision: e.Decision, Reason: e.Reason,
		ForwardAttempted: e.ForwardAttempted, RequestWritten: e.RequestWritten, ResponseReceived: e.ResponseReceived,
		ResponseComplete: e.ResponseComplete, NotSent: e.NotSent, AuditComplete: e.AuditComplete, StatusCode: e.StatusCode, At: e.At,
	}}
}
