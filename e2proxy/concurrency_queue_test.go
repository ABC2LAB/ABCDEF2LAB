package e2proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 동시 송신 대기열(C20). 자리가 없으면 거부하지 않고 기다리고, 설정한 동시 송신 수는 넘지 않는다.

const (
	// 대기 중인 요청이 끝나지 않았음을 확인하는 짧은 관찰 시간이다. 정상이라면 이 동안 계속 기다린다.
	queueObserve = 50 * time.Millisecond
	queueLimit   = 2
	queueBurst   = 12
)

type gatedTarget struct {
	server   *httptest.Server
	entered  chan struct{}
	release  chan struct{}
	inflight atomic.Int64
	maxSeen  atomic.Int64
	received atomic.Int64
}

func newGatedTarget(t *testing.T) *gatedTarget {
	g := &gatedTarget{entered: make(chan struct{}, queueBurst), release: make(chan struct{})}
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.received.Add(1)
		now := g.inflight.Add(1)
		for {
			seen := g.maxSeen.Load()
			if now <= seen || g.maxSeen.CompareAndSwap(seen, now) {
				break
			}
		}
		if r.URL.Path == "/allowed/slow" {
			g.entered <- struct{}{}
			<-g.release
		}
		g.inflight.Add(-1)
		io.WriteString(w, "ok")
	}))
	t.Cleanup(g.server.Close)
	return g
}

type doOutcome struct {
	r   Receipt
	err error
}

func doAsync(c *Call, ctx context.Context, t *testing.T, url string) chan doOutcome {
	out := make(chan doOutcome, 1)
	go func() {
		resp, r, err := c.Do(ctx, coreRequest(t, "GET", url))
		if resp != nil {
			resp.Body.Close()
		}
		out <- doOutcome{r, err}
	}()
	return out
}

func TestConcurrentRequestWaitsForSlotInsteadOfDenial(t *testing.T) {
	g := newGatedTarget(t)
	cfg := coreConfig(t, g.server.URL, &coreRecorder{}, 4)
	cfg.Limits.MaxConcurrent = 1
	e, first := coreOpen(t, cfg)
	second, err := e.BeginCall(t.Context(), "second-worker")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = second.Close(context.Background()) }()
	slow := doAsync(first, context.Background(), t, g.server.URL+"/allowed/slow")
	<-g.entered
	fast := doAsync(second, context.Background(), t, g.server.URL+"/allowed/fast")
	select {
	case got := <-fast:
		t.Fatal("request did not wait for the concurrency slot", got.r.Reason, got.err)
	case <-time.After(queueObserve):
	}
	if e.Snapshot().Counts.Denied != 0 {
		t.Fatal("waiting was counted as a denial")
	}
	close(g.release)
	for _, ch := range []chan doOutcome{slow, fast} {
		if got := <-ch; got.err != nil || got.r.Reason != Complete {
			t.Fatal("queued request did not complete", got.r.Reason, got.err)
		}
	}
	if g.maxSeen.Load() != 1 || e.Snapshot().Counts.Denied != 0 {
		t.Fatal("concurrency limit exceeded or wait counted as denial", g.maxSeen.Load())
	}
}

func TestWaitingRequestEndsWithoutSendOnCancelOrRevoke(t *testing.T) {
	for _, stop := range []string{"cancel", "revoke"} {
		t.Run(stop, func(t *testing.T) {
			g := newGatedTarget(t)
			cfg := coreConfig(t, g.server.URL, &coreRecorder{}, 4)
			cfg.Limits.MaxConcurrent = 1
			e, first := coreOpen(t, cfg)
			second, err := e.BeginCall(t.Context(), "second-worker")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = second.Close(context.Background()) }()
			slow := doAsync(first, context.Background(), t, g.server.URL+"/allowed/slow")
			<-g.entered
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			waiting := doAsync(second, ctx, t, g.server.URL+"/allowed/fast")
			time.Sleep(queueObserve)
			if stop == "cancel" {
				cancel()
			} else if err := e.Revoke(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := <-waiting
			if got.err == nil || got.r.ForwardAttempted || got.r.Decision == "allowed" {
				t.Fatalf("waiting request was sent after %s: %+v %v", stop, got.r, got.err)
			}
			close(g.release)
			<-slow
			if g.received.Load() != 1 {
				t.Fatal("stopped waiting request reached the target", g.received.Load())
			}
			if stop == "cancel" {
				// 기다리다 끝난 요청은 예산을 쓰지 않는다. 먼저 보낸 1건만 예약됐다.
				if remaining, err := e.BudgetRemaining(); err != nil || remaining != 3 {
					t.Fatal("canceled waiting request consumed budget", remaining, err)
				}
			}
		})
	}
}

func TestApprovalWaitDoesNotHoldConcurrencySlot(t *testing.T) {
	g := newGatedTarget(t)
	release := make(chan struct{})
	waitingApproval := make(chan struct{}, 1)
	cfg := coreConfig(t, g.server.URL, &coreRecorder{}, 4)
	cfg.Limits.MaxConcurrent = 1
	cfg.ApprovalMode = ApprovalConfirmEveryRequest
	cfg.RequestApproval = requestApprovalFunc(func(ctx context.Context, p RequestApprovalProposal) (bool, error) {
		if p.URL == g.server.URL+"/allowed/held" {
			waitingApproval <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		return true, nil
	})
	e, first := coreOpen(t, cfg)
	second, err := e.BeginCall(t.Context(), "second-worker")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = second.Close(context.Background()) }()
	held := doAsync(first, context.Background(), t, g.server.URL+"/allowed/held")
	<-waitingApproval
	select {
	case got := <-doAsync(second, context.Background(), t, g.server.URL+"/allowed/fast"):
		if got.err != nil || got.r.Reason != Complete {
			t.Fatal("request behind a pending approval failed", got.r.Reason, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending human approval held the concurrency slot")
	}
	close(release)
	if got := <-held; got.err != nil || got.r.Reason != Complete || g.received.Load() != 2 {
		t.Fatal("approved request did not complete", got.r.Reason, got.err)
	}
}

func TestConcurrencyLimitHoldsUnderBurst(t *testing.T) {
	g := newGatedTarget(t)
	close(g.release)
	cfg := coreConfig(t, g.server.URL, &coreRecorder{}, queueBurst)
	cfg.Limits.MaxConcurrent = queueLimit
	e, anchor := coreOpen(t, cfg)
	calls := []*Call{anchor}
	for i := 0; i < 2; i++ {
		c, err := e.BeginCall(t.Context(), "burst-"+strconv.Itoa(i))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = c.Close(context.Background()) }()
		calls = append(calls, c)
	}
	var wg sync.WaitGroup
	var failed atomic.Int64
	for i := 0; i < queueBurst; i++ {
		wg.Add(1)
		go func(c *Call) {
			defer wg.Done()
			resp, r, err := c.Do(context.Background(), coreRequest(t, "GET", g.server.URL+"/allowed/slow"))
			if resp != nil {
				resp.Body.Close()
			}
			if err != nil || r.Reason != Complete {
				failed.Add(1)
			}
		}(calls[i%len(calls)])
	}
	wg.Wait()
	if failed.Load() != 0 || g.maxSeen.Load() > queueLimit || g.received.Load() != queueBurst || e.Snapshot().Counts.Denied != 0 {
		t.Fatal("burst", failed.Load(), g.maxSeen.Load(), g.received.Load(), e.Snapshot().Counts)
	}
}
