package e2runner

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
)

// delayedApprovalGate는 운영자가 delay 뒤에 허용한 것처럼 동작한다.
type delayedApprovalGate struct {
	delay time.Duration
	calls atomic.Int64
}

func (g *delayedApprovalGate) AwaitRequestApproval(ctx context.Context, _ e2proxy.RequestApprovalProposal) (bool, error) {
	g.calls.Add(1)
	timer := time.NewTimer(g.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// 운영자 승인을 기다리는 시간은 Worker가 준 명령 예산(timeout_ms)에 세지 않는다(#898 발견 3). 예산 1초인 명령이(격리 실행
// 시작에 수백 ms가 든다) 승인 2.5초 뒤 허용된 요청을 끝까지 보내고 완료된다. 승인 대기가 없는 명령은 같은 예산으로 시간 초과다(예산은 그대로 쓴다).
func TestRunnerCommandBudgetPausesWhileApprovalPending(t *testing.T) {
	helperBase := runnerTestDirectory(t)
	helper := runnerTestHelper(t, helperBase)
	base := runnerTestDirectory(t)
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		io.WriteString(w, "target-ok")
	}))
	defer target.Close()
	gate := &delayedApprovalGate{delay: 2500 * time.Millisecond}
	fixture := newRunnerFixtureWithLimits(t, base, helper, target.URL, nil, nil, true, nil, func(cfg *e2proxy.Config) {
		cfg.ApprovalMode, cfg.RequestApproval = e2proxy.ApprovalConfirmEveryRequest, gate
	})
	const budget = time.Second
	result, err := fixture.runner.Run(WithCommandBudget(t.Context(), budget), "curl --silent --show-error --max-time 4 '"+target.URL+"/allowed'")
	if err != nil || result.Trusted.Classification != "completed" || result.Trusted.Termination != "" || received.Load() != 1 || gate.calls.Load() != 1 {
		t.Fatalf("approval wait counted against the command budget: err=%v class=%s termination=%q received=%d approvals=%d",
			err, result.Trusted.Classification, result.Trusted.Termination, received.Load(), gate.calls.Load())
	}
	// 승인이 끝나면 남은 예산이 다시 흐른다: 승인된 요청을 보낸 뒤 예산보다 오래 걸리는 명령은 시간 초과다.
	result, _ = fixture.runner.Run(WithCommandBudget(t.Context(), budget), "curl --silent --show-error --max-time 4 '"+target.URL+"/allowed'; sleep 3")
	if result.Trusted.Termination != TerminationTimeout || received.Load() != 2 || gate.calls.Load() != 2 {
		t.Fatalf("budget did not resume after approval: termination=%q received=%d approvals=%d", result.Trusted.Termination, received.Load(), gate.calls.Load())
	}
	result, _ = fixture.runner.Run(WithCommandBudget(t.Context(), budget), "sleep 3")
	if result.Trusted.Termination != TerminationTimeout || result.Trusted.Classification != "transient_failure" {
		t.Fatalf("command budget not applied without approval wait: class=%s termination=%q", result.Trusted.Classification, result.Trusted.Termination)
	}
	// 러너에 닿기 전에 지난 시간(원격 왕복·Host 등록)은 예산에서 빠진다(#910 이동찬 리뷰): 3초 예산이 2.5초 전에 시작됐으면
	// 승인 대기가 없는 1초짜리 명령도 시간 초과다. 시작 시각을 무시하면 새 3초를 받아 완료된다.
	result, _ = fixture.runner.Run(WithCommandBudgetFrom(t.Context(), 3*time.Second, time.Now().Add(-2500*time.Millisecond)), "sleep 1")
	if result.Trusted.Termination != TerminationTimeout || result.Trusted.Classification != "transient_failure" {
		t.Fatalf("time before the runner not charged to the budget: class=%s termination=%q", result.Trusted.Classification, result.Trusted.Termination)
	}
}
