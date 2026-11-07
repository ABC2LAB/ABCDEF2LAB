package e2proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

const (
	// 각 시험은 거부 한 건과 대조 요청 한 건만 쓰므로 예산은 두 건이면 충분하다.
	pinBudget = 2
	// 감사 관문을 붙잡고 있는 동안 기록 기한이 먼저 끝나 엉뚱한 이유(감사 실패)로 막히지 않도록 넉넉히 둔다.
	pinAuditWindow = 10 * time.Second
	// 새 정책이 설치됐는지 관찰하는 상한이다. 정상이라면 즉시 끝나며, 넘기면 고정 실패로 보고한다.
	pinInstallLimit = 5 * time.Second
	// 만료를 늘리거나 줄이는 폭이다. 값 자체보다 방향만 의미가 있다.
	pinExpiryShift = time.Minute
	// 일시 오류 한 번에 실행 전체가 멈춰야 상한 가드가 실제로 동작한 것이다.
	pinTransientLimit = 1
	pinRevisionNext   = "revision2"
)

// pinOutcome 은 별도 고루틴에서 끝난 Do 의 결과를 시험 고루틴으로 넘긴다.
type pinOutcome struct {
	resp *http.Response
	r    Receipt
	err  error
}

func pinDo(ctx context.Context, c *Call, req *http.Request, out chan<- pinOutcome) {
	resp, r, err := c.Do(ctx, req)
	out <- pinOutcome{resp: resp, r: r, err: err}
}

// 변이된 코드에서 응답이 돌아오면 본문을 닫아 다음 반복에 자원이 새지 않게 한다.
func (o pinOutcome) close() {
	if o.resp != nil {
		o.resp.Body.Close()
	}
}

func pinRestriction(revision string, expires time.Time) Restriction {
	return Restriction{PolicyRevision: revision, ExpiresAt: expires, RuleRefs: []string{"rule"}, Methods: []string{"GET"}}
}

// Restrict 는 감사 잠금을 기다리는 동안 알려 주는 통로가 없다. 그래서 정해진 시간만큼 잠드는 대신
// 새 개정 번호가 실제로 설치된 상태를 조건으로 삼아 기다린다.
func pinWaitForRevision(t *testing.T, e *Execution, revision string) {
	t.Helper()
	deadline := time.Now().Add(pinInstallLimit)
	for e.Snapshot().PolicyRevision != revision {
		if time.Now().After(deadline) {
			t.Fatal("policy reduction was never installed")
		}
		runtime.Gosched()
	}
}

func pinCountingTarget(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	received := &atomic.Int64{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		io.WriteString(w, "pin target")
	}))
	t.Cleanup(target.Close)
	return target, received
}

// 공백 1(연결): Restrict 는 context 를 취소하지 않으므로, 이미 감사까지 통과한 요청을
// 막는 것은 연결 직전 재검사뿐이다. 쓰기 재검사가 남아 있어도 연결 자체가 시도되면 안 된다.
func TestGuardPinConnectRecheckRefusesRestrictedInFlightRequest(t *testing.T) {
	target, received := pinCountingTarget(t)
	rec := &coreRecorder{blockStage: "intent", entered: make(chan struct{}, 1), release: make(chan struct{})}
	cfg := coreConfig(t, target.URL, rec, pinBudget)
	cfg.Limits.RequestTimeout = pinAuditWindow
	e, c := coreOpen(t, cfg)
	req := coreRequest(t, "GET", target.URL+"/allowed")
	done := make(chan pinOutcome, 1)
	go pinDo(context.Background(), c, req, done)
	// 요청은 예약 전 재검사를 지나 intent 감사 기록에서 멈춰 있다.
	<-rec.entered
	restricted := make(chan error, 1)
	go func() {
		restricted <- e.Restrict(context.Background(), pinRestriction(pinRevisionNext, cfg.ExpiresAt.Add(-pinExpiryShift)))
	}()
	pinWaitForRevision(t, e, pinRevisionNext)
	close(rec.release)
	got := <-done
	defer got.close()
	if err := <-restricted; err != nil {
		t.Fatalf("restriction failed: %v", err)
	}
	if got.err == nil || got.resp != nil || got.r.ForwardAttempted || !got.r.NotSent || got.r.Reason != TransportFailed {
		t.Fatalf("restricted in-flight request reached connection: %+v %v", got.r, got.err)
	}
	if n := received.Load(); n != 0 {
		t.Fatalf("target received %d requests after restriction", n)
	}
}

// 공백 1(쓰기): 연결이 이미 열린 뒤 정책이 개정되면 그 연결로는 한 바이트도 나가면 안 된다.
// 이 시험은 쓰기 경로 자체를 실행하므로 만료 시점이 이른 기존 시험이 놓친 부분을 채운다.
func TestGuardPinWriteRecheckRefusesRestrictedConnection(t *testing.T) {
	const beforeProbe, afterProbe = "before-restrict", "after-restrict"
	cfg := coreConfig(t, "http://127.0.0.1", &coreRecorder{}, pinBudget)
	// 파이프 기한이 대조 쓰기 도중 끝나 엉뚱한 이유로 실패하지 않게 한다.
	cfg.Limits.RequestTimeout = pinAuditWindow
	e, c := coreOpen(t, cfg)
	local, peer := net.Pipe()
	defer peer.Close()
	ob := &observed{}
	conn, err := newDeadlineGuardedConn(local, c, cfg.PolicyRevision, ob)
	if err != nil {
		t.Fatalf("guarded connection setup: %v", err)
	}
	defer conn.Close()
	peerBytes := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(peer)
		peerBytes <- b
	}()
	// 대조: 개정 전에는 같은 연결로 쓰기가 통과해야 거부가 가드 때문임을 보일 수 있다.
	if n, err := conn.Write([]byte(beforeProbe)); err != nil || n != len(beforeProbe) {
		t.Fatalf("positive control write: %d %v", n, err)
	}
	// 대조 쓰기가 남긴 송신 표시를 지워야 거부된 쓰기의 표시만 따로 볼 수 있다.
	ob.forward.Store(false)
	if err := e.Restrict(context.Background(), pinRestriction(pinRevisionNext, cfg.ExpiresAt.Add(-pinExpiryShift))); err != nil {
		t.Fatalf("restriction failed: %v", err)
	}
	n, werr := conn.Write([]byte(afterProbe))
	conn.Close()
	got := <-peerBytes
	if !errors.Is(werr, ErrDenied) || n != 0 || ob.forward.Load() {
		t.Fatalf("write after restriction was admitted: n=%d err=%v forward=%t", n, werr, ob.forward.Load())
	}
	if string(got) != beforeProbe {
		t.Fatalf("peer received %q after restriction", got)
	}
}

// 공백 2: https 규칙이 같은 host:port 의 평문 http 요청을 허용하면 자격증명이 TLS 없이 나간다.
// 연결 검사도 host:port 만 비교하므로 스킴 비교가 유일한 방어선이다.
func TestGuardPinSchemeMismatchIsDenied(t *testing.T) {
	target, received := pinCountingTarget(t)
	_, port, err := net.SplitHostPort(target.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	cfg := coreConfig(t, "https://127.0.0.1:"+port, &coreRecorder{}, pinBudget)
	e, c := coreOpen(t, cfg)
	resp, r, err := c.Do(context.Background(), coreRequest(t, "GET", "http://127.0.0.1:"+port+"/allowed"))
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || resp != nil || !r.NotSent || r.ForwardAttempted || r.Decision != "denied" || r.Reason != PolicyDenied {
		t.Fatalf("plain http request matched https rule: %+v %v", r, err)
	}
	if n := received.Load(); n != 0 {
		t.Fatalf("target received %d plain requests", n)
	}
	if remaining, err := e.BudgetRemaining(); err != nil || remaining != pinBudget {
		t.Fatalf("scheme denial spent budget: %d %v", remaining, err)
	}
}

// 공백 3: Restrict 는 축소만 허용한다. 만료를 늘리는 요청은 개정 번호가 새것이어도 거부돼야 한다.
func TestGuardPinRestrictRefusesExpiryExtension(t *testing.T) {
	cfg := coreConfig(t, "http://127.0.0.1", &coreRecorder{}, pinBudget)
	e, _ := coreOpen(t, cfg)
	events := len(e.Events(0, maxEventPageSize))
	err := e.Restrict(context.Background(), pinRestriction(pinRevisionNext, cfg.ExpiresAt.Add(pinExpiryShift)))
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expiry extension accepted: %v", err)
	}
	if p := e.Policy(); !p.ExpiresAt.Equal(cfg.ExpiresAt) || p.PolicyRevision != cfg.PolicyRevision || len(e.Events(0, maxEventPageSize)) != events {
		t.Fatalf("refused extension changed policy: %+v", p)
	}
	// 대조: 만료만 줄인 같은 요청은 받아들여져야 거부가 만료 비교 때문임을 보인다.
	if err := e.Restrict(context.Background(), pinRestriction(pinRevisionNext, cfg.ExpiresAt.Add(-pinExpiryShift))); err != nil {
		t.Fatalf("positive control restriction: %v", err)
	}
}

// 공백 6: 같은 개정 번호를 다시 쓰면 진행 중 요청이 개정 비교로 막히지 않는다.
// 그래서 개정 번호를 바꾸지 않는 Restrict 는 거부돼야 한다.
func TestGuardPinRestrictRefusesSameRevision(t *testing.T) {
	cfg := coreConfig(t, "http://127.0.0.1", &coreRecorder{}, pinBudget)
	e, _ := coreOpen(t, cfg)
	events := len(e.Events(0, maxEventPageSize))
	reduced := cfg.ExpiresAt.Add(-pinExpiryShift)
	err := e.Restrict(context.Background(), pinRestriction(cfg.PolicyRevision, reduced))
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("same revision accepted: %v", err)
	}
	if p := e.Policy(); !p.ExpiresAt.Equal(cfg.ExpiresAt) || p.PolicyRevision != cfg.PolicyRevision || len(e.Events(0, maxEventPageSize)) != events {
		t.Fatalf("refused same revision changed policy: %+v", p)
	}
	// 대조: 개정 번호만 바꾼 같은 축소는 받아들여져야 한다.
	if err := e.Restrict(context.Background(), pinRestriction(pinRevisionNext, reduced)); err != nil {
		t.Fatalf("positive control restriction: %v", err)
	}
}

// 공백 7: 인코딩된 구분자(%2F)는 path.Clean 으로 걸러지지 않으므로 RawPath 검사만이 막는다.
func TestGuardPinEncodedSlashIsDenied(t *testing.T) {
	target, received := pinCountingTarget(t)
	_, c := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, pinBudget))
	req := coreRequest(t, "GET", target.URL+"/allowed%2Fx")
	if req.URL.RawPath == "" {
		t.Fatal("fixture did not produce an encoded path")
	}
	resp, r, err := c.Do(context.Background(), req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || resp != nil || !r.NotSent || r.ForwardAttempted || r.Decision != "denied" || r.Reason != PolicyDenied {
		t.Fatalf("encoded path admitted: %+v %v", r, err)
	}
	if n := received.Load(); n != 0 {
		t.Fatalf("target received %d encoded requests", n)
	}
	// 대조: 디코딩된 같은 경로는 허용돼야 거부가 인코딩 때문임을 보인다.
	resp, r, err = c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed/x"))
	if err != nil || resp == nil || !r.ResponseComplete {
		t.Fatalf("positive control: %+v %v", r, err)
	}
	resp.Body.Close()
	if n := received.Load(); n != 1 {
		t.Fatalf("target received %d requests; want only the positive control", n)
	}
}

// 공백 8: 연결 전에 끝난 송신 실패도 상한에 닿으면 실행 전체를 멈춰야 한다.
// intent 감사 대기 중 Call.Block 으로 연결 시도 없는 일시 오류를 만든다.
func TestGuardPinTransientLimitStopsExecution(t *testing.T) {
	target, received := pinCountingTarget(t)
	rec := &coreRecorder{blockStage: "intent", entered: make(chan struct{}, 1), release: make(chan struct{})}
	cfg := coreConfig(t, target.URL, rec, pinBudget)
	cfg.Limits.RequestTimeout = pinAuditWindow
	cfg.Limits.MaxTransient = pinTransientLimit
	e, c := coreOpen(t, cfg)
	req := coreRequest(t, "GET", target.URL+"/allowed")
	done := make(chan pinOutcome, 1)
	go pinDo(context.Background(), c, req, done)
	<-rec.entered
	c.Block()
	close(rec.release)
	got := <-done
	defer got.close()
	// 전제 확인: 이 결과가 아니면 상한 가드가 아니라 설정이 틀린 것이다.
	if got.r.Reason != TransportFailed || got.r.ForwardAttempted || received.Load() != 0 {
		t.Fatalf("fixture did not produce a pre-connection transient failure: %+v %v", got.r, got.err)
	}
	if view := e.Snapshot(); view.Reason != TransientLimit || view.State != "blocked" {
		t.Fatalf("transient limit did not stop execution: %+v", view)
	}
	if _, err := e.BeginCall(context.Background(), "after-transient-limit"); err == nil {
		t.Fatal("stopped execution admitted a new call")
	}
}
