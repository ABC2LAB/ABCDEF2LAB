package main

import (
	"strings"
	"testing"
)

// 작업 목표는 승인 기록의 메서드로 만든다. 읽기만 승인된 범위는 읽기 전용이라고 적고, 쓰기가 승인된 범위는 그 메서드와
// 건별 운영자 확인을 적되 확인을 지어내거나 결과 불명 요청을 다시 보내지 말라고 적는다(루프 복원 결정안 §3).
func TestURLDiagnosisGoalFollowsApprovedMethods(t *testing.T) {
	const target = "https://owned.example"
	for name, tc := range map[string]struct {
		mode    string
		methods []string
		want    []string
		absent  []string
	}{
		"read-only":          {"", []string{"GET", "HEAD"}, []string{"approved read-only HTTPS target " + target, "Approved methods: GET, HEAD.", "Do not request state changes or other origins."}, []string{"Write methods", "per request"}},
		"read-only-workflow": {"auto", []string{"GET"}, []string{"approved read-only HTTPS target", "Workbench workflow mode: auto.", "State-changing requests require separately granted scope"}, []string{"Write methods"}},
		"selective-writes":   {"user_approval_v1", []string{"GET", "POST", "PATCH"}, []string{"Inspect the approved HTTPS target " + target, "Approved methods: GET, POST, PATCH.", "Write methods (POST, PATCH) are approved only per request", "Never invent that confirmation", "never resend a request whose result is unknown", "Do not request other origins."}, []string{"read-only", "State-changing requests require separately granted scope"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := urlDiagnosisGoal(target, tc.mode, tc.methods)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Fatalf("missing %q in %q", w, got)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Fatalf("unexpected %q in %q", a, got)
				}
			}
		})
	}
}
