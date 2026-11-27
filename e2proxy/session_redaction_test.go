package e2proxy

import (
	"bytes"
	"encoding/json"
	"testing"
)

// 세션 값 등록(AddRedactValues)은 시험 계정 실행이 아닌 실행에서만 출력 가림 목록에 들어가 모델 출력에서 가려진다. 시험 계정
// 실행은 세션을 모델에 주고 가리지 않으므로(절대규칙 2, 2026-11-26 DLP 폐기) 값을 받기만 하고 남기지 않는다(짧은 값도 받는다).
// 저장 기록은 어느 실행에서도 세션 값을 가리지 않는다(RedactRecord는 E③ 표식·제어 비밀만).
func TestSessionValuesAreMaskedOnlyOutsideTestAccountRuns(t *testing.T) {
	const session = "synthetic-session-value-77aa"
	for _, mode := range []HeaderMode{HeaderTestAccountPassthrough, HeaderPreserve, HeaderProtectedSessionProposal} {
		t.Run(string(mode), func(t *testing.T) {
			e := &Execution{cfg: Config{HeaderMode: mode}}
			if err := e.AddRedactValues([]byte(session)); err != nil {
				t.Fatal(err)
			}
			testAccount := mode == HeaderTestAccountPassthrough
			if kept := len(e.redactOnly) != 0; kept == testAccount {
				t.Fatalf("가림 목록 %d개", len(e.redactOnly))
			}
			text := []byte("Cookie: sid=" + session)
			if hidden := !bytes.Contains(e.RedactOutput(text), []byte(session)); hidden == testAccount {
				t.Fatalf("모델 출력 세션 가림=%v", hidden)
			}
			if !bytes.Equal(e.RedactRecord(text), text) {
				t.Fatal("저장 기록에서 세션 값을 가림")
			}
			short := []byte("s1")
			if err := e.AddRedactValues(short); (err == nil) != testAccount {
				t.Fatalf("짧은 세션 값 등록: %v", err)
			}
		})
	}
}

// 출력 가림 전용 값의 길이 오류는 묶음 전체를 거부한다(시험 계정 실행이 아닌 실행). 일부만 등록하지 않는다.
func TestRedactValueByteLimitsAreAtomic(t *testing.T) {
	for _, size := range []int{minRedactValueBytes - 1, maxRedactValueBytes + 1} {
		e := &Execution{cfg: Config{HeaderMode: HeaderPreserve}}
		if e.AddRedactValues([]byte("synthetic-session-value-77aa"), bytes.Repeat([]byte("x"), size)) == nil || len(e.redactOnly) != 0 {
			t.Fatal("길이 오류 뒤 일부 세션이 등록됨")
		}
	}
}

// 알려진 값을 가리는 동안 JSON의 공개 값과 구조는 그대로다(이스케이프된 값도 가린다). 가림은 입력을 고치지 않고, 이미 가린
// 출력을 다시 가려도 같다. 시험 계정 실행이 아닌 실행의 세션 값과, 모든 실행의 제어 비밀이 이 길을 탄다.
func TestRedactOutputPreservesPublicJSONAlongsideEscapedValue(t *testing.T) {
	secret := []byte("synthetic-session/🔒\"value")
	session := &Execution{cfg: Config{HeaderMode: HeaderPreserve}}
	if err := session.AddRedactValues(secret); err != nil {
		t.Fatal(err)
	}
	control := &Execution{cfg: Config{HeaderMode: HeaderTestAccountPassthrough}, secrets: [][]byte{secret}}
	for name, redact := range map[string]func([]byte) []byte{"session value": session.RedactOutput, "control secret output": control.RedactOutput, "control secret record": control.RedactRecord} {
		input := []byte(`{ "intent_id":9223372036854775807, "values":[true,null,{"detail":"before synthetic-session\u002f\ud83d\udd12\"value after","public":"catalog\u002fname"}] }`)
		original := bytes.Clone(input)
		want := []byte(`{ "intent_id":9223372036854775807, "values":[true,null,{"detail":"before [REDACTED] after","public":"catalog\u002fname"}] }`)
		output := redact(input)
		if !json.Valid(output) || !bytes.Equal(output, want) {
			t.Fatalf("%s: 알려진 값을 가리는 동안 JSON의 공개 값 또는 구조가 바뀌었습니다: %s", name, output)
		}
		if !bytes.Equal(input, original) {
			t.Fatalf("%s: 가림이 호출자의 원본 입력을 수정했습니다", name)
		}
		if !bytes.Equal(redact(output), want) {
			t.Fatalf("%s: 이미 가린 출력을 다시 가리면 값이 바뀝니다", name)
		}
	}
}
