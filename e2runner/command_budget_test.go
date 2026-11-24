package e2runner

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeWaiter struct {
	mu      sync.Mutex
	waiting bool
	changed chan struct{}
}

func (w *fakeWaiter) ApprovalWaiting() (bool, <-chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.changed == nil {
		w.changed = make(chan struct{})
	}
	return w.waiting, w.changed
}

func (w *fakeWaiter) set(waiting bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.waiting = waiting
	if w.changed != nil {
		close(w.changed)
		w.changed = nil
	}
}

func TestCommandBudgetValue(t *testing.T) {
	before := time.Now()
	if d, start, ok := CommandBudget(WithCommandBudget(context.Background(), 2*time.Second)); !ok || d != 2*time.Second || start.Before(before) {
		t.Fatalf("budget roundtrip: %v %v %v", d, start, ok)
	}
	from := time.Unix(1700000000, 0)
	if _, start, ok := CommandBudget(WithCommandBudgetFrom(context.Background(), time.Second, from)); !ok || !start.Equal(from) {
		t.Fatal("budget start not carried")
	}
	for _, d := range []time.Duration{0, -time.Second} {
		if _, _, ok := CommandBudget(WithCommandBudget(context.Background(), d)); ok {
			t.Fatalf("non-positive budget accepted: %v", d)
		}
	}
	if _, _, ok := CommandBudget(WithCommandBudgetFrom(context.Background(), time.Second, time.Time{})); ok {
		t.Fatal("budget without start accepted")
	}
	if _, _, ok := CommandBudget(context.Background()); ok {
		t.Fatal("missing budget accepted")
	}
}

// 예산은 도구 시작부터 흐른다: 러너에 닿기 전에 지난 시간(원격 왕복·Host 등록)은 빼고, 시작이 지금보다 뒤면 0으로 본다
// (#910 이동찬 리뷰).
func TestRemainingCommandBudget(t *testing.T) {
	now := time.Unix(1700000000, 0)
	if got := remainingCommandBudget(10*time.Second, now.Add(-8*time.Second), now); got != 2*time.Second {
		t.Fatalf("elapsed not charged: %v", got)
	}
	if got := remainingCommandBudget(10*time.Second, now.Add(-12*time.Second), now); got != -2*time.Second {
		t.Fatalf("overdue budget: %v", got)
	}
	if got := remainingCommandBudget(10*time.Second, now.Add(time.Second), now); got != 10*time.Second {
		t.Fatalf("future start: %v", got)
	}
}

// 승인 대기가 없으면 예산이 다 될 때 기한 초과(DeadlineExceeded 원인)로 닫힌다.
func TestCommandBudgetExpiresWithoutApprovalWait(t *testing.T) {
	ctx, attach, stop := startCommandBudget(context.Background(), 30*time.Millisecond)
	defer stop()
	attach(&fakeWaiter{})
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("budget never expired")
	}
	if !commandTimedOut(ctx) {
		t.Fatal("budget expiry not reported as timeout")
	}
}

// 남은 예산이 없으면 돌려주기 전에 이미 닫혀 있다(러너가 슬롯·call을 만들기 전에 본다, #910 이동찬 리뷰).
func TestCommandBudgetExhaustedClosesSynchronously(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		ctx, attach, stop := startCommandBudget(context.Background(), d)
		if ctx.Err() == nil || !commandTimedOut(ctx) {
			t.Fatalf("exhausted budget %v still open on return", d)
		}
		attach(&fakeWaiter{})
		stop()
	}
}

// 승인을 기다리는 동안은 예산이 줄지 않고, 대기가 끝나면 남은 예산이 다시 흐른다(#898 발견 3).
func TestCommandBudgetPausesWhileApprovalWaits(t *testing.T) {
	const budget = 60 * time.Millisecond
	waiter := &fakeWaiter{waiting: true}
	start := time.Now()
	ctx, attach, stop := startCommandBudget(context.Background(), budget)
	defer stop()
	attach(waiter)
	select {
	case <-ctx.Done():
		t.Fatal("budget consumed while approval waited")
	case <-time.After(4 * budget):
	}
	waiter.set(false)
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("budget did not resume after approval wait")
	}
	if !commandTimedOut(ctx) || time.Since(start) < 4*budget {
		t.Fatalf("resumed budget wrong: timedOut=%v elapsed=%v", commandTimedOut(ctx), time.Since(start))
	}
}

// 부모 문맥(회차 기한)은 승인 대기 중에도 그대로 닫는다. stop으로 닫은 것은 시간 초과가 아니다.
func TestCommandBudgetKeepsParentDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	ctx, attach, stop := startCommandBudget(parent, time.Hour)
	defer stop()
	attach(&fakeWaiter{waiting: true})
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("parent deadline ignored during approval wait")
	}
	if !commandTimedOut(ctx) {
		t.Fatal("parent deadline not reported as timeout")
	}
	ctx2, _, stop2 := startCommandBudget(context.Background(), time.Hour)
	stop2()
	if ctx2.Err() == nil || commandTimedOut(ctx2) {
		t.Fatal("stopped budget reported as timeout")
	}
}
