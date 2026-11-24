package server

import (
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2runner"
)

// 명령 시간 예산은 run 요청에만, 러너 상한 안에서만 받는다.
func TestE2RemoteTimeoutOnlyOnRun(t *testing.T) {
	for _, tc := range []struct {
		op   string
		ms   int64
		want bool
	}{
		{e2RemoteRunOp, 0, true},
		{e2RemoteRunOp, 1, true},
		{e2RemoteRunOp, e2runner.MaxCommandBudget.Milliseconds(), true},
		{e2RemoteRunOp, e2runner.MaxCommandBudget.Milliseconds() + 1, false},
		{e2RemoteRunOp, -1, false},
		{e2RemoteViewOp, 1000, false},
		{e2RemoteViewOp, 0, true},
	} {
		if got := validE2RemoteTimeout(e2RemoteRequest{Op: tc.op, TimeoutMS: tc.ms}); got != tc.want {
			t.Fatalf("%s/%d: %v", tc.op, tc.ms, got)
		}
	}
	if e2runner.MaxCommandBudget != time.Hour {
		t.Fatal("runner command ceiling changed")
	}
}
