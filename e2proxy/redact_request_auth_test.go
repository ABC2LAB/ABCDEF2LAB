package e2proxy

import "testing"

// 시험 계정 실행에서는 모델이 보내는 요청 자격 자리를 값이 무엇이든 가린다. 모델이 받은 세션을 바꿔 보내면(서명이 다른 변형
// JWT 등) 정확한 값 가림을 지나가기 때문이다. Set-Cookie는 이름·속성을 남기는 기존 구조 가림을 그대로 따르고, 대상 응답의
// 다른 내용은 그대로 둔다. 시험 계정 실행이 아니면 구조 가림을 하지 않는다.
func TestTestAccountRunMasksRequestCredentialsStructurally(t *testing.T) {
	e := &Execution{cfg: Config{HeaderMode: HeaderTestAccountPassthrough}}
	for in, want := range map[string]string{
		`{"command": "curl -sv -H \"Authorization: Bearer eyJhbGc.eyJtb2RpZmllZA.c2ln\" https://owned.test/rest/basket/2"}`: `{"command": "curl -sv -H \"Authorization: [REDACTED]\" https://owned.test/rest/basket/2"}`,
		"curl -H 'authorization: Basic dXNlcjpwYXNz' https://owned.test/a":                                                  "curl -H 'authorization: [REDACTED]' https://owned.test/a",
		"> GET /rest/basket/2 HTTP/1.1\r\n> Authorization: Bearer modified.token.value\r\n> Cookie: token=abc; sid=def\r\n": "> GET /rest/basket/2 HTTP/1.1\r\n> Authorization: [REDACTED]\r\n> Cookie: [REDACTED]\r\n",
		`curl -H "Cookie: token=eyJx.eyJy.z" https://owned.test/b`:                                                          `curl -H "Cookie: [REDACTED]" https://owned.test/b`,
		"curl -b 'token=eyJx.eyJy.z' --cookie sid=abc https://owned.test/c":                                                 "curl -b '[REDACTED]' --cookie [REDACTED] https://owned.test/c",
		"Proxy-Authorization: Basic eHl6":                                                                                   "Proxy-Authorization: [REDACTED]",
		"< Set-Cookie: sid=short1; Path=/\r\n":                                                                              "< Set-Cookie: sid=[REDACTED]; Path=/\r\n",
		`{"products":[{"name":"Apple Juice","token":"public-catalog-value"}]}`:                                              `{"products":[{"name":"Apple Juice","token":"public-catalog-value"}]}`,
		"WWW-Authenticate: Bearer realm=\"owned\"":                                                                          "WWW-Authenticate: Bearer realm=\"owned\"",
	} {
		if got := string(e.RedactOutput([]byte(in))); got != want {
			t.Fatalf("redact %q\n got %q\nwant %q", in, got, want)
		}
	}
	plain := &Execution{cfg: Config{HeaderMode: HeaderPreserve}}
	in := "> Authorization: Bearer modified.token.value"
	if got := string(plain.RedactOutput([]byte(in))); got != in {
		t.Fatalf("non test-account run masked structurally: %q", got)
	}
}
