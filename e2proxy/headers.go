package e2proxy

import (
	"bytes"
	"net/http"
	"strings"
)

func headerPresent(headers http.Header, name string) bool {
	for key := range headers {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c) {
			continue
		}
		return false
	}
	return true
}

func framingHeader(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "proxy-") || reservedAttributionHeader(name) {
		return true
	}
	switch lower {
	case "host", "connection", "keep-alive", "te", "trailer", "transfer-encoding", "upgrade", "content-length", "expect":
		return true
	}
	return false
}

func forbiddenCredentialHeader(name string) bool {
	lower := strings.ToLower(name)
	return framingHeader(name) || strings.HasPrefix(lower, "content-") || strings.HasPrefix(lower, "accept") || strings.HasPrefix(lower, "if-") ||
		strings.HasPrefix(lower, "x-forwarded-") || lower == "range" || lower == "user-agent" || lower == "origin" || lower == "referer" || lower == "via" || lower == "forwarded"
}

func cloneHeaderClass(source http.Header, credential bool) (http.Header, error) {
	result := make(http.Header, len(source))
	for name, values := range source {
		if !validHeaderName(name) || framingHeader(name) || len(values) == 0 || credential && forbiddenCredentialHeader(name) ||
			!credential && (strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Cookie")) {
			return nil, ErrConfig
		}
		canonical := http.CanonicalHeaderKey(name)
		if _, duplicate := result[canonical]; duplicate {
			return nil, ErrConfig
		}
		for _, value := range values {
			if credential && (value == "" || strings.Trim(value, " \t") != value) {
				return nil, ErrConfig
			}
			for _, c := range value {
				if c < 32 && c != '\t' || c == 127 {
					return nil, ErrConfig
				}
			}
		}
		result[canonical] = append([]string(nil), values...)
	}
	return result, nil
}

func validateRuleHeaders(rule Rule) (Rule, error) {
	ordinary, err := cloneHeaderClass(rule.Headers, false)
	if err != nil || !identityEncoding(ordinary, "Accept-Encoding") {
		return Rule{}, ErrConfig
	}
	credentials, err := cloneHeaderClass(rule.CredentialHeaders, true)
	if err != nil {
		return Rule{}, err
	}
	for name := range credentials {
		if _, overlap := ordinary[name]; overlap {
			return Rule{}, ErrConfig
		}
	}
	rule.Headers, rule.CredentialHeaders = ordinary, credentials
	return rule, nil
}

// Ordinary headers are constraints on the caller's request, not value suppliers.
// Credential collisions are rejected before any removal or proposal injection.
func forwardRequestHeaders(source http.Header, rule compiledRule) (http.Header, bool) {
	for name := range rule.CredentialHeaders {
		if headerPresent(source, name) {
			return nil, false
		}
	}
	out := make(http.Header, len(source)+1)
	for name, values := range source {
		if !validHeaderName(name) {
			return nil, false
		}
		canonical := http.CanonicalHeaderKey(name)
		if _, duplicate := out[canonical]; duplicate {
			return nil, false
		}
		out[canonical] = append([]string(nil), values...)
	}
	if values, present := out["User-Agent"]; present && (len(values) != 1 || strings.TrimSpace(values[0]) == "") {
		return nil, false
	}
	stripHeaders(out)
	for name, expected := range rule.Headers {
		actual, present := out[name]
		if !present || len(actual) != len(expected) {
			return nil, false
		}
		for i := range expected {
			if actual[i] != expected[i] {
				return nil, false
			}
		}
	}
	// net/http otherwise manufactures Go-http-client/1.1 for an absent field.
	// A nil slice suppresses the default without writing an empty wire header.
	if !headerPresent(out, "User-Agent") {
		out["User-Agent"] = nil
	}
	return out, true
}

// identityAcceptEncoding은 호출자가 보낸 압축 협상을 identity 하나로 바꾼다. 압축 응답은 E2가 본문의 자격 반사·상한을
// 검사할 수 없어서 대상에 압축하지 말라고 요청한다. 내용은 같고 압축만 빠지므로 요청을 거부하지 않는다(C15).
// 호출자가 보내지 않았으면 새로 넣지 않는다. 대상이 그래도 압축해 보내면 safeHTTPResponse가 결과 불명으로 막는다.
func identityAcceptEncoding(headers http.Header) {
	if _, present := headers["Accept-Encoding"]; present {
		headers["Accept-Encoding"] = []string{"identity"}
	}
}

func credentialHeaderNames(rules []compiledRule) map[string]struct{} {
	out := map[string]struct{}{}
	for _, rule := range rules {
		for name := range rule.CredentialHeaders {
			out[strings.ToLower(name)] = struct{}{}
		}
	}
	return out
}

func credentialResponseHeader(name string) bool {
	switch strings.ToLower(name) {
	case "set-cookie", "set-cookie2", "www-authenticate", "authentication-info", "proxy-authenticate", "proxy-authentication-info", "authorization", "cookie", "proxy-authorization":
		return true
	}
	return false
}

// This is a bounded known-value guard, not universal encoded-data DLP. Unsafe
// representations are withheld whole; a successful HTTP response is never edited.
func (e *Execution) safeHTTPResponse(response *http.Response, body []byte) bool {
	if response == nil || !identityEncoding(response.Header, "Content-Encoding") || !identityEncoding(response.Trailer, "Content-Encoding") || response.Uncompressed {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, fields := range []http.Header{response.Header, response.Trailer} {
		for name := range fields {
			_, configured := e.secretHeaders[strings.ToLower(name)]
			// 시험 계정 실행은 대상이 발급한 자격 응답(Set-Cookie 등)도 모델에 준다(C14 결정 4). 등록한 보호 값 반사 검사는 아래서 그대로 한다.
			if configured || credentialResponseHeader(name) && !e.testAccountPassthrough() {
				return false
			}
		}
	}
	for _, secret := range e.secrets {
		if len(secret) == 0 {
			continue
		}
		value := string(secret)
		if bytes.Contains(body, secret) || strings.Contains(response.Status, value) || strings.Contains(response.Proto, value) {
			return false
		}
		for _, fields := range []http.Header{response.Header, response.Trailer} {
			for name, values := range fields {
				if strings.Contains(strings.ToLower(name), strings.ToLower(value)) {
					return false
				}
				for _, field := range values {
					if strings.Contains(field, value) {
						return false
					}
				}
			}
		}
	}
	// 시험 계정 실행은 대상이 새로 발급한 세션(Set-Cookie 회전 등)도 모델에 준다. 그 값은 도구 출력·근거·활동 기록에서
	// 가리도록 응답을 넘기기 전에 출력 가림 목록에 더한다(S09: 모델 입력만 예외, #522 이동찬 리뷰). 가릴 자리가 모자라면
	// 기록에 남기지 않도록 응답을 통째로 보류한다.
	if e.testAccountPassthrough() {
		issued := issuedSessionValues(response)
		fresh := make([][]byte, 0, len(issued))
		for _, v := range issued {
			known := false
			for _, old := range e.redactOnly {
				known = known || bytes.Equal(old, v)
			}
			if !known {
				fresh = append(fresh, v)
			}
		}
		if len(e.redactOnly)+len(fresh) > maxIssuedRedactValues {
			return false
		}
		e.redactOnly = append(e.redactOnly, fresh...)
	}
	return true
}

// maxIssuedRedactValues는 실행 하나가 가릴 수 있는 세션 값 수의 상한이다(인증 준비 세션 + 대상이 새로 발급한 세션).
// 세션을 요청마다 회전하는 대상도 회차 하나는 버티게 넉넉히 두고, 넘으면 그 응답을 보류한다.
const maxIssuedRedactValues = 1024

// issuedSessionValues는 응답이 새로 발급한 자격 값이다: Set-Cookie·Set-Cookie2의 쿠키 값, Authentication-Info·
// Proxy-Authentication-Info 값 전체. 가림 하한(8바이트)보다 짧은 값은 세션 값으로 보지 않는다(lang=en 같은 값까지
// 가리면 출력이 망가진다). 같은 값은 한 번만 낸다.
func issuedSessionValues(response *http.Response) [][]byte {
	seen := map[string]bool{}
	var out [][]byte
	add := func(v string) {
		if len(v) >= minRedactValueBytes && len(v) <= maxRedactValueBytes && !seen[v] {
			seen[v] = true
			out = append(out, []byte(v))
		}
	}
	for _, fields := range []http.Header{response.Header, response.Trailer} {
		for name, values := range fields {
			switch strings.ToLower(name) {
			case "set-cookie", "set-cookie2":
				for _, line := range values {
					if c, err := http.ParseSetCookie(line); err == nil {
						add(c.Value)
					} else if _, rest, ok := strings.Cut(line, "="); ok {
						// 형식이 어긋난 줄도 값 자리는 가린다(파싱 실패를 이유로 값을 기록에 남기지 않는다).
						v, _, _ := strings.Cut(rest, ";")
						add(strings.TrimSpace(v))
					}
				}
			case "authentication-info", "proxy-authentication-info":
				for _, v := range values {
					add(strings.TrimSpace(v))
				}
			}
		}
	}
	return out
}

func (Config) String() string   { return "e2proxy.Config[protected]" }
func (Config) GoString() string { return "e2proxy.Config[protected]" }
func (Rule) String() string     { return "e2proxy.Rule[protected]" }
func (Rule) GoString() string   { return "e2proxy.Rule[protected]" }

func connectionUpgrade(headers http.Header) bool {
	for name, values := range headers {
		if strings.EqualFold(name, "Connection") {
			for _, value := range values {
				if strings.Contains(strings.ToLower(value), "upgrade") {
					return true
				}
			}
		}
	}
	return false
}
