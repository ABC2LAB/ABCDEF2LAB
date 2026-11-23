package server

import (
	"bytes"
	"testing"
)

type splitRedactor struct{}

func (splitRedactor) RedactOutput(data []byte) []byte { return append([]byte("output:"), data...) }
func (splitRedactor) RedactRecord(data []byte) []byte { return append([]byte("record:"), data...) }

// 원격 가림 연산은 저장 기록 연산(redact_record)에 RedactRecord를, 모델 출력 연산에 RedactOutput을 쓴다(model_visible_v1).
func TestE2RemoteRedactChoosesRecordOrOutput(t *testing.T) {
	if got := e2RemoteRedactWith(splitRedactor{}, []byte("x"), true); !bytes.Equal(got, []byte("record:x")) {
		t.Fatalf("저장 기록 연산: %q", got)
	}
	if got := e2RemoteRedactWith(splitRedactor{}, []byte("x"), false); !bytes.Equal(got, []byte("output:x")) {
		t.Fatalf("모델 출력 연산: %q", got)
	}
}

// 정책 문서의 password_response는 구간 가림과 model_visible_v1만 받고, 별칭·빈 값·모르는 값은 거부한다.
func TestE2HTTPPasswordResponseFieldAcceptsModelVisible(t *testing.T) {
	for doc, want := range map[string]bool{
		`{"auth":{"password_response":"mask_spans_v1"}}`:    true,
		`{"auth":{"password_response":"model_visible_v1"}}`: true,
		`{"auth":{}}`: true,
		`{"auth":{"password_response":"Model_Visible_V1"}}`: false,
		`{"auth":{"password_response":""}}`:                 false,
		`{"auth":{"Password_Response":"model_visible_v1"}}`: false,
	} {
		if got := validE2HTTPPasswordResponseField([]byte(doc)); got != want {
			t.Fatalf("%s: %v", doc, got)
		}
	}
}
