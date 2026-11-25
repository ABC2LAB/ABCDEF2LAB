package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
)

// 승인 대기가 결정 없이 닫힌 까닭은 승인 기한 → 운영자 종료 → 대기 문맥 원인(모델 단계 마감·기한) → 결속 기한 → 그 밖
// 순서로 고정 코드가 된다(2026-11-25 경준 요청).
func TestApprovalClosedReasonOrder(t *testing.T) {
	now := time.Now()
	later := now.Add(time.Minute)
	cancelledWith := func(cause error) context.Context {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(cause)
		return ctx
	}
	expiredBinding, cancel := context.WithDeadline(context.Background(), now.Add(-time.Second))
	defer cancel()
	live := requestApprovalBinding{ctx: context.Background()}
	for _, tc := range []struct {
		name    string
		wait    context.Context
		binding requestApprovalBinding
		expires time.Time
		want    string
	}{
		{"승인 기한이 먼저", cancelledWith(e2proxy.ErrModelStageClosed), requestApprovalBinding{ctx: context.Background(), stopRequested: true}, now, approvalClosedExpired},
		{"운영자 종료", cancelledWith(e2proxy.ErrModelStageClosed), requestApprovalBinding{ctx: context.Background(), stopRequested: true}, later, approvalClosedOperator},
		{"모델 단계 마감", cancelledWith(fmt.Errorf("감쌈: %w", e2proxy.ErrModelStageClosed)), live, later, approvalClosedModelStage},
		{"요청·call 기한", cancelledWith(context.DeadlineExceeded), live, later, approvalClosedTimeout},
		{"회차 결속 기한", cancelledWith(context.Canceled), requestApprovalBinding{ctx: expiredBinding}, later, approvalClosedTimeout},
		{"그 밖", cancelledWith(context.Canceled), live, later, approvalClosedRun},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := approvalClosedReason(tc.wait, tc.binding, tc.expires, now); got != tc.want {
				t.Fatalf("closed_reason %q, 기대 %q", got, tc.want)
			}
		})
	}
}

// 대기가 닫힐 때 저장 기록에 까닭을 남기고, 응답 투영은 cancelled·expired에만 싣는다(pending·allowed·denied에는 없음).
func TestRequestApprovalStoresAndProjectsClosedReason(t *testing.T) {
	for _, tc := range []struct {
		name, state, want string
		expire, stop      bool
	}{
		{name: "모델 단계 마감", state: "cancelled", want: approvalClosedModelStage},
		{name: "승인 기한", state: "expired", want: approvalClosedExpired, expire: true},
		{name: "운영자 종료", state: "cancelled", want: approvalClosedOperator, stop: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wait, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			store := &approvalReviewStore{}
			s, p := approvalReviewFixture(t, store)
			if tc.expire {
				p.ExpiresAt = time.Now().Add(20 * time.Millisecond)
			}
			store.insert = func(context.Context) error {
				go func() {
					switch {
					case tc.expire:
					case tc.stop:
						s.mu.Lock()
						b := s.bindings[1]
						b.stopRequested = true
						s.bindings[1] = b
						s.mu.Unlock()
						cancel(context.Canceled)
					default:
						cancel(e2proxy.ErrModelStageClosed)
					}
				}()
				return nil
			}
			allowed, err := s.AwaitRequestApproval(wait, p)
			if allowed || err != nil || store.row.State != tc.state || store.row.ClosedReason != tc.want || store.row.Reason != "" {
				t.Fatalf("저장 기록 state=%q closed_reason=%q reason=%q err=%v", store.row.State, store.row.ClosedReason, store.row.Reason, err)
			}
			s.mu.Lock()
			got := s.decorate(store.row)
			s.mu.Unlock()
			if got.ClosedReason != tc.want {
				t.Fatalf("응답 closed_reason %q", got.ClosedReason)
			}
		})
	}
	s, _ := approvalReviewFixture(t, &approvalReviewStore{})
	future := time.Now().Add(time.Minute)
	s.waiters["live"] = &requestApprovalWaiter{ctx: t.Context(), decision: make(chan struct{})}
	for _, row := range []db.RequestApproval{
		{ID: "live", TaskID: 1, RunRef: "run", State: "pending", ExpiresAt: future, ClosedReason: approvalClosedRun},
		{ID: "decided", TaskID: 1, RunRef: "run", State: "allowed", Consumed: true, ExpiresAt: future},
		{ID: "denied", TaskID: 1, RunRef: "run", State: "denied", ExpiresAt: future, ClosedReason: approvalClosedRun},
	} {
		s.mu.Lock()
		got := s.decorate(row)
		s.mu.Unlock()
		if got.ClosedReason != "" {
			t.Fatalf("%s(%s)에 closed_reason %q", row.ID, got.State, got.ClosedReason)
		}
	}
	// 운영자 종료로 결속이 끝난 뒤 읽으면 투영으로 닫힌 대기 건과 까닭 없는 취소 기록은 operator_stopped다.
	stopped, _ := approvalReviewFixture(t, &approvalReviewStore{})
	b := stopped.bindings[1]
	b.stopRequested = true
	stopped.bindings[1] = b
	for _, row := range []db.RequestApproval{
		{ID: "orphan", TaskID: 1, RunRef: "run", State: "pending", ExpiresAt: future},
		{ID: "legacy", TaskID: 1, RunRef: "run", State: "cancelled", ExpiresAt: future},
	} {
		stopped.mu.Lock()
		got := stopped.decorate(row)
		stopped.mu.Unlock()
		if got.State != "cancelled" || got.ClosedReason != approvalClosedOperator {
			t.Fatalf("운영자 종료 뒤 %s: state=%q closed_reason=%q", row.ID, got.State, got.ClosedReason)
		}
	}
	// 대기자가 없는(재시작 등으로 투영으로 닫힌) 대기 건과 이 칸 이전 취소 기록은 run_closed다.
	for _, row := range []db.RequestApproval{
		{ID: "orphan", TaskID: 1, RunRef: "run", State: "pending", ExpiresAt: future},
		{ID: "legacy", TaskID: 1, RunRef: "run", State: "cancelled", ExpiresAt: future},
	} {
		s.mu.Lock()
		got := s.decorate(row)
		s.mu.Unlock()
		if got.State != "cancelled" || got.ClosedReason != approvalClosedRun {
			t.Fatalf("%s: state=%q closed_reason=%q", row.ID, got.State, got.ClosedReason)
		}
	}
}
