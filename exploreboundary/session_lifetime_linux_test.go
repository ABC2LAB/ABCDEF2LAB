//go:build linux

package exploreboundary

import (
	"testing"
	"time"

	"github.com/Autumn-27/artex/exploration"
)

// 세션 수명(2026-11-24 사용자 결정, docs/contracts/exploration-boundary.md): 실행기(준비 서비스)의 상태 조회가 끊기면
// 경계가 세션을 닫고, 새로 시작한 준비 서비스는 이전 실행의 세션을 닫는다. 둘 다 감사 원자료에 closed_by로 남는다.

// 시험은 시계를 가짜로 돌리지만 경계의 확인 주기(Heartbeat/4)는 실제 시간이라 짧게 둔다. 비율은 제품 값과 같다.
const lifetimeHeartbeat = 40 * time.Millisecond
const lifetimeStep = 10 * time.Millisecond

func lifetimeManager(t *testing.T) (*Manager, *fakeSource, *fakeClock, string) {
	t.Helper()
	dir := evidenceDir(t)
	ev, err := OpenEvidence(dir, evidenceTestMaxBytes, evidenceKey(t))
	if err != nil {
		t.Fatal(err)
	}
	m, source, clock := evidenceManager(t, ev, time.Second)
	m.Heartbeat = lifetimeHeartbeat
	return m, source, clock, dir
}

func waitClosed(t *testing.T, m *Manager) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m.Current() == nil {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// 실행기 조회가 Heartbeat보다 오래 끊기면 경계가 runtime_unavailable·closed_by control_lost로 닫는다.
func TestHeartbeatClosesSessionWhenControlLost(t *testing.T) {
	m, source, clock, dir := lifetimeManager(t)
	if resp := openVia(t, m, source); !resp.OK {
		t.Fatal(resp.Error)
	}
	clock.add(2 * lifetimeHeartbeat)
	if !waitClosed(t, m) {
		t.Fatal("조회가 끊긴 세션을 닫지 않음")
	}
	resp := m.Handle(t.Context(), ControlRequest{Op: OpStatus, SessionRef: "session-1"})
	if !resp.OK || resp.State.Active || resp.State.ClosedReason != exploration.RuntimeUnavailable {
		t.Fatalf("닫힌 상태: %+v", resp)
	}
	if err := m.Seal(SealedByShutdown, nil); err != nil {
		t.Fatal(err)
	}
	if got := readMarker(t, dir).Session; got == nil || got.ClosedBy != CloseCauseControlLost || got.ClosedReason != exploration.RuntimeUnavailable {
		t.Fatalf("감사 원자료: %+v", got)
	}
}

// 모델 응답이 오래 걸려도(여기서는 Heartbeat의 15배) 실행기가 상태 조회를 계속 보내면 닫지 않는다. 실행기의 조회는
// 모델 호출과 따로 도는 감시 고루틴이 보낸다(server/exploration_session.go watch).
func TestHeartbeatKeepsSessionWhileStatusFlows(t *testing.T) {
	m, source, clock, _ := lifetimeManager(t)
	if resp := openVia(t, m, source); !resp.OK {
		t.Fatal(resp.Error)
	}
	// Heartbeat의 15배(제품으로 치면 30초 × 15 = 7분 반)짜리 모델 응답 동안 조회가 계속 온다. 시계를 조회 주기만큼씩
	// 진행하고, 그 사이 경계의 확인 주기가 실제 시간으로도 돌게 한다.
	for range 60 {
		clock.add(lifetimeStep)
		if resp := m.Handle(t.Context(), ControlRequest{Op: OpStatus, SessionRef: "session-1"}); !resp.OK || !resp.State.Active {
			t.Fatalf("조회 중인 세션이 닫힘: %+v", resp)
		}
		time.Sleep(lifetimeStep / 2)
	}
	if m.Current() == nil {
		t.Fatal("조회가 이어지는 세션을 닫음")
	}
	// '지금 세션 조회(current)'는 실행기의 조회가 아니라서 끊김을 막지 않는다.
	for range 10 {
		clock.add(lifetimeStep)
		if resp := m.Handle(t.Context(), ControlRequest{Op: OpCurrent}); !resp.OK {
			t.Fatal(resp.Error)
		}
	}
	if !waitClosed(t, m) {
		t.Fatal("current만 오는 세션을 닫지 않음")
	}
}

// 새로 시작한 준비 서비스는 이전 실행이 남긴 세션을 restart_cleanup으로 닫는다. control_lost는 경계만 쓴다.
func TestCloseStaleRecordsRestartCleanup(t *testing.T) {
	m, source, _, dir := lifetimeManager(t)
	if resp := openVia(t, m, source); !resp.OK {
		t.Fatal(resp.Error)
	}
	if resp := m.Handle(t.Context(), ControlRequest{Op: OpClose, SessionRef: "session-1", Reason: exploration.Cancelled, Cause: CloseCauseControlLost}); resp.OK {
		t.Fatal("서버가 control_lost를 보냄")
	}
	if resp := m.Handle(t.Context(), ControlRequest{Op: OpClose, SessionRef: "session-1", Reason: exploration.Cancelled, Cause: "operator"}); resp.OK {
		t.Fatal("모르는 원인을 받음")
	}
	resp := m.Handle(t.Context(), ControlRequest{Op: OpClose, SessionRef: "session-1", Reason: exploration.Cancelled, Cause: CloseCauseRestartCleanup})
	if !resp.OK || resp.State.Active || resp.State.ClosedReason != exploration.Cancelled {
		t.Fatalf("재시작 정리: %+v", resp)
	}
	if m.Current() != nil {
		t.Fatal("닫은 세션을 프록시가 계속 씀")
	}
	if err := m.Seal(SealedByShutdown, nil); err != nil {
		t.Fatal(err)
	}
	if got := readMarker(t, dir).Session; got == nil || got.ClosedBy != CloseCauseRestartCleanup {
		t.Fatalf("감사 원자료: %+v", got)
	}
}

// 보통 마감은 closed_by를 쓰지 않는다(이전 기록과 같은 바이트, 평가 내보내기 호환).
func TestNormalCloseHasNoCause(t *testing.T) {
	m, source, _, dir := lifetimeManager(t)
	if resp := openVia(t, m, source); !resp.OK {
		t.Fatal(resp.Error)
	}
	if resp := m.Handle(t.Context(), ControlRequest{Op: OpClose, SessionRef: "session-1", Reason: exploration.Completed}); !resp.OK {
		t.Fatal(resp.Error)
	}
	if err := m.Seal(SealedByShutdown, nil); err != nil {
		t.Fatal(err)
	}
	if got := readMarker(t, dir).Session; got == nil || got.ClosedBy != "" {
		t.Fatalf("보통 마감에 원인: %+v", got)
	}
}
