package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Autumn-27/artex/llmpolicy"
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

// reservedInputFor는 before가 예약하는 입력 토큰(요청 JSON 바이트 + framing 여유, C28 결정 1)이다.
func reservedInputFor(t *testing.T, req llm.CompletionRequest) int64 {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return int64(len(raw)) + firstDiagnosisFramingTokens
}

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
	sample := llm.CompletionRequest{MaxTokens: 128, Messages: []llm.Message{llm.UserText("owned synthetic input")}}
	if u.Requests != 2 || u.ReservedInputTokens != 2*reservedInputFor(t, sample) || u.ReservedOutputTokens != 256 || u.InputTokens != 20 || u.OutputTokens != 40 || !u.UsageKnown {
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

// 리다이렉트는 따라가지 않고 그 호출의 실패로 끝난다. 실패한 호출은 예약한 양으로 정산하고(C05) 다음 호출을 막지 않는다.
func TestFirstDiagnosisModelBudgetRedirectIsNotFollowedAndCountsReservation(t *testing.T) {
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
			if b.after(usage, 128, callErr) == nil || first.Load() != 1 || second.Load() != 0 {
				t.Fatal("redirect escaped budget")
			}
			if u := b.Snapshot(); !u.UsageKnown || u.UnknownUsageCalls != 1 || u.Requests != 1 || u.InputTokens != reservedInputFor(t, req) || u.OutputTokens != 128 {
				t.Fatalf("failed call not counted at its reservation: %+v", u)
			}
			if b.before(context.Background(), cfg, &req) != nil || b.Snapshot().Requests != 2 {
				t.Fatal("one failed call blocked the next")
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
			if !u.UsageKnown || u.UnknownUsageCalls != 1 || u.InputTokens != reservedInputFor(t, req) || u.OutputTokens != 100 || u.Requests != 1 || persisted[len(persisted)-1] != u {
				t.Fatalf("bounded usage: %+v", u)
			}
			if b.before(context.Background(), cfg, &req) != nil {
				t.Fatal("failed call blocked the next call")
			}
			if b.after(llm.Usage{InputTokens: 10, OutputTokens: 20}, 100, nil) != nil {
				t.Fatal("next call")
			}
			u = b.Snapshot()
			if !u.UsageKnown || u.UnknownUsageCalls != 1 || u.InputTokens != reservedInputFor(t, req)+10 || u.OutputTokens != 120 {
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

// 입력 예약은 컨텍스트 창 전체가 아니라 요청 크기의 상한이다(P3 결정안 C28 결정 1). 같은 승인 예산으로 작은 요청을
// 더 보낼 수 있고, 창보다 큰 요청과 예약을 넘는 보고 사용량은 지금처럼 막는다.
func TestFirstDiagnosisModelBudgetReservesRequestSize(t *testing.T) {
	cfg := agent.Config{Format: llm.FormatOpenAI, ContextWindowK: 8, MaxTokens: 128}
	req := llm.CompletionRequest{MaxTokens: 128, Messages: []llm.Message{llm.UserText("owned synthetic input")}}
	want := reservedInputFor(t, req)
	// 입력 예산을 작은 요청 3개 분량으로 딱 맞춘다(승인은 창 하나 이상이어야 한다). 창 전체(8192)로 예약하면
	// 두 번째 호출부터 거부된다.
	b, err := NewFirstDiagnosisModelBudget(diagnosisprep.Budget{ModelRequests: 4, InputTokens: 3 * want, OutputTokens: 4 * 128}, 8192, 128, func(diagnosisprep.ModelUsage) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for call := int64(1); call <= 3; call++ {
		if err := b.before(context.Background(), cfg, &req); err != nil {
			t.Fatalf("small call %d denied: %v", call, err)
		}
		if u := b.Snapshot(); u.ReservedInputTokens != call*want || want >= 8192 {
			t.Fatalf("call %d reserved %d, want %d", call, u.ReservedInputTokens, call*want)
		}
		if err := b.after(llm.Usage{InputTokens: 10, OutputTokens: 20}, 128, nil); err != nil {
			t.Fatal(err)
		}
	}
	// 승인 입력 예산은 예약 합계로 지킨다. 남은 입력 예산이 요청 하나보다 작으면 거부한다.
	if b.before(context.Background(), cfg, &req) == nil || b.Snapshot().Requests != 3 {
		t.Fatal("input budget exceeded")
	}
	// 창(65536)에 framing 여유를 더하면 넘는 요청은 보내지 않는다.
	b, cfg = modelBudgetFixture(t, func(diagnosisprep.ModelUsage) error { return nil })
	want = reservedInputFor(t, req)
	big := llm.CompletionRequest{MaxTokens: 128, Messages: []llm.Message{llm.UserText(strings.Repeat("a", 65536-firstDiagnosisFramingTokens))}}
	if b.before(context.Background(), cfg, &big) == nil || b.Snapshot().Requests != 0 {
		t.Fatal("request larger than the context window reserved")
	}
	// 보고된 입력이 예약(요청 바이트 + 여유)을 넘으면 상한 가정이 깨진 것이라 막는다.
	if b.before(context.Background(), cfg, &req) != nil {
		t.Fatal("reservation")
	}
	if b.after(llm.Usage{InputTokens: int(want) + 1, OutputTokens: 1}, 128, nil) == nil || b.Snapshot().UsageKnown || b.before(context.Background(), cfg, &req) == nil {
		t.Fatal("usage above the reservation trusted")
	}
}
