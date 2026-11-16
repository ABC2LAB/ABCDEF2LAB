package e2proxy

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"net/http"
	"strings"
	"testing"
)

// 시험 계정 실행의 구조 가림이 무작위 쿠키에서도 자격 값을 남기지 않고 JSON을 깨지 않는지 본다(#571 이동찬 리뷰 뒤 회귀
// 방어). 쿠키 이름·값은 따옴표·구조 문자(}]:,;)·이스케이프·유니코드를 섞어 만들고, net/http가 name=value와 token=값을 모두
// 유효한 Cookie로 읽는 경우만 쓴다. 고정 시드 회귀는 CI에서 돌고, 퍼즈는 로컬에서 짧게 돌린다:
//
//	go test ./e2proxy -run '^$' -fuzz FuzzTestAccountRedactionCookie -fuzztime 30s

// cookieFuzzSecret은 가려야 할 자격 값이다. 유효한 쿠키 값이고 생성 조각으로는 만들어지지 않는다.
const cookieFuzzSecret = "zzfuzzsecret0123456789ABCDEFqq"

var (
	cookieFuzzNamePieces  = []string{"lang", "a", "Z", "9", "-", "_", ".", "!", "#", "$", "%", "&", "'", "*", "+", "^", "`", "|", "~", "=", "\"", " ", "}", "한"}
	cookieFuzzValuePieces = []string{"a", "Z", "9", "-", "_", ".", "\"", "}", "]", ":", ",", ";", "\\", "'", " ", "\t", "=", "{", "[", "/", "한", "é", "\\u0022", "\\\"", "%7D", "x y"}
)

// cookieFuzzValid는 요청 헤더로 보낼 수 있고(탭 말고 제어 문자 없음) net/http가 name=value와 token=cookieFuzzSecret을 모두
// 유효한 Cookie로 읽는지 본다. 줄바꿈이 든 값은 net/http 파서가 앞뒤를 잘라 받아도 헤더로 보낼 수 없어 쓰지 않는다.
func cookieFuzzValid(name, value string) bool {
	if name == "" || name == "token" || strings.Contains(name+value, cookieFuzzSecret) {
		return false
	}
	for _, c := range []byte(name + value) {
		if c < 0x20 && c != '\t' || c == 0x7f {
			return false
		}
	}
	r := &http.Request{Header: http.Header{"Cookie": {name + "=" + value + "; token=" + cookieFuzzSecret}}}
	if _, err := r.Cookie(name); err != nil {
		return false
	}
	token, err := r.Cookie("token")
	return err == nil && token.Value == cookieFuzzSecret
}

func cookieFuzzString(rng *rand.Rand, pieces []string, minN, maxN int) string {
	var b strings.Builder
	for n := minN + rng.Intn(maxN-minN+1); n > 0; n-- {
		b.WriteString(pieces[rng.Intn(len(pieces))])
	}
	return b.String()
}

// cookieFuzzJSON은 HTML 이스케이프 없이(실제 도구 출력처럼) 직렬화한다.
func cookieFuzzJSON(v any, indent bool) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	_ = enc.Encode(v)
	return strings.TrimRight(b.String(), "\n")
}

// cookieFuzzCheck는 한 쿠키를 curl -v 줄·-b 인수·JSON 문자열(한 줄·들여쓰기·두 번 감싼 이스케이프 단계)에 넣고 가린다.
// 모든 형태에서 자격 값이 남지 않아야 하고, JSON이면 결과도 유효해야 한다. strict면 다음 헤더 줄·URL이 남는지도 본다(생성
// 조각으로는 다른 자격 헤더 이름이 만들어지지 않는 고정 시드 회귀에서만).
func cookieFuzzCheck(t *testing.T, e *Execution, name, value string, strict bool) {
	t.Helper()
	pair := name + "=" + value + "; token=" + cookieFuzzSecret
	plain := map[string]string{"curl -v": "> Cookie: " + pair + "\r\n> Accept: */*\r\n"}
	if !strings.Contains(pair, "'") {
		plain["-b single"] = "curl -s -b '" + pair + "' https://owned.test/x"
	}
	if !strings.Contains(pair, "\"") {
		plain["-b double"] = "curl -s -b \"" + pair + "\" https://owned.test/x"
	}
	docs := map[string]bool{}
	for form, text := range plain {
		docs[text] = false
		inner := map[string]string{"stdout": text, "kind": "observation"}
		docs[cookieFuzzJSON(inner, false)] = true
		docs[cookieFuzzJSON(inner, true)] = true
		docs[cookieFuzzJSON(map[string]string{"payload": cookieFuzzJSON(inner, false)}, false)] = true
		if strict {
			out := string(e.RedactOutput([]byte(text)))
			keep := "https://owned.test/x"
			if form == "curl -v" {
				keep = "\r\n> Accept: */*\r\n"
			}
			if !strings.HasSuffix(out, keep) {
				t.Errorf("%s over-redacted the following text:\n in %q\nout %q", form, text, out)
			}
		}
	}
	for doc, isJSON := range docs {
		out := e.RedactOutput([]byte(doc))
		if bytes.Contains(out, []byte(cookieFuzzSecret)) {
			t.Errorf("credential left:\n in %q\nout %q", doc, out)
		}
		if isJSON && !json.Valid(out) {
			t.Errorf("redaction broke JSON:\n in %q\nout %q", doc, out)
		}
	}
}

// 고정 시드로 유효한 쿠키를 충분히 만들어 모든 형태를 확인한다(CI 회귀).
func TestTestAccountRedactionCookieCorpus(t *testing.T) {
	e := &Execution{cfg: Config{HeaderMode: HeaderTestAccountPassthrough}}
	rng := rand.New(rand.NewSource(571))
	valid := 0
	for i := 0; i < 20000 && valid < 600; i++ {
		name := cookieFuzzString(rng, cookieFuzzNamePieces, 1, 3)
		value := cookieFuzzString(rng, cookieFuzzValuePieces, 0, 6)
		if rng.Intn(3) == 0 {
			value = "\"" + value + "\""
		}
		if !cookieFuzzValid(name, value) {
			continue
		}
		valid++
		cookieFuzzCheck(t, e, name, value, true)
		if t.Failed() {
			return
		}
	}
	// 생성기가 유효한 쿠키를 거의 못 만들면 이 시험은 의미가 없다.
	if valid < 600 {
		t.Fatalf("only %d valid cookies generated", valid)
	}
}

func FuzzTestAccountRedactionCookie(f *testing.F) {
	for _, seed := range [][2]string{{"lang", `"}"`}, {"lang", "en"}, {"a", `"]:,"`}, {"x", "{}"}, {"q", `""`}, {"lang", `"x y"`}} {
		f.Add(seed[0], seed[1])
	}
	e := &Execution{cfg: Config{HeaderMode: HeaderTestAccountPassthrough}}
	f.Fuzz(func(t *testing.T, name, value string) {
		if !cookieFuzzValid(name, value) {
			t.Skip()
		}
		cookieFuzzCheck(t, e, name, value, false)
	})
}
