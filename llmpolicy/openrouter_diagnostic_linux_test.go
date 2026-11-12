//go:build linux

package llmpolicy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
			req.Body = io.NopCloser(strings.NewReader(strings.Replace(`{"model":"qwen/qwen3.5-35b-a3b","messages":[{"role":"user","content":"private-request-marker"}],"max_tokens":100,"stream":false}`, "unused", "", 1)))
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
