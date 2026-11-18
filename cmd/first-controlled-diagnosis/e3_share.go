package main

import (
	"time"

	"github.com/Autumn-27/artex/server"
)

// withE3ConfirmationShare는 실행 정책에 E③ 확정 단계 몫을 적은 사본이다(사용자 결정 2026-11-18 ②). 시험할 종류가 없으면
// 몫은 0이고(확정 단계 꺼짐 → 모델 단계 예산은 제품 상한 그대로), 0도 늘 적어 러너가 "칸 없음"과 구분한다. 실행 발급
// (issue)과 러너 묶음 시험이 같은 이 함수를 쓴다.
func withE3ConfirmationShare(policy server.E2HTTPFilePolicy, runSeconds int64, types int) server.E2HTTPFilePolicy {
	share := server.ComputeE3ConfirmationShare(types, policy.MaxRequests, time.Duration(runSeconds)*time.Second,
		server.E3ConfirmationRequestTimeoutDefault)
	requests, timeoutMS := share.Requests, share.Timeout.Milliseconds()
	policy.Limits.E3ConfirmationRequests, policy.Limits.E3ConfirmationTimeoutMS = &requests, &timeoutMS
	return policy
}
