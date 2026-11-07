package e2proxy

import (
	"context"
	"net/http"
	"time"
)

// RequestApprovalGate는 신뢰 호스트가 연결하는 요청별 확인 경계다.
// 모델 도구나 대상 응답에 이 객체를 노출하지 않는다. nil은 기존 범위 승인을
// 유지하며, nil인 실행을 사용자 확인 모드라고 표시해서는 안 된다.
//
// 구현은 제안을 안전하게 저장·표시한 뒤 해당 사용자의 작업 권한을 검사하고,
// 명시적인 단회 허용에만 true를 반환해야 한다. timeout/fallback allow는 금지다.
// 저장·권한 확인 실패는 오류 또는 false이며, ctx 종료를 반드시 준수한다.
// 구현은 제안 원문을 공유 로그·LLM·일반 JSON에 보내지 않고 출력 정책을 적용한다.
// 현재 서버·UI 어댑터는 없으므로 이 경계만으로 제품 승인 모드가 완성되지는 않는다.
type RequestApprovalGate interface {
	AwaitRequestApproval(context.Context, RequestApprovalProposal) (bool, error)
}

// RequestApprovalProposal은 프로그램이 실제 정규화 요청에서 생성한 private 입력이다.
// 각 호출은 Receipt.AttemptID에 귀속되고, 요청은 승인 대기 동안 보류된다.
// Headers/Body/URL은 잠재적 비밀을 포함하므로 승인 저장부의 보호·출력 정책이 필요하다.
// 보호 세션 헤더 값은 포함하지 않으며 AccountRef/SessionRef로만 연결한다.
// 복사본을 수정해도 전송 요청은 바뀌지 않는다. 수정 실행은 새로운 제안이어야 한다.
type RequestApprovalProposal struct {
	OwnerRef   string      `json:"-"`
	Receipt    Receipt     `json:"-"`
	AccountRef string      `json:"-"`
	SessionRef string      `json:"-"`
	ExpiresAt  time.Time   `json:"-"`
	Method     string      `json:"-"`
	URL        string      `json:"-"`
	Host       string      `json:"-"`
	Headers    http.Header `json:"-"`
	Body       []byte      `json:"-"`
}

func (RequestApprovalProposal) String() string   { return "e2proxy.RequestApprovalProposal" }
func (RequestApprovalProposal) GoString() string { return "e2proxy.RequestApprovalProposal" }
