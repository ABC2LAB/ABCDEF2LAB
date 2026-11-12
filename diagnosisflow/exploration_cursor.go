package diagnosisflow

import "context"

// ExplorationCursor는 같은 회차의 검증·저장된 순번만 호스트에 돌려준다.
// 표시 목록·보호 목록의 생략과 무관하며, 닫힌 세션의 마감 수집에도 쓴다. 모델 도구에 노출하지 않는다.
func (c *Controller) ExplorationCursor(ctx context.Context, b ExplorationBinding) (uint64, error) {
	if c == nil || ctx == nil || ctx.Err() != nil {
		return 0, ErrExplorationBinding
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.record.Exploration
	if c.poisoned || state == nil || state.Binding != b || !c.matchingExploration(b) || c.binding.Current == nil || c.binding.Current() != nil || ctx.Err() != nil {
		return 0, ErrExplorationBinding
	}
	return state.LastSequence, nil
}
