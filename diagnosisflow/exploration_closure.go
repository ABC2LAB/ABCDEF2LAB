package diagnosisflow

import "context"

// CheckExplorationClosedBeforeDiagnosis는 탐색 마감의 보호 상태만 확인한다.
// 실제 모델 예산·세션 자원 정리·E② lease 부재는 호스트가 별도로 증명해야 한다.
func (c *Controller) CheckExplorationClosedBeforeDiagnosis(ctx context.Context, b ExplorationBinding, expectedRevision uint64) error {
	if c == nil || ctx == nil || ctx.Err() != nil {
		return ErrExplorationBinding
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s, state := c.record.Snapshot, c.record.Exploration
	if expectedRevision == 0 || s.Revision != expectedRevision {
		return ErrExplorationBinding
	}
	if c.poisoned || state == nil || state.Binding != b || !c.matchingExploration(b) || c.binding.Current == nil || c.binding.Current() != nil || ctx.Err() != nil {
		return ErrExplorationBinding
	}
	if state.ClosedReason != "cap_exhausted" || s.ExplorationReason != state.ClosedReason || s.ExplorationStatus != "PARTIAL" || !s.ExplorationPartial || !s.InitialCompleted || s.Paused {
		return ErrExplorationBinding
	}
	if s.Format != Version || s.JobID != b.JobID || s.TaskID != b.TaskID || s.RunRef != b.RunRef || s.SourceDigest != b.SourceDigest || s.Mode != c.binding.Mode {
		return ErrExplorationBinding
	}
	if c.binding.Mode == Manual && s.Stage != "awaiting_selection" || c.binding.Mode == Auto && s.Stage != "diagnosing" || !ValidMode(c.binding.Mode) {
		return ErrExplorationBinding
	}
	if s.DeepRuns != 0 || s.DeepClosed != 0 || s.Counts.Sent != 0 || s.Counts.Attempted != 0 || s.Counts.Unknown != 0 || s.Counts.Blocked != 0 || s.Counts.Held != 0 {
		return ErrExplorationBinding
	}
	// 저장 한도 때문에 관측 근거가 없을 수 있다. 남아 있는 관측의 세션만큼은 다른 회차와 섞이지 않아야 한다.
	for _, fact := range c.record.DiscoveryInventory {
		if fact.Confidence == "observed" && (fact.SessionRef != b.SessionRef || fact.Seq == 0 || fact.Seq > state.LastSequence) {
			return ErrExplorationBinding
		}
	}
	return nil
}
