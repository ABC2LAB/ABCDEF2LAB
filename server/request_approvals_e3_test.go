package server

import (
	"context"
	"errors"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/transmit"
)

// 확정 생성 요청의 승인 이력은 Host ACK 뒤 그 시도의 사건으로 갱신된다: 내보내기·응답 완료·결과 불명·미송신을 나누고,
// 다른 회차에 결속된 기록은 바꾸지 않으며, 저장 실패는 감사 실패로 올리지 않는다(2026-11-25 화면 회차: 보낸 확정 생성
// 요청의 승인 기록이 awaiting_dispatch로 남음, 이동찬 결정).
func TestRequestApprovalObserveE3UpdatesBoundAttempt(t *testing.T) {
	e3 := func(stage string, run string, change func(*e2proxy.Receipt)) e2proxy.Event {
		e := e2proxy.Event{Stage: stage, Receipt: e2proxy.Receipt{Program: e2proxy.ProgramE3Create, AttemptID: "attempt",
			Scope: transmit.Scope{TaskRef: "1", RunRef: run}, Decision: "allowed"}}
		if change != nil {
			change(&e.Receipt)
		}
		return e
	}
	for _, tc := range []struct {
		name     string
		event    e2proxy.Event
		failSwap bool
		want     string
		released bool
	}{
		{"내보내기", e3("intent", "run", nil), false, "dispatch_pending", false},
		{"응답 완료", e3("result", "run", func(r *e2proxy.Receipt) {
			r.ForwardAttempted, r.RequestWritten, r.ResponseReceived, r.ResponseComplete = true, true, true, true
		}), false, "completed", true},
		{"결과 불명", e3("result", "run", func(r *e2proxy.Receipt) { r.ForwardAttempted, r.RequestWritten = true, true }), false, "unknown", true},
		{"미송신", e3("result", "run", func(r *e2proxy.Receipt) { r.NotSent = true }), false, "not_sent", true},
		{"다른 회차", e3("result", "other-run", func(r *e2proxy.Receipt) {
			r.ForwardAttempted, r.ResponseReceived, r.ResponseComplete = true, true, true
		}), false, "awaiting_dispatch", false},
		{"저장 실패는 감사 실패가 아님", e3("result", "run", func(r *e2proxy.Receipt) {
			r.ForwardAttempted, r.ResponseReceived, r.ResponseComplete = true, true, true
		}), true, "awaiting_dispatch", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &approvalReviewStore{row: db.RequestApproval{ID: "attempt", TaskID: 1, RunRef: "run", State: "allowed", Consumed: true, ExecutionState: "awaiting_dispatch"}}
			if tc.failSwap {
				store.swap = func(context.Context, db.RequestApproval, bool) error { return errors.New("synthetic storage failure") }
			}
			s, _ := approvalReviewFixture(t, store)
			s.waiters["attempt"] = &requestApprovalWaiter{ctx: t.Context(), decision: make(chan struct{})}
			s.observeE3(t.Context(), tc.event)
			if store.row.ExecutionState != tc.want || (s.waiters["attempt"] == nil) != tc.released {
				t.Fatalf("승인 이력 %q(기대 %q), 대기 해제 %v", store.row.ExecutionState, tc.want, s.waiters["attempt"] == nil)
			}
		})
	}
}

// 확정 사건이 아닌 일반 관찰은 회차를 따로 대조하지 않던 지금 동작 그대로다(공용 관찰 경로는 바꾸지 않음).
func TestRequestApprovalGeneralObserveUnchanged(t *testing.T) {
	store := &approvalReviewStore{row: db.RequestApproval{ID: "attempt", TaskID: 1, RunRef: "run", State: "allowed", Consumed: true, ExecutionState: "dispatch_pending"}}
	s, _ := approvalReviewFixture(t, store)
	s.waiters["attempt"] = &requestApprovalWaiter{ctx: t.Context(), decision: make(chan struct{})}
	event := e2proxy.Event{Stage: "result", Receipt: e2proxy.Receipt{AttemptID: "attempt", Scope: transmit.Scope{TaskRef: "1", RunRef: "other-run"}, ForwardAttempted: true, ResponseReceived: true, ResponseComplete: true}}
	if err := s.observe(t.Context(), event); err != nil || store.row.ExecutionState != "completed" {
		t.Fatalf("일반 관찰 %v %q", err, store.row.ExecutionState)
	}
}
