//go:build linux

package exploreboundary

import (
	"testing"

	"github.com/Autumn-27/artex/exploration"
)

// 준비 서비스가 재시작해 세션 참조를 잃어도, 같은 제어 소켓에서 지금 세션을 찾아 닫고 닫힘을 확인할 수 있다.
func TestControlCurrentFindsSessionWithoutRef(t *testing.T) {
	m, client, source := controlFixture(t, sameUID)
	if st, err := client.CurrentSession(t.Context()); err != nil || st != nil {
		t.Fatalf("세션이 없을 때: %+v %v", st, err)
	}
	binding := fixtureBinding(exploration.ReadOnly, exploration.FullLocal)
	if _, err := (ControlBoundary{Client: client, SourceDigest: fixtureDigest, Target: source.Current}).Open(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	restarted := ControlClient{Path: client.Path, Timeout: client.Timeout}
	st, err := restarted.CurrentSession(t.Context())
	if err != nil || st == nil || !st.Active || st.Binding != binding {
		t.Fatalf("열린 세션을 찾지 못함: %+v %v", st, err)
	}
	closed, err := restarted.CloseSession(t.Context(), st.Binding, exploration.Cancelled)
	if err != nil || closed.Active || closed.ActiveConnections != 0 {
		t.Fatalf("닫기 실패: %+v %v", closed, err)
	}
	after, err := restarted.CurrentSession(t.Context())
	if err != nil || after == nil || after.Active || after.ClosedReason != exploration.Cancelled || m.Current() != nil {
		t.Fatalf("닫힌 세션이 열린 것으로 보임: %+v %v", after, err)
	}
	if out := m.Handle(t.Context(), ControlRequest{Op: OpCurrent, SessionRef: "x"}); out.OK {
		t.Fatal("current가 다른 칸을 받음")
	}
}
