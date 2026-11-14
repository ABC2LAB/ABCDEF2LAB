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
// 예산 검사는 정산한 사용량과 진행 중 예약으로 한다(P3 결정안 C28 결정 2). 정산한 호출의 남은 예약은 다시 쓸 수 있다.
// Reserved*는 기록용 누적 예약 합계라 줄이지 않는다.
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
	// externalBase는 이 예산을 만들 때의 transport 누적 값, externalSum은 호출 칸으로 정산한 합계다. 진행 중 호출이
	// 없을 때 transport 누적 값의 늘어난 양이 정산 합계와 같아야 한다(C28 결정 2).
	externalBase llmpolicy.OpenRouterUsage
	externalSum  llmpolicy.OpenRouterUsage
	// externalUsage는 transport 누적 값을 읽는다(시험이 바꿔 끼운다). nil이면 external.Snapshot이다.
	externalUsage func() llmpolicy.OpenRouterUsage
	manifestPath  string
	// inflight는 정산 전 예약 수다. 사용량이 모두 정산됐는지(UsageKnown)는 진행 중 예약이 없고 막힘이 없을 때다.
	inflight int
	// inflightInput·inflightOutput·inflightCost는 정산 전 예약의 합계다. 예산 검사는 정산 사용량 + 이 합계로 한다.
	inflightInput, inflightOutput, inflightCost int64
	targetDrain                                 *firstDiagnosisTargetDrain
	admission                                   func(context.Context) error
	// limitReached는 승인 예산이 남지 않아 새 호출을 받지 못한 적이 있다는 뜻이다(budget_limited).
	limitReached bool
	// retryDelays는 실패한 모델 호출을 다시 보내기 전 기다리는 간격이다(기본 firstDiagnosisModelRetryDelays). 비어 있으면
	// 다시 보내지 않는다.
	retryDelays []time.Duration
}

// firstDiagnosisModelCall은 호출 하나의 예약이다(P3 결정안 C28 결정 2). reserve가 만들고 settle이 한 번 닫는다.
// 정산은 이 예약만 보고 다른 진행 중 호출의 예약·정산은 건드리지 않는다.
type firstDiagnosisModelCall struct {
	input, output, cost int64
	// slot은 OpenRouter 호출 칸이다. 요청 문맥에 붙여 transport가 이 호출의 예약·사용량을 여기에만 남긴다.
	// 그래서 겹친 호출도 각자 자기 칸으로 정산한다. 로컬 모델 경로에는 없다.
	slot *llmpolicy.OpenRouterCall
	// result는 이 호출 칸의 transport 결과를 읽는다(시험이 바꿔 끼운다).
	result func() llmpolicy.OpenRouterCallResult
	closed bool
}

// requestContext는 모델 요청 문맥에 이 호출의 칸을 붙인다.
func (c *firstDiagnosisModelCall) requestContext(ctx context.Context) context.Context {
	if c == nil || c.slot == nil {
		return ctx
	}
	return llmpolicy.WithOpenRouterCall(ctx, c.slot)
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
	return &FirstDiagnosisModelBudget{callGate: make(chan struct{}, 1), limits: limits, inputPerCall: inputPerCall, outputPerCall: outputPerCall, usage: diagnosisprep.ModelUsage{UsageKnown: true}, observe: observe, retryDelays: firstDiagnosisModelRetryDelays}, nil
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
	b.limits, b.external, b.manifestPath, b.externalBase = limits, p, manifestPath, p.Snapshot()
	return b, nil
}

// 같은 회차의 Planner/Worker가 겹쳐도 미확인 사용량을 실패로 오인하지 않는다.
// 예약 전부터 결과 사용량의 동기 영속 확인까지 한 호출만 gate를 보유한다.
// 대기 취소는 예약이나 모델 호출을 만들지 않으며 실제 unknown latch는 해제하지 않는다.
// ConfigureConcurrency는 동시 모델 호출 수를 정한다(C32, P3 결정안 C28 결정 2). 첫 호출 전에 한 번만 바꾼다.
// 호출별 예약·정산과 호출 칸 대조가 있어 gate를 넓혀도 회계가 갈리지 않는다. OpenRouter는 승인 max_concurrent를
// 넘지 못하고, 로컬 모델은 코드에 동시 호출 상한이 없어 1만 받는다(설치 결정안 2-4).
func (b *FirstDiagnosisModelBudget) ConfigureConcurrency(n int) error {
	if b == nil || n < 1 {
		return ErrFirstDiagnosisModelBudget
	}
	if b.external == nil && n != 1 || b.external != nil && int64(n) > b.external.Limits().MaxConcurrent {
		return ErrFirstDiagnosisModelBudget
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.usage.Requests != 0 || b.inflight != 0 || len(b.callGate) != 0 {
		return ErrFirstDiagnosisModelBudget
	}
	b.callGate = make(chan struct{}, n)
	return nil
}

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

// LimitReached는 승인 모델 예산이 다음 호출을 받을 만큼 남지 않아 거부한 적이 있는지 본다. 막힘(사용량 불명·
// 정책 차단)은 여기에 들지 않는다.
// retryable은 실패한 호출을 새 예약으로 다시 보내도 되는지다: 이 예산과 외부 승인 정책이 막히지 않았다(일시 오류는
// 정산 뒤에도 막히지 않고, 설정 오류·회계 불일치·연속 일시 오류 상한은 막힌다).
func (b *FirstDiagnosisModelBudget) retryable() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	blocked := b.blocked
	b.mu.Unlock()
	return !blocked && (b.external == nil || !b.external.Snapshot().Blocked)
}

func (b *FirstDiagnosisModelBudget) LimitReached() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limitReached && !b.blocked
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

// reserve는 호출 하나를 예약한다. 예산 검사는 정산 사용량이 아니라 예약 합계(정산된 호출과 진행 중 호출의 예약)로
// 하므로 진행 중 호출이 있다는 이유만으로는 거부하지 않는다. 거부는 예산 부족·막힘(C05에 남긴 경우)·설정 위반이다.
func (b *FirstDiagnosisModelBudget) reserve(ctx context.Context, cfg agent.Config, req *llm.CompletionRequest) (*firstDiagnosisModelCall, error) {
	if ctx == nil || b == nil {
		return nil, ErrFirstDiagnosisModelBudget
	}
	if b.admission != nil {
		if err := b.admission(ctx); err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil || req == nil || b.checkConfig(cfg) != nil {
		return nil, ErrFirstDiagnosisModelBudget
	}
	if req.MaxTokens <= 0 || int64(req.MaxTokens) > b.outputPerCall {
		req.MaxTokens = int(b.outputPerCall)
	}
	raw, err := json.Marshal(req)
	// 텍스트·도구 JSON의 바이트 상한에 provider framing 여유를 포함한다.
	// provider의 모델 context pin은 설치자가 검증한 실제 로컬 서버 상한이어야 한다.
	reserveInput := int64(len(raw)) + firstDiagnosisFramingTokens
	if err != nil || reserveInput > b.inputPerCall {
		return nil, ErrFirstDiagnosisModelBudget
	}
	clear(raw)
	var cost int64
	if b.external != nil {
		// 비용 예약도 입력 예약(요청 크기 상한)으로 견적한다(C28 결정 1).
		cost, err = b.external.QuoteInput(reserveInput, int64(req.MaxTokens))
		if err != nil {
			return nil, ErrFirstDiagnosisModelBudget
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if ctx.Err() != nil || b.targetDrain != nil && b.targetDrain.admit() != nil {
		return nil, ErrFirstDiagnosisModelBudget
	}
	if b.blocked {
		return nil, ErrFirstDiagnosisModelBudget
	}
	if b.external != nil {
		// 연속 일시 오류 상한으로 잠긴 승인은 새 호출을 보내지 않는다. 예약하지 않고 거절하므로 회계는 그대로 알고 있다.
		// 그대로 보내면 transport가 정책 거부로 돌려주고 그 호출이 회계 막힘으로 남아 실행 전체가 결과 불명이 된다.
		if snap := b.externalSnapshot(); snap.Blocked && snap.TransientLimited {
			return nil, ErrFirstDiagnosisModelBudget
		}
	}
	if b.usage.Requests >= b.limits.ModelRequests ||
		reserveInput > b.limits.InputTokens-b.usage.InputTokens-b.inflightInput ||
		int64(req.MaxTokens) > b.limits.OutputTokens-b.usage.OutputTokens-b.inflightOutput ||
		cost > b.limits.CostUSDMicros-b.usage.CostUSDMicros-b.inflightCost {
		// 승인 예산이 다음 호출을 받을 만큼 남지 않았다. 막힘(불명·정책)과 달리 실행 실패가 아니라 마감 사유다.
		b.limitReached = true
		return nil, ErrFirstDiagnosisModelBudget
	}
	call := &firstDiagnosisModelCall{input: reserveInput, output: int64(req.MaxTokens), cost: cost}
	b.usage.Requests++
	b.usage.ReservedInputTokens += call.input
	b.usage.ReservedOutputTokens += call.output
	b.usage.ReservedCostUSDMicros += call.cost
	b.inflightInput += call.input
	b.inflightOutput += call.output
	b.inflightCost += call.cost
	if b.external != nil {
		call.slot = &llmpolicy.OpenRouterCall{}
		call.result = call.slot.Result
	}
	b.inflight++
	b.usage.UsageKnown = false
	// 진행 중 예약을 포함한 합계를 남긴다. 재시작 때 닫히지 않은 예약은 예약한 양을 쓴 것으로 센다.
	if b.observe(b.usage) != nil {
		b.blocked = true
		return nil, ErrFirstDiagnosisModelBudget
	}
	return call, nil
}

// failedCallBounded는 실패한 호출을 예약 상한으로 정산해도 되는지 본다. 호출이 성공했는데 사용량이 예약을
// 넘거나 형식이 틀리면 상한을 믿을 수 없어 막는다. 로컬 모델 정책이 막은 요청과 외부 제공자 정책이 막힌 상태도
// 막는다(통제 경계 사건이지 일시 실패가 아니다). 외부 제공자의 일시 오류 구분은 C06에서 한다.
// externalAccountingBlocked는 외부 승인이 회계를 믿을 수 없는 원인으로 막혔는지 본다. 연속 일시 오류 상한(C06)만으로
// 잠긴 것은 제공자가 처리하지 않은 호출이 이어진 것이라 실패 호출을 예약량으로 정산할 수 있다.
func (b *FirstDiagnosisModelBudget) externalAccountingBlocked() bool {
	if b.external == nil {
		return false
	}
	snap := b.externalSnapshot()
	return snap.Blocked && !snap.TransientLimited
}

// externalSnapshot은 transport 상태를 읽는다(externalUsage가 있으면 그것, 대조와 같은 출처).
func (b *FirstDiagnosisModelBudget) externalSnapshot() llmpolicy.OpenRouterUsage {
	if b.externalUsage != nil {
		return b.externalUsage()
	}
	return b.external.Snapshot()
}

func failedCallBounded(callErr error, externalBlocked bool) bool {
	if callErr == nil || externalBlocked || errors.Is(callErr, llmpolicy.ErrRequestBlocked) || errors.Is(callErr, llmpolicy.ErrClientOptions) {
		return false
	}
	return true
}

// reportedWithinReservation은 실패한 호출이 함께 보고한 사용량이 예약 안인지 본다. 보고 값이 예약보다 크면
// 예약을 상한으로 쓴 근거(요청 크기·max_tokens)가 깨진 것이므로 예약량으로 정산하지 않고 막는다.
// 보고 값이 없으면(0) 예약량으로 정산한다.
func reportedWithinReservation(usage llm.Usage, reservedInput int64, outputLimit int) bool {
	return usage.InputTokens >= 0 && usage.OutputTokens >= 0 && usage.CacheReadTokens >= 0 && usage.CacheWriteTokens >= 0 &&
		int64(usage.InputTokens) <= reservedInput && usage.OutputTokens <= outputLimit
}

// addExternal은 호출 칸 하나의 transport 결과를 정산 합계에 더한다. 예약 전에 거부된 호출은 transport도 세지 않는다.
func (b *FirstDiagnosisModelBudget) addExternal(r llmpolicy.OpenRouterCallResult) {
	s := &b.externalSum
	if r.Reserved {
		s.Requests++
		s.ReservedInputTokens += r.ReservedInputTokens
		s.ReservedOutputTokens += r.ReservedOutputTokens
		s.ReservedCostMicros += r.ReservedCostMicros
	}
	if r.Succeeded {
		s.InputTokens += r.InputTokens
		s.OutputTokens += r.OutputTokens
		s.CostMicros += r.CostMicros
	}
}

// externalReconciled는 이 예산을 만든 뒤 transport에서 늘어난 예약·사용량이 호출 칸으로 정산한 합계와 같은지 본다.
func (b *FirstDiagnosisModelBudget) externalReconciled() bool {
	read := b.external.Snapshot
	if b.externalUsage != nil {
		read = b.externalUsage
	}
	now, base, sum := read(), b.externalBase, b.externalSum
	return now.Requests-base.Requests == sum.Requests &&
		now.ReservedInputTokens-base.ReservedInputTokens == sum.ReservedInputTokens &&
		now.ReservedOutputTokens-base.ReservedOutputTokens == sum.ReservedOutputTokens &&
		now.ReservedCostMicros-base.ReservedCostMicros == sum.ReservedCostMicros &&
		now.InputTokens-base.InputTokens == sum.InputTokens &&
		now.OutputTokens-base.OutputTokens == sum.OutputTokens &&
		now.CostMicros-base.CostMicros == sum.CostMicros
}

// settle은 호출 하나의 예약을 닫는다. 뒤에 시작한 호출이 먼저 끝나도 각자 자기 예약으로 정산한다.
func (b *FirstDiagnosisModelBudget) settle(call *firstDiagnosisModelCall, usage llm.Usage, callErr error) error {
	if b == nil || call == nil {
		return ErrFirstDiagnosisModelBudget
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if call.closed {
		// 같은 예약을 두 번 닫으면 회계를 믿을 수 없다.
		b.blocked = true
		b.usage.UsageKnown = false
		return ErrFirstDiagnosisModelBudget
	}
	call.closed = true
	b.inflight--
	b.inflightInput -= call.input
	b.inflightOutput -= call.output
	b.inflightCost -= call.cost
	outputLimit := int(call.output)
	known := callErr == nil && usage.InputTokens > 0 && usage.OutputTokens >= 0 &&
		int64(usage.InputTokens) <= call.input && usage.OutputTokens <= outputLimit &&
		usage.CacheReadTokens >= 0 && usage.CacheWriteTokens >= 0
	if b.external != nil {
		var r llmpolicy.OpenRouterCallResult
		if call.result != nil {
			r = call.result()
		}
		b.addExternal(r)
		// 이 SDK 응답은 이 호출 칸에 남은 transport 결과(정상 응답 하나)와 정확히 같아야 한다. 다른 호출의 누적 값은
		// 보지 않는다.
		known = known && r.Succeeded && r.InputTokens == int64(usage.InputTokens) && r.OutputTokens == int64(usage.OutputTokens) && r.CostMicros >= 0 && r.CostMicros <= call.cost
		if known {
			b.usage.CostUSDMicros += r.CostMicros
		}
	}
	switch {
	case known:
		b.usage.InputTokens += int64(usage.InputTokens)
		b.usage.OutputTokens += int64(usage.OutputTokens)
	case failedCallBounded(callErr, b.externalAccountingBlocked()) && reportedWithinReservation(usage, call.input, outputLimit):
		// 실패한 호출은 예약한 양을 쓴 것으로 센다(P3 결정안 C05). 재시도가 꺼져 있어 예약 하나에 HTTP 요청은
		// 하나이고, 입력 크기·max_tokens·가격 상한을 호출 전에 검사했으므로 이 값이 실제 사용의 상한이다.
		// 예약량을 정산 사용량에 넣으므로 다음 호출의 예산 검사(정산 사용량 + 진행 중 예약)에서 상한은 넓어지지 않는다.
		b.usage.InputTokens += call.input
		b.usage.OutputTokens += call.output
		b.usage.CostUSDMicros += call.cost
		b.usage.UnknownUsageCalls++
	default:
		b.blocked = true
	}
	// 진행 중 호출이 없으면 transport 누적 값이 호출 칸으로 정산한 합계와 같아야 한다. 칸 없이 나간 요청이나 어긋난
	// 회계가 있으면 예산 보장이 깨진 것이라 이 승인으로 새 호출을 막는다(C28 결정 2).
	if b.external != nil && b.inflight == 0 && !b.externalReconciled() {
		b.blocked = true
	}
	// 모든 예약이 정산됐고 막힘이 없을 때만 사용량을 안다. 진행 중 호출이 남아 있으면 아직 모른다.
	b.usage.UsageKnown = !b.blocked && b.inflight == 0
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
