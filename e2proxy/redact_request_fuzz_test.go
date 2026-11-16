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
	// 문자열 끝 형태: 자격 값이 `=`(base64 패딩)로 끝나고 그 뒤에 아무것도 없다. JSON으로 감싸면 바로 닫는 따옴표가 온다
	// (#571 이동찬 리뷰). 헤더 줄 형태(CRLF 뒤 다음 헤더)와 따로 본다.
	plain := map[string]string{
		"curl -v":           "> Cookie: " + pair + "\r\n> Accept: */*\r\n",
		"cookie end padded": "> Cookie: " + pair + "=",
		"basic end padded":  "needs Authorization: Basic " + cookieFuzzSecret + "==",
	}
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
		// 문자열 끝 형태는 뒤에 남을 글이 없어 과다 가림을 보지 않는다.
		keep := map[string]string{"curl -v": "\r\n> Accept: */*\r\n", "-b single": "https://owned.test/x", "-b double": "https://owned.test/x"}[form]
		if strict && keep != "" {
			out := string(e.RedactOutput([]byte(text)))
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

// 자격 값의 모양과 값 뒤에 오는 것을 축으로 나눠 조합한다(#571 이동찬 리뷰: `=`로 끝나는 값 뒤 문자열 끝). 값 모양은 실제
// 세션 값에서 흔한 것이고 모두 유효한 쿠키 값이다.
var credentialFuzzShapes = []struct {
	name string
	gen  func(*rand.Rand) string
}{
	{"base64 =", func(r *rand.Rand) string { return credentialFuzzPick(r, credentialFuzzBase64, 23) + "=" }},
	{"base64 ==", func(r *rand.Rand) string { return credentialFuzzPick(r, credentialFuzzBase64, 22) + "==" }},
	{"base64url", func(r *rand.Rand) string { return credentialFuzzPick(r, credentialFuzzBase64URL, 27) }},
	{"hex", func(r *rand.Rand) string { return credentialFuzzPick(r, "0123456789abcdef", 32) }},
	{"jwt", func(r *rand.Rand) string {
		return "eyJ" + credentialFuzzPick(r, credentialFuzzBase64URL, 14) + "." + credentialFuzzPick(r, credentialFuzzBase64URL, 16) + "." + credentialFuzzPick(r, credentialFuzzBase64URL, 12)
	}},
	{"bytes", func(r *rand.Rand) string { return credentialFuzzPick(r, credentialFuzzCookieBytes, 24) }},
}

// credentialFuzzFollows는 값 바로 뒤에 오는 것이다: 문자열 끝, 공백·탭, 줄바꿈 뒤 다음 헤더, 구조 문자, 다른 쿠키.
var credentialFuzzFollows = []string{"", " next", "\tnext", "\r\n> Accept: */*\r\n", ",next", "}next", "]next", ":next", ";next", "; other=1"}

const (
	credentialFuzzBase64    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	credentialFuzzBase64URL = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	// 쿠키 값으로 유효한 바이트 중 앞뒤 공백 자르기를 피하려고 공백만 뺀 것(net/http validCookieValueByte).
	credentialFuzzCookieBytes = "!#$%&'()*+,-./0123456789:<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[]^_`abcdefghijklmnopqrstuvwxyz{|}~"
)

func credentialFuzzPick(r *rand.Rand, alphabet string, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[r.Intn(len(alphabet))]
	}
	return string(b)
}

// credentialFuzzLeaked는 자격 값이 출력에 남았는지 본다. 값이 중간에서 잘려 뒷부분만 남는 것도 잡으려고 패딩을 뺀 값의
// 8글자 창마다 본다. 입력의 다른 곳(쿠키 이름·다른 값)에 원래 있던 창은 세지 않는다.
func credentialFuzzLeaked(in, out, secret string) bool {
	core := strings.TrimRight(secret, "=")
	rest := strings.ReplaceAll(in, secret, "")
	for k := 0; k+8 <= len(core); k++ {
		if w := core[k : k+8]; strings.Contains(out, w) && !strings.Contains(rest, w) {
			return true
		}
	}
	return false
}

// credentialFuzzCheck는 값 하나를 쿠키 줄·Bearer·Basic·-b 인수에 넣고, 각각을 원문과 JSON(한 줄·들여쓰기·두 겹)으로 가린다.
// 자격 값이 남지 않아야 하고 JSON이면 결과도 유효해야 한다. 줄바꿈 뒤 다음 헤더와 -b 뒤 URL은 남아야 한다(과다 가림).
func credentialFuzzCheck(t *testing.T, e *Execution, name, value, secret, follow string) {
	t.Helper()
	pair := name + "=" + value + "; token=" + secret + follow
	plain := map[string]string{
		"cookie": "> Cookie: " + pair,
		"bearer": "> Authorization: Bearer " + secret + follow,
		"basic":  "needs Authorization: Basic " + secret + follow,
	}
	if !strings.ContainsAny(follow, "\r\n") {
		if !strings.Contains(pair, "'") {
			plain["-b single"] = "curl -s -b '" + pair + "' https://owned.test/x"
		}
		if !strings.Contains(pair, "\"") {
			plain["-b double"] = "curl -s -b \"" + pair + "\" https://owned.test/x"
		}
	}
	for form, text := range plain {
		inner := map[string]string{"stdout": text, "kind": "observation"}
		docs := map[string]bool{text: false, cookieFuzzJSON(inner, false): true, cookieFuzzJSON(inner, true): true,
			cookieFuzzJSON(map[string]string{"payload": cookieFuzzJSON(inner, false)}, false): true}
		for doc, isJSON := range docs {
			out := string(e.RedactOutput([]byte(doc)))
			if credentialFuzzLeaked(doc, out, secret) {
				t.Errorf("%s: credential left:\n in %q\nout %q", form, doc, out)
			}
			if isJSON && !json.Valid([]byte(out)) {
				t.Errorf("%s: redaction broke JSON:\n in %q\nout %q", form, doc, out)
			}
		}
		keep := ""
		switch {
		case strings.HasPrefix(follow, "\r\n"):
			keep = follow
		case strings.HasPrefix(form, "-b"):
			keep = "https://owned.test/x"
		}
		if out := string(e.RedactOutput([]byte(text))); keep != "" && !strings.HasSuffix(out, keep) {
			t.Errorf("%s over-redacted the following text:\n in %q\nout %q", form, text, out)
		}
	}
}

// 값 모양 × 값 뒤에 오는 것의 모든 조합을 고정 시드로 확인한다(CI 회귀). 조합마다 무작위 앞 쿠키를 여러 개 붙인다.
func TestTestAccountRedactionCredentialAxes(t *testing.T) {
	e := &Execution{cfg: Config{HeaderMode: HeaderTestAccountPassthrough}}
	rng := rand.New(rand.NewSource(57101))
	for _, shape := range credentialFuzzShapes {
		for _, follow := range credentialFuzzFollows {
			checked := 0
			for tries := 0; checked < 8 && tries < 4000; tries++ {
				name := cookieFuzzString(rng, cookieFuzzNamePieces, 1, 3)
				value := cookieFuzzString(rng, cookieFuzzValuePieces, 0, 5)
				if rng.Intn(3) == 0 {
					value = "\"" + value + "\""
				}
				secret := shape.gen(rng)
				if !cookieFuzzValid(name, value) || strings.Contains(name+value, secret) {
					continue
				}
				checked++
				credentialFuzzCheck(t, e, name, value, secret, follow)
				if t.Failed() {
					t.Fatalf("shape %s follow %q", shape.name, follow)
				}
			}
			// 조합마다 유효한 앞 쿠키를 충분히 만들지 못하면 이 시험은 그 조합을 보지 않은 것이다.
			if checked < 8 {
				t.Fatalf("shape %s follow %q: only %d cases", shape.name, follow, checked)
			}
		}
	}
}

func FuzzTestAccountRedactionCredential(f *testing.F) {
	for i := range credentialFuzzShapes {
		for j := range credentialFuzzFollows {
			f.Add("lang", `"}"`, int64(i*len(credentialFuzzFollows)+j), uint8(i), uint8(j))
		}
	}
	e := &Execution{cfg: Config{HeaderMode: HeaderTestAccountPassthrough}}
	f.Fuzz(func(t *testing.T, name, value string, seed int64, shape, follow uint8) {
		secret := credentialFuzzShapes[int(shape)%len(credentialFuzzShapes)].gen(rand.New(rand.NewSource(seed)))
		if !cookieFuzzValid(name, value) || strings.Contains(name+value, secret) {
			t.Skip()
		}
		credentialFuzzCheck(t, e, name, value, secret, credentialFuzzFollows[int(follow)%len(credentialFuzzFollows)])
	})
}
