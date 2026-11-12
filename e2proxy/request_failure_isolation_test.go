package e2proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// 요청 단위 실패 격리(C01·C02·C03) 시험이다. 정상 요청의 실제 전달과 막아야 할 요청의 차단을 함께 본다.

const (
	// 여러 Worker 시험에서 정상 요청을 보내는 call 수다. 실패 call과 섞여도 모두 전달돼야 한다.
	isolationHealthyCalls = 6
	// 여러 Worker 시험의 회차 송신 예산이다. 정상 call, 예약까지 가는 실패 call 2개(연결 실패·결과 불명),
	// 마지막 감사 실패 확인 1건의 몫을 담는다.
	isolationBudget = isolationHealthyCalls + 3
)

func closedLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func eventCount(rec *coreRecorder) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.events)
}

// C01: 거부 상한은 그 call만 막는다. 막힌 call의 새 요청은 감사 기록을 늘리지 않고, 다른 call은 계속 보낸다.
func TestDenialLimitFencesOnlyItsCall(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); io.WriteString(w, "ok") }))
	defer target.Close()
	rec := &coreRecorder{}
	cfg := coreConfig(t, target.URL, rec, 4)
	cfg.Limits.MaxDenials = 2
	e, fenced := coreOpen(t, cfg)
	for i := 0; i < 2; i++ {
		if _, r, err := fenced.Do(t.Context(), coreRequest(t, "GET", target.URL+"/outside")); !errors.Is(err, ErrDenied) || r.Reason != PolicyDenied {
			t.Fatal("outside request was not denied", r.Reason, err)
		}
	}
	before := eventCount(rec)
	_, r, err := fenced.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
	if !errors.Is(err, ErrClosed) || r.Reason != DenialLimit || !r.NotSent || r.AttemptID != "" || received.Load() != 0 {
		t.Fatal("fenced call admitted a new request", r, err)
	}
	if outcome := proxyOutcomeOf(r, err); outcome.class != OutcomeNotSent || outcome.reason != DenialLimit {
		t.Fatalf("model-facing outcome lost the call limit reason: %+v", outcome)
	}
	if eventCount(rec) != before {
		t.Fatal("fenced call kept growing the audit journal")
	}
	if view := e.Snapshot(); view.State != "active" || view.Reason != Complete {
		t.Fatalf("one call's denials stopped the execution: %+v", view)
	}
	other, err := e.BeginCall(t.Context(), "healthy-command")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = other.Close(t.Context()) }()
	resp, r, err := other.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err != nil || r.Reason != Complete || received.Load() != 1 {
		t.Fatal("approved request in another call was not delivered", r, err)
	}
	resp.Body.Close()
	// 거부 정책 자체는 그대로다.
	if _, r, err = other.Do(t.Context(), coreRequest(t, "POST", target.URL+"/allowed")); err == nil || r.Reason != PolicyDenied || received.Load() != 1 {
		t.Fatal("method outside the approved scope was sent")
	}
}

// C01: 실행 전체를 멈추는 사유(감사 실패·철회·운영자 중지)는 call 단위 격리 뒤에도 새 송신을 멈춘다.
func TestExecutionWideStopsStillFenceEveryCall(t *testing.T) {
	for _, stop := range []string{"audit", "revoke", "operator"} {
		t.Run(stop, func(t *testing.T) {
			var received atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); io.WriteString(w, "ok") }))
			defer target.Close()
			rec := &coreRecorder{}
			e, first := coreOpen(t, coreConfig(t, target.URL, rec, 8))
			second, err := e.BeginCall(t.Context(), "second-command")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = second.Close(t.Context()) }()
			switch stop {
			case "audit":
				rec.mu.Lock()
				rec.failStage = "intent"
				rec.mu.Unlock()
				if _, r, err := first.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed")); !errors.Is(err, ErrAudit) || r.ForwardAttempted {
					t.Fatal("audit failure did not refuse the send", r, err)
				}
			case "revoke":
				if err := e.Revoke(t.Context()); err != nil {
					t.Fatal(err)
				}
			case "operator":
				if err := e.Stop(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := second.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed")); err == nil || received.Load() != 0 {
				t.Fatal("execution-wide stop did not fence another call")
			}
			if _, err := e.BeginCall(t.Context(), "after-stop"); err == nil {
				t.Fatal("stopped execution admitted a new call")
			}
			if view := e.Snapshot(); view.State != "blocked" {
				t.Fatalf("execution-wide stop was not visible: %+v", view)
			}
		})
	}
}

// C02: 대상 TCP 연결을 맺지 못한 실패는 결과 불명이 아니라 송신 실패다. 연결 시도는 감사에 남는다.
func TestConnectFailureIsTransportFailedNotUnknown(t *testing.T) {
	origin := "http://127.0.0.1:" + strconv.Itoa(closedLoopbackPort(t))
	e, c := coreOpen(t, coreConfig(t, origin, &coreRecorder{}, 2))
	resp, r, err := c.Do(t.Context(), coreRequest(t, "GET", origin+"/allowed"))
	if err == nil || resp != nil {
		t.Fatal("closed port produced a response")
	}
	if r.Reason != TransportFailed || !r.ForwardAttempted || r.RequestWritten || r.ResponseReceived || !r.AuditComplete {
		t.Fatalf("connect failure classification: %+v", r)
	}
	if outcome := proxyOutcomeOf(r, err); outcome.class != OutcomeNotSent || outcome.reason != TransportFailed {
		t.Fatalf("connect failure was presented as possibly received: %+v", outcome)
	}
	view := e.Snapshot()
	if view.Counts.Unknown != 0 || view.Counts.Transient != 1 || view.Counts.ForwardAttempted != 1 || view.State != "active" {
		t.Fatalf("connect failure counted as unknown or stopped the execution: %+v", view)
	}
}

// C02: TLS 검증 실패(신뢰하지 않는 인증서, SPKI 고정 불일치)는 요청을 쓰기 전에 끝나므로 송신 실패다.
func TestTLSVerificationFailureIsTransportFailed(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	for _, name := range []string{"untrusted", "spki_mismatch"} {
		t.Run(name, func(t *testing.T) {
			cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
			if name == "spki_mismatch" {
				cfg.RootCAs = target.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
				wrong := sha256.Sum256([]byte("not the target key"))
				cfg.Rules[0].TLSSPKISHA256 = hex.EncodeToString(wrong[:])
			}
			e, c := coreOpen(t, cfg)
			_, r, err := c.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
			if err == nil || r.Reason != TransportFailed || !r.ForwardAttempted || r.RequestWritten || received.Load() != 0 {
				t.Fatalf("TLS verification failure: %+v %v received=%d", r, err, received.Load())
			}
			if e.Snapshot().Counts.Unknown != 0 {
				t.Fatal("TLS failure counted as unknown")
			}
		})
	}
	// 양성 대조: 같은 고정값이 맞으면 실제로 전달된다.
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
	cfg.RootCAs = target.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	sum := sha256.Sum256(target.Certificate().RawSubjectPublicKeyInfo)
	cfg.Rules[0].TLSSPKISHA256 = hex.EncodeToString(sum[:])
	_, c := coreOpen(t, cfg)
	resp, r, err := c.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err != nil || r.Reason != Complete || received.Load() != 1 {
		t.Fatal("pinned trusted target was not reached", r, err)
	}
	resp.Body.Close()
}

// C02 음성 대조: 연결을 맺고 요청을 쓴 뒤 응답 없이 끊기면 대상이 받았을 수 있으므로 결과 불명으로 남는다.
func TestFailureAfterConnectionStaysUnknown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var got atomic.Int64
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 512)
			if n, _ := conn.Read(buf); n > 0 {
				got.Add(1)
			}
			conn.Close()
		}
	}()
	origin := "http://" + listener.Addr().String()
	e, c := coreOpen(t, coreConfig(t, origin, &coreRecorder{}, 2))
	_, r, err := c.Do(t.Context(), coreRequest(t, "GET", origin+"/allowed"))
	if err == nil || r.Reason != Unknown || !r.ForwardAttempted || !r.RequestWritten || got.Load() != 1 {
		t.Fatalf("post-connection loss was not kept unknown: %+v %v", r, err)
	}
	if outcome := proxyOutcomeOf(r, err); outcome.class != OutcomeUnknown {
		t.Fatalf("possibly received request was presented as not sent: %+v", outcome)
	}
	if e.Snapshot().Counts.Unknown != 1 {
		t.Fatal("unknown accounting")
	}
}

// C03: 한 intent의 결과 불명은 그 intent의 누계에만 잡힌다. 실행 전체 누계는 그대로 남긴다.
func TestIntentCountsIsolateUnknownAcrossIntents(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.URL.Path == "/allowed/broken" {
			abortMidBody(w)
		}
		io.WriteString(w, "ok")
	}))
	defer target.Close()
	e, first := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 4))
	if _, r, _ := first.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed/broken")); r.Reason != Unknown {
		t.Fatal("fixture did not produce an unknown outcome", r.Reason)
	}
	if err := e.RegisterIntent(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	second, err := e.BeginCallForIntent(t.Context(), 2, "second-intent-command")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = second.Close(t.Context()) }()
	resp, r, err := second.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed/plain"))
	if err != nil || r.Reason != Complete || received.Load() != 2 {
		t.Fatal("another intent's unknown outcome blocked an approved request", r, err)
	}
	resp.Body.Close()
	one, two := e.SnapshotForIntent(1), e.SnapshotForIntent(2)
	if one.IntentCounts.Unknown != 1 || two.IntentCounts.Unknown != 0 || two.IntentCounts.Allowed != 1 || two.IntentCounts.Calls != 1 {
		t.Fatalf("intent counts leaked across intents: one=%+v two=%+v", one.IntentCounts, two.IntentCounts)
	}
	if one.Counts.Unknown != 1 || two.Counts.Unknown != 1 || two.State != "active" {
		t.Fatalf("execution-wide counts changed meaning: %+v", two)
	}
	if unknown := e.SnapshotForIntent(99); unknown.IntentCounts != (Counts{}) || unknown.Reason != Invalid {
		t.Fatalf("unregistered intent exposed counts: %+v", unknown)
	}
}

// C01·C02·C03 결합: 여러 Worker가 동시에 거부·연결 실패·결과 불명을 겪어도 다른 call의 정상 요청은
// 전달되고, 회차 예산을 넘지 않는다. 그 뒤 감사 실패는 여전히 모든 새 송신을 멈춘다.
func TestConcurrentWorkersIsolateRequestFailures(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.URL.Path == "/allowed/broken" {
			abortMidBody(w)
		}
		io.WriteString(w, "ok")
	}))
	defer target.Close()
	closedOrigin := "http://127.0.0.1:" + strconv.Itoa(closedLoopbackPort(t))
	rec := &coreRecorder{}
	cfg := coreConfig(t, target.URL, rec, isolationBudget)
	cfg.Rules = append(cfg.Rules, Rule{Ref: "closed-rule", Origin: closedOrigin, ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/allowed", PathScopeRef: "closed-prefix", Methods: []string{"GET"}})
	cfg.Limits.MaxDenials, cfg.Limits.MaxUnknown, cfg.Limits.MaxTransient = 1, 1, 1
	e, anchor := coreOpen(t, cfg)
	if err := e.RegisterIntent(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	type job struct {
		intent int64
		url    string
		want   Reason
	}
	jobs := []job{{1, target.URL + "/outside", PolicyDenied}, {2, closedOrigin + "/allowed", TransportFailed}, {1, target.URL + "/allowed/broken", Unknown}}
	for i := 0; i < isolationHealthyCalls; i++ {
		jobs = append(jobs, job{int64(1 + i%2), target.URL + "/allowed/plain", Complete})
	}
	var wg sync.WaitGroup
	errs := make(chan string, len(jobs))
	for i, j := range jobs {
		call, err := e.BeginCallForIntent(t.Context(), j.intent, "worker-"+strconv.Itoa(i))
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func(call *Call, j job) {
			defer wg.Done()
			resp, r, _ := call.Do(context.Background(), coreRequest(t, "GET", j.url))
			if resp != nil {
				resp.Body.Close()
			}
			if r.Reason != j.want {
				errs <- string(j.want) + " got " + string(r.Reason)
			}
			if _, err := call.Close(context.Background()); err != nil {
				errs <- "close " + err.Error()
			}
		}(call, j)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
	view := e.Snapshot()
	if view.State != "active" || view.Counts.Denied != 1 || view.Counts.Transient != 1 || view.Counts.Unknown != 1 || view.Counts.Allowed > isolationBudget {
		t.Fatalf("combined failure accounting: %+v", view)
	}
	// 대상에 실제로 닿은 요청 수는 정상 call 수와 결과 불명 1건뿐이다(연결 실패는 닿지 않음).
	if received.Load() != isolationHealthyCalls+1 {
		t.Fatalf("target received %d", received.Load())
	}
	if remaining, err := e.BudgetRemaining(); err != nil || remaining != 1 {
		t.Fatal("budget accounting", remaining, err)
	}
	// 감사 실패는 여전히 실행 전체를 멈춘다.
	rec.mu.Lock()
	rec.failStage = "intent"
	rec.mu.Unlock()
	if _, _, err := anchor.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed/plain")); !errors.Is(err, ErrAudit) {
		t.Fatal("audit failure was not reported", err)
	}
	if _, err := e.BeginCall(t.Context(), "after-audit-failure"); err == nil || e.Snapshot().Reason != AuditFailed {
		t.Fatal("audit failure did not stop new sends")
	}
	if received.Load() != isolationHealthyCalls+1 {
		t.Fatal("audit failure request reached the target")
	}
}

// 리뷰 재현(#477): intent 누계의 시도 수는 그 intent의 call이 접수한 요청을 센다. 차단된 call의 거절은 접수가 아니다.
func TestIntentCountsIncludeAcceptedAttempts(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 4)
	cfg.Limits.MaxDenials = 1
	e, c := coreOpen(t, cfg)
	resp, _, err := c.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	c.Do(t.Context(), coreRequest(t, "GET", target.URL+"/outside"))
	if _, r, err := c.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed")); !errors.Is(err, ErrClosed) || r.Reason != DenialLimit {
		t.Fatal("fixture did not fence the call", r.Reason, err)
	}
	view := e.SnapshotForIntent(1)
	if view.IntentCounts.Attempts != 2 || view.IntentCounts.Allowed != 1 || view.IntentCounts.Denied != 1 || view.Counts.Attempts != 2 {
		t.Fatalf("intent attempts do not match accepted requests: intent=%+v execution=%+v", view.IntentCounts, view.Counts)
	}
}

// abortMidBody는 응답 본문 도중 연결을 끊는다. 대상이 요청을 받은 뒤 끝까지 답했는지 모르는 결과 불명을 만든다.
// (자격 응답 보류는 C16부터 결과 불명이 아니라 withheld다.)
func abortMidBody(w http.ResponseWriter) {
	w.Header().Set("Content-Length", "64")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "partial")
	w.(http.Flusher).Flush()
	panic(http.ErrAbortHandler)
}
