package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/norma/llm"
)

var ErrFirstDiagnosisModelBudget = errors.New("first_diagnosis_model_budget_unavailable")

// 입력은 추정 토큰 대신 설치자가 고정한 전체 context를 매 호출 보수적으로 예약한다.
// 출력 한도와 직렬화 크기도 호출 전에 검사하며 성공 뒤에도 예약을 환불하지 않는다.
type FirstDiagnosisModelBudget struct {
	mu            sync.Mutex
	callGate      chan struct{}
	limits        diagnosisprep.Budget
	inputPerCall  int64
	outputPerCall int64
	usage         diagnosisprep.ModelUsage
	blocked       bool
	observe       func(diagnosisprep.ModelUsage) error
	external      *llmpolicy.OpenRouterPolicy
	manifestPath  string
	lastExternal  llmpolicy.OpenRouterUsage
}

func NewFirstDiagnosisModelBudget(limits diagnosisprep.Budget, inputPerCall, outputPerCall int64, observe func(diagnosisprep.ModelUsage) error) (*FirstDiagnosisModelBudget, error) {
	if limits.ModelRequests < 1 || limits.InputTokens < 1 || limits.OutputTokens < 1 || limits.CostUSDMicros != 0 ||
		inputPerCall < 4096 || inputPerCall > 4<<20 || outputPerCall < 1 || outputPerCall > inputPerCall ||
		inputPerCall > limits.InputTokens || outputPerCall > limits.OutputTokens || observe == nil {
		return nil, ErrFirstDiagnosisModelBudget
	}
	return &FirstDiagnosisModelBudget{callGate: make(chan struct{}, 1), limits: limits, inputPerCall: inputPerCall, outputPerCall: outputPerCall, usage: diagnosisprep.ModelUsage{UsageKnown: true}, observe: observe}, nil
}

// One policy and call gate survive the preparation -> execution handoff.
// The URL journal has already reserved the entire run against delegation totals.
// Transport owns approval accounting; this adapter persists run reservations
// before transport and copies validated actual cost, never sums both ledgers.
func NewFirstDiagnosisOpenRouterBudget(limits diagnosisprep.Budget, inputPerCall, outputPerCall int64, manifestPath string, observe func(diagnosisprep.ModelUsage) error) (*FirstDiagnosisModelBudget, error) {
	p, err := llmpolicy.LoadOpenRouterManifestFile(manifestPath)
	if err != nil || p.CheckAvailable() != nil {
		return nil, ErrFirstDiagnosisModelBudget
	}
	m := p.Limits()
	if limits.CostUSDMicros <= 0 || limits.CostUSDMicros > m.CostMicros || limits.ModelRequests > m.Requests || limits.InputTokens > m.InputTokens || limits.OutputTokens > m.OutputTokens || inputPerCall != m.InputPerCall || outputPerCall > m.OutputPerCall {
		return nil, ErrFirstDiagnosisModelBudget
	}
	localLimits := limits
	localLimits.CostUSDMicros = 0
	b, err := NewFirstDiagnosisModelBudget(localLimits, inputPerCall, outputPerCall, observe)
	if err != nil {
		return nil, err
	}
	b.limits, b.external, b.manifestPath = limits, p, manifestPath
	return b, nil
}

// 같은 회차의 Planner/Worker가 겹쳐도 미확인 사용량을 실패로 오인하지 않는다.
// 예약 전부터 결과 사용량의 동기 영속 확인까지 한 호출만 gate를 보유한다.
// 대기 취소는 예약이나 모델 호출을 만들지 않으며 실제 unknown latch는 해제하지 않는다.
func (b *FirstDiagnosisModelBudget) acquireCall(ctx context.Context) (func(), error) {
	if b == nil || b.callGate == nil || ctx == nil {
		return nil, ErrFirstDiagnosisModelBudget
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case b.callGate <- struct{}{}:
	}
	release := func() { <-b.callGate }
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
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
	var cost int64
	if b.external != nil {
		cost, err = b.external.Quote(int64(req.MaxTokens))
		if err != nil {
			return ErrFirstDiagnosisModelBudget
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.blocked || !b.usage.UsageKnown || b.usage.Requests >= b.limits.ModelRequests ||
		b.inputPerCall > b.limits.InputTokens-b.usage.ReservedInputTokens ||
		int64(req.MaxTokens) > b.limits.OutputTokens-b.usage.ReservedOutputTokens || cost > b.limits.CostUSDMicros-b.usage.ReservedCostUSDMicros {
		return ErrFirstDiagnosisModelBudget
	}
	b.usage.Requests++
	b.usage.ReservedInputTokens += b.inputPerCall
	b.usage.ReservedOutputTokens += int64(req.MaxTokens)
	b.usage.ReservedCostUSDMicros += cost
	if b.external != nil {
		b.lastExternal = b.external.Snapshot()
	}
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
	if b.external != nil {
		next, previous := b.external.Snapshot(), b.lastExternal
		// Exactly one transport call must account for this SDK completion.
		known = known && !next.Blocked && next.Requests == previous.Requests+1 && next.Errors == previous.Errors && next.InputTokens-previous.InputTokens == int64(usage.InputTokens) && next.OutputTokens-previous.OutputTokens == int64(usage.OutputTokens) && next.CostMicros >= previous.CostMicros && next.CostMicros <= b.usage.ReservedCostUSDMicros
		if known {
			b.usage.CostUSDMicros += next.CostMicros - previous.CostMicros
		}
	}
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
