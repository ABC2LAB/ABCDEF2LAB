package e2host

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
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
