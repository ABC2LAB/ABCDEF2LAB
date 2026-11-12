package e2proxy

// #487 리뷰(C20): 자리 대기에 쓴 시간이 송신을 끊거나 거부 상한을 채우지 않는다.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// C20: 대기열에서 호출자 기한(프록시 연결 시간)을 거의 다 쓴 요청도 보낸 뒤에는 E② 요청 시간 상한으로만 끊는다.
// 호출자 기한으로 끊으면 대상이 받은 요청이 결과 불명이 된다(#487 리뷰 P3).
func TestQueueWaitDoesNotCutSend(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var got atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Add(1)
		switch r.URL.Path {
		case "/allowed/hold":
			entered <- struct{}{}
			<-release
		case "/allowed/late":
			time.Sleep(200 * time.Millisecond)
		}
		io.WriteString(w, "ok")
	}))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 4)
	cfg.Limits.MaxConcurrent = 1
	e, first := coreOpen(t, cfg)
	second, err := e.BeginCall(t.Context(), "second-worker")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = second.Close(context.Background()) }()
	slow := doAsync(first, context.Background(), t, target.URL+"/allowed/hold")
	<-entered
	// 프록시가 연결마다 주는 ctx(RequestTimeout)와 같다.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	waiting := doAsync(second, ctx, t, target.URL+"/allowed/late")
	time.Sleep(250 * time.Millisecond)
	close(release)
	<-slow
	res := <-waiting
	if res.r.Reason != Complete || res.err != nil || e.Snapshot().Counts.Unknown != 0 {
		t.Fatalf("queued request cut by the caller deadline: reason=%s err=%v unknown=%d", res.r.Reason, res.err, e.Snapshot().Counts.Unknown)
	}
}

// C20: 자리 대기 중 호출자 시간 초과는 보내지 않은 요청(closed)이고 call 거부 상한에 세지 않는다(#487 리뷰 P3).
func TestQueueTimeoutDoesNotFenceCall(t *testing.T) {
	g := newGatedTarget(t)
	cfg := coreConfig(t, g.server.URL, &coreRecorder{}, 4)
	cfg.Limits.MaxConcurrent = 1
	cfg.Limits.MaxDenials = 1
	e, first := coreOpen(t, cfg)
	second, err := e.BeginCall(t.Context(), "second-worker")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = second.Close(context.Background()) }()
	slow := doAsync(first, context.Background(), t, g.server.URL+"/allowed/slow")
	<-g.entered
	ctx, cancel := context.WithTimeout(context.Background(), queueObserve)
	defer cancel()
	got := <-doAsync(second, ctx, t, g.server.URL+"/allowed/fast")
	close(g.release)
	<-slow
	_, r, err := second.Do(context.Background(), coreRequest(t, "GET", g.server.URL+"/allowed/fast"))
	if got.r.Reason != Closed || got.r.ForwardAttempted || r.Reason != Complete || err != nil || e.Snapshot().Counts.Denied != 1 {
		t.Fatalf("queue timeout fenced the call: wait=%s next=%s err=%v denied=%d", got.r.Reason, r.Reason, err, e.Snapshot().Counts.Denied)
	}
}
