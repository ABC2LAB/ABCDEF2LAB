package main

import (
	"context"
	"testing"
	"time"

	"github.com/Autumn-27/artex/server"
)

// 운영자 중지(상위 문맥 취소)와 대상 철회만 운영자 중지다. 감시자의 원인 없는 취소와 시간 상한은 아니다(#454).
func TestURLOperatorStop(t *testing.T) {
	newCtx := func() (context.Context, context.CancelFunc, context.Context, context.CancelFunc, context.CancelCauseFunc) {
		parent, stopParent := context.WithCancel(context.Background())
		hard, cancelHard := context.WithTimeout(parent, time.Hour)
		ctx, stopTarget := context.WithCancelCause(hard)
		return parent, stopParent, ctx, cancelHard, stopTarget
	}
	parent, stopParent, ctx, cancelHard, stopTarget := newCtx()
	if urlOperatorStop(parent, ctx) {
		t.Fatal("running context counted as stopped")
	}
	stopParent()
	if !urlOperatorStop(parent, ctx) {
		t.Fatal("operator stop not recognized")
	}
	cancelHard()
	stopTarget(nil)

	parent, stopParent, ctx, cancelHard, stopTarget = newCtx()
	stopTarget(server.ErrFirstDiagnosisTargetEnded)
	if !urlOperatorStop(parent, ctx) {
		t.Fatal("target withdrawal not recognized")
	}
	stopParent()
	cancelHard()

	parent, stopParent, ctx, cancelHard, stopTarget = newCtx()
	cancelHard() // 감시자의 설치·위임 검사 실패, 상태 조회 실패
	if urlOperatorStop(parent, ctx) {
		t.Fatal("watcher cancel counted as operator stop")
	}
	stopTarget(nil)
	stopParent()

	parent, stopParent = context.WithCancel(context.Background())
	hard, cancel := context.WithTimeout(parent, time.Nanosecond)
	ctx, stopTarget = context.WithCancelCause(hard)
	<-ctx.Done()
	if urlOperatorStop(parent, ctx) {
		t.Fatal("run time limit counted as operator stop")
	}
	stopTarget(nil)
	cancel()
	stopParent()
}
