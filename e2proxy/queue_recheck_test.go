package e2proxy

// #487 리뷰(C20 × C01): 자리를 기다린 요청은 요청 경계와 같은 call의 상한을 다시 본다.

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// C20 × C01: 자리를 기다리는 동안 같은 call이 결과 불명 상한에 닿으면, 줄 서 있던 요청은 보내지 않는다.
func TestQueuedRequestKeepsCallFence(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var got atomic.Int64
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				buf := make([]byte, 2048)
				n, _ := conn.Read(buf)
				if n > 0 {
					got.Add(1)
				}
				entered <- struct{}{}
				<-release
				// 응답 없이 끊는다: 요청을 받은 뒤라 결과 불명.
				conn.Close()
			}(conn)
		}
	}()
	origin := "http://" + listener.Addr().String()
	cfg := coreConfig(t, origin, &coreRecorder{}, 10)
	cfg.Limits.MaxConcurrent = 1
	cfg.Limits.MaxUnknown = 1
	_, c := coreOpen(t, cfg)
	first := doAsync(c, context.Background(), t, origin+"/allowed/a")
	<-entered
	second := doAsync(c, context.Background(), t, origin+"/allowed/b")
	time.Sleep(queueObserve)
	close(release)
	a := <-first
	b := <-second
	_, third, terr := c.Do(context.Background(), coreRequest(t, "GET", origin+"/allowed/c"))
	if a.r.Reason != Unknown || b.r.ForwardAttempted || b.r.Reason != UnknownLimit || third.Reason != UnknownLimit || terr == nil || got.Load() != 1 {
		t.Fatalf("call fence after queue: first=%s second=%s/%v third=%s target=%d", a.r.Reason, b.r.Reason, b.r.ForwardAttempted, third.Reason, got.Load())
	}
}

type toggleBoundary struct {
	deny   atomic.Bool
	before atomic.Int64
}

func (b *toggleBoundary) Before(context.Context, RequestView) error {
	b.before.Add(1)
	if b.deny.Load() {
		return errors.New("paused")
	}
	return nil
}

func (b *toggleBoundary) PreapprovedRead(RequestView) bool { return true }

func (b *toggleBoundary) After(context.Context, RequestView, Receipt) error { return nil }

// C20: 자리를 기다리는 동안 요청 경계(워크플로 일시정지·단계·선택)가 닫히면, 자리를 얻은 뒤 다시 보고 보내지 않는다.
func TestQueuedRequestRechecksBoundary(t *testing.T) {
	g := newGatedTarget(t)
	cfg := coreConfig(t, g.server.URL, &coreRecorder{}, 4)
	cfg.Limits.MaxConcurrent = 1
	boundary := &toggleBoundary{}
	cfg.RequestBoundary = boundary
	e, first := coreOpen(t, cfg)
	second, err := e.BeginCall(t.Context(), "second-worker")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = second.Close(context.Background()) }()
	slow := doAsync(first, context.Background(), t, g.server.URL+"/allowed/slow")
	<-g.entered
	waiting := doAsync(second, context.Background(), t, g.server.URL+"/allowed/fast")
	time.Sleep(queueObserve)
	// 운영자가 워크플로를 일시정지(또는 단계 종료·선택 해제)한 것과 같다: 이후 Before는 거부한다.
	boundary.deny.Store(true)
	close(g.release)
	<-slow
	got := <-waiting
	if got.r.ForwardAttempted || got.r.Reason != PolicyDenied || g.received.Load() != 1 {
		t.Fatalf("request boundary closed while queued: reason=%s forward=%v target=%d", got.r.Reason, got.r.ForwardAttempted, g.received.Load())
	}
}
