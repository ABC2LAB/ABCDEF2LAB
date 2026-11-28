package learnedlogin

import (
	"strconv"
	"strings"
)

// 로그인 시도 기록(§3)은 서명 저널이 아니다. 그래서 학습에 쓰는 성공 행이 실제로 대상에 나간 요청인지를 그 회차의 감사
// 근거와 대조한다(§6-2 "출처 결속"). 대조할 수 있는 범위는 감사 스키마가 담은 것뿐이다: E② 결과 사건은 호출·시도 참조,
// 회차, 메서드, 송신·응답 완료, 상태, 감사 완료, 모델 호출 여부를 담고 경로·계정은 담지 않는다. 경로·계정 결속은 기록의
// 보호 파일 검사와 행 일관성에 기대며 감사로 입증했다고 말하지 않는다.

// AuditedSend는 E② 감사 결과 사건 하나에서 대조에 쓰는 칸이다(e2proxy.Event의 부분). 의존을 피하려고 따로 둔다.
type AuditedSend struct {
	CallRef          string
	AttemptID        string
	RunRef           string
	ModelCall        bool
	Method           string
	Allowed          bool
	RequestWritten   bool
	ResponseReceived bool
	ResponseComplete bool
	StatusCode       int
	AuditComplete    bool
}

// Provenance는 학습한 시도의 출처를 어디까지 확인했는지다.
type Provenance string

const (
	// ProvenanceE2Audited: 진단 단계 행이 같은 실행의 감사 완료된 E② 모델 송신 결과와 맞는다.
	ProvenanceE2Audited Provenance = "e2_audited"
	// ProvenanceExplorationObserved means a separate exploration boundary record
	// and private observation matched the attempt. It does not assert E② audit,
	// account identity, or response body completion.
	ProvenanceExplorationObserved Provenance = "exploration_observed"
	// ProvenanceExplorationUnaudited: 탐색 단계 행이다. 제품 회차의 탐색 경계 기록은 감사 근거로 남지 않아(경계 원자료는
	// 평가·수용 회차 전용) 대조하지 못한다. 형식만 확인했다.
	ProvenanceExplorationUnaudited Provenance = "exploration_unaudited"
)

// VerifyTransport는 고른 성공 행의 transport_ref를 감사 근거와 대조한다. lookup은 (호출, 시도) 참조로 그 실행의 감사 완료된
// 결과 사건을 찾는다(없으면 ok=false). 탐색 행은 경계 순번 형식만 보고 ProvenanceExplorationUnaudited를 준다.
func VerifyTransport(row Row, runRef string, lookup func(callRef, attemptID string) (AuditedSend, bool)) (Provenance, Reason) {
	if runRef == "" || row.RunRef != runRef || !validRef(row.TransportRef) {
		return "", ReasonTransportUnverified
	}
	switch row.Stage {
	case StageExploration:
		if n, err := strconv.ParseUint(row.TransportRef, 10, 64); err != nil || n == 0 || strconv.FormatUint(n, 10) != row.TransportRef {
			return "", ReasonTransportUnverified
		}
		return ProvenanceExplorationUnaudited, ""
	case StageDiagnosis:
	default:
		return "", ReasonTransportUnverified
	}
	callRef, attemptID, ok := strings.Cut(row.TransportRef, "/")
	if !ok || callRef == "" || attemptID == "" || strings.Contains(attemptID, "/") || lookup == nil {
		return "", ReasonTransportUnverified
	}
	send, found := lookup(callRef, attemptID)
	if !found || send.CallRef != callRef || send.AttemptID != attemptID || send.RunRef != runRef || !send.ModelCall ||
		send.Method != row.Method || !send.Allowed || !send.RequestWritten || !send.ResponseReceived || !send.ResponseComplete ||
		send.StatusCode != row.Status || !send.AuditComplete {
		return "", ReasonTransportUnverified
	}
	return ProvenanceE2Audited, ""
}
