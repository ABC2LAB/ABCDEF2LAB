package e3v3

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/e3contract"
)

type spyBridge struct {
	binding e3contract.RunBinding
	calls   atomic.Int64
	closed  atomic.Bool
}

func (b *spyBridge) RegisterMarker(context.Context, e3contract.RunBinding, e3contract.Marker) error {
	b.calls.Add(1)
	return nil
}
func (b *spyBridge) Send(context.Context, Request) (Event, error) {
	b.calls.Add(1)
	return Event{}, errors.New("spy send")
}
func (b *spyBridge) AccountBindingRef(context.Context, e3contract.RunBinding, string) (string, error) {
	b.calls.Add(1)
	return "", errors.New("spy account")
}
func (b *spyBridge) Verified(context.Context, e3contract.RunBinding, e3contract.CallRef) (Event, error) {
	b.calls.Add(1)
	return Event{}, errors.New("spy verified")
}
func (b *spyBridge) Close() { b.closed.Store(true) }

// 브리지는 표식 등록 때 한 번만, 그 회차 결속으로 만들어진다. 등록 전 다른 호출은 브리지를 만들지 않고 거부한다. 브리지를
// 만들지 못하면 표식 등록이 실패한다(e3contract.Run: start_refused·marker_registration_failed).
func TestLazyE2(t *testing.T) {
	if _, err := NewLazy(nil); !errors.Is(err, ErrConfig) {
		t.Fatal("생성기 없이 만듦")
	}
	var created atomic.Int64
	var spy *spyBridge
	lazy, err := NewLazy(func(binding e3contract.RunBinding) (Bridge, error) {
		created.Add(1)
		spy = &spyBridge{binding: binding}
		return spy, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	binding := e3contract.RunBinding{JobRef: "job-1", RunRef: "run-1"}
	if _, err := lazy.Send(t.Context(), Request{}); !errors.Is(err, ErrNotRegistered) || created.Load() != 0 {
		t.Fatal("표식 등록 전 송신이 브리지를 만들거나 통과함")
	}
	if _, err := lazy.Verified(t.Context(), binding, e3contract.CallRef{}); !errors.Is(err, ErrNotRegistered) || created.Load() != 0 {
		t.Fatal("표식 등록 전 검증 조회가 브리지를 만들거나 통과함")
	}
	if _, err := lazy.AccountBindingRef(t.Context(), binding, "tester-a"); !errors.Is(err, ErrNotRegistered) || created.Load() != 0 {
		t.Fatal("표식 등록 전 계정 결속 조회가 브리지를 만들거나 통과함")
	}
	if _, _, err := lazy.VerifiedCheckpoint(t.Context(), binding, e3contract.CallRef{}); !errors.Is(err, ErrNotRegistered) || created.Load() != 0 {
		t.Fatal("표식 등록 전 체크포인트 조회가 브리지를 만들거나 통과함")
	}
	if lazy.Created() {
		t.Fatal("등록 전 생성 표시")
	}
	for range 2 {
		if err := lazy.RegisterMarker(t.Context(), binding, e3contract.Marker{}); err != nil {
			t.Fatal(err)
		}
	}
	if created.Load() != 1 || spy.binding != binding || spy.calls.Load() != 2 || !lazy.Created() {
		t.Fatalf("브리지 생성 %d·결속 %+v·위임 %d", created.Load(), spy.binding, spy.calls.Load())
	}
	if _, err := lazy.Send(t.Context(), Request{}); err == nil || spy.calls.Load() != 3 {
		t.Fatal("등록 뒤 송신이 브리지로 넘어가지 않음")
	}
	if _, err := lazy.Verified(t.Context(), binding, e3contract.CallRef{}); err == nil || spy.calls.Load() != 4 {
		t.Fatal("등록 뒤 검증 조회가 브리지로 넘어가지 않음")
	}
	if _, err := lazy.AccountBindingRef(t.Context(), binding, "tester-a"); err == nil || spy.calls.Load() != 5 {
		t.Fatal("등록 뒤 계정 결속 조회가 브리지로 넘어가지 않음")
	}
	// 체크포인트를 주지 못하는 브리지는 근거 체크포인트 없음이다(저장 쪽은 봉인 미확인으로 남긴다).
	if _, _, err := lazy.VerifiedCheckpoint(t.Context(), binding, e3contract.CallRef{}); !errors.Is(err, ErrNoCheckpoint) {
		t.Fatal("체크포인트 없는 브리지", err)
	}
	lazy.Close()
	if !spy.closed.Load() {
		t.Fatal("닫을 때 브리지를 닫지 않음")
	}
	noHost := errors.New("no v3 host")
	failing, _ := NewLazy(func(e3contract.RunBinding) (Bridge, error) { return nil, noHost })
	if err := failing.RegisterMarker(t.Context(), binding, e3contract.Marker{}); !errors.Is(err, noHost) || failing.Created() {
		t.Fatal("브리지를 못 만들었는데 표식 등록이 성공함", err)
	}
	empty, _ := NewLazy(func(e3contract.RunBinding) (Bridge, error) { return nil, nil })
	if err := empty.RegisterMarker(t.Context(), binding, e3contract.Marker{}); !errors.Is(err, ErrNotRegistered) {
		t.Fatal("빈 브리지로 표식 등록이 성공함", err)
	}
	failing.Close() // 브리지가 없어도 닫기는 안전하다.
}
