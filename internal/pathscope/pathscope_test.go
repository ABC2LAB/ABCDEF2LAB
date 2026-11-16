package pathscope

import "testing"

func TestExactAndPrefixRemainDistinct(t *testing.T) {
	for _, tc := range []struct {
		expression, path string
		query, want      bool
	}{
		{"=/", "/", false, true}, {"=/", "/createdb", false, false}, {"=/", "/", true, false},
		{"/users/v1", "/users/v1", false, true}, {"/users/v1", "/users/v1/123", false, true}, {"/users/v1", "/users/v10", false, false},
		{"/", "/createdb", false, true}, {"=/a/", "/a", false, false}, {"=/a/", "/a/", false, true},
	} {
		if Allows(tc.path, tc.query, tc.expression) != tc.want {
			t.Errorf("경로 모드 불일치: %q %q", tc.expression, tc.path)
		}
	}
	for _, expression := range []string{"=", "==/", "=/../", "=//", "=/%2f", "=/a\\b", "=/a?x=1", "=/a#b", "prefix", "=/a//b"} {
		if _, ok := Parse(expression); ok {
			t.Errorf("잘못된 승인 표현 수용: %q", expression)
		}
	}
}
