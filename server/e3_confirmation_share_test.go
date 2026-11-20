package server

import (
	"testing"
	"time"
)

// 확정 몫 = 신원 2 + 종류×3 요청, 시간 = 요청 × 확정 요청 시간 상한. 요청·시간 모두 실행 상한의 1/4 안에서만 종류를 받는다.
func TestComputeE3ConfirmationShare(t *testing.T) {
	run := 1800 * time.Second
	for name, c := range map[string]struct {
		types           int
		requests        uint64
		run, perRequest time.Duration
		want            E3ConfirmationShare
	}{
		// 꺼짐: 시험할 종류가 없으면 몫 0, 모델 단계 예산 그대로.
		"disabled": {0, 400, run, E3ConfirmationRequestTimeoutDefault, E3ConfirmationShare{}},
		// 제품 기본(요청 400·1800초)에서 종류 1개: 고정 4(다시 로그인 2 + 신원 2) + 4 = 8건·80초.
		"one type": {1, 400, run, E3ConfirmationRequestTimeoutDefault, E3ConfirmationShare{Requests: 8, Timeout: 80 * time.Second, Admitted: 1}},
		// 시간 1/4(450초)÷10초 = 45건 → 종류 10개(4+40=44건)까지. 넘치는 종류는 예산으로 건너뜀.
		"time bound": {20, 400, run, E3ConfirmationRequestTimeoutDefault, E3ConfirmationShare{Requests: 44, Timeout: 440 * time.Second, Admitted: 10, BudgetSkipped: 10}},
		// 요청 1/4(100건) → 종류 24개(4+96=100건)까지.
		"request bound": {40, 400, run, time.Second, E3ConfirmationShare{Requests: 100, Timeout: 100 * time.Second, Admitted: 24, BudgetSkipped: 16}},
		// 종류 하나도 안 들면 몫 0이고 전부 예산으로 건너뜀(고정 몫만 떼지 않는다).
		"none fits":       {3, 16, run, E3ConfirmationRequestTimeoutDefault, E3ConfirmationShare{BudgetSkipped: 3}},
		"none fits time":  {3, 400, 60 * time.Second, E3ConfirmationRequestTimeoutDefault, E3ConfirmationShare{BudgetSkipped: 3}},
		"exact time edge": {1, 400, 320 * time.Second, E3ConfirmationRequestTimeoutDefault, E3ConfirmationShare{Requests: 8, Timeout: 80 * time.Second, Admitted: 1}},
		"just over time":  {1, 400, 319 * time.Second, E3ConfirmationRequestTimeoutDefault, E3ConfirmationShare{BudgetSkipped: 1}},
		"exact req edge":  {1, 32, run, E3ConfirmationRequestTimeoutDefault, E3ConfirmationShare{Requests: 8, Timeout: 80 * time.Second, Admitted: 1}},
		"just over req":   {1, 31, run, E3ConfirmationRequestTimeoutDefault, E3ConfirmationShare{BudgetSkipped: 1}},
		"no per-request":  {2, 400, run, 0, E3ConfirmationShare{BudgetSkipped: 2}},
		"no run budget":   {2, 0, run, E3ConfirmationRequestTimeoutDefault, E3ConfirmationShare{BudgetSkipped: 2}},
	} {
		if got := ComputeE3ConfirmationShare(c.types, c.requests, c.run, c.perRequest); got != c.want {
			t.Fatalf("%s: %+v, want %+v", name, got, c.want)
		}
	}
}
