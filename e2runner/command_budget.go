package e2runner

import (
	"context"
	"errors"
	"time"
)

// MaxCommandBudget은 명령 시간 예산·CommandTimeout의 상한이다.
const MaxCommandBudget = time.Hour

type commandBudgetKey struct{}

type commandBudgetValue struct {
	d     time.Duration
	start time.Time
}

// WithCommandBudget은 명령 하나에 Worker가 준 시간 예산이다(진단 Bash 도구 입력 timeout_ms). 예산은 지금(도구 시작)부터
// 흐른다. 러너는 그동안 지난 시간(원격 왕복·Host 등록·대기열)을 빼고 CommandTimeout과 작은 쪽을 쓰며, 그 call에 운영자
// 승인을 기다리는 요청이 있는 동안에만 시계를 멈춘다(#898 발견 3: 모델이 준 10~15초가 승인 대기 중에 끝나 승인 요청 8건이
// 운영자 결정 전에 닫혔다). 회차 기한·E② call 기한(승인 만료)은 그대로 상한이다.
func WithCommandBudget(ctx context.Context, d time.Duration) context.Context {
	return WithCommandBudgetFrom(ctx, d, time.Now())
}

// WithCommandBudgetFrom은 예산이 start부터 흐르기 시작한 것으로 싣는다(원격 Host가 도구 시작 시각을 되살릴 때).
func WithCommandBudgetFrom(ctx context.Context, d time.Duration, start time.Time) context.Context {
	return context.WithValue(ctx, commandBudgetKey{}, commandBudgetValue{d: d, start: start})
}

// CommandBudget은 WithCommandBudget으로 준 예산과 흐르기 시작한 시각이다. 없거나 0 이하면 ok가 거짓이다.
func CommandBudget(ctx context.Context) (time.Duration, time.Time, bool) {
	v, ok := ctx.Value(commandBudgetKey{}).(commandBudgetValue)
	return v.d, v.start, ok && v.d > 0 && !v.start.IsZero()
}

// remainingCommandBudget은 예산에서 시작 뒤 지난 시간을 뺀 값이다. 시작이 지금보다 뒤(시계 차이)면 지난 시간을 0으로 본다.
func remainingCommandBudget(d time.Duration, start time.Time, now time.Time) time.Duration {
	elapsed := now.Sub(start)
	if elapsed < 0 {
		elapsed = 0
	}
	return d - elapsed
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
	// 남은 예산이 없으면(원격 왕복이 다 씀) 돌려주기 전에 닫는다. 고루틴 안에서 닫으면 그 사이 러너가 슬롯을 잡고 call을
	// 만들어 이미 만료된 명령이 송신까지 갈 수 있다(#910 이동찬 리뷰).
	if d <= 0 {
		cancel(context.DeadlineExceeded)
		close(done)
		return ctx, func(approvalWaiter) {}, func() {}
	}
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
