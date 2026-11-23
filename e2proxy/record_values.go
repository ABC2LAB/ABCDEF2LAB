package e2proxy

import (
	"bytes"
	"sort"
	"unicode/utf8"
)

// ValidProtectedPassword는 AddProtectedPasswords가 받는 비밀번호인지 본다(길이·UTF-8). E② 실행 밖에서 같은 비밀번호
// 목록을 받는 기록(탐색 활동 기록)이 E②와 같은 값만 가리게 한다. 받지 못하는 값은 E②도 등록하지 않아 그 계정으로
// 로그인하지 않는다.
func ValidProtectedPassword(value []byte) bool {
	return len(value) >= minProtectedValueBytes && len(value) <= maxRedactValueBytes && utf8.Valid(value)
}

// RedactRecordValues는 E② 저장 기록 가림(RedactRecord)처럼 values의 정확값을 가린다. JSON 문자열 안에 이스케이프돼
// 들어간 값도 가리고, 긴 값부터 가려 한 값이 다른 값의 일부여도 남지 않게 한다. E② 실행 밖의 기록(탐색 활동 기록)이
// 같은 가림을 쓰려고 연다(2026-11-24 사용자 결정, #875 발견 3). 입력은 바꾸지 않는다.
func RedactRecordValues(b []byte, values [][]byte) []byte {
	out := bytes.Clone(b)
	sorted := make([][]byte, 0, len(values))
	for _, v := range values {
		if len(v) > 0 {
			sorted = append(sorted, v)
		}
	}
	if len(sorted) == 0 {
		return out
	}
	sort.SliceStable(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	out = redactEscapedJSONValues(out, sorted)
	for _, v := range sorted {
		out = bytes.ReplaceAll(out, v, []byte("[REDACTED]"))
	}
	return out
}
