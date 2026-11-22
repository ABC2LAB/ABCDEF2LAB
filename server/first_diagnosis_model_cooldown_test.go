package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type fakeProviderCooldown struct {
	until    time.Time
	awaitErr error
	released atomic.Int64
}

func (f *fakeProviderCooldown) CooldownUntil() time.Time { return f.until }
func (f *fakeProviderCooldown) AwaitCooldown(ctx context.Context) (func(), error) {
	if f.awaitErr != nil {
		return func() {}, f.awaitErr
	}
	return func() { f.released.Add(1) }, nil
}

// 쉬는 중에 기다리던 호출의 문맥이 끝나면 그 호출은 오류로 돌아오고, 쉬는 시간이 끝나는 때에 Planner 알림을 한 번 보낸다.
// 쉬는 중이 아니거나 기다림이 끝까지 간 호출은 알림을 보내지 않는다(그 호출이 스스로 이어 간다).
func TestFirstDiagnosisAwaitProviderCooldownWakesPlannerOnce(t *testing.T) {
	var wakes atomic.Int64
	wake := func() { wakes.Add(1) }

	gaveUp := &FirstDiagnosisModelBudget{cooldown: &fakeProviderCooldown{until: time.Now().Add(40 * time.Millisecond), awaitErr: context.DeadlineExceeded}}
	if _, err := gaveUp.awaitProviderCooldown(context.Background(), wake); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("문맥이 끝난 대기: %v", err)
	}
	if wakes.Load() != 0 {
		t.Fatal("쉬는 시간이 끝나기 전에 깨움")
	}
	deadline := time.Now().Add(2 * time.Second)
	for wakes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond)
	if wakes.Load() != 1 {
		t.Fatalf("쉬는 시간 끝의 알림 수: %d", wakes.Load())
	}

	waited := &fakeProviderCooldown{until: time.Now().Add(time.Hour)}
	release, err := (&FirstDiagnosisModelBudget{cooldown: waited}).awaitProviderCooldown(context.Background(), wake)
	if err != nil {
		t.Fatal(err)
	}
	release()
	// 쉬는 중이 아닌데 문맥만 끝난 경우(잠김 등)는 알림 없이 오류만 돌려준다.
	if _, err := (&FirstDiagnosisModelBudget{cooldown: &fakeProviderCooldown{awaitErr: context.Canceled}}).awaitProviderCooldown(context.Background(), wake); err == nil {
		t.Fatal("오류를 삼킴")
	}
	// 로컬 모델 경로(외부 정책 없음)는 기다리지 않는다.
	if release, err := (&FirstDiagnosisModelBudget{}).awaitProviderCooldown(context.Background(), wake); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	time.Sleep(30 * time.Millisecond)
	if wakes.Load() != 1 || waited.released.Load() != 1 {
		t.Fatalf("알림 %d번, 자리 반납 %d번", wakes.Load(), waited.released.Load())
	}
}
