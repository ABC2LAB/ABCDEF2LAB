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
		// 쿠키 여러 개·--cookie= 형태(#571 재상 리뷰).
		"curl -b 'language=en; token=eyJx.eyJy.z' https://owned.test/c":                     "curl -b '[REDACTED]' https://owned.test/c",
		"curl --cookie=token=eyJx.eyJy.z https://owned.test/c":                              "curl --cookie=[REDACTED] https://owned.test/c",
		`curl --cookie="language=en; token=eyJx.eyJy.z" https://owned.test/c`:               `curl --cookie="[REDACTED]" https://owned.test/c`,
		`{"command": "curl -s -b \"language=en; token=eyJx.eyJy.z\" https://owned.test/c"}`: `{"command": "curl -s -b \"[REDACTED]\" https://owned.test/c"}`,
		// curl 밖 형태: python 표준 라이브러리 dict·add_header·튜플, JSON 헤더 객체, httpie, wget.
		"urllib.request.Request(u, headers={'Authorization': 'Bearer eyJx.eyJy.z', 'Accept': '*/*'})":                                                     "urllib.request.Request(u, headers={'Authorization': '[REDACTED]', 'Accept': '*/*'})",
		"req.add_header('Cookie', 'language=en; token=eyJx.eyJy.z')":                                                                                      "req.add_header('Cookie', '[REDACTED]')",
		"opener.addheaders = [('Authorization', 'Bearer eyJx.eyJy.z')]":                                                                                   "opener.addheaders = [('Authorization', '[REDACTED]')]",
		`{"Authorization": "Bearer eyJx.eyJy.z", "Accept": "application/json"}`:                                                                           `{"Authorization": "[REDACTED]", "Accept": "application/json"}`,
		`{"command": "python3 -c \"import urllib.request as r; r.urlopen(r.Request('https://owned.test/a', headers={'Cookie': 'token=eyJx.eyJy.z'}))\""}`: `{"command": "python3 -c \"import urllib.request as r; r.urlopen(r.Request('https://owned.test/a', headers={'Cookie': '[REDACTED]'}))\""}`,
		`{\"Cookie\": \"token=eyJx.eyJy.z\"}`:                                                                                                             `{\"Cookie\": \"[REDACTED]\"}`,
		"http GET https://owned.test/a Authorization:'Bearer eyJx.eyJy.z'":                                                                                "http GET https://owned.test/a Authorization:'[REDACTED]'",
		"wget --header='Cookie: language=en; token=eyJx.eyJy.z' https://owned.test/a":                                                                     "wget --header='Cookie: [REDACTED]' https://owned.test/a",
		`r.add_header(\"Cookie\", \"token=eyJx.eyJy.z\")`:                                                                                                 `r.add_header(\"Cookie\", \"[REDACTED]\")`,
		// 값 안의 JSON 이스케이프(\u002e·\/)와 겹 이스케이프는 값으로 본다(#571 이동찬 리뷰). 해석하면 한 값이다.
		`{"Authorization":"Bearer changed\u002ecredential-tail-0123456789"}`:                                    `{"Authorization":"[REDACTED]"}`,
		`{\"Authorization\":\"Bearer changed\\u002ecredential-tail-0123456789\"}`:                               `{\"Authorization\":\"[REDACTED]\"}`,
		`{"Cookie":"language=en; token=abc\u002ecredential-tail-0123"}`:                                         `{"Cookie":"[REDACTED]"}`,
		`curl -H "Authorization: Bearer a\/credential-tail" https://owned.test/a`:                               `curl -H "Authorization: [REDACTED]" https://owned.test/a`,
		`req.add_header('Cookie', 'token=abc\u002ecredential-tail')`:                                            `req.add_header('Cookie', '[REDACTED]')`,
		`curl -b "token=abc\u002ecredential-tail" https://owned.test/c`:                                         `curl -b "[REDACTED]" https://owned.test/c`,
		`curl -b token=abc\u002ecredential-tail https://owned.test/c`:                                           `curl -b [REDACTED] https://owned.test/c`,
		`{\"command\": \"curl -H \\\"Authorization: Bearer a\\u002ecredential-tail\\\" https://owned.test/a\"}`: `{\"command\": \"curl -H \\\"Authorization: [REDACTED]\\\" https://owned.test/a\"}`,
		`{\"command\": \"curl -b token=abc\\u002ecredential-tail https://owned.test/c\"}`:                       `{\"command\": \"curl -b [REDACTED] https://owned.test/c\"}`,
		`{"command": "curl -b token=abc\necho done"}`:                                                           `{"command": "curl -b [REDACTED]\necho done"}`,
		// JSON으로 감싼 curl -v 출력의 줄바꿈 이스케이프는 값의 끝이다. 다음 헤더 줄은 남는다.
		`"> Authorization: Bearer abc.def\r\n> Host: owned.test\r\n> Cookie: token=abc\r\n> Accept: */*"`: `"> Authorization: [REDACTED]\r\n> Host: owned.test\r\n> Cookie: [REDACTED]\r\n> Accept: */*"`,
		// 대상 응답의 Set-Cookie JSON 이름은 요청 Cookie로 보지 않는다.
		`{"Set-Cookie": "sid=short1"}`:                                         `{"Set-Cookie": "sid=short1"}`,
		"Proxy-Authorization: Basic eHl6":                                      "Proxy-Authorization: [REDACTED]",
		"< Set-Cookie: sid=short1; Path=/\r\n":                                 "< Set-Cookie: sid=[REDACTED]; Path=/\r\n",
		`{"products":[{"name":"Apple Juice","token":"public-catalog-value"}]}`: `{"products":[{"name":"Apple Juice","token":"public-catalog-value"}]}`,
		"WWW-Authenticate: Bearer realm=\"owned\"":                             "WWW-Authenticate: Bearer realm=\"owned\"",
	} {
		if got := string(e.RedactOutput([]byte(in))); got != want {
			t.Errorf("redact %q\n got %q\nwant %q", in, got, want)
		}
	}
	plain := &Execution{cfg: Config{HeaderMode: HeaderPreserve}}
	in := "> Authorization: Bearer modified.token.value"
	if got := string(plain.RedactOutput([]byte(in))); got != in {
		t.Fatalf("non test-account run masked structurally: %q", got)
	}
}
