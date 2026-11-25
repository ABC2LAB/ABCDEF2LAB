package e2proxy

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const (
	clockTestTimeout = 300 * time.Millisecond
	// clockTestHold는 승인 대기 시간이다. 요청 상한의 몇 배라 시계가 멈추지 않으면 승인 전에 요청이 끊긴다.
	clockTestHold = 4 * clockTestTimeout
)

// 프록시 요청 상한은 승인 대기 동안 멈춘다. 운영자가 상한보다 늦게 허용해도 요청이 나가고 응답이 돌아온다(사용자
// 결정 2026-11-25, 승인 모드 화면 회차에서 60초 상한에 승인 4건이 닫힘).
func TestProxyRequestTimeoutPausesDuringApproval(t *testing.T) {
	for _, secured := range []bool{false, true} {
		t.Run(fmt.Sprint(secured), func(t *testing.T) {
			var hits atomic.Int64
			target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				io.WriteString(w, "approved response")
			}))
			roots := clockTestTarget(target, secured)
			defer target.Close()
			var decided atomic.Int64
			_, call, _ := proxyTestCallWith(t, target.URL, roots, func(c *Config) {
				c.ApprovalMode = ApprovalConfirmEveryRequest
				c.RequestApproval = requestApprovalFunc(func(ctx context.Context, _ RequestApprovalProposal) (bool, error) {
					select {
					case <-time.After(clockTestHold):
						decided.Add(1)
						return true, nil
					case <-ctx.Done():
						return false, ctx.Err()
					}
				})
			})
			_, addr, proxyRoots := proxyTestListenerWith(t, call, clockTestProxy)
			resp, err := proxyTestClient(t, addr, proxyRoots).Get(target.URL + "/allowed")
			if err != nil {
				t.Fatalf("승인 대기가 요청 상한보다 길어 요청이 끊김(허용 %d): %v", decided.Load(), err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || string(body) != "approved response" || hits.Load() != 1 || decided.Load() != 1 {
				t.Fatalf("응답 %d %q, 대상 %d, 허용 %d", resp.StatusCode, body, hits.Load(), decided.Load())
			}
		})
	}
}

// 승인 대기가 끝나면 요청 상한을 처음부터 다시 잰다. 승인 뒤 대상이 상한보다 늦으면 프록시 연결은 지금처럼 끊긴다.
func TestProxyRequestTimeoutRestartsAfterApproval(t *testing.T) {
	release := make(chan struct{})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		io.WriteString(w, "late")
	}))
	defer target.Close()
	defer close(release)
	var decidedAt atomic.Int64
	_, call, _ := proxyTestCallWith(t, target.URL, nil, func(c *Config) {
		c.ApprovalMode = ApprovalConfirmEveryRequest
		c.RequestApproval = requestApprovalFunc(func(context.Context, RequestApprovalProposal) (bool, error) {
			time.Sleep(clockTestHold)
			decidedAt.Store(time.Now().UnixNano())
			return true, nil
		})
	})
	_, addr, proxyRoots := proxyTestListenerWith(t, call, clockTestProxy)
	_, err := proxyTestClient(t, addr, proxyRoots).Get(target.URL + "/allowed")
	ended := time.Now()
	if err == nil || decidedAt.Load() == 0 {
		t.Fatalf("승인 뒤 상한을 넘긴 요청이 끊기지 않음(허용 시각 %d)", decidedAt.Load())
	}
	if waited := ended.Sub(time.Unix(0, decidedAt.Load())); waited < clockTestTimeout/2 {
		t.Fatalf("승인 직후 %s 만에 끊김: 상한을 다시 재지 않음", waited)
	}
}

func TestRequestClockPauseRestartAndParent(t *testing.T) {
	newClock := func(parent context.Context) (*requestClock, net.Conn) {
		client, server := net.Pipe()
		t.Cleanup(func() { client.Close(); server.Close() })
		c := startRequestClock(parent, server, clockTestTimeout)
		t.Cleanup(c.cancel)
		return c, server
	}
	t.Run("기한은 DeadlineExceeded", func(t *testing.T) {
		c, _ := newClock(context.Background())
		select {
		case <-c.Done():
		case <-time.After(10 * clockTestTimeout):
			t.Fatal("기한에 닿지 않음")
		}
		if !errors.Is(c.Err(), context.DeadlineExceeded) {
			t.Fatalf("기한 오류 %v", c.Err())
		}
	})
	t.Run("멈춘 동안 기한 없음, 다시 재면 처음부터", func(t *testing.T) {
		c, _ := newClock(context.Background())
		c.pause()
		if _, ok := c.Deadline(); ok {
			t.Fatal("멈춘 동안 기한이 보임")
		}
		select {
		case <-c.Done():
			t.Fatal("멈춘 동안 끝남")
		case <-time.After(3 * clockTestTimeout):
		}
		restarted := time.Now()
		c.restart()
		if d, ok := c.Deadline(); !ok || d.Before(restarted.Add(clockTestTimeout-10*time.Millisecond)) {
			t.Fatalf("다시 잰 기한 %v %v", d, ok)
		}
		select {
		case <-c.Done():
		case <-time.After(10 * clockTestTimeout):
			t.Fatal("다시 잰 뒤 기한에 닿지 않음")
		}
		if waited := time.Since(restarted); waited < clockTestTimeout-10*time.Millisecond || !errors.Is(c.Err(), context.DeadlineExceeded) {
			t.Fatalf("다시 잰 뒤 %s, 오류 %v", waited, c.Err())
		}
	})
	t.Run("멈춘 동안에도 부모 취소는 따른다", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		c, _ := newClock(parent)
		c.pause()
		cancel()
		select {
		case <-c.Done():
		case <-time.After(10 * clockTestTimeout):
			t.Fatal("부모 취소를 따르지 않음")
		}
		if !errors.Is(c.Err(), context.Canceled) {
			t.Fatalf("부모 취소 오류 %v", c.Err())
		}
		c.restart()
		if !errors.Is(c.Err(), context.Canceled) {
			t.Fatal("끝난 시계가 다시 열림")
		}
	})
	t.Run("값과 프록시 밖 요청", func(t *testing.T) {
		c, _ := newClock(context.WithValue(context.Background(), clockTestKey{}, "kept"))
		if requestClockFrom(c) != c || c.Value(clockTestKey{}) != "kept" {
			t.Fatal("문맥 값")
		}
		if requestClockFrom(context.Background()) != nil {
			t.Fatal("프록시 밖 요청에 시계가 있음")
		}
		var none *requestClock
		none.pause()
		none.restart()
	})
}

type clockTestKey struct{}

func clockTestProxy(c *ProxyConfig) {
	c.RequestTimeout, c.HeaderTimeout = clockTestTimeout, clockTestTimeout
}

func clockTestTarget(target *httptest.Server, secured bool) *x509.CertPool {
	if !secured {
		target.Start()
		return nil
	}
	target.StartTLS()
	roots := x509.NewCertPool()
	roots.AddCert(target.Certificate())
	return roots
}
