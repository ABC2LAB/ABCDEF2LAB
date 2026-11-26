package learnedlogin

import (
	"bytes"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// LoginResult는 프로그램이 다시 보낸 로그인 응답의 판정이다. Session은 뒤 요청(E③ 신원·조회)에 실을 헤더이고, 값은 실행
// 메모리에만 있다.
type LoginResult struct {
	// Outcome은 로그인 응답만 본 결과다: 실패 신호가 있으면 failed, 세션을 받았으면 unconfirmed(대조 전)다.
	Outcome           string
	Reason            Reason
	Session           http.Header
	RedirectPath      string
	LoginFormReturned bool
	SessionNames      []string
}

func (LoginResult) GoString() string { return "learnedlogin.LoginResult[protected]" }

// CheckLogin은 로그인 응답을 §4 실패 신호와 학습한 세션 자리로 판정한다. loginPaths는 로그인 화면 경로(로그인 경로와, 세션
// 없이 받은 비밀번호 칸 있는 화면의 경로)다. jar는 로그인 요청에 실린 쿠키이고, 응답의 Set-Cookie를 담아 갱신한다.
//   - 실패 신호: 400 이상, HTML 응답의 비밀번호 칸, 로그인 화면 경로로의 같은 origin 리다이렉트.
//   - 세션: set_cookie는 배운 쿠키 이름을 이 응답이 모두 새로 줘야 하고, continued는 로그인 요청에 실린 그 쿠키가 응답 뒤에도
//     남아 있어야 한다. json_field·header는 배운 자리의 토큰 모양 값이어야 한다. 뒤 요청에 싣는 방식은 배운 session_sent_as다.
//
// 세션을 받아도 여기서는 succeeded가 아니다. 성공은 인증 대조(Contrast)가 선 뒤다(§4).
func CheckLogin(m Method, resp Response, jar Jar, origin *url.URL, loginPaths []string, tokenMinBytes int, now time.Time) LoginResult {
	out := LoginResult{Outcome: OutcomeFailed}
	out.RedirectPath = RedirectPath(origin, resp)
	out.LoginFormReturned = HasPasswordInput(resp) || out.RedirectPath != "" && containsPath(loginPaths, out.RedirectPath)
	newCookies := Jar{}
	newCookies.Absorb(resp, m.Path, origin, now)
	jar.Absorb(resp, m.Path, origin, now)
	if resp.Status >= 400 || out.LoginFormReturned {
		out.Reason = ReasonRejected
		return out
	}
	// 세션 쿠키는 확정 단계의 다른 경로 요청(신원·생성·조회)에도 실으므로, 브라우저도 모든 경로에 싣는 쿠키(유효 경로 /)만
	// 쓴다(Codex 리뷰). 좁은 경로 쿠키를 다른 경로에 실으면 브라우저의 쿠키 범위 밖 요청이 근거가 된다.
	if (m.SessionSource == SourceSetCookie || m.SessionSource == SourceContinued) && !SessionCookieScopeOK(resp, m.Path, m.SessionNames) {
		out.Reason = ReasonFormatUnsupported
		return out
	}
	session := http.Header{}
	switch m.SessionSource {
	case SourceSetCookie, SourceContinued:
		source := newCookies
		if m.SessionSource == SourceContinued {
			source = jar
		}
		parts := make([]string, 0, len(m.SessionNames))
		for _, name := range m.SessionNames {
			value := source[name]
			if value == "" {
				out.Reason = ReasonSessionNotFound
				return out
			}
			parts = append(parts, name+"="+value)
		}
		session.Set("Cookie", strings.Join(parts, "; "))
	case SourceJSONField, SourceHeader:
		header, prefix, ok := SentAsHeader(m.SessionSentAs)
		if !ok {
			out.Reason = ReasonFormatUnsupported
			return out
		}
		value := ""
		if m.SessionSource == SourceHeader {
			if values := resp.Header.Values(m.SessionNames[0]); len(values) == 1 {
				value = values[0]
			}
		} else {
			value = jsonToken(resp, m.SessionNames[0])
		}
		if !TokenShaped(value, tokenMinBytes) {
			out.Reason = ReasonSessionNotFound
			return out
		}
		session.Set(header, prefix+value)
	default:
		out.Reason = ReasonFormatUnsupported
		return out
	}
	out.Outcome, out.Session, out.SessionNames = OutcomeUnconfirmed, session, append([]string{}, m.SessionNames...)
	return out
}

// TokenShaped는 §3의 토큰 모양 값이다: 하한 이상이고 영문·숫자와 -_.=+/~:만 든 값. 공백·@·/로 시작하는 값은 세션이 아니다.
func TokenShaped(v string, min int) bool {
	if min <= 0 || len(v) < min || strings.HasPrefix(v, "/") {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-_.=+/~:", c) >= 0) {
			return false
		}
	}
	return true
}

func jsonToken(resp Response, p string) string {
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "application/json" && !strings.HasSuffix(media, "+json") {
		return ""
	}
	var doc any
	dec := json.NewDecoder(bytes.NewReader(resp.Body))
	if dec.Decode(&doc) != nil || dec.More() || !UniqueJSONKeys(resp.Body) {
		return ""
	}
	for _, segment := range strings.Split(p, ".") {
		object, ok := doc.(map[string]any)
		if !ok {
			return ""
		}
		doc = object[segment]
	}
	v, _ := doc.(string)
	return v
}

// HasPasswordInput은 HTML 응답에 type="password" 입력 칸이 있는지다(§4 로그인 화면 신호).
func HasPasswordInput(resp Response) bool {
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "text/html" && media != "application/xhtml+xml" || len(resp.Body) == 0 {
		return false
	}
	return parseHTMLScreen(resp.Body).LoginForm
}

// RedirectPath는 같은 origin으로 가는 3xx의 Location 경로다. 다른 origin이거나 3xx가 아니면 빈 값이다.
func RedirectPath(origin *url.URL, resp Response) string {
	if resp.Status < 300 || resp.Status > 399 {
		return ""
	}
	loc := resp.Header.Get("Location")
	u, err := url.Parse(loc)
	if loc == "" || err != nil {
		return ""
	}
	if u.Host != "" || u.Scheme != "" {
		if origin == nil || !strings.EqualFold(u.Host, origin.Host) || u.Scheme != "" && !strings.EqualFold(u.Scheme, origin.Scheme) {
			return ""
		}
	}
	p := u.EscapedPath()
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		// 상대 경로는 이 판정에서 로그인 화면 경로와 비교하지 않는다(모르면 로그인 화면으로 보지 않음 = 실패로 올리지 않음).
		return ""
	}
	clean := path.Clean(p)
	return clean
}

func containsPath(paths []string, p string) bool {
	for _, candidate := range paths {
		if candidate == p {
			return true
		}
	}
	return false
}

// Contrast는 같은 URL(질의 없는 같은 경로)의 세션 없는 응답과 이 세션을 실은 응답으로 인증 대조를 본다(§4 용어).
//   - 막힘: 401·403이거나 3xx이고 Location 경로가 로그인 화면 경로다.
//   - 열림: 400 미만이고 로그인 화면 경로로 리다이렉트되지 않았다.
//
// 세션 없는 응답이 막힘이고 세션 응답이 열림이면 authenticated_contrast다. 둘 중 하나라도 없으면 대조가 아니다.
func Blocked(status int, redirect string, loginPaths []string) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden ||
		status >= 300 && status <= 399 && redirect != "" && containsPath(loginPaths, redirect)
}

// Open은 §4의 열림이다.
func Open(status int, redirect string, loginPaths []string) bool {
	return status > 0 && status < 400 && !(redirect != "" && containsPath(loginPaths, redirect))
}

// LoginPaths는 로그인 화면 경로 목록이다: 로그인 경로, 그리고 세션 없이 받은 로그인 화면(비밀번호 칸)의 경로.
func LoginPaths(m Method, screenPath string, screen Screen) []string {
	out := []string{m.Path}
	if screenPath != "" && screenPath != m.Path && screen.LoginForm {
		out = append(out, screenPath)
	}
	return out
}

// SessionCookieScopeOK는 응답이 준 세션 이름 쿠키의 유효 경로가 모두 "/"인지 본다. Path 속성이 없으면 요청 경로의 기본
// 경로(RFC 6265 5.1.4)다. 응답이 주지 않은 이름은 보지 않는다(있는지는 세션 판정이 따로 본다).
func SessionCookieScopeOK(resp Response, requestPath string, names []string) bool {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	for _, c := range (&http.Response{Header: resp.Header}).Cookies() {
		if !want[c.Name] {
			continue
		}
		scope := c.Path
		if scope == "" || scope[0] != '/' {
			scope = defaultCookiePath(requestPath)
		}
		if scope != "/" {
			return false
		}
	}
	return true
}

// defaultCookiePath는 RFC 6265 5.1.4 기본 경로다.
func defaultCookiePath(requestPath string) string {
	if requestPath == "" || requestPath[0] != '/' {
		return "/"
	}
	i := strings.LastIndex(requestPath, "/")
	if i <= 0 {
		return "/"
	}
	return requestPath[:i]
}
