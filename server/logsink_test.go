package server

import "testing"

// 로그를 한국어로 옮겨도 수준이 내려가지 않아야 한다. 아직 남은 중국어·영어 로그의 수준도 그대로여야 한다.
func TestLevelOfKeepsLevelAcrossLanguages(t *testing.T) {
	for _, tc := range []struct{ msg, want string }{
		{"[tools] seed x 실패: boom", "error"},
		{"[activity] task 1 활동 기록 폐기", "error"},
		{"[intercept] 승인 거부", "error"},
		{"[mcp] 서버 도달 불가", "error"},
		{"[activity] task 1 기록 2 번째 재시도 성공", "warn"},
		{"[traffic] 본문 검색을 비활성화한다", "warn"},
		{"[tools] seed x 失败", "error"},
		{"[activity] 重试", "warn"},
		{"[tools] refreshed", "info"},
		{"[prompts] worker 프롬프트를 현재 기본값으로 갱신", "info"},
	} {
		if got := levelOf(tc.msg); got != tc.want {
			t.Errorf("levelOf(%q)=%s, want %s", tc.msg, got, tc.want)
		}
	}
}
