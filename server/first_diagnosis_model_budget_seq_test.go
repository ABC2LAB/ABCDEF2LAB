package server

import (
	"context"
	"sync"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/norma/llm"
)

// sequentialModelCalls는 시험의 before·after 래퍼가 들고 있는 예약이다(예산마다 하나, 순차 호출).
var sequentialModelCalls sync.Map

// before·after는 동시 1의 순차 호출을 그대로 쓰는 기존 시험용 래퍼다. 제품 경로는 reserve가 돌려준 예약을
// settle에 넘긴다(firstDiagnosisModelProvider.Complete).
func (b *FirstDiagnosisModelBudget) before(ctx context.Context, cfg agent.Config, req *llm.CompletionRequest) error {
	if ctx == nil {
		return ErrFirstDiagnosisModelBudget
	}
	if b == nil {
		return nil
	}
	call, err := b.reserve(ctx, cfg, req)
	if err != nil {
		return err
	}
	sequentialModelCalls.Store(b, call)
	return nil
}

func (b *FirstDiagnosisModelBudget) after(usage llm.Usage, outputLimit int, callErr error) error {
	if b == nil {
		return callErr
	}
	value, ok := sequentialModelCalls.LoadAndDelete(b)
	if !ok {
		return ErrFirstDiagnosisModelBudget
	}
	call := value.(*firstDiagnosisModelCall)
	call.output = int64(outputLimit)
	return b.settle(call, usage, callErr)
}
