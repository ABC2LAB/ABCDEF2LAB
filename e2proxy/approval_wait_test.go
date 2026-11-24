package e2proxy

import "testing"

// call의 승인 대기 수와 변화 알림. 러너는 이 상태로 명령 예산 시계를 멈춘다(#898 발견 3).
func TestApprovalWaitStateAndChange(t *testing.T) {
	var c Call
	waiting, changed := c.ApprovalWaiting()
	if waiting {
		t.Fatal("new call waiting")
	}
	c.approvals.add(1)
	select {
	case <-changed:
	default:
		t.Fatal("change not signaled")
	}
	if waiting, _ = c.ApprovalWaiting(); !waiting {
		t.Fatal("pending approval not reported")
	}
	c.approvals.add(1)
	c.approvals.add(-1)
	if waiting, _ = c.ApprovalWaiting(); !waiting {
		t.Fatal("one of two approvals finishing cleared the wait")
	}
	c.approvals.add(-1)
	if waiting, _ = c.ApprovalWaiting(); waiting {
		t.Fatal("finished approvals still waiting")
	}
	if waiting, ch := (*Call)(nil).ApprovalWaiting(); waiting || ch != nil {
		t.Fatal("nil call")
	}
}
