package e2proxy

import "sync"

// approvalWait는 call 하나에서 운영자 승인을 기다리는 요청 수다. 러너가 명령 시간 예산을 승인 대기 동안 멈추는 데
// 쓴다(#898 발견 3: 모델이 준 짧은 명령 시간이 승인 대기 중에 끝나 승인 요청이 운영자 결정 전에 닫혔다). Execution 잠금과
// 따로 둔다(승인 대기는 그 잠금 밖에서 일어난다).
type approvalWait struct {
	mu      sync.Mutex
	waiting int
	changed chan struct{}
}

func (w *approvalWait) add(delta int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.waiting += delta
	if w.changed != nil {
		close(w.changed)
		w.changed = nil
	}
}

func (w *approvalWait) state() (bool, <-chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.changed == nil {
		w.changed = make(chan struct{})
	}
	return w.waiting > 0, w.changed
}

// ApprovalWaiting은 이 call에서 운영자 승인을 기다리는 요청이 있는지와, 그 상태가 바뀌면 닫히는 채널이다. 승인 기한은
// call 기한(c.expires)이라 이 상태가 끝없이 이어지지 않는다.
func (c *Call) ApprovalWaiting() (bool, <-chan struct{}) {
	if c == nil {
		return false, nil
	}
	return c.approvals.state()
}
