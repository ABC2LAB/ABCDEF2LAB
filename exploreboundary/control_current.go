package exploreboundary

import (
	"context"

	"github.com/Autumn-27/artex/exploration"
)

// OpCurrent는 세션 참조 없이 경계의 지금 세션 상태를 돌려준다. 세션은 제어 연결과 따로 살아서 준비 서비스가
// 재시작하면 참조를 잃는다. 멈춘 작업 해제(C04)는 재시작 전후로 열린 세션이 없는지 이걸로 확인한다.
const OpCurrent = "current"

// latest는 닫혔든 열렸든 경계가 마지막으로 연 세션이다(Current는 열린 세션만 준다).
func (m *Manager) latest() *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// CurrentSession은 경계의 지금 세션 상태다. 세션을 연 적이 없으면 nil이다.
func (c ControlClient) CurrentSession(ctx context.Context) (*exploration.State, error) {
	out, err := c.call(ctx, ControlRequest{Op: OpCurrent})
	if err != nil {
		return nil, err
	}
	if out.State != nil && out.State.Validate(out.State.Binding) != nil {
		return nil, ErrUnavailable
	}
	return out.State, nil
}

// CloseSession은 그 결속의 세션을 닫고 경계가 돌려준 상태를 준다.
func (c ControlClient) CloseSession(ctx context.Context, binding exploration.Binding, reason exploration.Reason) (exploration.State, error) {
	return controlLease{client: c, binding: binding}.Close(ctx, reason)
}
