package main

import (
	"context"
	"errors"
	"sync"

	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/server"
)

// 대상 저널의 철회와 경계 소켓의 마감을 같은 서버 소유 lease에 연결한다.
// 잠금 안에서는 등록만 직렬화하고 원격 마감 동안 다른 대상의 실행 잠금을 잡지 않는다.
type productExplorationBoundary struct {
	host    *productExplorationHost
	inner   exploration.Boundary
	record  productTargetRecord
	current func(productTargetRecord) error
}

type productExplorationLease struct {
	host    *productExplorationHost
	inner   exploration.Lease
	binding exploration.Binding
	record  productTargetRecord
	current func(productTargetRecord) error
	mu      sync.Mutex
	ended   exploration.Reason
	failed  bool
	final   *exploration.State
}

func (b productExplorationBoundary) Open(ctx context.Context, binding exploration.Binding) (exploration.Lease, error) {
	if ctx == nil || ctx.Err() != nil || binding.Validate() != nil || b.host == nil || b.inner == nil || b.current == nil || binding.TargetRef != b.record.Target.ID || binding.TargetRevision != b.record.Target.Revision {
		return nil, exploration.ErrBinding
	}
	h := b.host
	h.leasesMu.Lock()
	if b.current(b.record) != nil {
		h.leasesMu.Unlock()
		return nil, exploration.ErrClosed
	}
	inner, err := b.inner.Open(ctx, binding)
	if inner == nil {
		h.leasesMu.Unlock()
		return nil, err
	}
	lease := &productExplorationLease{host: h, inner: inner, binding: binding, record: b.record, current: b.current}
	if h.leases == nil {
		h.leases = map[string]*productExplorationLease{}
	}
	h.leases[binding.SessionRef] = lease
	currentErr := b.current(b.record)
	h.leasesMu.Unlock()
	if currentErr != nil {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), explorationCloseTimeout)
		defer stop()
		_, closeErr := lease.end(cleanup, productExplorationEndReason(currentErr))
		return lease, errors.Join(exploration.ErrClosed, closeErr)
	}
	return lease, err
}

func productExplorationEndReason(err error) exploration.Reason {
	if errors.Is(err, server.ErrFirstDiagnosisTargetEnded) {
		return exploration.TargetEnded
	}
	return exploration.BoundaryUnavailable
}

func (h *productExplorationHost) closeTarget(ctx context.Context, target string) error {
	if h == nil {
		return nil
	}
	h.leasesMu.Lock()
	var leases []*productExplorationLease
	for _, lease := range h.leases {
		if lease.binding.TargetRef == target {
			leases = append(leases, lease)
		}
	}
	h.leasesMu.Unlock()
	var failures []error
	for _, lease := range leases {
		if _, err := lease.end(ctx, exploration.TargetEnded); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (l *productExplorationLease) forget() {
	l.host.leasesMu.Lock()
	defer l.host.leasesMu.Unlock()
	if l.host.leases[l.binding.SessionRef] == l {
		delete(l.host.leases, l.binding.SessionRef)
	}
}

func (l *productExplorationLease) end(ctx context.Context, reason exploration.Reason) (exploration.State, error) {
	l.mu.Lock()
	if l.final == nil && l.ended == "" {
		l.ended = reason
	}
	l.mu.Unlock()
	return l.Close(ctx, reason)
}

func (l *productExplorationLease) Status(ctx context.Context) (exploration.State, error) {
	if ctx == nil || ctx.Err() != nil {
		return exploration.State{}, exploration.ErrUnavailable
	}
	l.mu.Lock()
	if l.final != nil {
		state, failed := *l.final, l.failed
		l.mu.Unlock()
		if failed {
			return state, exploration.ErrUnavailable
		}
		return state, nil
	}
	ended := l.ended
	l.mu.Unlock()
	if ended != "" {
		return l.Close(ctx, ended)
	}
	if err := l.current(l.record); err != nil {
		return l.end(ctx, productExplorationEndReason(err))
	}
	return l.inner.Status(ctx)
}

// 한 번 마감 결과가 불명이면 뒤의 재시도가 성공해도 그 회차의 진단 예산 증명은 다시 만들지 않는다.
// 실제 정리 재시도는 계속하되 유효한 최종 상태를 확인한 lease만 활성 목록에서 뺀다.
func (l *productExplorationLease) Close(ctx context.Context, reason exploration.Reason) (exploration.State, error) {
	if ctx == nil || !reason.Valid() {
		return exploration.State{}, exploration.ErrBinding
	}
	l.mu.Lock()
	if l.final != nil {
		state, failed := *l.final, l.failed
		l.mu.Unlock()
		if failed {
			return state, exploration.ErrUnavailable
		}
		return state, nil
	}
	if err := l.current(l.record); err != nil && l.ended == "" {
		l.ended = productExplorationEndReason(err)
	}
	if l.ended != "" {
		reason = l.ended
	}
	state, err := l.inner.Close(ctx, reason)
	clean := err == nil && !state.Active && state.Validate(l.binding) == nil
	if !clean {
		l.failed = true
	}
	if clean {
		final := state
		l.final = &final
	}
	failed := l.failed
	l.mu.Unlock()
	if clean {
		l.forget()
	}
	if failed {
		return state, exploration.ErrUnavailable
	}
	return state, nil
}
