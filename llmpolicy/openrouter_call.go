package llmpolicy

import (
	"context"
	"sync"
)

// OpenRouterCall은 호출 하나의 transport 결과 칸이다(P3 결정안 C28 결정 2). 모델 예산이 호출마다 만들어 요청
// 문맥에 붙이면 transport가 그 호출의 예약·실제 사용량을 여기에만 남긴다. 공유 누적 값의 차이로 대조하지 않으므로
// 겹친 호출도 호출별로 가른다. 한 칸은 HTTP 요청 하나에만 쓴다(재시도가 꺼져 있어 예약 하나에 요청 하나다).
type OpenRouterCall struct {
	mu     sync.Mutex
	used   bool
	result OpenRouterCallResult
}

// OpenRouterCallResult는 호출 하나의 transport 결과다.
type OpenRouterCallResult struct {
	// Reserved는 transport가 승인 예약을 만들고 송신을 시도했다는 뜻이다. 예약 전에 거부됐으면 false다.
	Reserved bool
	// Succeeded는 사용량·비용을 확인한 정상 응답이다.
	Succeeded                                                     bool
	ReservedInputTokens, ReservedOutputTokens, ReservedCostMicros int64
	InputTokens, OutputTokens, CostMicros                         int64
}

type openRouterCallKey struct{}

// WithOpenRouterCall은 요청 문맥에 호출 칸을 붙인다.
func WithOpenRouterCall(ctx context.Context, c *OpenRouterCall) context.Context {
	return context.WithValue(ctx, openRouterCallKey{}, c)
}

func openRouterCallFrom(ctx context.Context) *OpenRouterCall {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(openRouterCallKey{}).(*OpenRouterCall)
	return c
}

// Result는 지금까지 남은 결과다.
func (c *OpenRouterCall) Result() OpenRouterCallResult {
	if c == nil {
		return OpenRouterCallResult{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.result
}

// claim은 칸을 첫 요청에만 내준다. 같은 칸으로 두 번째 요청이 오면 거부한다.
func (c *OpenRouterCall) claim() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.used {
		return false
	}
	c.used = true
	return true
}

func (c *OpenRouterCall) reserved(input, output, cost int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.result.Reserved = true
	c.result.ReservedInputTokens, c.result.ReservedOutputTokens, c.result.ReservedCostMicros = input, output, cost
}

func (c *OpenRouterCall) succeeded(input, output, cost int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.result.Succeeded = true
	c.result.InputTokens, c.result.OutputTokens, c.result.CostMicros = input, output, cost
}
