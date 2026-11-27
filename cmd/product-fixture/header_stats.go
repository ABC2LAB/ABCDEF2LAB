package main

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
)

// credentialHeaderRequests는 대상이 받은 요청 가운데 자격 헤더(Authorization · Cookie)가 실린 수다. 계정이 등록된 대상의 익명
// 후보 검증(선택 후보 수용 S03A, 9칸-1)이 시험 계정 세션을 싣지 않았는지 센다.
var credentialHeaderRequests atomic.Int64

// countCredentialHeaders는 대상 요청 하나의 자격 헤더를 센다(값은 보지 않는다).
func countCredentialHeaders(r *http.Request) {
	if len(r.Header.Values("Authorization")) > 0 || len(r.Header.Values("Cookie")) > 0 {
		credentialHeaderRequests.Add(1)
	}
}

// fixtureHeaderStats는 대상 통계 포트의 자격 헤더 수다. 기존 /stats 칸 목록은 바꾸지 않는다.
func fixtureHeaderStats() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int64{"credential_header_requests": credentialHeaderRequests.Load()})
	})
}
