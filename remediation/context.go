package remediation

import (
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Autumn-27/artex/dlp"
)

// Context is a caller-selected nonsensitive summary, never raw response/log text.
// Source is assigned by the caller, not extracted from model-generated prose.
// Every field remains untrusted data even after conservative normalization.
type Context struct {
	Component             string               `json:"component"`
	ComponentRole         string               `json:"component_role"`
	Endpoint              string               `json:"endpoint"`
	Prerequisites         string               `json:"prerequisites"`
	Observation           string               `json:"observation"`
	ExpectedBehavior      string               `json:"expected_behavior"`
	ProhibitedBehavior    string               `json:"prohibited_behavior"`
	Source                string               `json:"source"`
	Observations          []ContextObservation `json:"observations,omitempty"`
	ObservationsTruncated bool                 `json:"observations_truncated,omitempty"`
}

// ContextObservation는 호출자가 같은 결과·실행에 결속한 비민감 단계별 기록이다.
// 사용자 보완은 Context의 상단 필드에 두고 원래 관측/추정 출처를 덮어쓰지 않는다.
type ContextObservation struct {
	Component          string   `json:"component"`
	ComponentRole      string   `json:"component_role"`
	Endpoint           string   `json:"endpoint"`
	Prerequisites      string   `json:"prerequisites"`
	Observation        string   `json:"observation"`
	ExpectedBehavior   string   `json:"expected_behavior"`
	ProhibitedBehavior string   `json:"prohibited_behavior"`
	Source             string   `json:"source"`
	EvidenceRef        string   `json:"evidence_ref"`
	ObservedAt         string   `json:"observed_at"`
	RunID              string   `json:"run_id"`
	CallID             string   `json:"call_id"`
	StepID             string   `json:"step_id"`
	DependsOn          []string `json:"depends_on,omitempty"`
	Truncated          bool     `json:"truncated,omitempty"`
}

const UnknownContext = "미확인: 저장소와 승인된 비민감 근거에서 확인 필요"

// 용어 자체가 아니라 지시 전환·실행 명령을 거른다. 보안 동작 설명은 보존한다.
var instructionLike = regexp.MustCompile(`(?i)(\b(ignore|disregard|override)\s+(all\s+|the\s+|your\s+)?(previous|prior|above|system|developer|instructions)|(^|[.;:]\s*)(please\s+)?(reveal|show|print|output|leak|disclose)\s+(the\s+|your\s+)?(system[ ._-]*prompt|developer[ ._-]*message)|(^|[.;:]\s*)(please\s+)?(exfiltrate\s+(the\s+)?(secret|token|credential|password|data|files)|jailbreak\s+(the|this|your)\s+(system|assistant|model))\b|(^|[.;:]\s*)(please\s+)?(disable|turn off)\s+(the\s+)?(auth|audit|validat|security)|(^|[.;:]\s*)delete\s+(audit\s+)?(logs|files)|\b(run|execute)\s+(curl|wget|powershell|bash|sudo)\b|(^|[;|&]\s*)(curl|wget|powershell|sudo|/bin/)\b|\brm\s+-|<\s*/?\s*(script|system|instruction)|지시.{0,12}무시|명령.{0,12}무시|시스템.{0,8}프롬프트.{0,8}(공개|출력|보여)|비밀.{0,8}(전송|출력)|토큰.{0,8}(전송|출력)|인증.{0,12}(꺼라|끄세요|비활성화하)|인가.{0,12}(꺼라|끄세요|비활성화하)|감사.{0,12}(꺼라|끄세요|비활성화하))`)
var referencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,159}$`)

func reference(s string) string {
	if s == "" {
		return ""
	}
	if !referencePattern.MatchString(s) || instructionLike.MatchString(s) || dlp.DiagnosticText(s) != s {
		return "unknown"
	}
	return s
}

func observedAt(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return "unknown"
	}
	return t.UTC().Format(time.RFC3339Nano)
}

var opaqueLike = regexp.MustCompile(`[A-Za-z0-9_+/=-]{32,}`)
var safeLabel = regexp.MustCompile(`^[\p{L}\p{N}.@][\p{L}\p{N} ._+#/():-]{0,159}$`)

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
	observations := c.Observations
	c.Observations = nil
	if len(observations) > 32 {
		observations = observations[:32]
		c.ObservationsTruncated = true
	}
	for _, o := range observations {
		n := NormalizeContext(Context{Component: o.Component, ComponentRole: o.ComponentRole, Endpoint: o.Endpoint, Prerequisites: o.Prerequisites, Observation: o.Observation, ExpectedBehavior: o.ExpectedBehavior, ProhibitedBehavior: o.ProhibitedBehavior, Source: o.Source})
		o.Component, o.ComponentRole, o.Endpoint, o.Prerequisites = n.Component, n.ComponentRole, n.Endpoint, n.Prerequisites
		o.Observation, o.ExpectedBehavior, o.ProhibitedBehavior, o.Source = n.Observation, n.ExpectedBehavior, n.ProhibitedBehavior, n.Source
		o.EvidenceRef, o.ObservedAt = reference(o.EvidenceRef), observedAt(o.ObservedAt)
		o.RunID, o.CallID, o.StepID = reference(o.RunID), reference(o.CallID), reference(o.StepID)
		deps := o.DependsOn
		o.DependsOn = nil
		if len(deps) > 16 {
			deps = deps[:16]
			o.Truncated = true
		}
		for _, dep := range deps {
			o.DependsOn = append(o.DependsOn, reference(dep))
		}
		c.Observations = append(c.Observations, o)
	}
	return c
}
