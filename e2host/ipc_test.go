package e2host

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit"
)

func TestClientUnknownACKStopsLaterEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	count := make(chan int, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			count <- 0
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		var request message
		if decodeLine(reader, &request) != nil || request.Operation != "open" || json.NewEncoder(conn).Encode(reply{OK: true}) != nil {
			count <- 0
			return
		}
		if decodeLine(reader, &request) != nil || request.Operation != "record" {
			count <- 0
			return
		}
		count <- 1 // 기록은 받았지만 ACK 없이 연결을 닫는다.
	}()
	scope := transmit.Scope{TaskRef: "task-test", RunRef: "run-test", CaseRef: "case-test", ResetRef: "reset-test", PolicyRef: "policy-test"}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := OpenClient(ctx, path, scope, "revision-test")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Abort()
	if client.Record(ctx, hostEvent(scope)) == nil || client.Record(ctx, hostEvent(scope)) == nil || client.Seal(ctx) == nil {
		t.Fatal("ACK 불명 뒤 성공 또는 추가 기록 허용")
	}
	if got := <-count; got != 1 {
		t.Fatal("예상 밖의 RPC 횟수", got)
	}
}

func TestHostConnectionSurvivesLongPlannerIdle(t *testing.T) {
	host, _, scope := fixture(t)
	path := filepath.Join(t.TempDir(), "audit.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.AcceptUnix()
		if err == nil {
			host.serveConn(ctx, conn, uint32(os.Geteuid()))
		}
	}()
	client, err := OpenClient(ctx, path, scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Abort()
	// 이전 30초 소켓 유휴 기한보다 긴 모델 계획 공백 뒤에도 같은
	// 실행의 v3 감사와 봉인이 이어져야 한다.
	timer := time.NewTimer(31 * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal("유휴 시험 기한 초과")
	}
	if err := client.Record(ctx, hostEvent(scope)); err != nil {
		t.Fatal("유휴 뒤 기록 거부", err)
	}
	if err := client.Seal(ctx); err != nil {
		t.Fatal("유휴 뒤 봉인 거부", err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("소켓 세션이 마감되지 않음")
	}
	if err := host.CheckRestoredHeads(ctx); err != nil {
		t.Fatal("유휴 뒤 현재 head 검증 실패", err)
	}
}

func TestHostRejectsPeerWithWrongUID(t *testing.T) {
	host, pool, scope := fixture(t)
	if err := host.Serve(t.Context(), filepath.Join(t.TempDir(), "same-uid.sock"), uint32(os.Geteuid())); err == nil {
		t.Fatal("Host와 같은 UID를 허용하는 소켓 기동")
	}
	path := filepath.Join(t.TempDir(), "audit.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.AcceptUnix()
		if err == nil {
			host.serveConn(t.Context(), conn, uint32(os.Geteuid()+1))
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if client, err := OpenClient(ctx, path, scope, "revision-host"); err == nil {
		client.Abort()
		t.Fatal("다른 UID의 소켓 연결 허용")
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("거부한 소켓 연결이 마감되지 않음")
	}
	var heads int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM e2_http_audit_heads WHERE run_ref=$1`, scope.RunRef).Scan(&heads); err != nil || heads != 0 {
		t.Fatal("거부한 호출이 Host head를 발급함", err)
	}
}
