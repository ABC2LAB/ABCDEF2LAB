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
			if configured || credentialResponseHeader(name) {
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
	return true
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
