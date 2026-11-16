// Package pathscope는 보호된 승인 경로 표현을 해석한다. 접두와 정확 일치를 같은 문자열 비교로 혼동하지 않는다.
package pathscope

import (
	"path"
	"strings"
)

type Mode string

const (
	Prefix Mode = ""
	Exact  Mode = "exact"
)

type Scope struct {
	Path string
	Mode Mode
}

// Parse는 기존 /prefix와 새 =/exact를 구분한다. 정확 일치는 query 없는 한 경로만 허용한다.
func Parse(expression string) (Scope, bool) {
	s := Scope{Path: expression}
	if strings.HasPrefix(expression, "=") {
		s.Path, s.Mode = expression[1:], Exact
	}
	p := s.Path
	for _, b := range []byte(p) {
		if b < 0x20 || b == 0x7f {
			return Scope{}, false
		}
	}
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "%\\?#\x00\r\n\t ") || strings.Contains(p, "//") || (p != "/" && path.Clean(p) != strings.TrimSuffix(p, "/")) {
		return Scope{}, false
	}
	return s, true
}
func (s Scope) Expression() string {
	if s.Mode == Exact {
		return "=" + s.Path
	}
	return s.Path
}
func (s Scope) Allows(p string, hasQuery bool) bool {
	if s.Mode == Exact {
		return !hasQuery && p == s.Path
	}
	if s.Mode != Prefix {
		return false
	}
	return s.Path == "/" || p == strings.TrimSuffix(s.Path, "/") || strings.HasPrefix(p, strings.TrimSuffix(s.Path, "/")+"/")
}
func Allows(p string, hasQuery bool, expression string) bool {
	s, ok := Parse(expression)
	return ok && s.Allows(p, hasQuery)
}
