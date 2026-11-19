package main

import (
	"testing"
	"time"

	"github.com/Autumn-27/artex/server"
)

// 시간 상한 마감은 회차 실행 시간 안에서 끝나도록 실행 끝 시각에서 유예와 결과 저장 여유를 뺀 때 시작한다.
// 짧은 회차는 둘을 각각 실행 시간의 1/4로 줄여 절반 이상을 작업에 쓴다.
func TestURLTimeBudgetClose(t *testing.T) {
	end := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		budget, shutdown, drain, before time.Duration
	}{
		"workflow-default": {600 * time.Second, 20 * time.Second, server.FirstDiagnosisTimeBudgetDrain, server.FirstDiagnosisTimeBudgetDrain + 20*time.Second},
		"short-run":        {60 * time.Second, 20 * time.Second, 15 * time.Second, 30 * time.Second},
		"product-default":  {180 * time.Second, 20 * time.Second, 45 * time.Second, 65 * time.Second},
	} {
		at, drain := urlTimeBudgetClose(end, tc.budget, tc.shutdown, 0)
		if drain != tc.drain || !at.Equal(end.Add(-tc.before)) {
			t.Fatalf("%s: close=%s drain=%s", name, end.Sub(at), drain)
		}
		if end.Sub(at) > tc.budget/2 {
			t.Fatalf("%s: closing takes more than half of the run", name)
		}
	}
}

// 계획이 비어도 마감하지 않으므로 모델 단계는 시간 상한까지 갈 수 있다. 그때도 E③ 확정 단계는 실행 시작 때 예약한 시간
// 몫을 다 쓴다: 모델 단계는 "전체 상한 − 확정 몫 − 유예 − 저장 여유"에서 끝나고, 유예가 끝난 뒤 확정 몫을 다 써도 결과
// 저장 여유가 남는다. 확정 몫은 발급 정책과 같은 함수(ComputeE3ConfirmationShare)로 정한 값이다.
func TestURLTimeBudgetCloseReservesE3Share(t *testing.T) {
	end := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		budget, shutdown time.Duration
		requests         uint64
		types            int
	}{
		"workflow-1800": {1800 * time.Second, 20 * time.Second, 300, 3},
		"workflow-max":  {time.Duration(urlMaxRunSeconds) * time.Second, 20 * time.Second, 1000, 64},
		"short-run":     {600 * time.Second, 20 * time.Second, 300, 1},
	} {
		share := server.ComputeE3ConfirmationShare(tc.types, tc.requests, tc.budget, server.E3ConfirmationRequestTimeoutDefault)
		if share.Timeout <= 0 {
			t.Fatalf("%s: fixture admits no E3 time share", name)
		}
		without, drain := urlTimeBudgetClose(end, tc.budget, tc.shutdown, 0)
		at, drainWith := urlTimeBudgetClose(end, tc.budget, tc.shutdown, share.Timeout)
		if drainWith != drain || !at.Equal(without.Add(-share.Timeout)) {
			t.Fatalf("%s: E3 share not taken from the model stage: close=%s drain=%s", name, end.Sub(at), drainWith)
		}
		// 확정 단계는 진행 중 작업 유예가 끝난 뒤에야 시작할 수 있다. 그 뒤 몫을 다 써도 결과 저장 여유 전에 끝나야 한다.
		margin := min(tc.shutdown, tc.budget/urlTimeCloseShare)
		if confirmEnd := at.Add(drain + share.Timeout); confirmEnd.After(end.Add(-margin)) {
			t.Fatalf("%s: E3 share overruns the save margin by %s", name, confirmEnd.Sub(end.Add(-margin)))
		}
		if model := at.Sub(end.Add(-tc.budget)); model < tc.budget/urlTimeCloseShare {
			t.Fatalf("%s: model stage left %s of %s", name, model, tc.budget)
		}
	}
}
