package exploreboundary

import (
	"testing"

	"github.com/Autumn-27/artex/exploration"
)

// 탐색 경계의 제외 경로 판정은 진단 흐름·E②와 같은 공유 함수다(#636 후속, 공식 측정 전 필수). 경로 매개변수(";")를 붙인
// 표기와 대소문자 변형으로 제외 경로에 닿지 못하고, 이름 앞부분만 같은 경로·더 깊은 와일드카드는 지금처럼 보낸다.
// "%"가 든 표기는 원래대로 범위 밖이다.
func TestExploreExclusionUsesSharedMatcher(t *testing.T) {
	for path, forward := range map[string]bool{
		"/app/admin":            false,
		"/app/admin;jsessionid": false,
		"/app/admin;x/users":    false,
		"/app/ADMIN":            false,
		"/app/Admin/users":      false,
		"/app/v1/Secret":        false,
		"/app/v1/secret;x":      false,
		"/app/administrator":    true,
		"/app/x;admin":          true,
		"/app/v1/v2/secret":     true,
		"/app/public":           true,
		"/app/%61dmin":          false,
	} {
		t.Run(path, func(t *testing.T) {
			s, _, _ := fixtureSession(t, exploration.ApprovedWrites, exploration.FullLocal, func(tg *Target, _ *Policy) {
				tg.ExcludedPrefixes = []string{"/app/admin", "/app/*/secret"}
			})
			d, ticket := s.Admit(t.Context(), approved("GET", path))
			if ticket != nil {
				ticket.Done(Result{Status: 200})
			}
			if d.Forward != forward || !forward && d.Reason != ReasonOutOfScopePath {
				t.Fatalf("forward=%v reason=%q, want forward=%v", d.Forward, d.Reason, forward)
			}
		})
	}
}
