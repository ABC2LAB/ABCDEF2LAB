package server

import (
	"context"
	"errors"
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
