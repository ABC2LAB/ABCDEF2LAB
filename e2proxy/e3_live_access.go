package e2proxy

import (
	"context"

	"github.com/Autumn-27/artex/e3contract"
)

// CheckE3Binding은 신뢰한 제품 조립자가 E③ 표식을 등록하기 전 v3 Host와 실행 범위를 대조한다.
func (e *Execution) CheckE3Binding(binding e3contract.RunBinding) error {
	if e == nil || binding.Validate() != nil || binding.Policy.ValidateProduct() != nil {
		return ErrE3JournalUnavailable
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cfg.AuditVersion != JournalVersionV3 || e.cfg.E3Verifier == nil ||
		e.cfg.E3ConfirmationRequests == 0 || e.cfg.Scope.RunRef != binding.RunRef ||
		e.cfg.Scope.TaskRef != binding.TaskRef || e.cfg.Scope.PolicyRef != binding.E2PolicyRef ||
		e.stopped || e.auditFailed || !e.authorityCurrentLocked() {
		return ErrE3JournalUnavailable
	}
	return nil
}

// VerifiedE3은 이 실행이 결속한 독립 Host의 현재 epoch·head 검증 결과만 돌려준다.
func (e *Execution) VerifiedE3(ctx context.Context, binding e3contract.RunBinding, ref e3contract.CallRef) (Event, error) {
	if ctx == nil || ctx.Err() != nil || e.CheckE3Binding(binding) != nil {
		return Event{}, ErrE3JournalUnavailable
	}
	e.mu.Lock()
	registered := len(e.marker) == markerBytes
	verifier := e.cfg.E3Verifier
	e.mu.Unlock()
	if !registered {
		return Event{}, e3contract.ErrMarkerNotRegistered
	}
	return verifier.Verified(ctx, binding, ref)
}

// VerifiedE3Checkpoint는 VerifiedE3과 같고, Host가 그 검증에 쓴 현재 head 체크포인트를 함께 돌려준다. 검증자가 체크포인트를
// 주지 못하면 ErrE3JournalUnavailable이다(판정 저장 쪽은 근거 체크포인트 없음으로 남긴다).
func (e *Execution) VerifiedE3Checkpoint(ctx context.Context, binding e3contract.RunBinding, ref e3contract.CallRef) (Event, JournalCheckpoint, error) {
	if ctx == nil || ctx.Err() != nil || e.CheckE3Binding(binding) != nil {
		return Event{}, JournalCheckpoint{}, ErrE3JournalUnavailable
	}
	e.mu.Lock()
	registered := len(e.marker) == markerBytes
	verifier, ok := e.cfg.E3Verifier.(E3CheckpointVerifier)
	e.mu.Unlock()
	if !registered {
		return Event{}, JournalCheckpoint{}, e3contract.ErrMarkerNotRegistered
	}
	if !ok {
		return Event{}, JournalCheckpoint{}, ErrE3JournalUnavailable
	}
	ev, checkpoint, err := verifier.VerifiedCheckpoint(ctx, binding, ref)
	if err != nil {
		return Event{}, JournalCheckpoint{}, err
	}
	// 체크포인트는 이 실행 범위의 v3 저널 것이어야 한다.
	if checkpoint.Version != JournalVersionV3 || checkpoint.Scope != e.cfg.Scope || checkpoint.Sequence == 0 {
		return Event{}, JournalCheckpoint{}, ErrE3JournalUnavailable
	}
	return ev, checkpoint, nil
}
