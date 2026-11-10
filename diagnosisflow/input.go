package diagnosisflow

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxProjectionBytes = 48 << 10 // Leaves room for the 64KiB IPC envelope.

func normalizedMethod(method string) string {
	if len(method) > 7 {
		return "UNKNOWN"
	}
	switch strings.ToUpper(method) {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return strings.ToUpper(method)
	}
	return "UNKNOWN"
}
func canonicalPath(u *url.URL) (string, bool) {
	if u == nil || !utf8.ValidString(u.Path) || u.RawPath != "" || strings.Contains(u.Path, "%") {
		return "", false
	}
	for _, r := range u.Path {
		if unicode.IsControl(r) || r == '\\' {
			return "", false
		}
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if len(path) > 128 || !strings.HasPrefix(path, "/") || strings.Contains(path, "//") {
		return "", false
	}
	for _, p := range strings.Split(u.Path, "/") {
		if p == "." || p == ".." {
			return "", false
		}
	}
	return path, true
}
func boundedStatus(status int) int {
	if status >= 100 && status <= 599 {
		return status
	}
	return 0
}

// Only optional observation detail is trimmed. Scope, selected IDs and command
// receipts stay intact; clipping is explicit and never grants authority.
func compactProjection(s Snapshot) Snapshot {
	fits := func() bool { raw, _ := json.Marshal(s); return len(raw) <= MaxProjectionBytes }
	for !fits() {
		s.DetailTruncated = true
		switch {
		case len(s.Activity) > 0:
			s.Activity = s.Activity[:len(s.Activity)-1]
		case len(s.Plan) > 0:
			s.Plan = s.Plan[:len(s.Plan)-1]
		case len(s.Events) > 0:
			s.Events = s.Events[1:]
		default:
			changed := false
			for i := range s.Endpoints {
				e := &s.Endpoints[i]
				if e.Request != "" || e.Response != "" {
					e.Request = ""
					e.Response = ""
					changed = true
				}
			}
			if !changed {
				return s
			}
		}
	}
	return s
}
