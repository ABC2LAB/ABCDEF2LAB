package e2runner

import (
	"context"
	"errors"
	"time"
)

// MaxCommandBudget은 명령 시간 예산·CommandTimeout의 상한이다.
const MaxCommandBudget = time.Hour

type commandBudgetKey struct{}

// WithCommandBudget은 명령 하나에 Worker가 준 시간 예산이다(진단 Bash 도구 입력 timeout_ms). 러너는 CommandTimeout과
// 작은 쪽을 쓰고, 그 call에 운영자 승인을 기다리는 요청이 있는 동안에는 예산 시계를 멈춘다(#898 발견 3: 모델이 준 10~15초가
// 승인 대기 중에 끝나 승인 요청 8건이 운영자 결정 전에 닫혔다). 회차 기한·E② call 기한(승인 만료)은 그대로 상한이다.
func WithCommandBudget(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, commandBudgetKey{}, d)
}

// CommandBudget은 WithCommandBudget으로 준 예산이다. 없거나 0 이하면 ok가 거짓이다.
func CommandBudget(ctx context.Context) (time.Duration, bool) {
	d, ok := ctx.Value(commandBudgetKey{}).(time.Duration)
	return d, ok && d > 0
}

// approvalWaiter는 운영자 승인을 기다리는 요청이 있는지 알려 주는 call이다(e2proxy.Call).
type approvalWaiter interface {
	ApprovalWaiting() (bool, <-chan struct{})
}

// startCommandBudget은 d를 승인 대기가 없는 시간에만 쓰는 문맥을 만든다. 예산이 다 되면 원인 context.DeadlineExceeded로
// 취소한다(기존 기한 초과와 같은 판정). attach로 call을 붙이기 전(대기열)에는 그냥 흐른다. stop은 시계를 멈추고 문맥을 닫는다.
func startCommandBudget(parent context.Context, d time.Duration) (context.Context, func(approvalWaiter), func()) {
	ctx, cancel := context.WithCancelCause(parent)
	attach := make(chan approvalWaiter, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		remaining := d
		var call approvalWaiter
		for {
			var waiting bool
			var changed <-chan struct{}
			if call != nil {
				waiting, changed = call.ApprovalWaiting()
			}
			if waiting {
				select {
				case <-changed:
				case <-ctx.Done():
					return
				}
				continue
			}
			start := time.Now()
			timer := time.NewTimer(remaining)
			select {
			case <-timer.C:
				cancel(context.DeadlineExceeded)
				return
			case <-changed:
			case next := <-attach:
				call = next
			case <-ctx.Done():
				timer.Stop()
				return
			}
			timer.Stop()
			if remaining -= time.Since(start); remaining <= 0 {
				cancel(context.DeadlineExceeded)
				return
			}
		}
	}()
	attachCall := func(call approvalWaiter) {
		select {
		case attach <- call:
		case <-done:
		}
	}
	stop := func() {
		cancel(context.Canceled)
		<-done
	}
	return ctx, attachCall, stop
}

// commandTimedOut은 명령 문맥이 시간 상한(예산 또는 부모 기한)으로 끝났는지다.
func commandTimedOut(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(context.Cause(ctx), context.DeadlineExceeded)
}
