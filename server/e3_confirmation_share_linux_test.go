//go:build linux

package server

import (
	"encoding/json"
	"testing"
)

// 확정 단계 몫 칸은 실행 정책에서만 받는다. 실행 정책이 아닌 검사(설치·평가 정책)는 같은 문서에 칸이 있으면 거부한다.
func TestE3ConfirmationShareOnlyInRunPolicy(t *testing.T) {
	_, _, policy := e2HostPolicyFixture(t)
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := E2HTTPPolicyContentFingerprint(raw, true); err != nil {
		t.Fatal("양성 대조: 칸 없는 정책", err)
	}
	requests, timeout := uint64(0), int64(0)
	policy.Limits.E3ConfirmationRequests, policy.Limits.E3ConfirmationTimeoutMS = &requests, &timeout
	withShare, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := E2HTTPPolicyContentFingerprint(withShare, true); err == nil {
		t.Fatal("실행 정책이 아닌 검사에서 확정 몫 칸 수용")
	}
}
