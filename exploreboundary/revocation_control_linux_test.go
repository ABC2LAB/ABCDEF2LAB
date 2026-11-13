//go:build linux

package exploreboundary

import (
	"encoding/json"
	"github.com/Autumn-27/artex/exploration"
	"net"
	"path/filepath"
	"testing"
)

// open 응답만 유실해도 실제 열린 경계의 마감 책임과 요청 몫 확인을 잃지 않는다.
func TestLostOpenReplyReturnsOwnedLeaseForVerifiedCleanup(t *testing.T) {
	manager, client, source := controlFixture(t, sameUID)
	path := filepath.Join(filepath.Dir(client.Path), "lost.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var request ControlRequest
			if json.NewDecoder(conn).Decode(&request) != nil {
				conn.Close()
				return
			}
			response := manager.Handle(t.Context(), request)
			if request.Op != OpOpen {
				_ = json.NewEncoder(conn).Encode(response)
			}
			conn.Close()
		}
	}()
	client.Path = path
	boundary := ControlBoundary{Client: client, SourceDigest: fixtureDigest, Target: source.Current}
	binding := fixtureBinding(exploration.ReadOnly, exploration.FullLocal)
	lease, err := boundary.Open(t.Context(), binding)
	if err == nil || lease == nil || manager.Current() == nil {
		t.Fatal("응답 유실 뒤 열린 경계의 소유권을 잃음")
	}
	state, err := lease.Close(t.Context(), exploration.Cancelled)
	if err != nil || state.Validate(binding) != nil || state.Active || state.TargetRequests != 0 || manager.Current() != nil {
		t.Fatal("열기 불명 뒤 실제 마감·요청 몫을 확인하지 못함")
	}
	<-done
}
func TestExplicitBusyDoesNotClaimAnotherSessionsLease(t *testing.T) {
	_, client, source := controlFixture(t, sameUID)
	boundary := ControlBoundary{Client: client, SourceDigest: fixtureDigest, Target: source.Current}
	binding := fixtureBinding(exploration.ReadOnly, exploration.FullLocal)
	first, err := boundary.Open(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(t.Context(), exploration.Completed)
	binding.SessionRef = "separate-session"
	if lease, err := boundary.Open(t.Context(), binding); err == nil || lease != nil {
		t.Fatal("명시적 거부를 다른 세션의 소유 lease로 바꿈")
	}
}
