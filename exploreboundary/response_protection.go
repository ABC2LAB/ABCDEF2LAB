package exploreboundary

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const responseWithheld = "response_withheld"

var responseEscapes = regexp.MustCompile(`%[0-9a-fA-F]{2}|\\u[0-9a-fA-F]{4}`)
var plainCredential = regexp.MustCompile(`(?im)(?:^|[?&])(?:password|passwd|pwd|private_key)[ \t]*[=:][ \t]*[^\s&"'<>{},;]+`)

func normalizeProtectionText(text string) string {
	// 정규화된 문자열은 검사에만 쓰고 실제 응답에는 사용하지 않는다.
	return responseEscapes.ReplaceAllStringFunc(html.UnescapeString(text), func(s string) string {
		if s[0] == '%' {
			out, _ := url.PathUnescape(s)
			return out
		}
		value, _ := strconv.ParseUint(s[2:], 16, 16)
		return string(rune(value))
	})
}

func passwordField(key string) bool {
	switch strings.ToLower(key) {
	case "password", "passwd", "pwd", "client_secret", "private_key":
		return true
	}
	return false
}

func visitPasswords(v any, visit func(string) bool) bool {
	switch value := v.(type) {
	case map[string]any:
		for k, item := range value {
			if s, ok := item.(string); ok && passwordField(k) && s != "" && visit(s) {
				return true
			}
			if visitPasswords(item, visit) {
				return true
			}
		}
	case []any:
		for _, item := range value {
			if visitPasswords(item, visit) {
				return true
			}
		}
	}
	return false
}

// requestProtectionValues는 이 전송에 이미 실린 인증값만 반사 검사에 쓴다. 원문은 기록하거나 세션에 보관하지 않는다.
func requestProtectionValues(req *http.Request, body []byte) []string {
	var values []string
	add := func(value string) bool {
		if len(value) >= 6 && len(value) <= 8192 && len(values) < 32 {
			values = append(values, value)
		}
		return false
	}
	if auth := strings.Fields(req.Header.Get("Authorization")); len(auth) == 2 {
		add(auth[1])
	}
	for _, cookie := range req.Cookies() {
		name := strings.ToLower(cookie.Name)
		if strings.Contains(name, "session") || strings.Contains(name, "token") || strings.Contains(name, "auth") || name == "sid" {
			add(cookie.Value)
		}
	}
	switch mediaType(req.Header.Get("Content-Type")) {
	case "application/json":
		var value any
		if json.Unmarshal(body, &value) == nil {
			visitPasswords(value, add)
		}
	case "application/x-www-form-urlencoded":
		fields, _ := url.ParseQuery(string(body))
		for key, items := range fields {
			if passwordField(key) {
				for _, item := range items {
					add(item)
				}
			}
		}
	}
	return values
}

func responseContainsProtectedValue(text string, values []string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func responseMayContainCredential(resp *http.Response, body []byte, maxBytes int64, protected []string) bool {
	for key, values := range resp.Header {
		if strings.EqualFold(key, "Set-Cookie") {
			// 브라우저 쿠키 저장소는 세션 발급을 계속 처리한다. 직접 입구에서는 이 헤더를 제거한다.
			continue
		}
		for _, value := range values {
			decoded := normalizeProtectionText(value)
			if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Proxy-Authorization") ||
				responseContainsProtectedValue(value, protected) || responseContainsProtectedValue(decoded, protected) ||
				plainCredential.MatchString(decoded) {
				return true
			}
		}
	}
	if len(body) == 0 {
		return false
	}
	var decoded io.ReadCloser
	var err error
	var encoding string
	for _, field := range strings.Split(strings.Join(resp.Header.Values("Content-Encoding"), ","), ",") {
		field = strings.ToLower(strings.TrimSpace(field))
		if field == "" || field == "identity" {
			continue
		}
		if encoding != "" {
			return true
		}
		encoding = field
	}
	switch encoding {
	case "":
	case "gzip":
		decoded, err = gzip.NewReader(bytes.NewReader(body))
	case "deflate":
		decoded, err = zlib.NewReader(bytes.NewReader(body))
	default:
		return true
	}
	if err != nil {
		return true
	}
	if decoded != nil {
		defer decoded.Close()
		body, err = io.ReadAll(io.LimitReader(decoded, maxBytes+1))
		defer clear(body)
		if err != nil || int64(len(body)) > maxBytes {
			return true
		}
	}
	text := string(body)
	normalized := normalizeProtectionText(text)
	if responseContainsProtectedValue(text, protected) || responseContainsProtectedValue(normalized, protected) {
		return true
	}
	if first := strings.TrimSpace(text); strings.HasPrefix(first, "{") || strings.HasPrefix(first, "[") {
		var value any
		if json.Unmarshal(body, &value) == nil {
			return visitPasswords(value, func(string) bool { return true })
		}
	}
	media := mediaType(resp.Header.Get("Content-Type"))
	return (media == "" || media == "text/plain" || media == "application/x-www-form-urlencoded") &&
		plainCredential.MatchString(normalized)
}
