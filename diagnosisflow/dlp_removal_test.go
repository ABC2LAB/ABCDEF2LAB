package diagnosisflow

import "testing"

// 진단 흐름 화면의 경로 · 활동 글은 대상 경로와 응답 글을 가리지 않는다(절대규칙 2, 2026-11-26 DLP 폐기: 제품 기록 · 화면은
// 내부 담당자만 본다). 자격 모양이 든 경로 조각 · 글도 그대로다.
func TestFlowDisplayKeepsTargetValues(t *testing.T) {
	for _, path := range []string{
		"/api/users/token=abc123def456",
		"/api/session/eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJlLXZhbHVl",
		"GET /read/token=ab HTTP/1.1",
	} {
		if got := safePath(path); got != path {
			t.Fatalf("경로를 가림: %q → %q", path, got)
		}
	}
	for _, text := range []string{"password=synthetic-pass-77", "Authorization: Bearer synthetic-bearer-value"} {
		if got := displayText(text, 240); got != text {
			t.Fatalf("활동 글을 가림: %q → %q", text, got)
		}
	}
}
