package e2proxy

import (
	"context"
	"net/http"
	"reflect"
	"time"
)

// RequestApprovalGate는 신뢰 호스트가 연결하는 요청별 확인 경계다.
// 모델 도구나 대상 응답에 이 객체를 노출하지 않는다. 사용자 확인 모드에서
// nil 처리기는 설정 오류이며 자동 실행으로 전환하지 않는다.
//
// 구현은 제안을 안전하게 저장·표시한 뒤 해당 사용자의 작업 권한을 검사하고,
// 명시적인 단회 허용에만 true를 반환해야 한다. timeout/fallback allow는 금지다.
// 저장·권한 확인 실패는 오류 또는 false이며, ctx 종료를 반드시 준수한다.
// 구현은 제안 원문을 공유 로그·LLM·일반 JSON에 보내지 않고 출력 정책을 적용한다.
// 이 인터페이스만으로 저장·인증·화면을 제공하지 않는다. 서버 어댑터의 검증은 별도다.
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

// ApprovalMode는 신뢰 호스트가 선택하며 모델 도구가 바꿀 수 없다.
// 확인 모드는 위험 분류 없이 지원 요청을 모두 개별 확인한다.
type ApprovalMode string

const (
	ApprovalConfirmEveryRequest ApprovalMode = "confirm_every_request"
	// 선택/원 승인 범위를 함께 집행하고, 운영자가 사전 확인한 읽기만 개별 확인을 생략한다.
	ApprovalWorkbenchV1        ApprovalMode = "workbench_selective_v1"
	ApprovalPreauthorizedScope ApprovalMode = "preauthorized_scope"
)

func validApprovalConfig(cfg Config) bool {
	if cfg.RequestBoundary != nil && nilRequestBoundary(cfg.RequestBoundary) {
		return false
	}
	switch cfg.ApprovalMode {
	case ApprovalWorkbenchV1:
		return !nilApprovalGate(cfg.RequestApproval) && !nilRequestBoundary(cfg.RequestBoundary)
	case ApprovalConfirmEveryRequest:
		return !nilApprovalGate(cfg.RequestApproval)
	case ApprovalPreauthorizedScope:
		return cfg.RequestApproval == nil
	default:
		return false
	}
}

// 인터페이스 안의 typed nil도 초기화 실패로 취급한다.
func nilApprovalGate(gate RequestApprovalGate) bool {
	if gate == nil {
		return true
	}
	v := reflect.ValueOf(gate)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
