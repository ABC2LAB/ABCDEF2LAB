package learnedlogin

import (
	"bytes"
	"encoding/json"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// 로그인 화면에서 받는 값의 상한이다. CSRF 토큰·폼 기본값은 짧다. 넘거나 제어 문자가 든 값은 쓰지 않는다.
const maxPrepValueBytes = 4096

// Response는 프로그램이 받은 대상 응답의 판정용 사본이다. Body는 E② 응답 상한 안에서 끝까지 받은 본문이다(잘렸으면
// E②가 판정 전에 보류한다). 호출자가 판정 뒤 지운다.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Jar는 계정 하나의 로그인 문맥 쿠키다(이름 → 값). 계정마다 따로 만들고 확정 단계 다시 로그인 한 번에만 쓴다. 다른 계정
// 세션과 섞지 않는다.
type Jar map[string]string

func (Jar) String() string   { return "learnedlogin.Jar[protected]" }
func (Jar) GoString() string { return "learnedlogin.Jar[protected]" }

// Absorb는 응답의 Set-Cookie를 담는다. 브라우저가 그 쿠키를 로그인 요청에 싣지 않을 것은 담지 않는다: 쿠키 Path가 로그인
// 경로를 덮지 않거나, Domain이 대상 호스트를 덮지 않거나, https가 아닌 origin의 Secure 쿠키. 만료·삭제 쿠키는 지운다. 한
// 응답이 같은 이름을 다른 값으로 둘 이상 주면 어느 것을 쓸지 모르므로 그 이름은 담지 않는다(Codex 2차 리뷰).
func (j Jar) Absorb(resp Response, loginPath string, origin *url.URL, now time.Time) {
	seen := map[string]string{}
	conflict := map[string]bool{}
	for _, c := range (&http.Response{Header: resp.Header}).Cookies() {
		if !validCookieName(c.Name) || c.Path != "" && !cookiePathMatch(loginPath, c.Path) || !cookieDomainMatch(origin, c.Domain) ||
			c.Secure && (origin == nil || origin.Scheme != "https") {
			continue
		}
		if prev, ok := seen[c.Name]; ok && prev != c.Value {
			conflict[c.Name] = true
		}
		seen[c.Name] = c.Value
		if c.Value == "" || c.MaxAge < 0 || !c.Expires.IsZero() && !c.Expires.After(now) {
			delete(j, c.Name)
			continue
		}
		j[c.Name] = c.Value
	}
	for name := range conflict {
		delete(j, name)
	}
}

// cookieDomainMatch는 RFC 6265 5.1.3 도메인 일치다. Domain이 없으면 응답 호스트 전용 쿠키다.
func cookieDomainMatch(origin *url.URL, domain string) bool {
	if domain == "" {
		return true
	}
	if origin == nil {
		return false
	}
	host := strings.ToLower(origin.Hostname())
	domain = strings.ToLower(strings.TrimPrefix(domain, "."))
	return domain != "" && (host == domain || strings.HasSuffix(host, "."+domain) && net.ParseIP(host) == nil)
}

// Header는 로그인 요청에 실을 Cookie 헤더 값이다(이름순). 비었으면 빈 값이다.
func (j Jar) Header() string {
	names := make([]string, 0, len(j))
	for name := range j {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+j[name])
	}
	return strings.Join(parts, "; ")
}

// cookiePathMatch는 RFC 6265 5.1.4 경로 일치다.
func cookiePathMatch(requestPath, cookiePath string) bool {
	if requestPath == cookiePath {
		return true
	}
	if !strings.HasPrefix(requestPath, cookiePath) {
		return false
	}
	return strings.HasSuffix(cookiePath, "/") || requestPath[len(cookiePath)] == '/'
}

// Screen은 로그인 화면(준비 요청) 응답에서 읽은 것이다.
type Screen struct {
	// LoginForm은 HTML 응답에 type="password" 입력 칸이 있는지다(§4 로그인 화면).
	LoginForm bool
	html      bool
	inputs    map[string][]formInput
	json      any
}

type formInput struct {
	kind     string
	value    string
	hasValue bool
}

// ParseScreen은 로그인 화면 응답을 읽는다. 2xx가 아니거나 HTML·JSON이 아니면 화면이 아니다(ReasonPrepUnavailable).
func ParseScreen(resp Response) (Screen, Reason) {
	if resp.Status < 200 || resp.Status > 299 {
		return Screen{}, ReasonPrepUnavailable
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return Screen{}, ReasonPrepUnavailable
	}
	switch media {
	case "text/html", "application/xhtml+xml":
		return parseHTMLScreen(resp.Body), ""
	case "application/json":
		var doc any
		dec := json.NewDecoder(bytes.NewReader(resp.Body))
		dec.UseNumber()
		// 같은 키가 둘이면 뒤 값이 앞 값을 덮어 어느 값이 맞는지 모른다(Codex 2차 리뷰).
		if dec.Decode(&doc) != nil || dec.More() || !UniqueJSONKeys(resp.Body) {
			return Screen{}, ReasonPrepUnavailable
		}
		return Screen{json: doc}, ""
	}
	return Screen{}, ReasonPrepUnavailable
}

func parseHTMLScreen(body []byte) Screen {
	s := Screen{html: true, inputs: map[string][]formInput{}}
	z := html.NewTokenizer(bytes.NewReader(body))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return s
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		name, hasAttr := z.TagName()
		tag := string(name)
		if tag != "input" && tag != "button" || !hasAttr {
			continue
		}
		in := formInput{kind: "text"}
		if tag == "button" {
			in.kind = "submit"
		}
		field := ""
		for {
			key, val, more := z.TagAttr()
			switch string(key) {
			case "name":
				field = string(val)
			case "type":
				in.kind = strings.ToLower(strings.TrimSpace(string(val)))
			case "value":
				in.value, in.hasValue = string(val), true
			}
			if !more {
				break
			}
		}
		if tag == "input" && in.kind == "password" {
			s.LoginForm = true
		}
		if field != "" {
			s.inputs[field] = append(s.inputs[field], in)
		}
	}
}

// Values는 방법의 나머지 칸 값을 로그인 화면에서 꺼낸다. 응답에서 받는 칸(fields_from_response)은 HTML 입력 칸의 값이나
// JSON 응답의 같은 점 경로 문자열이다. 값이 기록되지 않은 다른 칸은 HTML 로그인 화면의 숨은 칸·제출 단추에 적힌 값만 쓴다
// (브라우저가 그대로 보내는 값). 사람이 입력하는 칸·체크 칸 값은 모르므로 재현하지 않는다. 같은 이름의 값이 둘 이상 다르면
// 모호해 쓰지 않는다. HTML에서 칸을 꺼내려면 그 화면이 로그인 화면(비밀번호 칸 있음)이어야 한다.
func (s Screen) Values(m Method) (map[string]string, Reason) {
	out := map[string]string{}
	if len(m.OtherFields) == 0 {
		return out, ""
	}
	if s.html && !s.LoginForm {
		return nil, ReasonPrepUnavailable
	}
	for _, name := range m.OtherFields {
		response := m.fromResponse(name)
		var value string
		var reason Reason
		switch {
		case s.html:
			value, reason = s.htmlValue(name, response)
		case s.json != nil && response:
			value, reason = jsonValue(s.json, name)
		case response:
			reason = ReasonPrepFieldMissing
		default:
			reason = ReasonInputUnavailable
		}
		if reason != "" {
			return nil, reason
		}
		out[name] = value
	}
	return out, ""
}

func (s Screen) htmlValue(name string, response bool) (string, Reason) {
	missing := ReasonInputUnavailable
	if response {
		missing = ReasonPrepFieldMissing
	}
	inputs := s.inputs[name]
	value, found := "", false
	for _, in := range inputs {
		if !in.hasValue {
			continue
		}
		// 기록되지 않은 칸은 사용자가 바꾸지 않는 칸(숨은 칸·제출 단추)만 받는다.
		if !response && in.kind != "hidden" && in.kind != "submit" {
			return "", ReasonInputUnavailable
		}
		if found && in.value != value {
			return "", missing
		}
		value, found = in.value, true
	}
	if !found || response && value == "" || !prepValueOK(value) {
		return "", missing
	}
	return value, ""
}

func jsonValue(doc any, name string) (string, Reason) {
	for _, segment := range strings.Split(name, ".") {
		object, ok := doc.(map[string]any)
		if !ok {
			return "", ReasonPrepFieldMissing
		}
		doc = object[segment]
	}
	value, ok := doc.(string)
	if !ok || value == "" || !prepValueOK(value) {
		return "", ReasonPrepFieldMissing
	}
	return value, ""
}

func prepValueOK(v string) bool {
	if len(v) > maxPrepValueBytes || !utf8.ValidString(v) {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// UniqueJSONKeys는 JSON 문서의 어느 객체에도 같은 키가 둘 이상 없는지 본다(중첩 포함). 해석할 수 없으면 거짓이다.
func UniqueJSONKeys(raw []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	return uniqueJSONValue(dec, 0) && !dec.More()
}

// maxJSONDepth는 중복 키 검사가 따라가는 객체·배열 깊이 상한이다. 로그인 화면·로그인 응답은 얕다.
const maxJSONDepth = 32

func uniqueJSONValue(dec *json.Decoder, depth int) bool {
	if depth > maxJSONDepth {
		return false
	}
	tok, err := dec.Token()
	if err != nil {
		return false
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return true
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] {
				return false
			}
			seen[name] = true
			if !uniqueJSONValue(dec, depth+1) {
				return false
			}
		}
	case '[':
		for dec.More() {
			if !uniqueJSONValue(dec, depth+1) {
				return false
			}
		}
	default:
		return false
	}
	_, err = dec.Token()
	return err == nil
}
