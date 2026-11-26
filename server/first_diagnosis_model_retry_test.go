package server

import (
	"context"
	"errors"
	"github.com/Autumn-27/artex/llmpolicy"
	"slices"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/norma/llm"
)

// 실패한 모델 호출(429 등)은 예산·승인이 막히지 않았으면 정한 횟수만큼 다시 보내고, 성공하면 그 결과를 쓴다. 예산 거부·막힘·
// 문맥 취소는 다시 보내지 않는다(P3 결정안 C06, 11/13 실제 회차의 429로 Planner가 바로 실패하던 것).
func TestFirstDiagnosisRetryModelCall(t *testing.T) {
	transient := errors.New("synthetic 429")
	delays := []time.Duration{time.Millisecond, time.Millisecond}
	script := func(results ...[2]error) (func() (llm.Message, string, llm.Usage, error, error), *int) {
		n := 0
		return func() (llm.Message, string, llm.Usage, error, error) {
			r := results[n]
			n++
			if r[1] == nil {
				return llm.Message{Content: []llm.ContentBlock{llm.TextBlock("ok")}}, "stop", llm.Usage{InputTokens: 1}, nil, nil
			}
			return llm.Message{}, "", llm.Usage{}, r[0], r[1]
		}, &n
	}
	always := func() bool { return true }
	// 실패 뒤 성공: 다시 보낸 결과를 쓴다.
	once, n := script([2]error{transient, ErrFirstDiagnosisModelBudget}, [2]error{nil, nil})
	if msg, _, _, err := firstDiagnosisRetryModelCall(t.Context(), delays, once, always); err != nil || msg.Text() != "ok" || *n != 2 {
		t.Fatalf("retry after a transient failure: err=%v attempts=%d", err, *n)
	}
	// 계속 실패하면 정한 횟수(1 + 재시도 2)에서 멈춘다.
	once, n = script([2]error{transient, ErrFirstDiagnosisModelBudget}, [2]error{transient, ErrFirstDiagnosisModelBudget}, [2]error{transient, ErrFirstDiagnosisModelBudget})
	if _, _, _, err := firstDiagnosisRetryModelCall(t.Context(), delays, once, always); err == nil || *n != 3 {
		t.Fatalf("retries not bounded: attempts=%d", *n)
	}
	// 예산 거부(호출 없음)·막힌 예산·취소된 문맥은 다시 보내지 않는다.
	once, n = script([2]error{nil, ErrFirstDiagnosisModelBudget})
	if _, _, _, err := firstDiagnosisRetryModelCall(t.Context(), delays, once, always); err == nil || *n != 1 {
		t.Fatal("budget refusal retried")
	}
	once, n = script([2]error{transient, ErrFirstDiagnosisModelBudget})
	if _, _, _, err := firstDiagnosisRetryModelCall(t.Context(), delays, once, func() bool { return false }); err == nil || *n != 1 {
		t.Fatal("blocked budget retried")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	once, n = script([2]error{transient, ErrFirstDiagnosisModelBudget})
	if _, _, _, err := firstDiagnosisRetryModelCall(cancelled, delays, once, always); err == nil || *n != 1 {
		t.Fatal("cancelled call retried")
	}
}

// 다시 보내도 되는 것은 예산이 막히지 않았을 때다. 일시 실패를 예약량으로 정산한 뒤에는 막히지 않고, 정책이 막은 실패는 막힌다.
func TestFirstDiagnosisModelBudgetRetryable(t *testing.T) {
	b, err := NewFirstDiagnosisModelBudget(diagnosisprep.Budget{ModelRequests: 4, InputTokens: 4 * 65536, OutputTokens: 512}, 65536, 128, func(diagnosisprep.ModelUsage) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !b.retryable() {
		t.Fatal("fresh budget not retryable")
	}
	b.blocked = true
	if b.retryable() || (*FirstDiagnosisModelBudget)(nil).retryable() {
		t.Fatal("blocked budget retryable")
	}
}

// 마지막 실패가 요청 한도(429, 청구 없음)이면 더 긴 간격표로 더 여러 번 다시 보낸다. 그 밖의 실패는 지금 간격표 그대로다.
func TestFirstDiagnosisRetryModelCallRateLimitSchedule(t *testing.T) {
	transient := errors.New("synthetic failure")
	short := []time.Duration{time.Millisecond, time.Millisecond}
	long := []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}
	always := func() bool { return true }
	for name, tc := range map[string]struct {
		rateLimited bool
		attempts    int
	}{
		"rate-limited": {true, len(long) + 1},
		"other":        {false, len(short) + 1},
	} {
		n := 0
		once := func() (llm.Message, string, llm.Usage, error, error) {
			n++
			return llm.Message{}, "", llm.Usage{}, transient, ErrFirstDiagnosisModelBudget
		}
		limited := tc.rateLimited
		if _, _, _, err := firstDiagnosisRetryModelCallWith(t.Context(), short, long, func() bool { return limited }, once, always); err == nil || n != tc.attempts {
			t.Fatalf("%s: attempts=%d want %d", name, n, tc.attempts)
		}
	}
	// 요청 한도가 풀리면 다음 시도의 결과를 쓴다.
	n := 0
	once := func() (llm.Message, string, llm.Usage, error, error) {
		n++
		if n < 4 {
			return llm.Message{}, "", llm.Usage{}, transient, ErrFirstDiagnosisModelBudget
		}
		return llm.Message{Content: []llm.ContentBlock{llm.TextBlock("ok")}}, "stop", llm.Usage{InputTokens: 1}, nil, nil
	}
	if msg, _, _, err := firstDiagnosisRetryModelCallWith(t.Context(), short, long, func() bool { return true }, once, always); err != nil || msg.Text() != "ok" || n != 4 {
		t.Fatalf("rate limit recovery: err=%v attempts=%d", err, n)
	}
	// 기본 간격표는 요청 한도 쪽이 더 길고, 합계가 준비 단계 상한(FirstDiagnosisPreparationTimeout)보다 짧다.
	var total time.Duration
	for _, d := range firstDiagnosisRateLimitRetryDelays {
		total += d
	}
	if len(firstDiagnosisRateLimitRetryDelays) <= len(firstDiagnosisModelRetryDelays) || total >= FirstDiagnosisPreparationTimeout {
		t.Fatalf("rate limit schedule %v", firstDiagnosisRateLimitRetryDelays)
	}
}

// 호출의 요청 한도 여부는 transport 칸의 청구 없음 표시(429)에서만 온다.
func TestFirstDiagnosisModelCallRateLimited(t *testing.T) {
	for name, tc := range map[string]struct {
		call *firstDiagnosisModelCall
		want bool
	}{
		"rate-limited": {&firstDiagnosisModelCall{result: func() llmpolicy.OpenRouterCallResult {
			return llmpolicy.OpenRouterCallResult{Reserved: true, NotBilled: true}
		}}, true},
		"server-error": {&firstDiagnosisModelCall{result: func() llmpolicy.OpenRouterCallResult { return llmpolicy.OpenRouterCallResult{Reserved: true} }}, false},
		"not-sent":     {&firstDiagnosisModelCall{result: func() llmpolicy.OpenRouterCallResult { return llmpolicy.OpenRouterCallResult{} }}, false},
		"local":        {&firstDiagnosisModelCall{}, false},
		"nil":          {nil, false},
	} {
		if got := tc.call.rateLimited(); got != tc.want {
			t.Fatalf("%s: rateLimited=%v", name, got)
		}
	}
}

// 호출의 요청 거부 여부는 transport 칸의 거부 표시(400·413·422)에서만 온다.
func TestFirstDiagnosisModelCallRequestRejected(t *testing.T) {
	for name, tc := range map[string]struct {
		call *firstDiagnosisModelCall
		want bool
	}{
		"rejected": {&firstDiagnosisModelCall{result: func() llmpolicy.OpenRouterCallResult {
			return llmpolicy.OpenRouterCallResult{Reserved: true, Rejected: true}
		}}, true},
		"rate-limited": {&firstDiagnosisModelCall{result: func() llmpolicy.OpenRouterCallResult {
			return llmpolicy.OpenRouterCallResult{Reserved: true, NotBilled: true}
		}}, false},
		"server-error": {&firstDiagnosisModelCall{result: func() llmpolicy.OpenRouterCallResult { return llmpolicy.OpenRouterCallResult{Reserved: true} }}, false},
		"local":        {&firstDiagnosisModelCall{}, false},
		"nil":          {nil, false},
	} {
		if got := tc.call.requestRejected(); got != tc.want {
			t.Fatalf("%s: requestRejected=%v", name, got)
		}
	}
}

// 요청 한도 응답에 Retry-After가 있으면 간격표의 그 칸과 Retry-After 중 큰 쪽을 기다리되 요청 한도 간격표의 가장 긴 간격을
// 넘지 않는다(사용자 결정 2026-11-27 (나)). 다시 보내기는 요청 한도에서 최대 5번이다(사용자 결정 2026-11-26).
func TestFirstDiagnosisRetryModelCallHonorsRetryAfter(t *testing.T) {
	transient := errors.New("synthetic failure")
	short := []time.Duration{50 * time.Millisecond}
	long := []time.Duration{time.Millisecond, time.Millisecond, 40 * time.Millisecond}
	always := func() bool { return true }
	for name, tc := range map[string]struct {
		hint     time.Duration
		has      bool
		limited  bool
		min, max time.Duration
	}{
		// 간격표만이면 1+1+40ms.
		"no hint": {0, false, true, 40 * time.Millisecond, time.Second},
		// 힌트 0은 간격표보다 짧게 기다리게 하지 않는다(1+1+40ms 그대로).
		"zero hint": {0, true, true, 40 * time.Millisecond, time.Second},
		// 간격표 칸보다 긴 힌트(20ms)는 따른다: 20+20+40ms(마지막 칸은 40ms가 더 큼).
		"middle hint": {20 * time.Millisecond, true, true, 80 * time.Millisecond, time.Second},
		// 아주 긴 힌트는 가장 긴 간격(40ms)으로 잘린다: 3번 × 40ms.
		"long hint": {time.Hour, true, true, 120 * time.Millisecond, time.Second},
		// 요청 한도가 아니면 힌트(0)를 보지 않고 일반 간격표(50ms)대로 기다린다.
		"not limited": {0, true, false, 50 * time.Millisecond, time.Second},
	} {
		n := 0
		once := func() (llm.Message, string, llm.Usage, error, error) {
			n++
			return llm.Message{}, "", llm.Usage{}, transient, ErrFirstDiagnosisModelBudget
		}
		limited := tc.limited
		started := time.Now()
		_, _, _, err := firstDiagnosisRetryModelCallAfter(t.Context(), short, long, func() bool { return limited }, func() (time.Duration, bool) { return tc.hint, tc.has }, once, always)
		elapsed := time.Since(started)
		want := len(long) + 1
		if !tc.limited {
			want = len(short) + 1
		}
		if err == nil || n != want || elapsed < tc.min || elapsed > tc.max {
			t.Fatalf("%s: attempts=%d want %d elapsed=%v", name, n, want, elapsed)
		}
	}
	// 기본 요청 한도 간격표는 다시 보내기 5번이고, 힌트가 모두 상한(가장 긴 간격)이어도 준비 단계 상한보다 짧다.
	if len(firstDiagnosisRateLimitRetryDelays) != 5 || time.Duration(len(firstDiagnosisRateLimitRetryDelays))*slices.Max(firstDiagnosisRateLimitRetryDelays) >= FirstDiagnosisPreparationTimeout || slices.Max(firstDiagnosisRateLimitRetryDelays) != 30*time.Second {
		t.Fatalf("요청 한도 간격표 %v", firstDiagnosisRateLimitRetryDelays)
	}
}

// 호출 칸의 Retry-After는 transport가 남긴 값 그대로 읽는다.
func TestFirstDiagnosisModelCallRetryAfter(t *testing.T) {
	call := &firstDiagnosisModelCall{result: func() llmpolicy.OpenRouterCallResult {
		return llmpolicy.OpenRouterCallResult{Reserved: true, NotBilled: true, RetryAfter: 3 * time.Second, HasRetryAfter: true}
	}}
	if d, ok := call.retryAfter(); !ok || d != 3*time.Second {
		t.Fatal("Retry-After를 읽지 못함")
	}
	none := &firstDiagnosisModelCall{result: func() llmpolicy.OpenRouterCallResult { return llmpolicy.OpenRouterCallResult{NotBilled: true} }}
	if _, ok := none.retryAfter(); ok {
		t.Fatal("없는 Retry-After를 읽음")
	}
	if _, ok := (*firstDiagnosisModelCall)(nil).retryAfter(); ok {
		t.Fatal("빈 호출")
	}
}
