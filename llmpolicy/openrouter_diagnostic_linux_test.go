//go:build linux

package llmpolicy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/internal/diagnosticlog"
)

func routerDiagnosticFixture(t *testing.T) (context.Context, *diagnosticlog.Logger) {
	t.Helper()
	log, err := diagnosticlog.Create(t.TempDir(), "run", 42, "synthetic-run")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return diagnosticlog.WithRole(diagnosticlog.With(context.Background(), log), "worker"), log
}
func routerDiagnosticEvents(t *testing.T, log *diagnosticlog.Logger) []diagnosticlog.Event {
	t.Helper()
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(log.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []diagnosticlog.Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event diagnosticlog.Event
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			t.Fatal("invalid event JSON")
		}
		for _, forbidden := range []string{"private-request-marker", "private-provider-error", "private-key-marker", "fixture/endpoint"} {
			if strings.Contains(scanner.Text(), forbidden) {
				t.Fatal("private value persisted")
			}
		}
		out = append(out, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestOpenRouterDiagnosticRequestOutcomes(t *testing.T) {
	cases := []struct {
		name, reason, reply string
		status              int
		err                 error
	}{
		{name: "success", reply: openRouterReply, status: 200},
		{name: "unauthorized", status: 401, reason: "http_unauthorized"},
		{name: "rate_limit", status: 429, reason: "http_rate_limited"},
		{name: "server_error", status: 503, reason: "http_server_error"},
		{name: "client_error", status: 400, reason: "http_client_error"},
		{name: "transport", err: errors.New("private-provider-error"), reason: "transport_failure"},
		{name: "timeout", err: context.DeadlineExceeded, reason: "timeout"},
		{name: "cancelled", err: context.Canceled, reason: "cancelled"},
		{name: "bad_json", status: 200, reply: "private-provider-error", reason: "response_invalid"},
		{name: "bad_tool", status: 200, reply: strings.Replace(openRouterReply, `"finish_reason":"stop"`, `"finish_reason":"tool_calls"`, 1), reason: "response_invalid"},
		{name: "unknown_cost", status: 200, reply: strings.Replace(openRouterReply, `"cost":0.000012`, `"other":0`, 1), reason: "accounting_unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, log := routerDiagnosticFixture(t)
			p := openRouterFixture(t)
			hits := 0
			tr := &openRouterTransport{policy: p, base: openRouterRT(func(*http.Request) (*http.Response, error) {
				hits++
				if tc.err != nil {
					return nil, tc.err
				}
				reply := tc.reply
				if reply == "" {
					reply = "private-provider-error"
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(reply)), Header: make(http.Header)}, nil
			})}
			req := openRouterRequest().WithContext(ctx)
			req.Header.Set("Authorization", "private-key-marker")
			req.Body = io.NopCloser(strings.NewReader(strings.Replace(`{"model":"qwen/qwen3.6-35b-a3b","messages":[{"role":"user","content":"private-request-marker"}],"max_tokens":100,"stream":false}`, "unused", "", 1)))
			resp, err := tr.RoundTrip(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if (err == nil) != (tc.reason == "") {
				t.Fatal("unexpected request result")
			}
			events := routerDiagnosticEvents(t, log)
			if hits != 1 || len(events) != 2 || events[0].Event != "model_request_started" || events[1].Event != "model_request_finished" {
				t.Fatal("request lifecycle does not match send")
			}
			end := events[1]
			if end.ReasonCode != tc.reason || end.TaskID != 42 || end.Role != "worker" || end.Model != OpenRouterModel || len(end.ProviderRef) != 64 || end.RunRef != events[0].RunRef {
				t.Fatal("wrong diagnostic identity or classification")
			}
			usage := p.Snapshot()
			if end.ReservedInputTokens != usage.ReservedInputTokens || end.ReservedOutputTokens != usage.ReservedOutputTokens || end.ReservedCostMicros != usage.ReservedCostMicros || end.InputTokens != usage.InputTokens || end.OutputTokens != usage.OutputTokens || end.CostMicros != usage.CostMicros {
				t.Fatal("diagnostic disagrees with authoritative usage")
			}
			if tc.reason == "" {
				if end.Status != "succeeded" || end.CostUncertain {
					t.Fatal("valid response not reported")
				}
			} else if tc.name == "bad_tool" {
				// 도구 계약 위반은 사용량을 확인한 호출 하나의 실패다(C06 보완): 비용은 확정이다. 막힘은 연속 일시 오류
				// 상한을 따르고(v1 manifest는 1), 상한 시험은 TestOpenRouterToolContractViolationIsPerCall이 본다.
				if end.Status != "failed" || end.CostUncertain || usage.CostMicros == 0 || usage.FailureReason != "response_invalid" {
					t.Fatal("tool contract violation blocked the approval or lost its usage")
				}
			} else if tc.name == "rate_limit" {
				// 요청 한도(429)는 제공자가 처리하지 않아 청구가 없다: 비용 0 확정(C05 보완).
				if end.Status != "failed" || end.CostUncertain || end.CostMicros != 0 || !usage.Blocked {
					t.Fatal("rate-limited call cost not settled as zero")
				}
			} else if end.Status != "failed" || !end.CostUncertain || !usage.Blocked {
				t.Fatal("unknown cost represented as actual success")
			}
		})
	}
}
func TestOpenRouterDiagnosticBudgetDeniedWithoutAttempt(t *testing.T) {
	cases := []struct {
		name   string
		change func(*OpenRouterPolicy)
	}{
		{"expired", func(p *OpenRouterPolicy) { p.manifest.ExpiresAt = time.Now().Add(-time.Second) }},
		{"request_limit", func(p *OpenRouterPolicy) { p.manifest.MaxRequests = 0 }},
		{"token_limit", func(p *OpenRouterPolicy) { p.manifest.MaxInputTokens = 1 }},
		{"cost_limit", func(p *OpenRouterPolicy) { p.manifest.MaxCostMicros = 1 }},
		{"concurrency_limit", func(p *OpenRouterPolicy) { p.active = p.manifest.MaxConcurrent }},
		{"approval_consumed", func(p *OpenRouterPolicy) {
			p.ledger = func(int64, int64, int64) error {
				return fmt.Errorf("private-provider-error: %w", errOpenRouterApprovalExhausted)
			}
		}},
		{"approval_unavailable", func(p *OpenRouterPolicy) {
			p.ledger = func(int64, int64, int64) error { return errors.New("private-provider-error") }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, log := routerDiagnosticFixture(t)
			p := openRouterFixture(t)
			tc.change(p)
			tr := &openRouterTransport{policy: p, base: openRouterRT(func(*http.Request) (*http.Response, error) { t.Fatal("denied request sent"); return nil, nil })}
			if _, err := tr.RoundTrip(openRouterRequest().WithContext(ctx)); !errors.Is(err, ErrOpenRouterPolicy) {
				t.Fatal("budget accepted")
			}
			events := routerDiagnosticEvents(t, log)
			if len(events) != 1 || events[0].Event != "model_request_denied" || events[0].Status != "denied" || events[0].ReasonCode != tc.name || p.Snapshot().Requests != 0 {
				t.Fatal("denial fabricated an attempt")
			}
		})
	}
}

func TestOpenRouterDiagnosticConcurrentRequestsHavePerAttemptAccounting(t *testing.T) {
	ctx, log := routerDiagnosticFixture(t)
	p := openRouterFixture(t)
	const calls = 8
	p.manifest.MaxConcurrent = calls
	p.manifest.MaxRequests = calls
	p.manifest.MaxInputTokens = calls * openRouterContext
	p.manifest.MaxOutputTokens = calls * 100
	p.manifest.MaxCostMicros = calls * 1000000
	var hits atomic.Int64
	tr := &openRouterTransport{policy: p, base: openRouterRT(func(*http.Request) (*http.Response, error) {
		hits.Add(1)
		return openRouterResponse(openRouterReply), nil
	})}
	var group sync.WaitGroup
	for range calls {
		group.Go(func() {
			resp, err := tr.RoundTrip(openRouterRequest().WithContext(ctx))
			if err != nil {
				t.Error("synthetic request failed")
			}
			if resp != nil {
				_ = resp.Body.Close()
			}
		})
	}
	group.Wait()
	events := routerDiagnosticEvents(t, log)
	var input, output, cost, reservedInput, reservedOutput, reservedCost int64
	ends := 0
	for _, event := range events {
		if event.Event == "model_request_finished" {
			ends++
			input += event.InputTokens
			output += event.OutputTokens
			cost += event.CostMicros
			reservedInput += event.ReservedInputTokens
			reservedOutput += event.ReservedOutputTokens
			reservedCost += event.ReservedCostMicros
		}
	}
	usage := p.Snapshot()
	if hits.Load() != calls || ends != calls || len(events) != calls*2 || input != usage.InputTokens || output != usage.OutputTokens || cost != usage.CostMicros || reservedInput != usage.ReservedInputTokens || reservedOutput != usage.ReservedOutputTokens || reservedCost != usage.ReservedCostMicros {
		t.Fatal("concurrent per-attempt events disagree with aggregate")
	}
}

// 같은 호출 칸으로 온 두 번째 요청의 거부도 진단 로그 어휘 안의 사유로 남고, 로거를 망가뜨리지 않는다(#458 재상 지적).
func TestOpenRouterCallSlotReuseDiagnosticEvent(t *testing.T) {
	ctx, log := routerDiagnosticFixture(t)
	p := openRouterFixture(t)
	c := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) { return openRouterResponse(openRouterReply), nil }))
	slot := &OpenRouterCall{}
	for i := 0; i < 2; i++ {
		req := openRouterRequest()
		resp, err := c.Do(req.WithContext(WithOpenRouterCall(ctx, slot)))
		if resp != nil {
			_ = resp.Body.Close()
		}
		if (err == nil) != (i == 0) {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if log.Err() != nil || log.Incomplete() {
		t.Fatal("slot reuse broke the diagnostic logger", log.Err())
	}
	found := false
	for _, e := range routerDiagnosticEvents(t, log) {
		found = found || e.Event == "model_request_denied" && e.ReasonCode == "call_slot_reused"
	}
	if !found {
		t.Fatal("slot reuse not recorded")
	}
}

// 요청 한도(429) 응답의 Retry-After와 본문의 제공자 이름·원문 오류를 진단 기록에 남긴다(사용자 결정 2026-11-26). 원문
// 오류는 길이를 자르고 제어 문자를 빼며, 이 호출의 API 키(제어 비밀)는 어디에도 남지 않는다. Retry-After는 호출 칸에도
// 남아 모델 예산이 다시 보낼 때 따른다.
func TestOpenRouterRateLimitDetailRecorded(t *testing.T) {
	const key = "sk-or-v1-synthetic-control-key-123456"
	now := time.Now()
	for _, tc := range []struct {
		name, retryAfter, body string
		wantWait               time.Duration
		hasWait                bool
		wantName, wantError    string
	}{
		{"seconds", "7", `{"error":{"message":"Provider returned error","code":429,"metadata":{"provider_name":"PrimeIntellect","raw":"rate limited for ` + key + `\nretry later"}}}`, 7 * time.Second, true, "PrimeIntellect", "rate limited for [REDACTED] retry later"},
		{"http date", now.Add(5 * time.Second).UTC().Format(http.TimeFormat), `{"error":{"message":"Rate limit exceeded","metadata":{"provider_name":"DeepInfra"}}}`, 5 * time.Second, true, "DeepInfra", "Rate limit exceeded"},
		{"invalid header", "soon", `{"error":{"message":"busy","metadata":{"provider_name":"Prime<Intellect>","raw":{"detail":"quota"}}}}`, 0, false, "PrimeIntellect", `{"detail":"quota"}`},
		{"no header long raw", "", `{"error":{"message":"x","metadata":{"raw":"` + strings.Repeat("a", 2000) + `"}}}`, 0, false, "", strings.Repeat("a", 512)},
		{"not json", "3", `upstream busy ` + key, 3 * time.Second, true, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, log := routerDiagnosticFixture(t)
			p := openRouterFixture(t)
			call := &OpenRouterCall{}
			tr := &openRouterTransport{policy: p, base: openRouterRT(func(*http.Request) (*http.Response, error) {
				h := make(http.Header)
				if tc.retryAfter != "" {
					h.Set("Retry-After", tc.retryAfter)
				}
				return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(tc.body)), Header: h}, nil
			})}
			req := openRouterRequest().WithContext(WithOpenRouterCall(ctx, call))
			req.Header.Set("Authorization", "Bearer "+key)
			req.Body = io.NopCloser(strings.NewReader(`{"model":"qwen/qwen3.6-35b-a3b","messages":[{"role":"user","content":"hello"}],"max_tokens":100,"stream":false}`))
			if resp, err := tr.RoundTrip(req); err == nil {
				_ = resp.Body.Close()
				t.Fatal("429가 성공으로 보임")
			}
			raw, err := os.ReadFile(log.Path())
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), key) {
				t.Fatal("API 키가 진단 기록에 남음")
			}
			events := routerDiagnosticEvents(t, log)
			end := events[len(events)-1]
			if end.ReasonCode != "http_rate_limited" || end.ProviderName != tc.wantName || end.ProviderError != tc.wantError {
				t.Fatalf("기록: reason=%s name=%q error=%q", end.ReasonCode, end.ProviderName, end.ProviderError)
			}
			got := call.Result()
			if got.HasRetryAfter != tc.hasWait || !tc.hasWait && (got.RetryAfter != 0 || end.RetryAfterMS != 0) {
				t.Fatalf("Retry-After 칸: %+v ms=%d", got, end.RetryAfterMS)
			}
			if tc.hasWait && (got.RetryAfter > tc.wantWait || got.RetryAfter < tc.wantWait-2*time.Second || end.RetryAfterMS != got.RetryAfter.Milliseconds()) {
				t.Fatalf("Retry-After 값: %v ms=%d 기대 %v", got.RetryAfter, end.RetryAfterMS, tc.wantWait)
			}
		})
	}
}

func TestOpenRouterRetryAfterParse(t *testing.T) {
	now := time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)
	for value, want := range map[string]struct {
		wait time.Duration
		ok   bool
	}{
		"0": {0, true}, "12": {12 * time.Second, true}, " 4 ": {4 * time.Second, true}, "-1": {0, false}, "": {0, false}, "1.5": {0, false},
		"99999999": {openRouterRetryAfterLimit, true},
		now.Add(-time.Minute).Format(http.TimeFormat):    {0, true},
		now.Add(9 * time.Second).Format(http.TimeFormat): {9 * time.Second, true},
		now.Add(48 * time.Hour).Format(http.TimeFormat):  {openRouterRetryAfterLimit, true},
	} {
		if wait, ok := openRouterRetryAfter(value, now); wait != want.wait || ok != want.ok {
			t.Fatalf("%q: %v %v, 기대 %v %v", value, wait, ok, want.wait, want.ok)
		}
	}
	if openRouterBearer("Bearer abc ") != "abc" || openRouterBearer("abc") != "" || openRouterBearer("") != "" {
		t.Fatal("Bearer 토큰 읽기")
	}
}

// 기한이 지났는데 아직 끝났다고 알리지 않은 문맥이다(호출 시간 상한 타이머가 문맥보다 먼저 요청을 끊은 순간).
type openRouterPassedDeadline struct {
	context.Context
	at time.Time
}

func (c openRouterPassedDeadline) Deadline() (time.Time, bool) { return c.at, true }

// 전송 오류 분류: 표준 타입이 아닌 오류라도 문맥 기한이 지났으면 시한이다. 기한이 없거나 남았으면 전송 실패 그대로다.
func TestOpenRouterTransportReasonDeadlinePassed(t *testing.T) {
	cut := errors.New("net/http: request canceled")
	base := context.Background()
	future, stop := context.WithTimeout(base, time.Hour)
	defer stop()
	for name, tc := range map[string]struct {
		ctx  context.Context
		err  error
		want string
	}{
		"deadline passed":     {openRouterPassedDeadline{base, time.Now().Add(-time.Millisecond)}, cut, "timeout"},
		"deadline ahead":      {future, cut, "transport_failure"},
		"no deadline":         {base, cut, "transport_failure"},
		"no context":          {nil, cut, "transport_failure"},
		"cancelled wins":      {openRouterPassedDeadline{base, time.Now().Add(-time.Millisecond)}, context.Canceled, "cancelled"},
		"deadline error":      {base, context.DeadlineExceeded, "timeout"},
		"passed, read failed": {openRouterPassedDeadline{base, time.Now().Add(-time.Millisecond)}, io.ErrUnexpectedEOF, "timeout"},
	} {
		if got := openRouterDiagnosticTransportReason(tc.err, tc.ctx); got != tc.want {
			t.Fatalf("%s: %s, 기대 %s", name, got, tc.want)
		}
	}
}

// 호출 시간 상한(timeout_seconds)에 끊긴 요청은 응답 머리를 기다리다 끊겼든 본문을 받다 끊겼든 진단 기록에 시한으로
// 남는다(2026-11-27 NodeGoat 회차: 목표 분해 호출 하나가 상한에 끊겼는데 전송 실패로만 남아 원인을 알 수 없었다). 본문을
// 받다 끊겼으면 받은 상태 코드가 같이 남는다. 실제 http.Client·http.Transport로 본다(상한 타이머는 표준 라이브러리 것이다).
func TestOpenRouterCallTimeLimitRecordedAsTimeout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header bool
	}{{"awaiting headers", false}, {"reading body", true}} {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.header {
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, "  \n")
					w.(http.Flusher).Flush()
				}
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}))
			defer srv.Close()
			defer close(release)
			ctx, log := routerDiagnosticFixture(t)
			ctx = diagnosticlog.WithRole(ctx, "goals")
			p := openRouterFixture(t)
			p.manifest.TimeoutSeconds = 1
			limit := time.Duration(p.manifest.TimeoutSeconds) * time.Second
			// 제품 전송과 같은 설정(응답 머리 상한 = 호출 시간 상한)에 대상만 시험 서버로 바꾼다.
			base := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: limit,
				DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
				}}
			req := openRouterRequest().WithContext(ctx)
			req.Header.Set("Authorization", "Bearer synthetic")
			resp, err := openRouterClient(p, base).Do(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if err == nil {
				t.Fatal("상한에 끊긴 요청이 성공으로 보임")
			}
			events := routerDiagnosticEvents(t, log)
			end := events[len(events)-1]
			if end.Event != "model_request_finished" || end.Status != "failed" || end.ReasonCode != "timeout" || end.Role != "goals" {
				t.Fatalf("기록: event=%s status=%s reason=%s role=%s", end.Event, end.Status, end.ReasonCode, end.Role)
			}
			if end.DurationMS < limit.Milliseconds() || tc.header != (end.HTTPStatus == http.StatusOK) {
				t.Fatalf("기록: duration_ms=%d http_status=%d", end.DurationMS, end.HTTPStatus)
			}
			// 받은 것이 없으므로 제공자 이름·원문 오류 칸은 비어 있다.
			if end.ProviderName != "" || end.ProviderError != "" || end.RetryAfterMS != 0 {
				t.Fatalf("시한 기록에 제공자 칸이 남음: %q %q %d", end.ProviderName, end.ProviderError, end.RetryAfterMS)
			}
		})
	}
}
