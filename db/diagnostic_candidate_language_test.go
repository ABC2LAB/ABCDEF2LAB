package db

import "testing"

// '다른 언어 섞임' 표시는 한 번 이상 다시 쓰게 한 뒤에만 붙는다(2026-11-24). 다시 쓰기 횟수는 0~2뿐이다.
func TestDiagnosticCandidateLanguageFieldsValidate(t *testing.T) {
	for _, tc := range []struct {
		mixed    bool
		rewrites int
		ok       bool
	}{
		{false, 0, true}, {false, 1, true}, {true, 1, true}, {false, 2, true}, {true, 2, true},
		{true, 0, false}, {false, 3, false}, {true, 3, false}, {false, -1, false},
	} {
		c := syntheticDiagnosticCandidate(1, 2, "language-call")
		c.LanguageMixed, c.LanguageRewrites = tc.mixed, tc.rewrites
		if err := c.Validate(); (err == nil) != tc.ok {
			t.Errorf("mixed=%v rewrites=%d: %v", tc.mixed, tc.rewrites, err)
		}
	}
}
