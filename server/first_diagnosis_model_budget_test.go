package server

import (
	"context"
	"encoding/json"
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

// 실패한 호출이 예약보다 큰 사용량을 보고하면 예약 상한의 근거가 깨진 것이라 예약량으로 정산하지 않고 막는다.
func TestFirstDiagnosisModelBudgetFailedCallOverReservationBlocks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage func(reservedInput int) llm.Usage
		want  bool
	}{
		{"none-reported", func(int) llm.Usage { return llm.Usage{} }, true},
		{"at-reservation", func(in int) llm.Usage { return llm.Usage{InputTokens: in, OutputTokens: 100} }, true},
		{"input-over", func(in int) llm.Usage { return llm.Usage{InputTokens: in + 1} }, false},
		{"output-over", func(int) llm.Usage { return llm.Usage{OutputTokens: 101} }, false},
		{"negative", func(int) llm.Usage { return llm.Usage{CacheReadTokens: -1} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, cfg := modelBudgetFixture(t, func(diagnosisprep.ModelUsage) error { return nil })
			req := llm.CompletionRequest{MaxTokens: 100}
			if b.before(context.Background(), cfg, &req) != nil {
				t.Fatal("first call denied")
			}
			reserved := reservedInputFor(t, req)
			if b.after(tc.usage(int(reserved)), 100, errors.New("synthetic 503")) == nil {
				t.Fatal("failed call reported success")
			}
			u := b.Snapshot()
			bounded := u.UsageKnown && u.UnknownUsageCalls == 1 && u.InputTokens == reserved && u.OutputTokens == 100
			next := b.before(context.Background(), cfg, &req) == nil
			if bounded != tc.want || next != tc.want {
				t.Fatalf("bounded=%v next=%v want=%v usage=%+v", bounded, next, tc.want, u)
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
	// 예산 검사는 정산한 사용량 + 진행 중 예약이다(C28 결정 2). 정산한 세 호출은 10토큰씩만 썼으므로 남은 예약을
	// 다시 써서 네 번째 호출을 받는다.
	if err := b.before(context.Background(), cfg, &req); err != nil || b.Snapshot().Requests != 4 {
		t.Fatal("settled reservations were not released for the next call", err)
	}
	if err := b.after(llm.Usage{InputTokens: 10, OutputTokens: 20}, 128, nil); err != nil {
		t.Fatal(err)
	}
	// 정산 사용량과 진행 중 예약이 상한을 채우면 거부한다: 입력 예산 2·want에서 첫 호출이 want를 다 쓰고(정산),
	// 두 번째는 남은 want로 받고, 그 예약이 진행 중인 동안 세 번째는 거부한다.
	b, err = NewFirstDiagnosisModelBudget(diagnosisprep.Budget{ModelRequests: 4, InputTokens: 2 * want, OutputTokens: 4 * 128}, 8192, 128, func(diagnosisprep.ModelUsage) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := b.ConfigureConcurrency(1); err != nil {
		t.Fatal(err)
	}
	first, err := b.reserve(context.Background(), cfg, &req)
	if err != nil || b.settle(first, llm.Usage{InputTokens: int(want), OutputTokens: 1}, nil) != nil {
		t.Fatal("first call", err)
	}
	second, err := b.reserve(context.Background(), cfg, &req)
	if err != nil {
		t.Fatal("remaining input budget not usable", err)
	}
	if _, err := b.reserve(context.Background(), cfg, &req); err == nil || !b.limitReached || b.Snapshot().Requests != 2 {
		t.Fatal("settled usage plus in-flight reservation exceeded the input budget")
	}
	if b.settle(second, llm.Usage{InputTokens: 1, OutputTokens: 1}, nil) != nil {
		t.Fatal("second call")
	}
	if _, err := b.reserve(context.Background(), cfg, &req); err == nil {
		t.Fatal("call accepted although settled usage leaves less than one reservation")
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

// 승인 예산이 다음 호출을 받을 만큼 남지 않으면 LimitReached다(budget_limited). 막힘(불명·정책)은 아니다.
func TestFirstDiagnosisModelBudgetLimitReachedOnlyForBudget(t *testing.T) {
	b, cfg := modelBudgetFixture(t, func(diagnosisprep.ModelUsage) error { return nil })
	req := llm.CompletionRequest{MaxTokens: 128}
	for i := 0; i < 2; i++ {
		if b.before(context.Background(), cfg, &req) != nil || b.after(llm.Usage{InputTokens: 1, OutputTokens: 1}, 128, nil) != nil {
			t.Fatal("call within budget")
		}
	}
	if b.LimitReached() {
		t.Fatal("limit reached before a refused reservation")
	}
	if b.before(context.Background(), cfg, &req) == nil || !b.LimitReached() {
		t.Fatal("refused reservation over the request budget not recorded as a limit")
	}
	// 사용량 불명으로 막힌 예산은 마감 사유가 아니다.
	blocked, cfg := modelBudgetFixture(t, func(diagnosisprep.ModelUsage) error { return nil })
	if blocked.before(context.Background(), cfg, &req) != nil {
		t.Fatal("first call")
	}
	if blocked.after(llm.Usage{}, 128, nil) == nil {
		t.Fatal("missing usage accepted")
	}
	if blocked.before(context.Background(), cfg, &req) == nil || blocked.LimitReached() {
		t.Fatal("blocked budget reported as a budget limit")
	}
	var none *FirstDiagnosisModelBudget
	if none.LimitReached() {
		t.Fatal("nil budget limit")
	}
	// 감독자의 실행 상한 판정은 E② 런타임 표시와 모델 예산 상한 둘 다 본다.
	task := &Task{}
	if firstDiagnosisBudgetLimited(task, blocked) || firstDiagnosisBudgetLimited(nil, nil) || !firstDiagnosisBudgetLimited(task, b) {
		t.Fatal("model budget limit not part of the run budget limit")
	}
	task.firstDiagnosisBudgetLimited.Store(true)
	if !firstDiagnosisBudgetLimited(task, nil) {
		t.Fatal("runtime budget limit ignored")
	}
}

// 호출별 예약·정산(P3 결정안 C28 결정 2). 진행 중 호출이 있어도 예산 안이면 새 예약을 받고, 각 호출은 자기 예약만
// 닫는다. 역순으로 끝나도 각자 정산하고, 한 호출이 실패해도 다른 호출의 정산은 그대로다. 예산은 예약 합계로 본다.
func TestFirstDiagnosisModelBudgetPerCallReservations(t *testing.T) {
	type outcome struct {
		usage llm.Usage
		err   error
	}
	for _, tc := range []struct {
		name          string
		first, second outcome
		bounded       int64
	}{
		{"reverse-success", outcome{usage: llm.Usage{InputTokens: 10, OutputTokens: 20}}, outcome{usage: llm.Usage{InputTokens: 30, OutputTokens: 40}}, 0},
		{"first-fails", outcome{err: errors.New("synthetic 503")}, outcome{usage: llm.Usage{InputTokens: 30, OutputTokens: 40}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var persisted []diagnosisprep.ModelUsage
			b, cfg := modelBudgetFixture(t, func(u diagnosisprep.ModelUsage) error { persisted = append(persisted, u); return nil })
			req := llm.CompletionRequest{MaxTokens: 128, Messages: []llm.Message{llm.UserText("owned synthetic")}}
			first, err := b.reserve(context.Background(), cfg, &req)
			if err != nil {
				t.Fatal(err)
			}
			second, err := b.reserve(context.Background(), cfg, &req)
			if err != nil {
				t.Fatal("in-flight call blocked a reservation within budget")
			}
			if u := persisted[len(persisted)-1]; u.UsageKnown || u.Requests != 2 || u.ReservedInputTokens != 2*first.input {
				t.Fatalf("in-flight reservations not persisted: %+v", u)
			}
			// 승인 예산(호출 2회)은 예약 수로 센다.
			if _, err := b.reserve(context.Background(), cfg, &req); err == nil {
				t.Fatal("third reservation exceeded the budget")
			}
			// 뒤에 시작한 호출이 먼저 끝난다.
			if b.settle(second, tc.second.usage, tc.second.err) != nil {
				t.Fatal("second settlement")
			}
			if u := b.Snapshot(); u.UsageKnown || u.InputTokens != 30 || u.OutputTokens != 40 {
				t.Fatalf("settled with a call still in flight: %+v", u)
			}
			if (b.settle(first, tc.first.usage, tc.first.err) == nil) != (tc.first.err == nil) {
				t.Fatal("first settlement result")
			}
			wantInput, wantOutput := int64(40), int64(60)
			if tc.first.err != nil {
				wantInput, wantOutput = first.input+30, first.output+40
			}
			if u := b.Snapshot(); !u.UsageKnown || u.InputTokens != wantInput || u.OutputTokens != wantOutput || u.UnknownUsageCalls != tc.bounded || u.Requests != 2 {
				t.Fatalf("per-call settlement: %+v", u)
			}
			// 같은 예약을 두 번 닫으면 회계를 믿을 수 없어 막는다.
			if b.settle(first, llm.Usage{InputTokens: 1}, nil) == nil || b.Snapshot().UsageKnown {
				t.Fatal("double settlement accepted")
			}
		})
	}
}

// 정책 차단은 진행 중인 다른 호출의 정산을 지우지 않는다. 막힌 뒤의 새 예약만 거부한다.
func TestFirstDiagnosisModelBudgetBlockedCallKeepsOtherSettlement(t *testing.T) {
	b, cfg := modelBudgetFixture(t, func(diagnosisprep.ModelUsage) error { return nil })
	req := llm.CompletionRequest{MaxTokens: 128}
	first, err := b.reserve(context.Background(), cfg, &req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.reserve(context.Background(), cfg, &req)
	if err != nil {
		t.Fatal(err)
	}
	if b.settle(first, llm.Usage{}, fmt.Errorf("post: %w", llmpolicy.ErrRequestBlocked)) == nil {
		t.Fatal("policy rejection settled")
	}
	if b.settle(second, llm.Usage{InputTokens: 10, OutputTokens: 20}, nil) != nil {
		t.Fatal("in-flight call lost its own settlement")
	}
	if u := b.Snapshot(); u.UsageKnown || u.InputTokens != 10 || u.OutputTokens != 20 || u.UnknownUsageCalls != 0 {
		t.Fatalf("blocked budget usage: %+v", u)
	}
}

// 외부 제공자 경로도 진행 중 호출이 있다는 이유만으로 새 예약을 거부하지 않는다. 호출마다 칸을 받아 자기 칸의
// transport 결과로 정산하므로, 겹친 호출이 역순으로 끝나도 각자 정산되고 세 번째는 예산 부족으로만 거부된다
// (P3 결정안 C28 결정 2 완료 기준).
func TestFirstDiagnosisOpenRouterBudgetSettlesOverlappingCallsBySlot(t *testing.T) {
	b, _, cfg := urlExternalBudget(t, func(diagnosisprep.ModelUsage) error { return nil })
	req := llm.CompletionRequest{MaxTokens: 100}
	first, err := b.reserve(context.Background(), cfg, &req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.reserve(context.Background(), cfg, &req)
	if err != nil || first.slot == nil || second.slot == nil || first.slot == second.slot {
		t.Fatal("overlapping external reservation rejected or slots shared", err)
	}
	if _, err := b.reserve(context.Background(), cfg, &req); err == nil || !b.LimitReached() {
		t.Fatal("third reservation accepted beyond the approved requests")
	}
	a := llmpolicy.OpenRouterCallResult{Reserved: true, Succeeded: true, ReservedInputTokens: 5000, ReservedOutputTokens: 100, ReservedCostMicros: 5100, InputTokens: 10, OutputTokens: 5, CostMicros: 30}
	c := llmpolicy.OpenRouterCallResult{Reserved: true, Succeeded: true, ReservedInputTokens: 6000, ReservedOutputTokens: 100, ReservedCostMicros: 6100, InputTokens: 20, OutputTokens: 6, CostMicros: 40}
	first.result = func() llmpolicy.OpenRouterCallResult { return a }
	second.result = func() llmpolicy.OpenRouterCallResult { return c }
	b.externalUsage = func() llmpolicy.OpenRouterUsage {
		u := b.externalBase
		u.Requests += 2
		u.ReservedInputTokens += a.ReservedInputTokens + c.ReservedInputTokens
		u.ReservedOutputTokens += a.ReservedOutputTokens + c.ReservedOutputTokens
		u.ReservedCostMicros += a.ReservedCostMicros + c.ReservedCostMicros
		u.InputTokens += a.InputTokens + c.InputTokens
		u.OutputTokens += a.OutputTokens + c.OutputTokens
		u.CostMicros += a.CostMicros + c.CostMicros
		return u
	}
	// 뒤에 시작한 호출이 먼저 끝난다.
	if err := b.settle(second, llm.Usage{InputTokens: 20, OutputTokens: 6}, nil); err != nil || b.Snapshot().UsageKnown {
		t.Fatal("second call settlement", err)
	}
	if err := b.settle(first, llm.Usage{InputTokens: 10, OutputTokens: 5}, nil); err != nil {
		t.Fatal("first call settlement", err)
	}
	u := b.Snapshot()
	if !u.UsageKnown || b.blocked || u.InputTokens != 30 || u.OutputTokens != 11 || u.CostUSDMicros != 70 || u.UnknownUsageCalls != 0 {
		t.Fatalf("per-call settlement: %+v blocked=%v", u, b.blocked)
	}
}

// 겹친 호출 하나가 실패해도 그 호출만 예약량으로 닫고(C05) 다른 호출은 자기 칸으로 정산한다. 칸의 값과 SDK가
// 보고한 사용량이 다르면 그 호출을 믿을 수 없어 막는다.
func TestFirstDiagnosisOpenRouterBudgetFailedSlotDoesNotTouchOther(t *testing.T) {
	b, _, cfg := urlExternalBudget(t, func(diagnosisprep.ModelUsage) error { return nil })
	req := llm.CompletionRequest{MaxTokens: 100}
	first, _ := b.reserve(context.Background(), cfg, &req)
	second, err := b.reserve(context.Background(), cfg, &req)
	if err != nil {
		t.Fatal(err)
	}
	failed := llmpolicy.OpenRouterCallResult{Reserved: true, ReservedInputTokens: 5000, ReservedOutputTokens: 100, ReservedCostMicros: 5100}
	ok := llmpolicy.OpenRouterCallResult{Reserved: true, Succeeded: true, ReservedInputTokens: 6000, ReservedOutputTokens: 100, ReservedCostMicros: 6100, InputTokens: 20, OutputTokens: 6, CostMicros: 40}
	first.result = func() llmpolicy.OpenRouterCallResult { return failed }
	second.result = func() llmpolicy.OpenRouterCallResult { return ok }
	b.externalUsage = func() llmpolicy.OpenRouterUsage {
		u := b.externalBase
		u.Requests += 2
		u.ReservedInputTokens += 11000
		u.ReservedOutputTokens += 200
		u.ReservedCostMicros += 11200
		u.InputTokens, u.OutputTokens, u.CostMicros = u.InputTokens+20, u.OutputTokens+6, u.CostMicros+40
		return u
	}
	if err := b.settle(first, llm.Usage{}, errors.New("synthetic transient transport failure")); err == nil {
		t.Fatal("failed call reported success")
	}
	if err := b.settle(second, llm.Usage{InputTokens: 20, OutputTokens: 6}, nil); err != nil {
		t.Fatal("other call disturbed by the failure", err)
	}
	u := b.Snapshot()
	if !u.UsageKnown || b.blocked || u.UnknownUsageCalls != 1 || u.InputTokens != first.input+20 || u.CostUSDMicros != first.cost+40 {
		t.Fatalf("failed slot settlement: %+v blocked=%v", u, b.blocked)
	}

	b2, _, cfg2 := urlExternalBudget(t, func(diagnosisprep.ModelUsage) error { return nil })
	call, _ := b2.reserve(context.Background(), cfg2, &req)
	call.result = func() llmpolicy.OpenRouterCallResult {
		return llmpolicy.OpenRouterCallResult{Reserved: true, Succeeded: true, InputTokens: 10, OutputTokens: 5, CostMicros: 30}
	}
	if err := b2.settle(call, llm.Usage{InputTokens: 11, OutputTokens: 5}, nil); err == nil || !b2.blocked {
		t.Fatal("SDK usage different from the transport slot accepted")
	}
}

// 진행 중 호출이 없을 때 transport 누적 값이 호출 칸으로 정산한 합계와 다르면(칸 없이 나간 요청 등) 그 승인으로 새
// 호출을 막는다.
func TestFirstDiagnosisOpenRouterBudgetReconcilesTransportTotals(t *testing.T) {
	b, _, cfg := urlExternalBudget(t, func(diagnosisprep.ModelUsage) error { return nil })
	req := llm.CompletionRequest{MaxTokens: 100}
	call, err := b.reserve(context.Background(), cfg, &req)
	if err != nil {
		t.Fatal(err)
	}
	r := llmpolicy.OpenRouterCallResult{Reserved: true, Succeeded: true, ReservedInputTokens: 5000, ReservedOutputTokens: 100, ReservedCostMicros: 5100, InputTokens: 10, OutputTokens: 5, CostMicros: 30}
	call.result = func() llmpolicy.OpenRouterCallResult { return r }
	b.externalUsage = func() llmpolicy.OpenRouterUsage {
		u := b.externalBase
		u.Requests += 2 // 칸 없이 나간 요청 하나가 더 있다.
		u.ReservedInputTokens += r.ReservedInputTokens
		u.ReservedOutputTokens += r.ReservedOutputTokens
		u.ReservedCostMicros += r.ReservedCostMicros
		u.InputTokens, u.OutputTokens, u.CostMicros = u.InputTokens+10, u.OutputTokens+5, u.CostMicros+30
		return u
	}
	if err := b.settle(call, llm.Usage{InputTokens: 10, OutputTokens: 5}, nil); err != nil {
		t.Fatal(err)
	}
	if !b.blocked || b.Snapshot().UsageKnown {
		t.Fatal("transport total mismatch not blocked")
	}
	if _, err := b.reserve(context.Background(), cfg, &req); err == nil {
		t.Fatal("new call accepted after a reconciliation mismatch")
	}
}
