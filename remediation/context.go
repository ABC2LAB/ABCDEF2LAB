package remediation

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Autumn-27/artex/dlp"
)

// Context is a caller-selected nonsensitive summary, never raw response/log text.
// Source is assigned by the caller, not extracted from model-generated prose.
// Every field remains untrusted data even after conservative normalization.
type Context struct {
	Component          string `json:"component"`
	ComponentRole      string `json:"component_role"`
	Endpoint           string `json:"endpoint"`
	Prerequisites      string `json:"prerequisites"`
	Observation        string `json:"observation"`
	ExpectedBehavior   string `json:"expected_behavior"`
	ProhibitedBehavior string `json:"prohibited_behavior"`
	Source             string `json:"source"`
}

const UnknownContext = "미확인: 저장소와 승인된 비민감 근거에서 확인 필요"

var instructionLike = regexp.MustCompile(`(?i)(disable.{0,30}(auth|audit|validat|security)|turn.off.{0,30}(auth|audit|validat|security)|delete.{0,20}(logs|files)|인증.{0,12}(끄|비활성)|인가.{0,12}(끄|비활성)|감사.{0,12}(끄|비활성)|ignore|disregard|override|previous.instructions|system.prompt|developer.message|assistant|jailbreak|exfiltrat|curl\b|wget\b|powershell|/bin/|\bbash\b|\bsudo\b|\brm\s+-|\bexec\b|\beval\b|<\s*/?\s*(script|system|instruction)|지시.{0,12}무시|명령.{0,12}무시|시스템.{0,8}프롬프트|비밀.{0,8}(전송|출력)|토큰.{0,8}(전송|출력))`)
var opaqueLike = regexp.MustCompile(`[A-Za-z0-9_+/=-]{32,}`)
var safeLabel = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._+#/-]{0,79}$`)

// safeData is defense in depth, not a universal secret/injection detector.
// Callers must exclude raw data and apply their disclosure/credential policy first.
func safeData(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > 512 {
		return UnknownContext
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return UnknownContext
		}
	}
	if instructionLike.MatchString(s) || opaqueLike.MatchString(s) || strings.ContainsAny(s, "`\\") {
		return UnknownContext
	}
	if dlp.DiagnosticText(s) != s {
		return UnknownContext
	}
	return s
}
func role(s string) string {
	if member(s, "frontend", "backend", "service", "database", "proxy", "unknown") {
		return s
	}
	return "unknown"
}
func label(s string) string {
	v := safeData(s)
	if v == UnknownContext || !safeLabel.MatchString(v) {
		return "unknown"
	}
	return v
}

// NormalizeContext returns bounded evidence DATA. It does not certify its truth,
// authorize its disclosure, or grant the recipient AI execution permissions.
func NormalizeContext(c Context) Context {
	c.Component = label(c.Component)
	c.ComponentRole = role(c.ComponentRole)
	if !member(c.Source, "user_provided", "observed", "inferred", "unknown") {
		c.Source = "unknown"
	}
	c.Endpoint = safeData(c.Endpoint)
	if c.Endpoint != UnknownContext {
		// Query values, fragments, credentials and encoded characters do not belong
		// in copyable route context. Caller supplies a nonsensitive route template.
		parts := strings.SplitN(c.Endpoint, " ", 2)
		route := c.Endpoint
		if len(parts) == 2 {
			if !member(parts[0], "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS") {
				c.Endpoint = UnknownContext
			}
			route = parts[1]
		}
		if !strings.HasPrefix(route, "/") || strings.ContainsAny(route, "?#%") || strings.Contains(route, "//") {
			c.Endpoint = UnknownContext
		}
		if u, e := url.Parse(route); e != nil || u.IsAbs() || u.Host != "" {
			c.Endpoint = UnknownContext
		}
	}
	c.Prerequisites = safeData(c.Prerequisites)
	c.Observation = safeData(c.Observation)
	c.ExpectedBehavior = safeData(c.ExpectedBehavior)
	c.ProhibitedBehavior = safeData(c.ProhibitedBehavior)
	return c
}
func contextJSON(c Context) string { b, _ := json.Marshal(c); return string(b) }
