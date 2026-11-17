package pathscope

import (
	"strings"
	"testing"
)

// 제외 경로 일치는 진단 흐름과 E②가 같이 쓰는 함수다(#627). 풀린 경로 기준, 조각 경계, "*" 한 조각, ";" 매개변수를
// 뗀 이름, 대소문자 무시(유니코드 단순 fold)를 고정한다.
func TestExcludedMatchesDecodedSegments(t *testing.T) {
	for _, tc := range []struct {
		pattern, path string
		want          bool
	}{
		{"/", "/anything", true},
		{"/read/admin", "/read/admin", true},
		{"/read/admin", "/read/admin/", true},
		{"/read/admin", "/read/admin/x/y", true},
		{"/read/admin/", "/read/admin", true},
		{"/read/admin", "/read/administrator", false},
		{"/read/admin", "/read/admi", false},
		{"/read/admin", "/read", false},
		{"/read/admin", "/read/admin;jsessionid=1", true},
		{"/read/admin", "/read/admin;x/deeper", true},
		{"/read/admin", "/read/x;admin", false},
		{"/read/a;b", "/read/a;b", true},
		{"/read/a;b", "/read/a", false},
		{"/read/über", "/read/über", true},
		{"/read/über", "/read/über/x", true},
		{"/read/über", "/read/%C3%BCber", false}, // 다시 인코딩한 표기가 아니라 풀린 경로를 받는다
		{"/read/über", "/read/überx", false},
		{"/read/*/secret", "/read/a/secret", true},
		{"/read/*/secret", "/read/b/secret/z", true},
		{"/read/*/secret", "/read/secret", false},
		{"/read/*/secret", "/read/a/b/secret", false},
		{"/read/*", "/read/anything", true},
		{"/read/*", "/read", false},
		{"/read/admin", "/read/ADMIN", true},
		{"/read/admin", "/read/Admin/7", true},
		{"/read/ADMIN", "/read/admin", true},
		{"/read/admin", "/read/ADMIN;x", true},
		{"/read/admin", "/read/ADMINISTRATOR", false},
		{"/read/über", "/read/ÜBER", true},
		{"/read/café", "/read/CAFÉ/x", true},
		{"/read/*/secret", "/read/A/SECRET", true},
		{"/read/kit", "/read/\u212Ait", true}, // 켈빈 기호 K는 k와 단순 fold가 같다(#620과 같은 대조)
	} {
		if got := Excluded(tc.path, tc.pattern); got != tc.want {
			t.Errorf("Excluded(%q, %q)=%v want %v", tc.path, tc.pattern, got, tc.want)
		}
	}
	// 승인 접두는 지금처럼 대소문자를 구분한다(제외만 넓게 맞춤).
	if Allows("/READ/x", false, "/read") || !Allows("/read/x", false, "/read") {
		t.Fatal("approval prefix case handling changed")
	}
	if !ExcludedAny("/read/x/secret", []string{"/other", "/read/*/secret"}) || ExcludedAny("/read/x", []string{"/other", "/read/*/secret"}) || ExcludedAny("/read/x", nil) {
		t.Fatal("ExcludedAny가 패턴 하나라도 맞는지로 판정하지 않음")
	}
}

// 문법은 진단 입력 검사(diagnosisprep)가 받던 규칙 그대로다. E② 정책 검사가 같은 함수를 써서 입력이 받은 패턴을
// E²가 거부하지 않는다.
func TestValidExcludedKeepsInputRules(t *testing.T) {
	for _, ok := range []string{"/", "/read", "/read/über", "/read/*/secret", "/read/a;b", "/read/admin/", "/" + strings.Repeat("a", 255)} {
		if !ValidExcluded(ok) {
			t.Errorf("valid pattern rejected: %q", ok)
		}
	}
	for _, bad := range []string{"", "read", "/read/a*", "/read/*x", "/read/%C3%BC", "/read?x", "/read#x", "/read!", "/read\\x", "/read x", "/read\tx", "/read\rx", "//read", "/read//x", "/read/../x", "/read/..", "/" + strings.Repeat("a", 256)} {
		if ValidExcluded(bad) {
			t.Errorf("invalid pattern accepted: %q", bad)
		}
	}
}
