package e2proxy

import (
	"context"
	"net"
	"sync"
	"time"
)

// requestClock은 프록시 연결 하나의 요청 시간 상한(ProxyConfig.RequestTimeout)을 지키는 문맥이다. context.WithTimeout과
// 같게 기한에 닿으면 Err()가 context.DeadlineExceeded다. 그래서 보낸 뒤에는 기한으로 끊지 않는 처리(#487)가 그대로다.
// 다른 점은 모델 단계 요청이 운영자 승인을 기다리는 동안 멈출 수 있다는 것이다. 승인 대기가 끝나면 처음부터 다시 잰다.
//
// 왜: 승인 대기 중에도 이 상한이 흘러 운영자가 60초 안에 결정하지 않으면 승인이 취소됐다(2026-11-25 승인 모드 화면
// 회차 4건, 사용자 결정 2026-11-25). 승인 대기는 승인 기한과 call 기한까지만 기다린다(Call.do의 승인 문맥). E③ 확정
// 요청은 프록시를 지나지 않으므로 이 시계가 없고, 짧은 승인 기한(#836)도 그대로다.
type requestClock struct {
	parent  context.Context
	conn    net.Conn
	timeout time.Duration
	done    chan struct{}

	mu       sync.Mutex
	err      error
	deadline time.Time
	paused   bool
	timer    *time.Timer
	// gen은 지금 걸린 타이머의 세대다. 멈추기 전에 이미 떠난 타이머가 다시 잰 뒤에 늦게 돌아도 새 기한을 끝내지 못하게
	// 세대를 맞춰 본다(#946 윤재상 P3).
	gen uint64
	// stopParent는 부모 문맥 취소를 따라가는 등록을 푼다.
	stopParent func() bool
}

type requestClockKey struct{}

var _ context.Context = (*requestClock)(nil)

// startRequestClock은 지금부터 timeout 뒤에 끝나는 요청 시계를 연다. 부모(call 문맥)가 끝나면 같이 끝난다.
func startRequestClock(parent context.Context, conn net.Conn, timeout time.Duration) *requestClock {
	c := &requestClock{parent: parent, conn: conn, timeout: timeout, done: make(chan struct{}), deadline: time.Now().Add(timeout)}
	c.mu.Lock()
	c.armLocked()
	c.mu.Unlock()
	c.stopParent = context.AfterFunc(parent, func() { c.end(parent.Err()) })
	return c
}

// requestClockFrom은 문맥에 실린 프록시 요청 시계다. 프록시를 지나지 않은 요청(E③ 확정·인증 준비)은 nil이다.
func requestClockFrom(ctx context.Context) *requestClock {
	c, _ := ctx.Value(requestClockKey{}).(*requestClock)
	return c
}

// armLocked는 지금부터 timeout 뒤에 끝나는 새 세대 타이머를 건다. c.mu를 쥐고 부른다.
func (c *requestClock) armLocked() {
	c.gen++
	gen := c.gen
	c.timer = time.AfterFunc(c.timeout, func() { c.expire(gen) })
}

func (c *requestClock) expire(gen uint64) {
	c.mu.Lock()
	// 멈춘 동안이나 다시 잰 뒤에 늦게 불린 지난 세대 타이머는 기한이 아니다(멈춤과 타이머가 경합한 경우).
	stale := c.paused || gen != c.gen
	c.mu.Unlock()
	if !stale {
		c.end(context.DeadlineExceeded)
	}
}

func (c *requestClock) end(err error) {
	if err == nil {
		err = context.Canceled
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.err = err
	c.timer.Stop()
	close(c.done)
}

// cancel은 연결이 끝나거나 프록시가 닫힐 때 시계를 끝낸다(context.CancelFunc 자리).
func (c *requestClock) cancel() {
	c.end(context.Canceled)
	c.stopParent()
}

// pause는 승인 대기 동안 요청 상한을 멈춘다. 승인 대기 중에는 연결에서 읽거나 쓰지 않으므로(본문은 승인 전에 다
// 읽었다) 연결의 절대 기한(serveConn)은 그대로 두고, restart가 새 기한으로 바꾼다. 이미 끝났으면 아무것도 하지 않는다.
func (c *requestClock) pause() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil || c.paused {
		return
	}
	c.paused = true
	c.timer.Stop()
}

// restart는 승인 대기가 끝난 뒤 요청 상한을 처음부터 다시 잰다(사용자 결정: 모델 단계는 승인이 난 뒤부터 요청 시간을 잰다).
// 멈춘 동안 지난 연결 기한도 새 기한으로 바꾼다. 안 바꾸면 승인 뒤 응답을 쓰지 못한다.
func (c *requestClock) restart() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil || !c.paused {
		return
	}
	c.paused = false
	c.deadline = time.Now().Add(c.timeout)
	// 닫힌 연결이면 기한을 못 바꾸지만, 그때는 이 요청이 이미 끝나 쓸 일이 없다.
	_ = c.conn.SetDeadline(c.deadline)
	c.armLocked()
}

// Deadline은 지금 흐르는 요청 기한이다. 멈춘 동안은 부모 기한만 있다.
func (c *requestClock) Deadline() (time.Time, bool) {
	parent, ok := c.parent.Deadline()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paused {
		return parent, ok
	}
	if ok && parent.Before(c.deadline) {
		return parent, true
	}
	return c.deadline, true
}

func (c *requestClock) Done() <-chan struct{} { return c.done }

func (c *requestClock) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *requestClock) Value(key any) any {
	if _, ok := key.(requestClockKey); ok {
		return c
	}
	return c.parent.Value(key)
}
