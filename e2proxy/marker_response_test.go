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

func TestProgramMarkerOutputRedactsEmbeddedBase64(t *testing.T) {
	execution := &Execution{}
	if err := execution.RegisterProgramMarker([]byte(fixtureMarker)); err != nil {
		t.Fatal(err)
	}
	codecs := map[string]*base64.Encoding{
		"standard": base64.StdEncoding, "raw_standard": base64.RawStdEncoding,
		"url": base64.URLEncoding, "raw_url": base64.RawURLEncoding,
	}
	for name, codec := range codecs {
		for offset := 0; offset < 3; offset++ {
			t.Run(fmt.Sprintf("%s/%d", name, offset), func(t *testing.T) {
				value := append(bytes.Repeat([]byte{0xff}, offset), fixtureMarker...)
				value = append(value, 0xfe, 'z')
				input := []byte(fmt.Sprintf("{\"blob\":%q}", codec.EncodeToString(value)))
				if out := execution.RedactOutput(input); !bytes.Equal(out, []byte("[REDACTED]")) {
					t.Fatal("공유 출력의 Base64 보호 값을 가리지 못함")
				}
			})
		}
	}
	public := []byte(fmt.Sprintf("{\"blob\":%q}", base64.StdEncoding.EncodeToString([]byte("public data"))))
	if out := execution.RedactOutput(public); !bytes.Equal(out, public) {
		t.Fatal("표식이 없는 Base64 출력이 변경됨")
	}
}

func TestProgramMarkerOutputRedactsUnterminatedNumericEntities(t *testing.T) {
	execution := &Execution{}
	if err := execution.RegisterProgramMarker([]byte(fixtureMarker)); err != nil {
		t.Fatal(err)
	}
	for name, ref := range map[string]string{"decimal": "&#%d", "hex": "&#x%x", "escaped_ampersand": "\\u0026#%d"} {
		t.Run(name, func(t *testing.T) {
			var encoded strings.Builder
			for _, b := range []byte(fixtureMarker) {
				fmt.Fprintf(&encoded, ref, b)
			}
			if out := execution.RedactOutput([]byte(encoded.String())); !bytes.Equal(out, []byte("[REDACTED]")) {
				t.Fatal("해석되지 않은 숫자 참조를 공유 출력으로 전달함")
			}
		})
	}
}

func TestProgramMarkerOutputRedactsOverlappingSourceSpans(t *testing.T) {
	execution := &Execution{}
	marker := "j" + strings.Repeat("x", markerBytes-2) + "f"
	if err := execution.RegisterProgramMarker([]byte(marker)); err != nil {
		t.Fatal(err)
	}
	// The two copies of the marker share one entity in the original text.
	input := []byte("before " + marker[:len(marker)-1] + "&fjlig;" + marker[1:] + " after")
	if out := execution.RedactOutput(input); !bytes.Equal(out, []byte("before [REDACTED] after")) {
		t.Fatal("겹치는 두 원문 범위 전체를 가리지 못함")
	}
}

func TestRegisterProgramMarkerRejectsStoppedExecutionAndUppercase(t *testing.T) {
	for _, tc := range []struct {
		name, marker string
		stopped      bool
	}{
		{name: "stopped", marker: fixtureMarker, stopped: true},
		{name: "uppercase", marker: strings.ToUpper(fixtureMarker)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			execution := &Execution{stopped: tc.stopped}
			if err := execution.RegisterProgramMarker([]byte(tc.marker)); err != ErrConfig {
				t.Fatalf("등록 거부 오류 = %v, 기대 %v", err, ErrConfig)
			}
			if len(execution.marker) != 0 {
				t.Fatal("거부한 표식을 실행에 저장함")
			}
		})
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
