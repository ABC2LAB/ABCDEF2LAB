package e2host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2checkpoint"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e3contract"
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

func TestClientVerifiedReadsCurrentE3Result(t *testing.T) {
	host, _, scope := fixture(t)
	path := filepath.Join(t.TempDir(), "audit.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
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
	binding, intent, result := hostE3Events(scope)
	if err := client.Record(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if err := client.Record(ctx, result); err != nil {
		t.Fatal(err)
	}
	ref := e3contract.CallRef{CallRef: result.CallRef, AttemptID: result.AttemptID, Sequence: result.Sequence}
	missing := ref
	missing.CallRef = "missing-call"
	if _, err := client.Verified(ctx, binding, missing); !errors.Is(err, ErrEventMissing) {
		t.Fatal("없는 사건을 구분하지 못함", err)
	}
	wrong := binding
	wrong.JobRef = "other-job"
	if _, err := client.Verified(ctx, wrong, ref); !errors.Is(err, ErrBindingMismatch) {
		t.Fatal("결속 불일치를 구분하지 못함", err)
	}
	got, err := client.Verified(ctx, binding, ref)
	if err != nil || got.Index != result.Index || got.E3Audit == nil || got.E3Audit.SubjectDigest != result.E3Audit.SubjectDigest {
		t.Fatal("소켓을 통한 현재 head 검증 실패", err)
	}
	client.Abort()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("Host 연결이 닫히지 않음")
	}
}

func TestClientObservedAtOrdinaryResultAndSeal(t *testing.T) {
	host, _, scope := fixture(t)
	path := filepath.Join(t.TempDir(), "audit.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
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
	intent, result := hostSentEvents(scope)
	if err := client.Record(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if err := client.Record(ctx, result); err != nil {
		t.Fatal(err)
	}
	ref := ObservationRef{CallRef: result.CallRef, AttemptID: result.AttemptID, Sequence: result.Sequence}
	verified, err := client.ObservedAt(ctx, ref)
	if err != nil || verified.Event.CallRef != ref.CallRef || verified.Checkpoint.Sequence != 2 {
		t.Fatal("ordinary IPC attestation", err)
	}
	seal, err := client.SealChecked(ctx)
	if err != nil || seal == nil || len(seal.Checks) != 1 || !seal.Checks[0].Included || seal.Checks[0].MAC != verified.Checkpoint.MAC {
		t.Fatal("ordinary IPC seal", err)
	}
	client.Abort()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("Host socket did not close")
	}
}

func TestClientVerifiedTamperedJournalIsNotMissing(t *testing.T) {
	host, _, scope := fixture(t)
	path := filepath.Join(t.TempDir(), "audit.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	go func() {
		conn, acceptErr := listener.AcceptUnix()
		if acceptErr == nil {
			host.serveConn(ctx, conn, uint32(os.Geteuid()))
		}
	}()
	client, err := OpenClient(ctx, path, scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Abort()
	binding, intent, result := hostE3Events(scope)
	if err := client.Record(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if err := client.Record(ctx, result); err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	r := host.active[scope.RunRef]
	host.mu.Unlock()
	if r == nil {
		t.Fatal("현재 실행 없음")
	}
	journal := filepath.Join(r.directory, "events.jsonl")
	info, err := os.Stat(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(journal, info.Size()-1); err != nil {
		t.Fatal(err)
	}
	ref := e3contract.CallRef{CallRef: result.CallRef, AttemptID: result.AttemptID, Sequence: result.Sequence}
	if _, err := client.Verified(ctx, binding, ref); !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrEventMissing) {
		t.Fatal("감사 손상을 사건 누락으로 분류", err)
	}
}

func TestClientVerifiedUnknownACKFencesConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		var request message
		if decodeLine(reader, &request) != nil || request.Operation != "open" ||
			json.NewEncoder(conn).Encode(reply{OK: true}) != nil {
			return
		}
		if decodeLine(reader, &request) != nil || request.Operation != "verified" {
			return
		}
		// 요청을 받았지만 현재 head 확인 응답을 내지 못한 Host를 흉내 낸다.
	}()
	scope := transmit.Scope{TaskRef: "task-test", RunRef: "run-test", CaseRef: "case-test", ResetRef: "reset-test", PolicyRef: "policy-test"}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := OpenClient(ctx, path, scope, "revision-test")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Abort()
	binding, _, result := hostE3Events(scope)
	ref := e3contract.CallRef{CallRef: result.CallRef, AttemptID: result.AttemptID, Sequence: result.Sequence}
	if _, err := client.Verified(ctx, binding, ref); !errors.Is(err, ErrUnavailable) {
		t.Fatal("현재 head ACK 불명을 성공으로 처리", err)
	}
	if err := client.Record(ctx, hostEvent(scope)); !errors.Is(err, ErrUnavailable) {
		t.Fatal("ACK 불명 뒤 기록 허용", err)
	}
	<-done
}

func TestDisconnectedHostSessionClosesOnlyCompleteAudit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		events  string
		recover bool
	}{
		{"runner failed before send", "none", true},
		{"sent and completed", "complete", true},
		{"sent with unknown outcome recorded", "unknown", true},
		{"send result missing", "intent", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, pool, scope := fixture(t)
			path := filepath.Join(t.TempDir(), "audit.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
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
			if tc.events != "none" {
				intent, result := hostSentEvents(scope)
				if err := client.Record(ctx, intent); err != nil {
					t.Fatal(err)
				}
				if tc.events == "unknown" {
					result.Reason = e2proxy.Unknown
					result.ResponseReceived, result.ResponseComplete, result.StatusCode = false, false, 0
				}
				if tc.events != "intent" {
					if err := client.Record(ctx, result); err != nil {
						t.Fatal(err)
					}
				}
			}
			client.Abort()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("연결 종료 뒤 Host 검증이 끝나지 않음")
			}
			var phase string
			if err := pool.QueryRowContext(ctx, `SELECT phase FROM e2_http_audit_heads WHERE run_ref=$1`, scope.RunRef).Scan(&phase); err != nil {
				t.Fatal(err)
			}
			if tc.recover && phase != string(e2checkpoint.Interrupted) || !tc.recover && phase != string(e2checkpoint.Open) {
				t.Fatalf("잘못된 감사 마감 상태: %s", phase)
			}
			next := scope
			next.RunRef = "next-after-disconnect"
			later, err := host.Open(ctx, next, "revision-host")
			if tc.recover {
				if err != nil {
					t.Fatal("안전 마감 뒤 다음 회차 거부", err)
				}
				if err := later.Seal(ctx); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("미완료 송신 뒤 다음 회차 허용")
			}
		})
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
