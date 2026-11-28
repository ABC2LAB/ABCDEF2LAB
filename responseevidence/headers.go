package responseevidence

import (
	"net/http"
	"sort"
	"strings"
)

// HeaderStructure는 응답 헤더의 보안 관련 사실만 담는다. 헤더 값·쿠키 이름·쿠키 값·Origin 값은 없다.
type HeaderStructure struct {
	// Present는 고정 목록 중 응답에 있던 헤더 이름이다(정렬).
	Present []string `json:"present,omitempty"`
	// Repeated는 같은 이름이 둘 이상 온 헤더다(해석이 갈릴 수 있어 비교 근거로 표시).
	Repeated           []string `json:"repeated,omitempty"`
	ContentTypeOptions string   `json:"content_type_options,omitempty"` // nosniff|other
	FrameOptions       string   `json:"frame_options,omitempty"`        // deny|sameorigin|other
	CSPFrameAncestors  bool     `json:"csp_frame_ancestors,omitempty"`
	CacheNoStore       bool     `json:"cache_no_store,omitempty"`
	CORSAllowOrigin    string   `json:"cors_allow_origin,omitempty"` // wildcard|null|specific
	CORSCredentials    bool     `json:"cors_credentials,omitempty"`
	Cookies            *Cookies `json:"cookies,omitempty"`
}

// Cookies는 Set-Cookie 속성의 개수만 센다.
type Cookies struct {
	Count           int `json:"count"`
	Secure          int `json:"secure"`
	HTTPOnly        int `json:"http_only"`
	SameSiteStrict  int `json:"same_site_strict,omitempty"`
	SameSiteLax     int `json:"same_site_lax,omitempty"`
	SameSiteNone    int `json:"same_site_none,omitempty"`
	SameSiteMissing int `json:"same_site_missing,omitempty"`
}

var securityHeaders = []string{"Access-Control-Allow-Credentials", "Access-Control-Allow-Origin", "Cache-Control",
	"Content-Security-Policy", "Content-Security-Policy-Report-Only", "Cross-Origin-Embedder-Policy", "Cross-Origin-Opener-Policy",
	"Cross-Origin-Resource-Policy", "Permissions-Policy", "Referrer-Policy", "Server", "Set-Cookie", "Strict-Transport-Security",
	"WWW-Authenticate", "X-Content-Type-Options", "X-Frame-Options", "X-Powered-By", "X-XSS-Protection"}

var securityHeaderSet = func() map[string]bool {
	m := map[string]bool{}
	for _, h := range securityHeaders {
		m[h] = true
	}
	return m
}()

func extractHeaders(h http.Header) *HeaderStructure {
	if h == nil {
		return nil
	}
	out := &HeaderStructure{}
	for _, name := range securityHeaders {
		values := h.Values(name)
		if len(values) == 0 {
			continue
		}
		out.Present = append(out.Present, name)
		if len(values) > 1 && name != "Set-Cookie" {
			out.Repeated = append(out.Repeated, name)
		}
	}
	if v := h.Values("X-Content-Type-Options"); len(v) == 1 {
		out.ContentTypeOptions = "other"
		if strings.EqualFold(strings.TrimSpace(v[0]), "nosniff") {
			out.ContentTypeOptions = "nosniff"
		}
	}
	if v := h.Values("X-Frame-Options"); len(v) == 1 {
		switch strings.ToLower(strings.TrimSpace(v[0])) {
		case "deny":
			out.FrameOptions = "deny"
		case "sameorigin":
			out.FrameOptions = "sameorigin"
		default:
			out.FrameOptions = "other"
		}
	}
	for _, v := range h.Values("Content-Security-Policy") {
		for _, directive := range strings.Split(v, ";") {
			fields := strings.Fields(directive)
			if len(fields) != 0 && strings.EqualFold(fields[0], "frame-ancestors") {
				out.CSPFrameAncestors = true
			}
		}
	}
	for _, v := range h.Values("Cache-Control") {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "no-store") {
				out.CacheNoStore = true
			}
		}
	}
	if v := h.Values("Access-Control-Allow-Origin"); len(v) == 1 {
		switch strings.TrimSpace(v[0]) {
		case "*":
			out.CORSAllowOrigin = "wildcard"
		case "null":
			out.CORSAllowOrigin = "null"
		default:
			out.CORSAllowOrigin = "specific"
		}
	}
	if v := h.Values("Access-Control-Allow-Credentials"); len(v) == 1 && strings.EqualFold(strings.TrimSpace(v[0]), "true") {
		out.CORSCredentials = true
	}
	if cookies := h.Values("Set-Cookie"); len(cookies) != 0 {
		c := &Cookies{Count: len(cookies)}
		for _, raw := range cookies {
			parts := strings.Split(raw, ";")
			sameSite := ""
			secure, httpOnly := false, false
			for _, attr := range parts[1:] {
				name, value, _ := strings.Cut(strings.TrimSpace(attr), "=")
				switch strings.ToLower(strings.TrimSpace(name)) {
				case "secure":
					secure = true
				case "httponly":
					httpOnly = true
				case "samesite":
					sameSite = strings.ToLower(strings.TrimSpace(value))
				}
			}
			if secure {
				c.Secure++
			}
			if httpOnly {
				c.HTTPOnly++
			}
			switch sameSite {
			case "strict":
				c.SameSiteStrict++
			case "lax":
				c.SameSiteLax++
			case "none":
				c.SameSiteNone++
			default:
				c.SameSiteMissing++
			}
		}
		out.Cookies = c
	}
	sort.Strings(out.Present)
	sort.Strings(out.Repeated)
	return out
}

func validateHeaders(h HeaderStructure) bool {
	if !sortedUnique(h.Present, securityHeaderSet) || !sortedUnique(h.Repeated, securityHeaderSet) {
		return false
	}
	present := make(map[string]bool, len(h.Present))
	for _, name := range h.Present {
		present[name] = true
	}
	for _, name := range h.Repeated {
		if !present[name] {
			return false
		}
	}
	if present["Set-Cookie"] != (h.Cookies != nil) {
		return false
	}
	switch h.ContentTypeOptions {
	case "", "nosniff", "other":
	default:
		return false
	}
	switch h.FrameOptions {
	case "", "deny", "sameorigin", "other":
	default:
		return false
	}
	switch h.CORSAllowOrigin {
	case "", "wildcard", "null", "specific":
	default:
		return false
	}
	if c := h.Cookies; c != nil {
		if c.Count < 1 || c.Secure < 0 || c.Secure > c.Count || c.HTTPOnly < 0 || c.HTTPOnly > c.Count ||
			c.SameSiteStrict < 0 || c.SameSiteLax < 0 || c.SameSiteNone < 0 || c.SameSiteMissing < 0 ||
			c.SameSiteStrict+c.SameSiteLax+c.SameSiteNone+c.SameSiteMissing != c.Count {
			return false
		}
	}
	return true
}

func (c *comparer) headers(l, r *HeaderStructure) {
	if l == nil || r == nil {
		if l != nil || r != nil {
			c.notCompared("headers")
		}
		return
	}
	lp, rp := map[string]bool{}, map[string]bool{}
	for _, h := range l.Present {
		lp[h] = true
	}
	for _, h := range r.Present {
		rp[h] = true
	}
	for _, h := range securityHeaders {
		if lp[h] != rp[h] {
			c.diff(DiffHeader, h, presence(lp[h]), presence(rp[h]))
		}
	}
	lRepeated, rRepeated := map[string]bool{}, map[string]bool{}
	for _, h := range l.Repeated {
		lRepeated[h] = true
	}
	for _, h := range r.Repeated {
		rRepeated[h] = true
	}
	for _, h := range securityHeaders {
		if lRepeated[h] != rRepeated[h] {
			c.diff(DiffHeaderValue, h+":repeated", presence(lRepeated[h]), presence(rRepeated[h]))
		}
	}
	pairs := [][3]string{{"content_type_options", l.ContentTypeOptions, r.ContentTypeOptions}, {"frame_options", l.FrameOptions, r.FrameOptions},
		{"cors_allow_origin", l.CORSAllowOrigin, r.CORSAllowOrigin}}
	for _, p := range pairs {
		if p[1] != p[2] {
			c.diff(DiffHeaderValue, p[0], p[1], p[2])
		}
	}
	bools := []struct {
		name string
		a, b bool
	}{{"csp_frame_ancestors", l.CSPFrameAncestors, r.CSPFrameAncestors}, {"cache_no_store", l.CacheNoStore, r.CacheNoStore},
		{"cors_credentials", l.CORSCredentials, r.CORSCredentials}}
	for _, b := range bools {
		if b.a != b.b {
			c.diff(DiffHeaderValue, b.name, presence(b.a), presence(b.b))
		}
	}
	lc, rc := l.Cookies, r.Cookies
	if lc != nil && rc != nil && (lc.Count != rc.Count || lc.Secure != rc.Secure || lc.HTTPOnly != rc.HTTPOnly ||
		lc.SameSiteStrict != rc.SameSiteStrict || lc.SameSiteLax != rc.SameSiteLax ||
		lc.SameSiteNone != rc.SameSiteNone || lc.SameSiteMissing != rc.SameSiteMissing) {
		c.diff(DiffHeaderValue, "set_cookie_attributes", cookieSummary(lc), cookieSummary(rc))
	}
}

func cookieSummary(c *Cookies) string {
	return strings.Join([]string{"count=" + itoa(c.Count), "secure=" + itoa(c.Secure), "http_only=" + itoa(c.HTTPOnly),
		"same_site_strict=" + itoa(c.SameSiteStrict), "same_site_lax=" + itoa(c.SameSiteLax),
		"same_site_none=" + itoa(c.SameSiteNone), "same_site_missing=" + itoa(c.SameSiteMissing)}, ",")
}
