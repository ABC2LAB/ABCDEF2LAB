package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/norma/llm"
)

var ErrFirstDiagnosisModelBudget = errors.New("first_diagnosis_model_budget_unavailable")

// 대상의 정상 철회·만료는 신규 작업을 막되 이미 예약한 모델 사용량은 마감한다.
var ErrFirstDiagnosisTargetEnded = errors.New("first_diagnosis_target_approval_ended")

type firstDiagnosisTargetDrain struct {
	hard  context.Context
	grace time.Duration
	admit func() error
}

// 입력은 직렬화한 요청 바이트 수에 framing 여유를 더한 값으로 예약한다(P3 결정안 C28 결정 1). 토큰 하나는 1바이트
// 이상이라 이 값이 입력 토큰의 상한이고, 설치자가 고정한 context보다 크면 거부한다. 출력은 max_tokens로 예약한다.
// 성공 뒤에도 예약을 환불하지 않는다.
// firstDiagnosisFramingTokens는 요청 JSON 바이트에 더하는 provider framing 여유다. 대화 템플릿의 특수 토큰은
// 메시지마다 몇 개뿐이고 JSON의 역할·구분 바이트가 그보다 많으므로, 이 여유는 템플릿 머리말과 시스템 토큰 몫이다.
const firstDiagnosisFramingTokens = 4096

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
	// 진행 중 호출 하나의 예약(동시 호출 1, gate 안). 실패한 호출을 이 양으로 정산한다(C05).
	callInput   int64
	callCost    int64
	targetDrain *firstDiagnosisTargetDrain
	admission   func(context.Context) error
}

// 실행 전에 한 번만 설정한다. 사용자 중단·시간 상한·감사 실패는 hard context로 즉시 취소한다.
func (b *FirstDiagnosisModelBudget) ConfigureTargetDrain(hard context.Context, grace time.Duration, admit func() error) error {
	if b == nil || hard == nil || hard.Err() != nil || grace <= 0 || grace > 30*time.Second || admit == nil {
		return ErrFirstDiagnosisModelBudget
	}
	if _, ok := hard.Deadline(); !ok {
		return ErrFirstDiagnosisModelBudget
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.targetDrain != nil || b.usage.Requests != 0 {
		return ErrFirstDiagnosisModelBudget
	}
	b.targetDrain = &firstDiagnosisTargetDrain{hard: hard, grace: grace, admit: admit}
	return nil
}

// 준비 전 설치된 워크플로가 일시정지 때 새 모델 예약을 기다리게 한다.
func (b *FirstDiagnosisModelBudget) ConfigureAdmission(wait func(context.Context) error) error {
	if b == nil || wait == nil {
		return ErrFirstDiagnosisModelBudget
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.admission != nil || b.usage.Requests != 0 {
		return ErrFirstDiagnosisModelBudget
	}
	b.admission = wait
	return nil
}

// 원래 deadline을 유지하며 정상 대상 종료만 제한된 시간 동안 응답 수신을 허용한다.
func (b *FirstDiagnosisModelBudget) modelCallContext(ctx context.Context) (context.Context, context.CancelFunc) {
	d := b.targetDrain
	if d == nil {
		return context.WithCancel(ctx)
	}
	deadline, ok := ctx.Deadline()
	hardDeadline, _ := d.hard.Deadline()
	if !ok || hardDeadline.Before(deadline) {
		deadline = hardDeadline
	}
	call, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	var mu sync.Mutex
	var timer *time.Timer
	closed := false
	stopHard := context.AfterFunc(d.hard, cancel)
	stopCall := context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return
		}
		if errors.Is(context.Cause(ctx), ErrFirstDiagnosisTargetEnded) {
			timer = time.AfterFunc(d.grace, cancel)
		} else {
			cancel()
		}
	})
	return call, func() {
		stopHard()
		stopCall()
		mu.Lock()
		closed = true
		if timer != nil {
			timer.Stop()
		}
		mu.Unlock()
		cancel()
	}
}

func NewFirstDiagnosisModelBudget(limits diagnosisprep.Budget, inputPerCall, outputPerCall int64, observe func(diagnosisprep.ModelUsage) error) (*FirstDiagnosisModelBudget, error) {
	if limits.ModelRequests < 1 || limits.InputTokens < 1 || limits.OutputTokens < 1 || limits.CostUSDMicros != 0 ||
		inputPerCall < 4096 || inputPerCall > 4<<20 || outputPerCall < 1 || outputPerCall > inputPerCall ||
		inputPerCall > limits.InputTokens || outputPerCall > limits.OutputTokens || observe == nil {
		return nil, ErrFirstDiagnosisModelBudget
	}
	return &FirstDiagnosisModelBudget{callGate: make(chan struct{}, 1), limits: limits, inputPerCall: inputPerCall, outputPerCall: outputPerCall, usage: diagnosisprep.ModelUsage{UsageKnown: true}, observe: observe}, nil
}

// 준비에서 실행으로 넘어가도 정책과 호출 gate 하나를 공유한다.
// URL 저널은 위임 총량에서 회차 전체를 이미 예약한 상태다.
// transport가 승인 원장을 소유하고, 이 어댑터는 송신 전 회차 예약을 저장한다.
// 검증된 실제 비용만 복사하며 두 원장의 금액을 더하지 않는다.
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
	if ctx == nil {
		return ErrFirstDiagnosisModelBudget
	}
	if b != nil && b.admission != nil {
		if err := b.admission(ctx); err != nil {
			return err
		}
	}
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
	reserveInput := int64(len(raw)) + firstDiagnosisFramingTokens
	if err != nil || reserveInput > b.inputPerCall {
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
	if ctx.Err() != nil || b.targetDrain != nil && b.targetDrain.admit() != nil {
		return ErrFirstDiagnosisModelBudget
	}
	if b.blocked || !b.usage.UsageKnown || b.usage.Requests >= b.limits.ModelRequests ||
		reserveInput > b.limits.InputTokens-b.usage.ReservedInputTokens ||
		int64(req.MaxTokens) > b.limits.OutputTokens-b.usage.ReservedOutputTokens || cost > b.limits.CostUSDMicros-b.usage.ReservedCostUSDMicros {
		return ErrFirstDiagnosisModelBudget
	}
	b.usage.Requests++
	b.usage.ReservedInputTokens += reserveInput
	b.usage.ReservedOutputTokens += int64(req.MaxTokens)
	b.usage.ReservedCostUSDMicros += cost
	b.callInput, b.callCost = reserveInput, cost
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

// failedCallBounded는 실패한 호출을 예약 상한으로 정산해도 되는지 본다. 호출이 성공했는데 사용량이 예약을
// 넘거나 형식이 틀리면 상한을 믿을 수 없어 막는다. 로컬 모델 정책이 막은 요청과 외부 제공자 정책이 막힌 상태도
// 막는다(통제 경계 사건이지 일시 실패가 아니다). 외부 제공자의 일시 오류 구분은 C06에서 한다.
func failedCallBounded(callErr error, externalBlocked bool) bool {
	if callErr == nil || externalBlocked || errors.Is(callErr, llmpolicy.ErrRequestBlocked) || errors.Is(callErr, llmpolicy.ErrClientOptions) {
		return false
	}
	return true
}

func (b *FirstDiagnosisModelBudget) after(usage llm.Usage, outputLimit int, callErr error) error {
	if b == nil {
		return callErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	known := callErr == nil && usage.InputTokens > 0 && usage.OutputTokens >= 0 &&
		int64(usage.InputTokens) <= b.callInput && usage.OutputTokens <= outputLimit &&
		usage.CacheReadTokens >= 0 && usage.CacheWriteTokens >= 0
	if b.external != nil {
		next, previous := b.external.Snapshot(), b.lastExternal
		// 이 SDK 응답은 정확히 한 번의 transport 호출 사용량과 일치해야 한다.
		known = known && !next.Blocked && next.Requests == previous.Requests+1 && next.Errors == previous.Errors && next.InputTokens-previous.InputTokens == int64(usage.InputTokens) && next.OutputTokens-previous.OutputTokens == int64(usage.OutputTokens) && next.CostMicros >= previous.CostMicros && next.CostMicros <= b.usage.ReservedCostUSDMicros
		if known {
			b.usage.CostUSDMicros += next.CostMicros - previous.CostMicros
		}
	}
	switch {
	case known:
		b.usage.InputTokens += int64(usage.InputTokens)
		b.usage.OutputTokens += int64(usage.OutputTokens)
		b.usage.UsageKnown = true
	case failedCallBounded(callErr, b.external != nil && b.external.Snapshot().Blocked):
		// 실패한 호출은 예약한 양을 쓴 것으로 센다(P3 결정안 C05). 재시도가 꺼져 있어 예약 하나에 HTTP 요청은
		// 하나이고, 입력 크기·max_tokens·가격 상한을 호출 전에 검사했으므로 이 값이 실제 사용의 상한이다.
		// 다음 호출의 예산 검사는 예약 합계로 하므로 상한은 넓어지지 않는다.
		b.usage.InputTokens += b.callInput
		b.usage.OutputTokens += int64(outputLimit)
		b.usage.CostUSDMicros += b.callCost
		b.usage.UnknownUsageCalls++
		b.usage.UsageKnown = true
	default:
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
