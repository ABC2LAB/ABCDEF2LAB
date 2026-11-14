package main

import (
	"testing"

	"github.com/Autumn-27/artex/diagnosisprep"
)

// 설치 정책의 대상 요청 상한은 1부터 코드 상한(diagnosisprep.MaxTargetRequests)까지 받는다(C32, 2026-11-14 100에서 올림).
func TestTargetPolicyMaxRequestsUpToCodeCap(t *testing.T) {
	for requests, ok := range map[int64]bool{1: true, 101: true, diagnosisprep.MaxTargetRequests: true, diagnosisprep.MaxTargetRequests + 1: false} {
		p := withTargetPolicyDefaults(productTargetPolicy{PrivateCIDRs: []string{"10.200.1.0/24"}, MaxRequests: requests})
		if validProductTargetPolicy(p) != ok {
			t.Fatalf("max_requests=%d: valid=%v", requests, !ok)
		}
	}
}
