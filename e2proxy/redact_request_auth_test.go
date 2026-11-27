package e2proxy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// 모델이 보내는 요청 자격(명령의 Authorization·Cookie 헤더, curl --cookie, curl -v가 되찍는 요청 헤더 줄)과 대상이 준 Set-Cookie는
// 모델 출력에서도 저장 기록에서도 가리지 않는다(절대규칙 2, 2026-11-26 DLP 폐기: 제품 기록은 내부 담당자만 보고, 모델 입력에
// 시험 계정 자격·대상 발급 세션을 넣을 수 있다). 값 자리를 구조로 가리던 것을 걷었으므로 JSON 문서(그래프 도구 인수 등)도
// 바이트 그대로다. 시험 계정 실행이 아닌 실행도 구조 가림은 하지 않는다(전에도 안 했다).
func TestRequestCredentialsAreNotMaskedStructurally(t *testing.T) {
	inputs := []string{
		`{"command": "curl -sv -H \"Authorization: Bearer eyJhbGc.eyJtb2RpZmllZA.c2ln\" https://owned.test/rest/basket/2"}`,
		"curl -H 'authorization: Basic dXNlcjpwYXNz' https://owned.test/a",
		"> GET /rest/basket/2 HTTP/1.1\r\n> Authorization: Bearer modified.token.value\r\n> Cookie: token=abc; sid=def\r\n",
		"curl -b 'language=en; token=eyJx.eyJy.z' --cookie sid=abc https://owned.test/c",
		"urllib.request.Request(u, headers={'Authorization': 'Bearer eyJx.eyJy.z', 'Accept': '*/*'})",
		`{"request":{"Authorization":"Bearer synthetic-private-auth","Cookie":"lang=\"en\"; sid=synthetic-private-session","Accept":"application/json"},"response":{"token":"public-catalog-label"},"intent_id":42}`,
		"< Set-Cookie: sid=short1; Path=/\r\n< Authentication-Info: nextnonce=a1\r\n",
	}
	for _, mode := range []HeaderMode{HeaderTestAccountPassthrough, HeaderPreserve} {
		e := &Execution{cfg: Config{HeaderMode: mode, PasswordResponse: PasswordResponseModelVisible}}
		for name, redact := range map[string]func([]byte) []byte{"model output": e.RedactOutput, "record": e.RedactRecord} {
			for _, in := range inputs {
				if got := string(redact([]byte(in))); got != in {
					t.Errorf("%s %s: 요청 자격을 가림\n in %s\nout %s", mode, name, in, got)
				}
			}
		}
	}
}

// 가림을 걷은 뒤에도 우리 제어 비밀(설정한 보호 값)과 E③ 표식은 모델 출력·저장 기록 어디에도 남지 않는다(절대규칙 2). 같은
// 글에 든 시험 계정 세션·비밀번호·대상 응답은 그대로 남는다. 원문과 JSON 문자열 안에 이스케이프된 값 둘 다 본다.
func TestControlSecretsAndMarkerStayMaskedAfterDLPRemoval(t *testing.T) {
	const control = "synthetic-control/secret\"value-0123"
	marker := strings.Repeat("m", markerBytes)
	const session, password = "synthetic-session-value-77aa", "seeded-pass-77!"
	for _, mode := range []HeaderMode{HeaderTestAccountPassthrough, HeaderPreserve} {
		e := &Execution{cfg: Config{HeaderMode: mode, PasswordResponse: PasswordResponseModelVisible}, secrets: [][]byte{[]byte(control)}}
		if err := e.RegisterProgramMarker([]byte(marker)); err != nil {
			t.Fatal(err)
		}
		if err := e.AddProtectedPasswords([]byte(password)); err != nil {
			t.Fatal(err)
		}
		plain := "Cookie: sid=" + session + " password=" + password + " key=" + control + " canary=" + marker
		doc, _ := json.Marshal(map[string]string{"tool_output": plain, "kind": "observation"})
		for name, redact := range map[string]func([]byte) []byte{"model output": e.RedactOutput, "record": e.RedactRecord} {
			for _, in := range [][]byte{[]byte(plain), doc} {
				out := redact(in)
				var decoded string
				if json.Valid(in) {
					var got map[string]string
					if json.Unmarshal(out, &got) != nil || got["kind"] != "observation" {
						t.Fatalf("%s %s: 가림 뒤 JSON이 깨짐: %s", mode, name, out)
					}
					decoded = got["tool_output"]
				} else {
					decoded = string(out)
				}
				if strings.Contains(decoded, control) || bytes.Contains(out, []byte(control)) || strings.Contains(decoded, marker) {
					t.Fatalf("%s %s: 제어 비밀 또는 E③ 표식이 남음: %s", mode, name, out)
				}
				if !strings.Contains(decoded, session) || !strings.Contains(decoded, password) {
					t.Fatalf("%s %s: 시험 계정 세션·비밀번호까지 가림: %s", mode, name, out)
				}
			}
		}
	}
}
