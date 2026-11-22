package e3v3

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Autumn-27/artex/e3contract"
)

// Request는 E²에 맡기는 E③ 프로그램 요청 하나다(신원·생성·조회, 계약 §9). E²가 목적 규칙으로 열고, 계정 세션을 붙이고,
// 표식을 넣고 응답을 검사한다. 표식은 Go 값으로만 넘어간다.
type Request struct {
	Program      e3contract.Purpose
	Binding      e3contract.RunBinding
	Role         e3contract.Role
	AccountLabel string
	ResourceType string
	Identity     e3contract.IdentitySpec
	Create       e3contract.CreateSpec
	Read         e3contract.ReadSpec
	MarkerField  string
	CreateRef    e3contract.CallRef
	Marker       e3contract.Marker
	// Body는 확정 단계가 조립한 생성 본문이다(생성만). ObjectID는 요청 본문 칸 ID다(id_field일 때 생성·조회).
	Body     e3contract.RequestBody
	ObjectID string
	Timeout  time.Duration
}

// E2는 어댑터가 E²(영수증·저널 v3, 이동찬)에 기대는 것이다(계약 §5·§9).
//
//   - RegisterMarker: 표식을 그 실행의 보호 값으로 올린다. v3 감사가 아직 연결되지 않았으면 오류다(지금 e2proxy의
//     ErrE3JournalUnavailable). 실패하면 확정 단계는 시작하지 않는다.
//   - Send: 요청을 보내고, 그 호출의 마지막 시도 이벤트(result: 송신 전 거부면 intent 없이 result 하나, 송신했으면
//     intent 뒤 result, intent 뒤 송신 전 중단이면 reason=closed result)를 기록·Sync하고 그 이벤트를 포함한 정본 head를
//     신뢰 저장소에 게시해 ACK를 확인한 뒤에 돌려준다(계약 §2.3·§5·§9). 로컬 Sync 실패, 정본 게시 실패·ACK 미확인이면
//     이벤트 없이 오류다(결과 불명). control·call_closed는 시도 밖 관리 행이라 돌려주지 않는다. 실행 권한 철회는
//     e3contract.ErrAuthorityRevoked, 등록하지 않은 표식은 e3contract.ErrMarkerNotRegistered다.
//   - AccountBindingRef: 그 실행·라벨에 지금 결속된 C14 계정 결속 참조다(세션 저장소가 발급, 다시 로그인하면 바뀜).
//   - Verified: 기다리지 않고, 신뢰 저장소가 조회 시점에 인증한 현재 epoch·head까지 MAC·체인으로 검증한 같은 스냅샷에서
//     그 호출의 마지막 이벤트를 돌려준다. 검증하지 못하면 e3contract.ErrJournalIntegrity, 검증된 스냅샷에 없으면
//     e3contract.ErrJournalEventMissing이다.
type E2 interface {
	RegisterMarker(ctx context.Context, binding e3contract.RunBinding, marker e3contract.Marker) error
	Send(ctx context.Context, req Request) (Event, error)
	AccountBindingRef(ctx context.Context, binding e3contract.RunBinding, label string) (string, error)
	Verified(ctx context.Context, binding e3contract.RunBinding, ref e3contract.CallRef) (Event, error)
}

// ErrConfig는 어댑터를 만들 수 없는 설정이다.
var ErrConfig = errors.New("e3v3: 설정 오류")

// Adapter는 E2를 e3contract.Transport·Journal로 바꾼다.
type Adapter struct {
	e2 E2
}

var (
	_ e3contract.Transport = (*Adapter)(nil)
	_ e3contract.Journal   = (*Adapter)(nil)
)

// New는 어댑터를 만든다.
func New(e2 E2) (*Adapter, error) {
	if e2 == nil {
		return nil, ErrConfig
	}
	return &Adapter{e2: e2}, nil
}

// RegisterMarker는 E²에 표식 등록을 넘긴다.
func (a *Adapter) RegisterMarker(ctx context.Context, binding e3contract.RunBinding, marker e3contract.Marker) error {
	return a.e2.RegisterMarker(ctx, binding, marker)
}

// Identity는 신원 확인 GET이다.
func (a *Adapter) Identity(ctx context.Context, req e3contract.IdentityRequest) (e3contract.Observation, error) {
	return a.send(ctx, Request{Program: e3contract.PurposeIdentity, Binding: req.Binding, Role: req.Role,
		AccountLabel: req.AccountLabel, Identity: req.Spec, Timeout: req.Timeout})
}

// Create는 A 세션으로 표식을 심은 시험 객체를 만든다.
func (a *Adapter) Create(ctx context.Context, req e3contract.CreateRequest) (e3contract.Observation, error) {
	return a.send(ctx, Request{Program: e3contract.PurposeCreate, Binding: req.Binding, Role: e3contract.RoleOwner,
		AccountLabel: req.AccountLabel, ResourceType: req.ResourceType, Create: req.Spec, MarkerField: req.Spec.MarkerField,
		Marker: req.Marker, Body: req.Body, ObjectID: req.ObjectID, Timeout: req.Timeout})
}

// Read는 생성한 객체를 조회한다.
func (a *Adapter) Read(ctx context.Context, req e3contract.ReadRequest) (e3contract.Observation, error) {
	return a.send(ctx, Request{Program: e3contract.PurposeRead, Binding: req.Binding, Role: req.Role,
		AccountLabel: req.AccountLabel, ResourceType: req.ResourceType, Read: req.Spec, MarkerField: req.MarkerField,
		CreateRef: req.Create, ObjectID: req.ObjectID, Marker: req.Marker, Timeout: req.Timeout})
}

func (a *Adapter) send(ctx context.Context, req Request) (e3contract.Observation, error) {
	ev, err := a.e2.Send(ctx, req)
	if err != nil {
		return e3contract.Observation{}, err
	}
	// intent 뒤 송신 전 중단(계약 §2.3): 실행 문맥이 끝났을 때만 생긴다. 관찰 없이 문맥 오류를 돌려줘 그 요청은 결과 불명,
	// 회차는 중지·시간 몫 끝으로 마감한다(생성이면 쓰기 기록을 거두지 않음). 문맥이 살아 있으면 관찰 계약 위반이다.
	if ev.StoppedAfterIntent() {
		if cerr := ctx.Err(); cerr != nil {
			return e3contract.Observation{}, fmt.Errorf("e3v3: intent 뒤 송신 전 중단: %w", cerr)
		}
		return e3contract.Observation{}, fmt.Errorf("%w: v3 stopped_after_intent_without_stop", e3contract.ErrObservation)
	}
	return a.observe(ctx, req.Binding, ev)
}

// observe는 이벤트를 관찰로 옮기고, 관찰에 없는 칸인 계정 결속 참조를 대조한다(계약 §4). 목적·역할·라벨·종류·생성 참조·
// 실행 결속의 대조는 e3contract.Run이 관찰로 한다.
func (a *Adapter) observe(ctx context.Context, binding e3contract.RunBinding, ev Event) (e3contract.Observation, error) {
	o, err := ev.Observation()
	if err != nil {
		return e3contract.Observation{}, err
	}
	// 대상에 보내지 않은 관찰(연결 전 거부: 승인 거절·만료, 정책 거부, 확정 몫 소진)의 결속 참조 확인은 대상 요청 없는 로컬
	// 조회다. 사용자 승인 모드의 승인 만료는 승인 기한이 확정 단계 시간 몫 끝에 맞춰져 문맥이 끝나는 바로 그 순간에 온다.
	// 끝난 문맥으로 조회하면 보내지 않은 생성이 결과 불명·쓰기 unknown으로 남는다(리허설 F15, 2026-11-21). 그래서 취소
	// 없는 문맥으로 조회한다. 보낸 관찰은 지금처럼 호출자 문맥이다.
	lookupCtx := ctx
	if !o.Sent {
		lookupCtx = context.WithoutCancel(ctx)
	}
	want, err := a.e2.AccountBindingRef(lookupCtx, binding, ev.AccountLabel)
	if err != nil {
		return e3contract.Observation{}, err
	}
	if want == "" || ev.AccountBindingRef != want {
		return e3contract.Observation{}, fmt.Errorf("%w: account_binding_ref", e3contract.ErrObservationMismatch)
	}
	return o, nil
}

// Sealed는 검증된 v3 이벤트를 관찰로 옮긴다. 요청한 호출이 아닌 이벤트, 모양·계정 결속이 맞지 않는 이벤트는 저널을
// 믿을 수 없는 것이라 무결성 실패다(이벤트 없음과 나눔, 계약 §5·§7).
func (a *Adapter) Sealed(ctx context.Context, binding e3contract.RunBinding, ref e3contract.CallRef) (e3contract.Observation, error) {
	ev, err := a.e2.Verified(ctx, binding, ref)
	if err != nil {
		return e3contract.Observation{}, err
	}
	if ev.Ref() != ref {
		return e3contract.Observation{}, fmt.Errorf("%w: 다른 호출의 이벤트", e3contract.ErrJournalIntegrity)
	}
	o, err := a.observe(ctx, binding, ev)
	if err != nil {
		return e3contract.Observation{}, fmt.Errorf("%w: %w", e3contract.ErrJournalIntegrity, err)
	}
	return o, nil
}
