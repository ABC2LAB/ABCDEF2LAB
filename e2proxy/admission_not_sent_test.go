package e2proxy

import (
	"context"
	"errors"
	"testing"
)

func TestAdmissionNotSentRequiresAcknowledgedControlEvent(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "ack", true: "audit-failure"}[fail], func(t *testing.T) {
			rec := &coreRecorder{}
			cfg := coreConfig(t, "http://owned.invalid", rec, 10)
			e, err := NewExecution(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if fail {
				rec.failStage = "control"
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			r, err := e.RecordAdmissionNotSent(ctx, cfg.IntentID, "command_not_sent")
			if fail {
				if !errors.Is(err, ErrAudit) || e.HasAdmissionNotSent(r) || e.Snapshot().AuditComplete {
					t.Fatal("감사 실패를 전송 전 확정 결과로 수용")
				}
				return
			}
			if err != nil || !e.HasAdmissionNotSent(r) || !r.NotSent || !r.AuditComplete || r.CallRef != "" || r.At.IsZero() {
				t.Fatal("취소 뒤에도 독립 기한 안에서 미전송 감사를 확정하지 못함", err)
			}
			if len(e.Calls()) != 0 || e.Snapshot().Counts != (Counts{}) {
				t.Fatal("미전송이 호출·송신 계수를 소비")
			}
			remaining, err := e.BudgetRemaining()
			if err != nil || remaining != 10 {
				t.Fatal("미전송이 대상 요청 예산 소비")
			}
			for _, mutate := range []func(*Receipt){func(r *Receipt) { r.CommandRef = "forged" }, func(r *Receipt) { r.IntentID++ }, func(r *Receipt) { r.PolicyRevision = "other" }, func(r *Receipt) { r.At = r.At.Add(1) }, func(r *Receipt) { r.ResponseReceived = true }} {
				fake := r
				mutate(&fake)
				if e.HasAdmissionNotSent(fake) {
					t.Fatal("실제 ACK와 다른 미전송 영수증 수용")
				}
			}
		})
	}
}
