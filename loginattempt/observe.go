package loginattempt

import (
	"bytes"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

// Request는 경계가 대상에 보내려는 요청이다. Path·RawQuery는 승인 origin 기준이다.
type Request struct {
	Method      string
	Path        string
	RawQuery    string
	ContentType string
	Body        []byte
	Header      http.Header
}

// Response는 대상 응답이다. Body는 경계가 정한 상한까지의 앞부분이고, 없어도 된다(그때 HTML·JSON 신호는 없음으로 본다).
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

var passwordInput = regexp.MustCompile(`(?i)<input\b[^>]*\btype\s*=\s*["']?password\b`)

// hasPasswordInput은 응답이 HTML이고 type="password" 입력 칸이 있는지 본다(§4 login_form_returned·로그인 화면 경로).
func hasPasswordInput(resp Response) bool {
	if len(resp.Body) == 0 {
		return false
	}
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if media != "text/html" && media != "application/xhtml+xml" {
		return false
	}
	return passwordInput.Match(resp.Body)
}

// redirectPath는 같은 origin으로 가는 3xx의 Location 경로다. 다른 origin이거나 3xx가 아니면 빈 값이다.
func redirectPath(origin *url.URL, requestPath string, resp Response) string {
	if resp.Status < 300 || resp.Status > 399 {
		return ""
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return ""
	}
	u, err := url.Parse(loc)
	if err != nil {
		return ""
	}
	if u.Host != "" {
		if origin == nil || !strings.EqualFold(u.Host, origin.Host) || u.Scheme != "" && !strings.EqualFold(u.Scheme, origin.Scheme) {
			return ""
		}
		return cleanPath(u.EscapedPath())
	}
	if u.Scheme != "" {
		return ""
	}
	if strings.HasPrefix(u.EscapedPath(), "/") {
		return cleanPath(u.EscapedPath())
	}
	base := requestPath
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[:i+1]
	}
	return cleanPath(base + u.EscapedPath())
}

func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	c := path.Clean(p)
	if strings.HasSuffix(p, "/") && c != "/" {
		c += "/"
	}
	return c
}

// newCookies는 Set-Cookie가 새로 준 세션 후보다. 지우는 쿠키(빈 값·지난 만료)는 세지 않는다.
func newCookies(resp Response, now time.Time) map[string]string {
	out := map[string]string{}
	for _, c := range (&http.Response{Header: resp.Header}).Cookies() {
		if c.Value == "" || c.MaxAge < 0 || !c.Expires.IsZero() && c.Expires.Before(now) {
			continue
		}
		out[c.Name] = c.Value
	}
	return out
}

// requestCookies는 요청의 Cookie 헤더 이름·값이다.
func requestCookies(h http.Header) map[string]string {
	out := map[string]string{}
	for _, c := range (&http.Request{Header: h}).Cookies() {
		out[c.Name] = c.Value
	}
	return out
}

// tokenLike는 세션 토큰 후보 값인지 본다. 사람이 읽는 메시지(공백)·메일 주소(@)·경로는 뺀다. 계약 밖 해석이라
// 길이 하한은 설정값으로 둔다.
func tokenLike(v string, minBytes int) bool {
	if len(v) < minBytes || len(v) > maxTokenBytes || strings.HasPrefix(v, "/") || strings.HasPrefix(strings.ToLower(v), "http") {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("-_.=+/~:", r):
		default:
			return false
		}
	}
	return true
}

const maxTokenBytes = 8192

type token struct {
	source string // json_field·header
	name   string
	value  string
}

// jsonTokens는 JSON 응답 본문의 토큰 후보 문자열을 점 경로와 함께 모은다.
func jsonTokens(body []byte, minBytes int) []token {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var root map[string]any
	if json.Unmarshal(trimmed, &root) != nil {
		return nil
	}
	var out []token
	var walk func(prefix string, v any, depth int)
	walk = func(prefix string, v any, depth int) {
		if depth > maxJSONDepth || len(out) >= maxSessionNames {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				name := k
				if prefix != "" {
					name = prefix + "." + k
				}
				walk(name, child, depth+1)
			}
		case string:
			if tokenLike(x, minBytes) {
				out = append(out, token{source: SourceJSONField, name: prefix, value: x})
			}
		}
	}
	walk("", root, 0)
	return out
}

// 응답 헤더 가운데 세션 토큰으로 보지 않는 표준 헤더다.
var standardHeaders = map[string]bool{
	"Date": true, "Server": true, "Content-Type": true, "Content-Length": true, "Content-Encoding": true, "Content-Language": true,
	"Content-Security-Policy": true, "Cache-Control": true, "Expires": true, "Pragma": true, "Etag": true, "Last-Modified": true,
	"Vary": true, "Set-Cookie": true, "Location": true, "Strict-Transport-Security": true, "X-Powered-By": true,
	"X-Content-Type-Options": true, "X-Frame-Options": true, "Access-Control-Allow-Origin": true, "Access-Control-Allow-Headers": true,
	"Access-Control-Allow-Methods": true, "Access-Control-Expose-Headers": true, "Feature-Policy": true, "Permissions-Policy": true,
	"Referrer-Policy": true, "Connection": true, "Keep-Alive": true, "Transfer-Encoding": true, "Age": true, "Accept-Ranges": true,
	"Report-To": true, "Nel": true, "Alt-Svc": true, "Via": true, "X-Request-Id": true, "X-Correlation-Id": true,
}

func headerTokens(h http.Header, minBytes int) []token {
	var out []token
	for name, values := range h {
		if standardHeaders[http.CanonicalHeaderKey(name)] {
			continue
		}
		for _, v := range values {
			if tokenLike(strings.TrimSpace(v), minBytes) && len(out) < maxSessionNames {
				out = append(out, token{source: SourceHeader, name: http.CanonicalHeaderKey(name), value: strings.TrimSpace(v)})
			}
		}
	}
	return out
}
