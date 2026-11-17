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

// MaxExcluded는 제외 경로 패턴 수 상한이다(대상 입력 검사와 E② 정책 검사가 같은 값).
const MaxExcluded = 32

// ValidExcluded는 운영자가 넣은 제외 경로 패턴의 문법이다. "/"로 시작하고, 조각 하나 전체인 "*"만 와일드카드로
// 받으며, percent 인코딩·query·조각 표시·점 조각·빈 조각은 받지 않는다. 진단 입력 검사(diagnosisprep)와 E② 정책
// 검사가 이 함수를 같이 써서, 입력이 받은 패턴을 E²가 거부해 실행이 시작되지 않는 일이 없게 한다.
func ValidExcluded(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if strings.Contains(part, "*") && part != "*" {
			return false
		}
	}
	return strings.HasPrefix(p, "/") && !strings.ContainsAny(p, "%?#!\\ \r\t") && !strings.Contains(p, "//") && !strings.Contains(p, "..") && len(p) <= 256
}

// Excluded는 대상이 처리하는 풀린 경로(percent 인코딩을 한 번 푼 것)가 제외 패턴 아래인지다. 진단 흐름과 E②가 같은
// 함수를 써서 제외 판정이 갈리지 않는다(#627).
//   - 패턴 "/"는 모든 경로다. 그 밖에는 조각 단위로 앞에서부터 맞추고 패턴은 그 아래 경로까지 덮는다(/admin은 /admin·
//     /admin/x를 덮고 /administrator는 덮지 않음).
//   - 조각 하나 전체인 "*"는 그 자리의 아무 조각 하나와 맞는다.
//   - 경로 매개변수(";" 뒤)를 조각에서 떼는 서버(Tomcat·Spring)가 있어, 조각은 원문과 ";" 앞 이름 둘 다로 맞춘다
//     (/admin;x도 /admin 제외에 걸림). 판정을 좁히기만 한다.
//   - 풀린 경로로 맞추므로 /x/%C3%BCber는 제외 /x/über에, /x/%61dmin은 제외 /x/admin에 걸린다. 다시 인코딩한 표기로
//     맞추면 비ASCII 제외가 빠진다(#627 재현).
//   - 대소문자는 구분한다(대소문자를 무시하는 서버에서 /ADMIN은 /admin 제외에 걸리지 않는 한계).
func Excluded(decoded, pattern string) bool {
	return excludedSegments(strings.Split(strings.Trim(decoded, "/"), "/"), pattern)
}

// ExcludedAny는 패턴 중 하나라도 걸리는지다. 경로는 한 번만 나눈다(E②는 제외 경로가 있는 실행에서 요청마다 부름).
func ExcludedAny(decoded string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	actual := strings.Split(strings.Trim(decoded, "/"), "/")
	for _, p := range patterns {
		if excludedSegments(actual, p) {
			return true
		}
	}
	return false
}

// excludedSegments는 나눈 경로 조각과 패턴 하나를 맞춘다. 패턴 조각은 나누지 않고 차례로 읽는다(할당 없음).
func excludedSegments(actual []string, pattern string) bool {
	if pattern == "/" {
		return true
	}
	rest := strings.Trim(pattern, "/")
	for i := 0; ; i++ {
		part, next, more := strings.Cut(rest, "/")
		if i >= len(actual) {
			return false
		}
		name, _, _ := strings.Cut(actual[i], ";")
		if part != "*" && part != actual[i] && part != name {
			return false
		}
		if !more {
			return true
		}
		rest = next
	}
}
