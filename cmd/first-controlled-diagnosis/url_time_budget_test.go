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
		at, drain := urlTimeBudgetClose(end, tc.budget, tc.shutdown)
		if drain != tc.drain || !at.Equal(end.Add(-tc.before)) {
			t.Fatalf("%s: close=%s drain=%s", name, end.Sub(at), drain)
		}
		if end.Sub(at) > tc.budget/2 {
			t.Fatalf("%s: closing takes more than half of the run", name)
		}
	}
}
