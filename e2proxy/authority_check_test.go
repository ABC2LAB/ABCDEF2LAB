package e2proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAuthorityCheckAllowedAndLegacyNil(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "legacy-nil"
		if enabled {
			name = "trusted-check"
		}
		t.Run(name, func(t *testing.T) {
			var hits, checks atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); io.WriteString(w, "owned target") }))
			defer target.Close()
			cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
			if enabled {
				cfg.CheckAuthority = func() error { checks.Add(1); return nil }
			}
			_, call := coreOpen(t, cfg)
			resp, receipt, err := call.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
			if err != nil || resp == nil || hits.Load() != 1 || !receipt.ResponseComplete {
				t.Fatalf("hits=%d receipt=%+v error=%v", hits.Load(), receipt, err)
			}
			resp.Body.Close()
			if enabled && checks.Load() < 4 {
				t.Fatalf("admission, reservation, dial and write not all checked: %d", checks.Load())
			}
		})
	}
}
func TestAuthorityCheckDeniesBeforeReservation(t *testing.T) {
	for _, afterApproval := range []bool{false, true} {
		name := "before-admission"
		if afterApproval {
			name = "after-request-approval"
		}
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int64
			var revoked atomic.Bool
			revoked.Store(!afterApproval)
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
			defer target.Close()
			cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
			cfg.CheckAuthority = func() error {
				if revoked.Load() {
					return errors.New("private installation detail")
				}
				return nil
			}
			if afterApproval {
				cfg.ApprovalMode = ApprovalConfirmEveryRequest
				cfg.RequestApproval = requestApprovalFunc(func(context.Context, RequestApprovalProposal) (bool, error) { revoked.Store(true); return true, nil })
			}
			execution, call := coreOpen(t, cfg)
			resp, receipt, err := call.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
			if err == nil || resp != nil || !receipt.NotSent || receipt.ForwardAttempted || receipt.Reason != Revoked || hits.Load() != 0 || strings.Contains(err.Error(), "private") {
				t.Fatalf("hits=%d receipt=%+v error=%v", hits.Load(), receipt, err)
			}
			remaining, budgetErr := execution.BudgetRemaining()
			if budgetErr != nil || remaining != 2 {
				t.Fatalf("denial consumed request budget: %d %v", remaining, budgetErr)
			}
			revoked.Store(false)
			if execution.Snapshot().Reason != Revoked {
				t.Fatal("revocation not latched")
			}
		})
	}
}
func TestAuthorityCheckRechecksAfterIntentBeforeDial(t *testing.T) {
	var hits atomic.Int64
	var revoked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	recorder := &coreRecorder{blockStage: "intent", entered: make(chan struct{}, 1), release: make(chan struct{})}
	cfg := coreConfig(t, target.URL, recorder, 2)
	cfg.CheckAuthority = func() error {
		if revoked.Load() {
			return errors.New("revoked")
		}
		return nil
	}
	execution, call := coreOpen(t, cfg)
	done := make(chan Receipt, 1)
	go func() {
		_, receipt, _ := call.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
		done <- receipt
	}()
	select {
	case <-recorder.entered:
	case <-time.After(time.Second):
		t.Fatal("intent stage missing")
	}
	revoked.Store(true)
	close(recorder.release)
	var receipt Receipt
	select {
	case receipt = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("revoked call did not finish")
	}
	if hits.Load() != 0 || !receipt.NotSent || receipt.ForwardAttempted || receipt.Reason != Revoked || execution.Snapshot().Reason != Revoked {
		t.Fatalf("hits=%d receipt=%+v", hits.Load(), receipt)
	}
	remaining, err := execution.BudgetRemaining()
	if err != nil || remaining != 1 {
		t.Fatal("reserved attempt was refunded")
	}
}

type authorityWriteConn struct {
	net.Conn
	writes atomic.Int64
}

func (c *authorityWriteConn) Write(p []byte) (int, error) { c.writes.Add(1); return len(p), nil }
func TestAuthorityCheckRechecksEachGuardedWriteAndLatches(t *testing.T) {
	var revoked atomic.Bool
	cfg := coreConfig(t, "http://owned.invalid", &coreRecorder{}, 2)
	cfg.CheckAuthority = func() error {
		if revoked.Load() {
			return errors.New("revoked")
		}
		return nil
	}
	execution, call := coreOpen(t, cfg)
	conn := &authorityWriteConn{}
	guard := &guardedConn{Conn: conn, c: call, revision: cfg.PolicyRevision, ob: &observed{}}
	if n, err := guard.Write([]byte("first")); err != nil || n != 5 || conn.writes.Load() != 1 {
		t.Fatal("positive write missing")
	}
	revoked.Store(true)
	if n, err := guard.Write([]byte("denied")); n != 0 || !errors.Is(err, ErrDenied) || conn.writes.Load() != 1 {
		t.Fatal("write escaped revoked authority")
	}
	revoked.Store(false)
	if n, err := guard.Write([]byte("still denied")); n != 0 || !errors.Is(err, ErrDenied) || conn.writes.Load() != 1 || execution.Snapshot().Reason != Revoked {
		t.Fatal("revocation was reset")
	}
}
func TestAuthorityCheckIsNotSerialized(t *testing.T) {
	raw, err := json.Marshal(Config{CheckAuthority: func() error { return nil }})
	if err != nil || strings.Contains(string(raw), "CheckAuthority") {
		t.Fatal("host callback entered JSON contract")
	}
}

// 호스트 검사가 실행 중이어도 기존 context 취소는 잠금 없이 도달한다.
// 검사 결과가 nil인 것만으로 이미 취소된 요청의 송신을 허용하지 않는다.
func TestAuthorityCheckRechecksCancellationAfterCallback(t *testing.T) {
	for _, cancelExecution := range []bool{false, true} {
		name := "call"
		if cancelExecution {
			name = "execution"
		}
		t.Run(name, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			cfg := coreConfig(t, "http://owned.invalid", &coreRecorder{}, 2)
			cfg.CheckAuthority = func() error { close(entered); <-release; return nil }
			execution, call := coreOpen(t, cfg)
			conn := &authorityWriteConn{}
			guard := &guardedConn{Conn: conn, c: call, revision: cfg.PolicyRevision, ob: &observed{}}
			done := make(chan error, 1)
			go func() { _, err := guard.Write([]byte("must not write")); done <- err }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("authority check not entered")
			}
			if cancelExecution {
				execution.cancel()
			} else {
				call.cancel()
			}
			close(release)
			select {
			case err := <-done:
				if !errors.Is(err, ErrDenied) || conn.writes.Load() != 0 {
					t.Fatal("cancelled write reached connection")
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled callback did not finish")
			}
		})
	}
}
func TestAuthorityCheckRechecksCancellationBeforeReservation(t *testing.T) {
	cfg := coreConfig(t, "http://owned.invalid", &coreRecorder{}, 2)
	var call *Call
	var checks atomic.Int64
	cfg.CheckAuthority = func() error {
		if checks.Add(1) == 2 {
			call.cancel()
		}
		return nil
	}
	execution, created := coreOpen(t, cfg)
	call = created
	_, receipt, err := call.Do(context.Background(), coreRequest(t, "GET", "http://owned.invalid/allowed"))
	if err == nil || !receipt.NotSent {
		t.Fatal("cancelled request admitted")
	}
	remaining, budgetErr := execution.BudgetRemaining()
	if budgetErr != nil || remaining != 2 {
		t.Fatal("cancelled callback reserved a request")
	}
}
