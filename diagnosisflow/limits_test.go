package diagnosisflow

import "testing"

// 워크플로 상한의 경계: 페이지는 탐색 인벤토리와 같은 256, 공개 끝점 목록은 투영 크기에 맞춘 64(VAmPI 실제 회차 24b).
func TestWorkflowLimitsBounds(t *testing.T) {
	base := Limits{Format: Version, MaxPages: 8, MaxEndpoints: 32, MaxResponseBytes: 4096}
	for _, tc := range []struct {
		name  string
		pages int
		ends  int
		valid bool
	}{
		{"old defaults", 8, 32, true},
		{"pages at cap", MaxWorkflowPages, 32, true},
		{"pages over cap", MaxWorkflowPages + 1, 32, false},
		{"endpoints at cap", 8, MaxWorkflowEndpoints, true},
		{"endpoints over cap", 8, MaxWorkflowEndpoints + 1, false},
		{"zero pages", 0, 32, false},
		{"zero endpoints", 8, 0, false},
	} {
		l := base
		l.MaxPages, l.MaxEndpoints = tc.pages, tc.ends
		if l.Valid() != tc.valid {
			t.Errorf("%s: pages=%d endpoints=%d valid=%v", tc.name, tc.pages, tc.ends, l.Valid())
		}
	}
	if MaxWorkflowPages != 256 || MaxWorkflowEndpoints != 64 {
		t.Fatalf("caps changed without updating the web parser and the decision: pages=%d endpoints=%d", MaxWorkflowPages, MaxWorkflowEndpoints)
	}
}
