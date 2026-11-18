package e2proxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const fixtureMarker = "abcdefghijklmnopqrstuvwxfj123456"

func TestProgramMarkerInspectionAndOutputUseSameNormalization(t *testing.T) {
	marker := []byte(fixtureMarker)
	jsonEncoded := strings.Replace(fixtureMarker, "a", `\u0061`, 1)
	urlEncoded := strings.Replace(fixtureMarker, "b", "%62", 1)
	htmlNumeric := strings.Replace(fixtureMarker, "c", "&#99;", 1)
	htmlNamed := strings.Replace(fixtureMarker, "fj", "&fjlig;", 1)
	cases := []struct {
		name, value, method string
	}{
		{"exact", fixtureMarker, "exact"},
		{"case", strings.ToUpper(fixtureMarker), "case_insensitive"},
		{"json", jsonEncoded, "decoded_json"},
		{"url", urlEncoded, "decoded_url"},
		{"html_numeric", htmlNumeric, "decoded_html_numeric"},
		{"html_named", htmlNamed, "decoded_html_named"},
		{"mixed", strings.Replace(strings.Replace(jsonEncoded, "b", "%62", 1), "c", "&#99;", 1), "decoded_json"},
		{"unicode_prefix", "İ" + fixtureMarker, "exact"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"kind":"public","value":"` + tc.value + `","count":7}`)
			present, method, supported := inspectProgramMarker(body, marker)
			if !present || !supported || method != tc.method {
				t.Fatalf("표식 관찰 불일치: present=%v supported=%v method=%q", present, supported, method)
			}
			out := redactMarker(body, marker)
			var doc map[string]any
			wantValue := "[REDACTED]"
			if tc.name == "unicode_prefix" {
				wantValue = "İ[REDACTED]"
			}
			if err := json.Unmarshal(out, &doc); err != nil || doc["kind"] != "public" || doc["value"] != wantValue || doc["count"] != float64(7) {
				t.Fatalf("공개 구조 또는 표식 가림 불일치: %v", err)
			}
			if bytes.Contains(out, []byte(tc.value)) || !bytes.Equal(redactMarker(out, marker), out) {
				t.Fatal("표식 노출 또는 재가림 불안정")
			}
		})
	}
	if present, method, supported := inspectProgramMarker([]byte(`{"kind":"public"}`), marker); present || method != "" || !supported {
		t.Fatal("표식 없는 공개 본문을 잘못 분류")
	}
}

func TestProgramMarkerUnsupportedRepresentationsAreNotAbsent(t *testing.T) {
	marker := []byte(fixtureMarker)
	var pct strings.Builder
	for _, b := range marker {
		fmt.Fprintf(&pct, "%%%02X", b)
	}
	cases := map[string][]byte{
		"base64":     []byte(base64.StdEncoding.EncodeToString(marker)),
		"double_url": []byte(strings.ReplaceAll(pct.String(), "%", "%25")),
		"binary":     {0xff, 0xfe},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if present, method, supported := inspectProgramMarker(body, marker); present || method != "" || supported {
				t.Fatal("지원 밖 표현을 표식 없음으로 분류")
			}
			if !bytes.Equal(redactMarker(body, marker), []byte("[REDACTED]")) {
				t.Fatal("지원 밖 표현이 출력으로 전달됨")
			}
		})
	}
}

func TestRegisterProgramMarkerOnlyChangesMarkerOutput(t *testing.T) {
	execution := &Execution{}
	marker := []byte(fixtureMarker)
	if err := execution.RegisterProgramMarker(marker); err != nil {
		t.Fatal(err)
	}
	marker[0] = 'z'
	if err := execution.RegisterProgramMarker([]byte(fixtureMarker)); err != nil {
		t.Fatal("같은 보호 표식 재등록 실패")
	}
	if err := execution.RegisterProgramMarker([]byte(strings.Repeat("x", markerBytes))); err == nil || execution.RegisterProgramMarker([]byte("short")) == nil {
		t.Fatal("표식 교체 또는 잘못된 길이 수용")
	}
	input := []byte(`{"value":"` + strings.Replace(fixtureMarker, "fj", "&fjlig;", 1) + `","public":true}`)
	out := execution.RedactOutput(input)
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil || doc["value"] != "[REDACTED]" || doc["public"] != true {
		t.Fatalf("출력 경계 가림·공개 값 보존 실패: %v", err)
	}
	if bytes.Equal(out, input) || !bytes.Equal(execution.RedactOutput(out), out) {
		t.Fatal("출력 가림 실패 또는 재호출 불안정")
	}
	plain := (&Execution{}).RedactOutput(input)
	if !bytes.Equal(plain, input) {
		t.Fatal("표식 없는 일반 회차의 출력이 변경됨")
	}
}

func TestProgramMarkerNormalizationBoundsLongEntityPrefixes(t *testing.T) {
	input := bytes.Repeat([]byte{'&'}, maxMarkerOutput)
	if present, method, supported := inspectProgramMarker(input, []byte(fixtureMarker)); present || method != "" || !supported {
		t.Fatal("긴 엔티티 접두 입력을 잘못 분류")
	}
	if out := redactMarker(input, []byte(fixtureMarker)); !bytes.Equal(out, input) {
		t.Fatal("비민감 입력을 변경")
	}
	if out := redactMarker(append(input, '&'), []byte(fixtureMarker)); !bytes.Equal(out, []byte("[REDACTED]")) {
		t.Fatal("크기 상한을 넘은 출력을 그대로 전달")
	}
}
