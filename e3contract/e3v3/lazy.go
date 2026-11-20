package e3v3

import (
	"context"
	"errors"
	"sync"

	"github.com/Autumn-27/artex/e3contract"
)

// Bridge는 확정 단계가 쓰는 실제 E② 브리지(e2live.Live)다.
type Bridge interface {
	E2
	Close()
}

// ErrNotRegistered는 표식 등록 전에 보내기·조회를 부른 것이다. e3contract.Run은 표식을 등록한 뒤에만 보내므로 정상
// 흐름에서는 나오지 않는다.
var ErrNotRegistered = errors.New("e3v3: 표식 등록 전 호출")

// LazyE2는 E② 실행 연결이 있으면 확정 단계에 늘 붙이는 E2다(Go 조건 2: 최종 이미지에서 not_wired가 나오지 않고, 꺼진
// 회차는 실제 사유를 낸다). 실제 브리지는 표식 등록 때 처음 만든다. e3contract.Run은 꺼진 회차와 몫이 0인 회차에서 표식을
// 등록하지 않으므로, 그런 회차에서는 브리지를 만들지 않고 E²·Host·저널·쓰기 기록에 아무 흔적도 남기지 않는다(선언 없는
// 회차의 E② 동작 불변). 브리지를 만들지 못하면(v3 Host 없음·계정 저장소 없음·결속 불일치) 표식 등록이 실패해 확정 단계는
// 계약 §7대로 start_refused·marker_registration_failed다.
type LazyE2 struct {
	mu     sync.Mutex
	create func(e3contract.RunBinding) (Bridge, error)
	bridge Bridge
}

// NewLazy는 표식 등록 때 create로 브리지를 만드는 LazyE2다.
func NewLazy(create func(e3contract.RunBinding) (Bridge, error)) (*LazyE2, error) {
	if create == nil {
		return nil, ErrConfig
	}
	return &LazyE2{create: create}, nil
}

func (l *LazyE2) RegisterMarker(ctx context.Context, binding e3contract.RunBinding, marker e3contract.Marker) error {
	l.mu.Lock()
	if l.bridge == nil {
		bridge, err := l.create(binding)
		if err != nil || bridge == nil {
			l.mu.Unlock()
			if err == nil {
				err = ErrNotRegistered
			}
			return err
		}
		l.bridge = bridge
	}
	bridge := l.bridge
	l.mu.Unlock()
	return bridge.RegisterMarker(ctx, binding, marker)
}

// current는 표식 등록으로 만든 브리지다. 등록 전 호출은 보내지 않고 거부한다.
func (l *LazyE2) current() (Bridge, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.bridge == nil {
		return nil, ErrNotRegistered
	}
	return l.bridge, nil
}

func (l *LazyE2) Send(ctx context.Context, req Request) (Event, error) {
	bridge, err := l.current()
	if err != nil {
		return Event{}, err
	}
	return bridge.Send(ctx, req)
}

func (l *LazyE2) AccountBindingRef(ctx context.Context, binding e3contract.RunBinding, label string) (string, error) {
	bridge, err := l.current()
	if err != nil {
		return "", err
	}
	return bridge.AccountBindingRef(ctx, binding, label)
}

func (l *LazyE2) Verified(ctx context.Context, binding e3contract.RunBinding, ref e3contract.CallRef) (Event, error) {
	bridge, err := l.current()
	if err != nil {
		return Event{}, err
	}
	return bridge.Verified(ctx, binding, ref)
}

// Created는 실제 브리지를 만들었는지다(꺼진 회차의 흔적 0 확인용).
func (l *LazyE2) Created() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bridge != nil
}

func (l *LazyE2) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.bridge != nil {
		l.bridge.Close()
	}
}

// Checkpoint는 독립 Host가 확정 단계 근거를 검증한 현재 head 체크포인트다(E③ 판정 저장 근거, docs/contracts/
// e3-product-verdict-store.md §2). 같은 회차 저널이라 범위는 결속과 같다.
type Checkpoint struct {
	JournalID string
	Sequence  uint64
	MAC       string
}

// CheckpointE2는 Verified와 같고 검증에 쓴 현재 head 체크포인트를 함께 돌려주는 E2다(e2live.Live).
type CheckpointE2 interface {
	VerifiedCheckpoint(ctx context.Context, binding e3contract.RunBinding, ref e3contract.CallRef) (Event, Checkpoint, error)
}

// ErrNoCheckpoint는 브리지가 근거 체크포인트를 주지 못하는 것이다.
var ErrNoCheckpoint = errors.New("e3v3: 근거 체크포인트를 줄 수 없음")

// VerifiedCheckpoint는 표식 등록으로 만든 브리지에 근거 체크포인트를 묻는다. 브리지가 주지 못하면 ErrNoCheckpoint다.
func (l *LazyE2) VerifiedCheckpoint(ctx context.Context, binding e3contract.RunBinding, ref e3contract.CallRef) (Event, Checkpoint, error) {
	bridge, err := l.current()
	if err != nil {
		return Event{}, Checkpoint{}, err
	}
	withCheckpoint, ok := bridge.(CheckpointE2)
	if !ok {
		return Event{}, Checkpoint{}, ErrNoCheckpoint
	}
	return withCheckpoint.VerifiedCheckpoint(ctx, binding, ref)
}
