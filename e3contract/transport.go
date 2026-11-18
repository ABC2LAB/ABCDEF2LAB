package e3contract

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrNotImplemented는 연결이 아직 없는 갈래의 가짜·자리 구현이 돌려준다. 확정 단계는 시작하지 않는다.
	ErrNotImplemented = errors.New("e3contract: 구현되지 않음")
	// ErrMarkerNotRegistered는 이 실행의 보호 값으로 등록하지 않은 표식으로 생성·조회를 부른 경우다. E²는 보내지 않는다.
	ErrMarkerNotRegistered = errors.New("e3contract: 표식이 이 실행의 보호 값으로 등록되지 않았다")
	// ErrAuthorityRevoked는 실행 권한(대상 승인·실행 결속)이 철회된 경우다. 진위 실패로 다룬다.
	ErrAuthorityRevoked = errors.New("e3contract: 실행 권한이 철회됐다")
	// ErrJournalEventMissing은 관찰에 맞는 봉인된 저널 이벤트가 없는 경우다.
	ErrJournalEventMissing = errors.New("e3contract: 봉인된 저널 이벤트가 없다")
)

// IdentityRequest는 신원 확인 GET이다(#596 §5.1).
type IdentityRequest struct {
	Binding      RunBinding
	Spec         IdentitySpec
	Role         Role
	AccountLabel string
	// Timeout은 이 요청 하나의 확정 요청 전용 시간 상한이다(E② 요청 시간 상한과 따로).
	Timeout time.Duration
}

// CreateRequest는 A 세션으로 표식을 심은 시험 객체를 만드는 요청이다. E²가 본문 틀의 MarkerField에 표식을 넣는다.
type CreateRequest struct {
	Binding      RunBinding
	ResourceType string
	Spec         CreateSpec
	AccountLabel string
	Marker       Marker
	Timeout      time.Duration
}

// ReadRequest는 생성한 객체를 조회하는 GET이다. E²가 Create 관찰의 객체 ID를 경로 틀에 넣고, 응답의 MarkerField를
// 정규화기로 검사한다.
type ReadRequest struct {
	Binding      RunBinding
	ResourceType string
	Spec         ReadSpec
	MarkerField  string
	Role         Role
	AccountLabel string
	Create       CallRef
	Marker       Marker
	Timeout      time.Duration
}

// Transport는 E²가 여는 확정 단계 프로그램 요청이다(이동찬, E③ E² 쪽).
//
// 계약:
//   - RegisterMarker는 표식을 그 실행(Binding)의 보호 값으로 올린다. 그 뒤로 E²의 저널·영수증·승인 미리보기
//     (input_preview)와 저장 기록·로그·평가 내보내기에서 표식은 원문·지원 인코딩 모양 모두 가려진다. 실패하면 오류이고,
//     확정 단계는 시작하지 않는다. 등록하지 않은 표식으로 Create·Read를 부르면 보내지 않고 ErrMarkerNotRegistered다.
//   - 요청 하나는 영수증 하나다. 같은 요청을 다시 보내지 않는다(결과 불명 쓰기 포함).
//   - 돌려준 Observation과 같은 값이 저널에 봉인된다(Journal.Sealed). 다르면 E³는 진위 실패로 본다.
//   - 실행 권한이 철회됐으면 ErrAuthorityRevoked다. 그 밖의 오류는 그 요청의 결과 불명이다.
//   - 확정 단계 전용 몫(시간·요청)이 다 되면 보내지 않고 NotSent=confirmation_budget_exhausted 관찰을 돌려준다.
//   - 요청 하나는 요청의 Timeout(확정 요청 전용 시간 상한, 사용자 결정 2026-11-18) 안에 끝나야 한다. 넘으면 E²가 끊고,
//     보낸 뒤면 Sent=true·ResponseComplete=false·TimedOut=true(결과 불명)를 돌려준다. 다시 보내지 않는다.
//   - 사용자 승인 모드에서 생성 요청이 거절·만료되면 보내지 않고 NotSent=approval_denied·approval_timeout이다.
//
// E³와 E²는 지금 같은 서버 프로세스다(server/e2_http_factory.go의 e2proxy.NewExecution). 표식은 Go 값으로만 넘어가고
// 직렬화되지 않는다. 둘을 다른 프로세스로 나누면 그 전달 경로를 계약 문서에 적고 따로 시험한다.
type Transport interface {
	RegisterMarker(ctx context.Context, binding RunBinding, marker Marker) error
	Identity(ctx context.Context, req IdentityRequest) (Observation, error)
	Create(ctx context.Context, req CreateRequest) (Observation, error)
	Read(ctx context.Context, req ReadRequest) (Observation, error)
}

// Journal은 E² 저널(v3)의 봉인된 이벤트를 읽는다. 무결성 검증을 마친 이벤트만 돌려준다. 없으면 ErrJournalEventMissing이다.
type Journal interface {
	Sealed(ctx context.Context, binding RunBinding, ref CallRef) (Observation, error)
}

// Residue는 지난 회차 객체에 표식이 남았는지 확인한 결과다(#596 §6.1 (b)).
type Residue string

const (
	ResidueAbsent  Residue = "absent"
	ResiduePresent Residue = "present"
	ResidueUnknown Residue = "unknown"
)

// ResetState는 대상의 지난 확정 단계 쓰기와 리셋 확인 상태다(#596 §6.1 (a)).
type ResetState struct {
	// PreviousWrites는 지난 회차 확정 단계가 객체를 만들었는지다.
	PreviousWrites bool
	// ResetConfirmed는 그 뒤 운영자가 "대상을 리셋했다"고 진술한 기록이 대상 등록 보호 저널에 있는지다.
	ResetConfirmed bool
	// Residue는 지난 회차 객체를 읽어 지난 표식이 보이는지 확인한 결과다. PreviousWrites가 false면 absent다.
	Residue Residue
}

// ResetGate는 리셋 확인 기록과 지난 표식 점검을 읽는다(최민준, 정책 선언 저장·동결).
// WriteRecorder는 확정 단계 생성 요청의 쓰기 기록이다(기록하고 나서 보내기, 사용자 결정 2026-11-19).
//   - Reserve: 생성 요청을 보내기 전에 그 종류의 쓰기를 "ID 모름"으로 먼저 남긴다. 남기지 못하면 확정 단계는 그 생성
//     요청을 보내지 않고 그 종류를 UNDECIDABLE(product_write_record_unavailable)로 끝낸다.
//   - Settle: 생성 관찰을 받은 뒤 결과로 갱신한다. created면 객체 ID digest를 채우고, not_sent(보내지 않은 것이 확실)면
//     미리 남긴 기록을 거둔다. unknown이면 부르지 않는다(이미 ID 모름이다). 갱신이 실패해도 Reserve로 남긴 기록이 남아
//     다음 회차를 리셋 확인 전까지 막는다.
type WriteRecorder interface {
	Reserve(ctx context.Context, binding RunBinding, resourceType string) error
	Settle(ctx context.Context, binding RunBinding, write ConfirmationWrite) error
}

type ResetGate interface {
	Check(ctx context.Context, binding RunBinding, targetRef string) (ResetState, error)
}

// StartRefusal은 회차(또는 확정 단계)를 시작하지 않는 사유다.
type StartRefusal string

const (
	RefuseResetConfirmationMissing StartRefusal = "reset_confirmation_missing"
	RefusePreviousMarkersPresent   StartRefusal = "previous_markers_present"
	RefuseResetStateUnavailable    StartRefusal = "reset_state_unavailable"
	RefusePolicySourceUnavailable  StartRefusal = "policy_source_unavailable"
	RefuseMarkerGenerationFailed   StartRefusal = "marker_generation_failed"
	RefuseMarkerRegistrationFailed StartRefusal = "marker_registration_failed"
)

// CheckRunStart는 지난 회차 쓰기 뒤 리셋 확인과 표식 잔존을 본다. 서버는 회차 시작 때(모델 단계 전) 이것으로 회차를
// 거절하고, Run은 확정 단계 시작 때 다시 본다. 읽기 실패·모르는 값은 거절이다.
func CheckRunStart(ctx context.Context, gate ResetGate, binding RunBinding, targetRef string) StartRefusal {
	if gate == nil {
		return RefuseResetStateUnavailable
	}
	state, err := gate.Check(ctx, binding, targetRef)
	if err != nil {
		return RefuseResetStateUnavailable
	}
	// 리셋 확인이 없으면 잔존 점검 결과와 관계없이 그 사유로 멈춘다(운영자가 할 일을 정확히 보인다).
	if state.PreviousWrites && !state.ResetConfirmed {
		return RefuseResetConfirmationMissing
	}
	switch state.Residue {
	case ResidueAbsent:
		return ""
	case ResiduePresent:
		return RefusePreviousMarkersPresent
	}
	return RefuseResetStateUnavailable
}
