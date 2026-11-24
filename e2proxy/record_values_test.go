package e2proxy

import (
	"bytes"
	"strings"
	"testing"
)

// E② 밖 기록(탐색 활동 기록)이 쓰는 정확값 가림은 E② 저장 기록 가림처럼 원문·JSON 이스케이프 값 모두 가리고, 긴 값부터
// 가려 한 값이 다른 값의 일부여도 꼬리가 남지 않는다(#875 발견 3).
func TestRedactRecordValuesMasksRawAndEscapedLongestFirst(t *testing.T) {
	short := []byte("tester-pw")
	long := []byte("tester-pw-longer")
	quoted := []byte(`pa"ss\word`)
	in := []byte(`{"users":[{"password":"tester-pw-longer"},{"password":"tester-pw"},{"password":"pa\"ss\\word"}],"note":"raw tester-pw"}`)
	orig := bytes.Clone(in)
	out := string(RedactRecordValues(in, [][]byte{short, nil, quoted, long}))
	if !bytes.Equal(in, orig) {
		t.Fatal("input changed")
	}
	for _, leak := range []string{"tester-pw", "longer", `pa\"ss`, `ss\\word`} {
		if strings.Contains(out, leak) {
			t.Fatalf("value left in record: %q in %s", leak, out)
		}
	}
	if strings.Count(out, "[REDACTED]") != 4 {
		t.Fatalf("redaction count: %s", out)
	}
	// 값이 없으면 그대로(복사본)다.
	if got := RedactRecordValues(in, nil); !bytes.Equal(got, in) {
		t.Fatal("empty list changed the record")
	}
}

// 탐색 기록 가림은 E②가 등록하는 비밀번호만 받는다(같은 길이·UTF-8 기준).
func TestValidProtectedPasswordMatchesRegistration(t *testing.T) {
	for _, tc := range []struct {
		value []byte
		want  bool
	}{
		{[]byte("12345"), false},
		{[]byte("123456"), true},
		{[]byte{0xff, 0xfe, 'a', 'b', 'c', 'd'}, false},
		{bytes.Repeat([]byte("a"), maxRedactValueBytes), true},
		{bytes.Repeat([]byte("a"), maxRedactValueBytes+1), false},
	} {
		if got := ValidProtectedPassword(tc.value); got != tc.want {
			t.Fatalf("len=%d: %v", len(tc.value), got)
		}
		// 실제 등록도 같은 판정이다.
		e, _ := coreOpen(t, passwordConfig(t, "http://127.0.0.1:1", &coreRecorder{}))
		if got := e.AddProtectedPasswords(tc.value) == nil; got != tc.want {
			t.Fatalf("registration disagrees len=%d: %v", len(tc.value), got)
		}
	}
}
