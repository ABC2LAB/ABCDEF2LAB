package e2proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Autumn-27/artex/responseevidence"
)

// HTTPObservation은 실제 통제 경로의 제한된 비민감 projection이다. 본문·쿠키·계정 값은 없다.
// 응답 코드·제품 헤더의 관찰은 취약점 확정이나 실제 백엔드 구현의 증명이 아니다.
type HTTPObservation struct {
	Sequence   uint64 `json:"sequence"`
	AttemptRef string `json:"attempt_ref"`
	Method     string `json:"method"`
	Endpoint   string `json:"endpoint"`
	// ExactFunction is true only for an exact approved path and either no query
	// or an exact query pinned by the rule. Masked paths are never exact.
	ExactFunction    bool   `json:"exact_function,omitempty"`
	RuleRef          string `json:"rule_ref"`
	Decision         string `json:"decision"`
	Reason           Reason `json:"reason"`
	StatusCode       int    `json:"status_code,omitempty"`
	ResponseComplete bool   `json:"response_complete"`
	// ResponseTruncated는 본문 상한 때문에 앞부분만 도구에 준 응답이다(사유 withheld).
	ResponseTruncated bool      `json:"response_truncated,omitempty"`
	ForwardAttempted  bool      `json:"forward_attempted"`
	AuditComplete     bool      `json:"audit_complete"`
	ObservedAt        time.Time `json:"observed_at"`
	// RequestAuthContext records only the presence of target credential
	// header names. It never contains header values or cookie names.
	RequestAuthContext string `json:"request_auth_context,omitempty"`
	// RequestAccountRef is a non-secret revision of a protected test account.
	// Empty means the actual sent credential could not be bound to one account.
	RequestAccountRef   string `json:"request_account_ref,omitempty"`
	RequestQueryDigest  string `json:"request_query_digest,omitempty"`
	RequestHeaderDigest string `json:"request_header_digest,omitempty"`
	// ResponseMediaClass is a bounded content-type category, never a raw
	// target-controlled header value or response body.
	ResponseMediaClass string           `json:"response_media_class,omitempty"`
	Technologies       []HTTPTechnology `json:"technologies,omitempty"`
	// HTMLSecurity는 성공한 HTML 응답의 비밀 없는 헤더 속성만 담는다.
	HTMLSecurity string `json:"html_security,omitempty"`
}
type HTTPTechnology struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Header  string `json:"header"`
}

var diagnosticPathPart = regexp.MustCompile(`^[a-zA-Z][a-zA-Z-]{0,30}$`)
var diagnosticProduct = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9._+-]{0,31})(?:/([0-9]+(?:\.[0-9]+){0,3}))?$`)

func cloneHTTPObservations(in []HTTPObservation) []HTTPObservation {
	out := append([]HTTPObservation(nil), in...)
	for i := range out {
		out[i].Technologies = append([]HTTPTechnology(nil), out[i].Technologies...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	return out
}
func (c *Call) observeHTTP(req *http.Request, sentHeaders http.Header, resp *http.Response, r Receipt, evidence *responseevidence.Evidence) {
	if req == nil || req.URL == nil {
		return
	}
	// 승인된 prefix만 위치 의미로 남긴다. 실제 trailing 자원값·query·host는 보존하지 않는다.
	prefix := ""
	var ruleHeaders http.Header
	exactQuery := ""
	c.e.mu.Lock()
	for _, rule := range c.e.rules {
		if rule.Ref == r.RuleRef {
			prefix = rule.PathPrefix
			exactQuery = rule.ExactQuery
			ruleHeaders = rule.Headers
			break
		}
	}
	c.e.mu.Unlock()
	endpoint := ""
	if prefix != "" {
		parts := strings.Split(strings.Trim(prefix, "/"), "/")
		for i, p := range parts {
			if !diagnosticPathPart.MatchString(p) {
				parts[i] = "{segment}"
			}
		}
		endpoint = "/" + strings.Join(parts, "/")
		if req.URL.Path != strings.TrimSuffix(prefix, "/") && req.URL.Path != prefix {
			endpoint = strings.TrimSuffix(endpoint, "/") + "/{resource}"
		}
		// 우리 제어 비밀 · E③ 표식이 든 경로 틀만 숨긴다(RedactRecord). 대상 값 가림(DLP)은 걷었다(절대규칙 2, 2026-11-26).
		if string(c.e.RedactRecord([]byte(endpoint))) != endpoint {
			endpoint = "/{protected}"
		}
	}
	method := r.Method
	if !supportedMethod(method) {
		method = "UNSUPPORTED"
	}
	exactInput := req.URL.RawQuery == "" || exactQuery != "" && req.URL.RawQuery == exactQuery
	obs := HTTPObservation{Sequence: r.Sequence, AttemptRef: r.AttemptID, Method: method, Endpoint: endpoint, ExactFunction: prefix != "" && req.URL.EscapedPath() == prefix && exactInput && !strings.Contains(endpoint, "{"), RuleRef: r.RuleRef, Decision: r.Decision, Reason: r.Reason, StatusCode: r.StatusCode, ResponseComplete: r.ResponseComplete, ResponseTruncated: r.ResponseTruncated, ForwardAttempted: r.ForwardAttempted, AuditComplete: r.AuditComplete, ObservedAt: r.At}
	if r.ForwardAttempted {
		if exactQuery != "" && req.URL.RawQuery == exactQuery {
			sum := sha256.Sum256([]byte(exactQuery))
			obs.RequestQueryDigest = hex.EncodeToString(sum[:])
		}
		if len(ruleHeaders) != 0 {
			encoded, err := json.Marshal(ruleHeaders)
			if err == nil {
				sum := sha256.Sum256(encoded)
				obs.RequestHeaderDigest = hex.EncodeToString(sum[:])
			}
		}
		obs.RequestAuthContext = requestAuthContext(sentHeaders, c.e.cfg.TargetCredentialHeader)
		if c.e.cfg.ResolveSentAccount != nil {
			obs.RequestAccountRef = c.e.cfg.ResolveSentAccount(sentHeaders)
		}
	}
	if resp != nil && r.ResponseReceived && r.ResponseComplete {
		obs.ResponseMediaClass = diagnosticMediaClass(resp.Header.Get("Content-Type"))
		if (method == http.MethodGet || method == http.MethodHead) && r.StatusCode == http.StatusOK {
			mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
			if err == nil && strings.EqualFold(mediaType, "text/html") {
				values := resp.Header.Values("X-Content-Type-Options")
				if len(values) == 0 {
					obs.HTMLSecurity = "nosniff_missing"
				} else if len(values) == 1 && strings.EqualFold(strings.TrimSpace(values[0]), "nosniff") {
					obs.HTMLSecurity = "nosniff_present"
				}
			}
		}
		for _, header := range []string{"Server", "X-Powered-By"} {
			// 값이 여럿이면 하나를 골라 확인된 배너로 쓰지 않는다.
			if len(resp.Header.Values(header)) != 1 {
				continue
			}
			value := strings.TrimSpace(resp.Header.Get(header))
			if string(c.e.RedactRecord([]byte(value))) != value {
				continue
			}
			match := diagnosticProduct.FindStringSubmatch(value)
			if match != nil {
				obs.Technologies = append(obs.Technologies, HTTPTechnology{Name: match[1], Version: match[2], Header: header})
			}
		}
	}
	var attached *ResponseEvidence
	encodedBytes := 0
	if evidence != nil && obs.ResponseComplete && obs.ForwardAttempted && !obs.ResponseTruncated {
		// 근거 하나와 실행 전체의 크기를 묶는다. 넘으면 근거를 붙이지 않는다(없음 = 기록 안 함, 저장 쪽이 not_recorded로 읽음).
		if fitted, ok := responseevidence.Fit(*evidence, MaxResponseEvidenceBytes); ok {
			if raw, err := json.Marshal(fitted); err == nil {
				attached, encodedBytes = &ResponseEvidence{AttemptRef: r.AttemptID, Sequence: r.Sequence, Evidence: fitted}, len(raw)
			}
		}
	}
	c.e.mu.Lock()
	defer c.e.mu.Unlock()
	if len(c.diagnostics) >= 32 {
		c.diagnosticsTruncated = true
		return
	}
	c.diagnostics = append(c.diagnostics, obs)
	if attached != nil && c.e.responseEvidenceBytes+encodedBytes <= MaxExecutionResponseEvidenceBytes {
		c.e.responseEvidenceBytes += encodedBytes
		c.responseEvidence = append(c.responseEvidence, *attached)
	}
}

// 응답 근거 크기 상한이다. 근거 하나는 저장 관찰 행(4096바이트)에 Host 증빙과 같이 들어가야 하고, 실행 전체 근거는 원격 실행
// 조회 봉투(8 MiB)에 관찰과 함께 들어가야 한다.
const (
	MaxResponseEvidenceBytes          = 1024
	MaxExecutionResponseEvidenceBytes = 2 << 20
)

// ResponseEvidence는 시도 하나의 응답 구조 근거다.
type ResponseEvidence struct {
	AttemptRef string                    `json:"attempt_ref"`
	Sequence   uint64                    `json:"sequence"`
	Evidence   responseevidence.Evidence `json:"evidence"`
}

// cloneResponseEvidence는 반환된 결과와 실행 내부 근거 사이의 모든 별칭을 끊는다.
func cloneResponseEvidence(in []ResponseEvidence) []ResponseEvidence {
	if len(in) == 0 {
		return nil
	}
	out := append([]ResponseEvidence(nil), in...)
	for i := range out {
		out[i].Evidence = responseevidence.Clone(in[i].Evidence)
	}
	return out
}

// extractResponseEvidence는 완결 본문에서 구조 근거를 만든다. 제어 비밀·E③ 표식·출력 가림 값이 든 이름·값은 남기지 않는다.
// 호출자는 e.mu를 잡지 않은 상태여야 한다(가림 검사가 잠근다).
func (e *Execution) extractResponseEvidence(method string, resp *http.Response, body []byte) *responseevidence.Evidence {
	if resp == nil {
		return nil
	}
	protected := func(s string) bool { return string(e.redact([]byte(s), false)) != s }
	ev := responseevidence.Extract(responseevidence.Response{Received: true, Complete: true, StatusCode: resp.StatusCode, Method: method,
		ContentType: resp.Header.Get("Content-Type"), ContentEncoding: resp.Header.Get("Content-Encoding"), Header: resp.Header, Body: body},
		responseevidence.DefaultLimits(), protected)
	return &ev
}

func diagnosticMediaClass(raw string) string {
	media, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "unknown"
	}
	switch strings.ToLower(media) {
	case "text/html":
		return "html"
	case "application/json":
		return "json"
	case "text/plain":
		return "plain_text"
	case "application/xml", "text/xml":
		return "xml"
	case "application/octet-stream":
		return "binary"
	default:
		return "other"
	}
}

// This bounded category is the only credential information retained in an
// observation. Header values and cookie names never enter the projection.
func requestAuthContext(headers http.Header, custom ...string) string {
	// A target may use a nonstandard credential header. If its meaning is
	// unknown, never record the request as anonymous or comparable to one.
	for name := range headers {
		if knownOrdinaryObservationHeader(name) || strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Cookie") ||
			len(custom) == 1 && custom[0] != "" && strings.EqualFold(name, custom[0]) {
			continue
		}
		return "unknown"
	}
	switch {
	case headerPresent(headers, "Authorization") && headerPresent(headers, "Cookie"):
		return "authorization_and_cookie"
	case headerPresent(headers, "Authorization"):
		return "authorization"
	case headerPresent(headers, "Cookie"):
		return "cookie"
	case len(custom) == 1 && custom[0] != "" && headerPresent(headers, custom[0]):
		return "custom"
	default:
		return "none"
	}
}

func knownOrdinaryObservationHeader(name string) bool {
	if reservedAttributionHeader(name) {
		return true
	}
	switch http.CanonicalHeaderKey(name) {
	case "Accept", "Accept-Encoding", "Accept-Language", "User-Agent", "Content-Type", "X-Requested-With":
		return true
	}
	return false
}
func (o HTTPObservation) Summary() string {
	return fmt.Sprintf("실제 통제 요청 %s %s: 전송 시도=%t, HTTP 상태=%d, 응답 완료=%t, 승인 판단=%s, 결과=%s. 이 관찰만으로 취약점이나 구현 기술을 확정하지 않는다.", o.Method, o.Endpoint, o.ForwardAttempted, o.StatusCode, o.ResponseComplete, o.Decision, o.Reason)
}
