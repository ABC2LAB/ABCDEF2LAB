package diagnosisprep

import "testing"

// 대상 등록의 승인 요청 수는 1부터 코드 상한(MaxTargetRequests)까지 받는다(C32, 2026-11-14 100에서 올림).
func TestTargetRegistrationRequestsUpToCodeCap(t *testing.T) {
	for requests, ok := range map[int64]bool{1: true, 100: true, 101: true, MaxTargetRequests: true, MaxTargetRequests + 1: false, 0: false} {
		r := &ProductRequest{Operation: "register", Registration: &TargetRegistration{URL: "https://owned.example/", ReadonlyBasis: "Owned disposable practice target, read-only.", Requests: requests, ValidHours: 4, PathPrefixes: []string{"/"}, Methods: []string{"GET", "HEAD"}}}
		if validProductRequest(r) != ok {
			t.Fatalf("requests=%d: valid=%v", requests, !ok)
		}
	}
}
