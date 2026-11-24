package server

import (
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2runner"
)

// 명령 시간 예산은 run 요청에만, 러너 상한 안에서만 받는다.
func TestE2RemoteTimeoutOnlyOnRun(t *testing.T) {
	const start = int64(1700000000000)
	for _, tc := range []struct {
		op        string
		ms, start int64
		want      bool
	}{
		{e2RemoteRunOp, 0, 0, true},
		{e2RemoteRunOp, 1, start, true},
		{e2RemoteRunOp, e2runner.MaxCommandBudget.Milliseconds(), start, true},
		{e2RemoteRunOp, e2runner.MaxCommandBudget.Milliseconds() + 1, start, false},
		{e2RemoteRunOp, -1, start, false},
		// 예산과 시작 시각은 같이 온다.
		{e2RemoteRunOp, 1000, 0, false},
		{e2RemoteRunOp, 0, start, false},
		{e2RemoteViewOp, 1000, start, false},
		{e2RemoteViewOp, 0, 0, true},
	} {
		if got := validE2RemoteTimeout(e2RemoteRequest{Op: tc.op, TimeoutMS: tc.ms, BudgetStartUnixMS: tc.start}); got != tc.want {
			t.Fatalf("%s/%d/%d: %v", tc.op, tc.ms, tc.start, got)
		}
	}
	if e2runner.MaxCommandBudget != time.Hour {
		t.Fatal("runner command ceiling changed")
	}
}
