package exploreboundary

import (
	"context"
	"github.com/Autumn-27/artex/exploration"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type revocationObservedConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *revocationObservedConn) Close() error { c.closed.Store(true); return c.Conn.Close() }
func TestLateOldSessionWatchCannotCloseIndependentNewTunnel(t *testing.T) {
	for _, currentOld := range []bool{true, false} {
		old, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
		_, _ = old.Close(t.Context(), exploration.TargetEnded)
		next, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		conn := &revocationObservedConn{Conn: a}
		proxy := &Proxy{tunnels: map[net.Conn]struct{}{conn: {}}}
		manager := &Manager{Proxies: []*Proxy{proxy}, current: next}
		if currentOld {
			manager.current = old
		}
		manager.watch(old)
		if conn.closed.Load() != currentOld {
			t.Fatal("옛 세션 watcher가 새 독립 터널을 닫거나 기존 터널을 남김")
		}
	}
}
func TestClosedSessionWithInflightRequestCannotBeReplaced(t *testing.T) {
	old, source, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	decision, ticket := old.Admit(t.Context(), approved("GET", "/app"))
	if !decision.Forward || ticket == nil {
		t.Fatal("요청 예약 양성 대조 실패")
	}
	defer ticket.Done(Result{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _ = old.Close(ctx, exploration.TargetEnded)
	manager := &Manager{Policy: old.policy, CloseWait: time.Second, current: old}
	binding := old.binding
	binding.SessionRef = "new-session"
	source.mu.Lock()
	wire := WireTargetOf(source.target)
	source.mu.Unlock()
	result := manager.Handle(t.Context(), ControlRequest{Op: OpOpen, Binding: &binding, Target: &wire, SourceDigest: old.flow.SourceDigest})
	t.Cleanup(func() {
		if manager.current != nil {
			_, _ = manager.current.Close(ctx, exploration.Cancelled)
		}
	})
	if result.OK || result.Error != ControlErrBusy {
		t.Fatal("미마감 옛 요청과 새 세션이 동시에 열림")
	}
}
