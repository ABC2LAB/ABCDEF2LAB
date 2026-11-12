package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/Autumn-27/artex/llmpolicy"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/norma/llm"
)

func modelBudgetFixture(t *testing.T, observe func(diagnosisprep.ModelUsage) error) (*FirstDiagnosisModelBudget, agent.Config) {
	t.Helper()
	b, err := NewFirstDiagnosisModelBudget(diagnosisprep.Budget{ModelRequests: 2, InputTokens: 2 * 65536, OutputTokens: 256}, 65536, 128, observe)
	if err != nil {
		t.Fatal(err)
	}
	return b, agent.Config{Format: llm.FormatOpenAI, ContextWindowK: 64, MaxTokens: 128}
}
func TestFirstDiagnosisModelBudgetSharesPreparationAndRun(t *testing.T) {
	var persisted []diagnosisprep.ModelUsage
	b, cfg := modelBudgetFixture(t, func(u diagnosisprep.ModelUsage) error { persisted = append(persisted, u); return nil })
	for phase := 0; phase < 2; phase++ {
		req := llm.CompletionRequest{MaxTokens: 128, Messages: []llm.Message{llm.UserText("owned synthetic input")}}
		if err := b.before(context.Background(), cfg, &req); err != nil {
			t.Fatal(err)
		}
		if persisted[len(persisted)-1].UsageKnown {
			t.Fatal("unconfirmed call reported known")
		}
		if err := b.after(llm.Usage{InputTokens: 10, OutputTokens: 20}, 128, nil); err != nil {
			t.Fatal(err)
		}
	}
	req := llm.CompletionRequest{MaxTokens: 128}
	if b.before(context.Background(), cfg, &req) == nil {
		t.Fatal("shared budget reset")
	}
	u := b.Snapshot()
	if u.Requests != 2 || u.ReservedInputTokens != 131072 || u.ReservedOutputTokens != 256 || u.InputTokens != 20 || u.OutputTokens != 40 || !u.UsageKnown {
		t.Fatalf("bad aggregate: %+v", u)
	}
}
func TestFirstDiagnosisModelBudgetUnknownStopsFurtherCalls(t *testing.T) {
	for _, usage := range []llm.Usage{{}, {InputTokens: 70000, OutputTokens: 1}, {InputTokens: 1, OutputTokens: 129}} {
		b, cfg := modelBudgetFixture(t, func(diagnosisprep.ModelUsage) error { return nil })
		req := llm.CompletionRequest{MaxTokens: 128}
		if b.before(context.Background(), cfg, &req) != nil {
			t.Fatal("first call denied")
		}
		if b.after(usage, 128, nil) == nil || b.Snapshot().UsageKnown {
			t.Fatal("unknown usage accepted")
		}
		if b.before(context.Background(), cfg, &req) == nil {
			t.Fatal("retry after unknown")
		}
	}
}
func TestFirstDiagnosisModelBudgetReservationMustPersist(t *testing.T) {
	b, cfg := modelBudgetFixture(t, func(diagnosisprep.ModelUsage) error { return errors.New("synthetic storage failure") })
	req := llm.CompletionRequest{MaxTokens: 128}
	if b.before(context.Background(), cfg, &req) == nil {
		t.Fatal("unpersisted reservation allowed")
	}
	if b.Snapshot().Requests != 1 || b.Snapshot().UsageKnown {
		t.Fatal("uncertain reservation lost")
	}
}
func TestFirstDiagnosisModelBudgetRejectsStreamingOrChangedContext(t *testing.T) {
	b, cfg := modelBudgetFixture(t, func(diagnosisprep.ModelUsage) error { return nil })
	for _, change := range []func(*agent.Config){func(c *agent.Config) { c.Stream = true }, func(c *agent.Config) { c.ContextWindowK = 128 }, func(c *agent.Config) { c.MaxTokens = 0 }, func(c *agent.Config) { c.Format = llm.FormatAnthropic }} {
		changed := cfg
		change(&changed)
		req := llm.CompletionRequest{MaxTokens: 128}
		if b.before(context.Background(), changed, &req) == nil {
			t.Fatal("unbounded provider accepted")
		}
	}
	if b.Snapshot().Requests != 0 {
		t.Fatal("validation consumed a model call")
	}
}

// 리다이렉트는 따라가지 않는다. 리다이렉트 금지는 연결 정책 거부라서(OpenRouter의 3xx 설정 오류와 같은 축, C06)
// 일시 실패처럼 예약량으로 정산하고 계속하지 않고 다음 호출을 막는다(C05).
func TestFirstDiagnosisModelBudgetRedirectIsNotFollowedAndBlocks(t *testing.T) {
	for _, status := range []int{307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var first, second atomic.Int64
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/second" {
					second.Add(1)
					w.WriteHeader(200)
					return
				}
				first.Add(1)
				w.Header().Set("Location", "/second")
				w.WriteHeader(status)
			}))
			defer endpoint.Close()
			b, cfg := modelBudgetFixture(t, func(diagnosisprep.ModelUsage) error { return nil })
			cfg.BaseURL = endpoint.URL
			cfg.Model = "owned-synthetic"
			cfg.Retry = agent.RetryConfig{ConnectAttempts: -1, EmptyAttempts: -1, StreamAttempts: -1}
			policy, err := llmpolicy.New([]llmpolicy.Endpoint{{Origin: endpoint.URL, Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}})
			if err != nil {
				t.Fatal(err)
			}
			provider, err := cfg.NewLocalProvider(policy, llmpolicy.ClientOptions{DisableRedirects: true})
			if err != nil {
				t.Fatal(err)
			}
			req := llm.CompletionRequest{MaxTokens: 128, Messages: []llm.Message{llm.UserText("owned synthetic")}}
			if b.before(context.Background(), cfg, &req) != nil {
				t.Fatal("reservation")
			}
			_, _, usage, callErr := provider.Complete(context.Background(), req)
			if !errors.Is(callErr, llmpolicy.ErrRequestBlocked) || b.after(usage, 128, callErr) == nil || first.Load() != 1 || second.Load() != 0 {
				t.Fatal("redirect escaped budget or lost its policy classification")
			}
			if u := b.Snapshot(); u.UsageKnown || u.UnknownUsageCalls != 0 || u.Requests != 1 {
				t.Fatalf("policy rejection settled like a transient failure: %+v", u)
			}
			if b.before(context.Background(), cfg, &req) == nil || b.Snapshot().Requests != 1 {
				t.Fatal("call after a redirect rejection was admitted")
			}
		})
	}
}

func TestFirstDiagnosisModelBudgetHardStopInterruptsTargetGrace(t *testing.T) {
	hard, hardStop := context.WithTimeout(t.Context(), 10*time.Second)
	defer hardStop()
	target, stopTarget := context.WithCancelCause(hard)
	defer stopTarget(context.Canceled)
	b, _ := modelBudgetFixture(t, func(diagnosisprep.ModelUsage) error { return nil })
	if err := b.ConfigureTargetDrain(hard, 5*time.Second, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	call, finish := b.modelCallContext(target)
	defer finish()
	stopTarget(ErrFirstDiagnosisTargetEnded)
	select {
	case <-call.Done():
		t.Fatal("normal target stop cancelled active call before grace")
	case <-time.After(30 * time.Millisecond):
	}
	hardStop()
	select {
	case <-call.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("hard stop failed to interrupt target grace")
	}
}

// 실제 로컬 어댑터(Config.NewLocalProvider → SDK → budget.after) 경로에서 연결 정책 거부는 다음 예약을 막고, 정책과
// 무관한 일시 실패(서버 503)는 예약량으로 정산하고 계속한다(P3 결정안 C05). 정책 거부는 DNS 결속 위반이라 송신 0건이다.
func TestFirstDiagnosisModelBudgetLocalAdapterKeepsPolicyRejection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resolved string
		policy   bool
	}{
		{"dns-binding-violation", "127.0.0.2", true},
		{"server-503", "127.0.0.1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int64
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer endpoint.Close()
			_, port, err := net.SplitHostPort(strings.TrimPrefix(endpoint.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			origin := "http://model.fixture.invalid:" + port
			b, cfg := modelBudgetFixture(t, func(diagnosisprep.ModelUsage) error { return nil })
			cfg.BaseURL = origin
			cfg.Model = "owned-synthetic"
			cfg.Retry = agent.RetryConfig{ConnectAttempts: -1, EmptyAttempts: -1, StreamAttempts: -1}
			policy, err := llmpolicy.New([]llmpolicy.Endpoint{{Origin: origin, Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}})
			if err != nil {
				t.Fatal(err)
			}
			var lookups atomic.Int64
			lookup := func(context.Context, string) ([]netip.Addr, error) {
				lookups.Add(1)
				return []netip.Addr{netip.MustParseAddr(tc.resolved)}, nil
			}
			provider, err := cfg.NewLocalProvider(policy, llmpolicy.ClientOptions{DisableRedirects: true, LookupIP: lookup})
			if err != nil {
				t.Fatal(err)
			}
			req := llm.CompletionRequest{MaxTokens: 128, Messages: []llm.Message{llm.UserText("owned synthetic")}}
			if b.before(context.Background(), cfg, &req) != nil {
				t.Fatal("reservation")
			}
			_, _, usage, callErr := provider.Complete(context.Background(), req)
			if callErr == nil || errors.Is(callErr, llmpolicy.ErrRequestBlocked) != tc.policy || b.after(usage, 128, callErr) == nil || lookups.Load() == 0 {
				t.Fatalf("call error=%v policy=%v lookups=%d", callErr, errors.Is(callErr, llmpolicy.ErrRequestBlocked), lookups.Load())
			}
			u := b.Snapshot()
			next := b.before(context.Background(), cfg, &req) == nil
			if tc.policy {
				if hits.Load() != 0 || u.UsageKnown || u.UnknownUsageCalls != 0 || next {
					t.Fatalf("policy rejection: hits=%d usage=%+v next=%v", hits.Load(), u, next)
				}
				return
			}
			if hits.Load() != 1 || !u.UsageKnown || u.UnknownUsageCalls != 1 || !next {
				t.Fatalf("transient failure: hits=%d usage=%+v next=%v", hits.Load(), u, next)
			}
		})
	}
}

// 실패한 호출 하나가 실행 전체를 막지 않는다(P3 결정안 C05). 예약 상한으로 정산하고, 예산 소진·정책 차단은 계속 막는다.
func TestFirstDiagnosisModelBudgetFailedCallCountsReservation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		callErr error
		bounded bool
	}{
		{"transient", errors.New("synthetic 503"), true},
		{"cancelled", context.Canceled, true},
		{"local-policy-blocked", fmt.Errorf("post: %w", llmpolicy.ErrRequestBlocked), false},
		{"client-options", fmt.Errorf("client: %w", llmpolicy.ErrClientOptions), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var persisted []diagnosisprep.ModelUsage
			b, cfg := modelBudgetFixture(t, func(u diagnosisprep.ModelUsage) error { persisted = append(persisted, u); return nil })
			req := llm.CompletionRequest{MaxTokens: 100}
			if b.before(context.Background(), cfg, &req) != nil {
				t.Fatal("first call denied")
			}
			if b.after(llm.Usage{InputTokens: 3, OutputTokens: 4}, 100, tc.callErr) == nil {
				t.Fatal("failed call reported success")
			}
			u := b.Snapshot()
			if !tc.bounded {
				if u.UsageKnown || u.UnknownUsageCalls != 0 || b.before(context.Background(), cfg, &req) == nil {
					t.Fatalf("policy failure not blocking: %+v", u)
				}
				return
			}
			// 보고된 부분 사용량이 아니라 예약한 입력·출력으로 센다.
			if !u.UsageKnown || u.UnknownUsageCalls != 1 || u.InputTokens != 65536 || u.OutputTokens != 100 || u.Requests != 1 || persisted[len(persisted)-1] != u {
				t.Fatalf("bounded usage: %+v", u)
			}
			if b.before(context.Background(), cfg, &req) != nil {
				t.Fatal("failed call blocked the next call")
			}
			if b.after(llm.Usage{InputTokens: 10, OutputTokens: 20}, 100, nil) != nil {
				t.Fatal("next call")
			}
			u = b.Snapshot()
			if !u.UsageKnown || u.UnknownUsageCalls != 1 || u.InputTokens != 65546 || u.OutputTokens != 120 {
				t.Fatalf("mixed usage: %+v", u)
			}
			// 승인 예산(호출 2회)은 넓어지지 않는다.
			if b.before(context.Background(), cfg, &req) == nil {
				t.Fatal("budget widened after a failed call")
			}
		})
	}
}

func TestFirstDiagnosisFailedCallBoundedClassification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		external bool
		want     bool
	}{
		{"succeeded", nil, false, false},
		{"transient", errors.New("synthetic 503"), false, true},
		{"cancelled", context.Canceled, false, true},
		{"external-policy-blocked", errors.New("synthetic 503"), true, false},
		{"local-policy-blocked", fmt.Errorf("post: %w", llmpolicy.ErrRequestBlocked), false, false},
		{"client-options", fmt.Errorf("client: %w", llmpolicy.ErrClientOptions), false, false},
	} {
		if got := failedCallBounded(tc.err, tc.external); got != tc.want {
			t.Fatalf("%s: bounded=%v", tc.name, got)
		}
	}
}
