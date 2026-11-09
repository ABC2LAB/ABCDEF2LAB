package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/norma/llm"
)

var ErrFirstDiagnosisModelBudget = errors.New("first_diagnosis_model_budget_unavailable")

// 입력은 추정 토큰 대신 설치자가 고정한 전체 context를 매 호출 보수적으로 예약한다.
// 출력 한도와 직렬화 크기도 호출 전에 검사하며 성공 뒤에도 예약을 환불하지 않는다.
type FirstDiagnosisModelBudget struct {
	mu            sync.Mutex
	limits        diagnosisprep.Budget
	inputPerCall  int64
	outputPerCall int64
	usage         diagnosisprep.ModelUsage
	blocked       bool
	observe       func(diagnosisprep.ModelUsage) error
}

func NewFirstDiagnosisModelBudget(limits diagnosisprep.Budget, inputPerCall, outputPerCall int64, observe func(diagnosisprep.ModelUsage) error) (*FirstDiagnosisModelBudget, error) {
	if limits.ModelRequests < 1 || limits.InputTokens < 1 || limits.OutputTokens < 1 || limits.CostUSDMicros != 0 ||
		inputPerCall < 4096 || inputPerCall > 4<<20 || outputPerCall < 1 || outputPerCall > inputPerCall ||
		inputPerCall > limits.InputTokens || outputPerCall > limits.OutputTokens || observe == nil {
		return nil, ErrFirstDiagnosisModelBudget
	}
	return &FirstDiagnosisModelBudget{limits: limits, inputPerCall: inputPerCall, outputPerCall: outputPerCall, usage: diagnosisprep.ModelUsage{UsageKnown: true}, observe: observe}, nil
}

func (*FirstDiagnosisModelBudget) String() string {
	return "server.FirstDiagnosisModelBudget[protected]"
}

func (b *FirstDiagnosisModelBudget) Snapshot() diagnosisprep.ModelUsage {
	if b == nil {
		return diagnosisprep.ModelUsage{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usage
}

// 기본 SDK 재시도를 끄고 같은 정책을 준비·실행의 모든 provider가 공유한다.
func (b *FirstDiagnosisModelBudget) checkConfig(c agent.Config) error {
	if b == nil {
		return nil
	}
	if c.Format != llm.FormatOpenAI || c.Stream || c.ContextWindowK <= 0 || int64(c.ContextWindowK)*1024 != b.inputPerCall ||
		c.MaxTokens <= 0 || int64(c.MaxTokens) > b.outputPerCall || c.Proxy != "" ||
		(c.MaxTokensField != "" && c.MaxTokensField != "max_tokens") {
		return ErrFirstDiagnosisModelBudget
	}
	return nil
}

func (b *FirstDiagnosisModelBudget) before(ctx context.Context, cfg agent.Config, req *llm.CompletionRequest) error {
	if b == nil {
		return nil
	}
	if ctx == nil || ctx.Err() != nil || req == nil || b.checkConfig(cfg) != nil {
		return ErrFirstDiagnosisModelBudget
	}
	if req.MaxTokens <= 0 || int64(req.MaxTokens) > b.outputPerCall {
		req.MaxTokens = int(b.outputPerCall)
	}
	raw, err := json.Marshal(req)
	// 텍스트·도구 JSON의 바이트 상한에 provider framing 여유를 포함한다.
	// provider의 모델 context pin은 설치자가 검증한 실제 로컬 서버 상한이어야 한다.
	if err != nil || int64(len(raw))+4096 > b.inputPerCall {
		return ErrFirstDiagnosisModelBudget
	}
	clear(raw)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.blocked || !b.usage.UsageKnown || b.usage.Requests >= b.limits.ModelRequests ||
		b.inputPerCall > b.limits.InputTokens-b.usage.ReservedInputTokens ||
		int64(req.MaxTokens) > b.limits.OutputTokens-b.usage.ReservedOutputTokens {
		return ErrFirstDiagnosisModelBudget
	}
	b.usage.Requests++
	b.usage.ReservedInputTokens += b.inputPerCall
	b.usage.ReservedOutputTokens += int64(req.MaxTokens)
	b.usage.UsageKnown = false
	if b.observe(b.usage) != nil {
		b.blocked = true
		return ErrFirstDiagnosisModelBudget
	}
	return nil
}

func (b *FirstDiagnosisModelBudget) after(usage llm.Usage, outputLimit int, callErr error) error {
	if b == nil {
		return callErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	known := callErr == nil && usage.InputTokens > 0 && usage.OutputTokens >= 0 &&
		int64(usage.InputTokens) <= b.inputPerCall && usage.OutputTokens <= outputLimit &&
		usage.CacheReadTokens >= 0 && usage.CacheWriteTokens >= 0
	if known {
		b.usage.InputTokens += int64(usage.InputTokens)
		b.usage.OutputTokens += int64(usage.OutputTokens)
		b.usage.UsageKnown = true
	} else {
		b.blocked = true
		b.usage.UsageKnown = false
	}
	if b.observe(b.usage) != nil {
		b.blocked = true
		b.usage.UsageKnown = false
		return ErrFirstDiagnosisModelBudget
	}
	if !known {
		return ErrFirstDiagnosisModelBudget
	}
	return nil
}
