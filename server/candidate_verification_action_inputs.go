package server

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// The first structured request step accepts only short, noncredential read
// inputs. These fields are review data, not an authority to send. The exact
// canonical query and header values are additionally pinned in the E² rule.
var selectedQueryKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,31}$`)
var selectedQueryValue = regexp.MustCompile(`^[A-Za-z0-9._~-]{0,32}$`)
var selectedLanguage = regexp.MustCompile(`^[A-Za-z]{2,8}(?:-[A-Za-z]{2,8})?$`)

func validSelectedActionInputs(query, headers map[string]string) bool {
	if len(query) > 4 || len(headers) > 3 {
		return false
	}
	for key, value := range query {
		lower := strings.ToLower(key)
		if !selectedQueryKey.MatchString(key) || !selectedQueryValue.MatchString(value) ||
			strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") ||
			strings.Contains(lower, "session") || strings.Contains(lower, "auth") || strings.Contains(lower, "csrf") ||
			strings.Contains(lower, "key") || strings.Contains(lower, "cookie") {
			return false
		}
	}
	for key, value := range headers {
		switch key {
		case "Accept":
			if value != "text/html" && value != "application/json" && value != "*/*" {
				return false
			}
		case "Accept-Language":
			if !selectedLanguage.MatchString(value) {
				return false
			}
		case "X-Requested-With":
			if value != "XMLHttpRequest" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func selectedActionQuery(query map[string]string) string {
	values := url.Values{}
	for key, value := range query {
		values.Set(key, value)
	}
	return values.Encode()
}

func CandidateActionQuery(query map[string]string) string { return selectedActionQuery(query) }

func validSelectedPolicyQuery(raw string) bool {
	if raw == "" {
		return true
	}
	if len(raw) > 512 {
		return false
	}
	values, err := url.ParseQuery(raw)
	if err != nil || values.Encode() != raw || len(values) == 0 {
		return false
	}
	flat := make(map[string]string, len(values))
	for key, value := range values {
		if len(value) != 1 {
			return false
		}
		flat[key] = value[0]
	}
	return validSelectedActionInputs(flat, nil)
}

func selectedActionHeaders(headers map[string]string) http.Header {
	out := make(http.Header, len(headers))
	for key, value := range headers {
		out.Set(key, value)
	}
	return out
}
