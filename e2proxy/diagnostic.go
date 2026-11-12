package e2proxy

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Autumn-27/artex/dlp"
)

// HTTPObservation은 실제 통제 경로의 제한된 비민감 projection이다. 본문·쿠키·계정 값은 없다.
// 응답 코드·제품 헤더의 관찰은 취약점 확정이나 실제 백엔드 구현의 증명이 아니다.
type HTTPObservation struct {
	Sequence         uint64 `json:"sequence"`
	AttemptRef       string `json:"attempt_ref"`
	Method           string `json:"method"`
	Endpoint         string `json:"endpoint"`
	RuleRef          string `json:"rule_ref"`
	Decision         string `json:"decision"`
	Reason           Reason `json:"reason"`
	StatusCode       int    `json:"status_code,omitempty"`
	ResponseComplete bool   `json:"response_complete"`
	// ResponseTruncated는 본문 상한 때문에 앞부분만 도구에 준 응답이다(사유 withheld).
	ResponseTruncated bool             `json:"response_truncated,omitempty"`
	ForwardAttempted  bool             `json:"forward_attempted"`
	AuditComplete     bool             `json:"audit_complete"`
	ObservedAt        time.Time        `json:"observed_at"`
	Technologies      []HTTPTechnology `json:"technologies,omitempty"`
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
func (c *Call) observeHTTP(req *http.Request, resp *http.Response, r Receipt) {
	if req == nil || req.URL == nil {
		return
	}
	// 승인된 prefix만 위치 의미로 남긴다. 실제 trailing 자원값·query·host는 보존하지 않는다.
	prefix := ""
	c.e.mu.Lock()
	for _, rule := range c.e.rules {
		if rule.Ref == r.RuleRef {
			prefix = rule.PathPrefix
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
		if string(c.e.RedactOutput([]byte(endpoint))) != endpoint || dlp.DiagnosticText(endpoint) != endpoint {
			endpoint = "/{protected}"
		}
	}
	method := r.Method
	if !supportedMethod(method) {
		method = "UNSUPPORTED"
	}
	obs := HTTPObservation{Sequence: r.Sequence, AttemptRef: r.AttemptID, Method: method, Endpoint: endpoint, RuleRef: r.RuleRef, Decision: r.Decision, Reason: r.Reason, StatusCode: r.StatusCode, ResponseComplete: r.ResponseComplete, ResponseTruncated: r.ResponseTruncated, ForwardAttempted: r.ForwardAttempted, AuditComplete: r.AuditComplete, ObservedAt: r.At}
	if resp != nil && r.ResponseReceived && r.ResponseComplete {
		for _, header := range []string{"Server", "X-Powered-By"} {
			value := strings.TrimSpace(resp.Header.Get(header))
			if string(c.e.RedactOutput([]byte(value))) != value || dlp.DiagnosticText(value) != value {
				continue
			}
			match := diagnosticProduct.FindStringSubmatch(value)
			if match != nil {
				obs.Technologies = append(obs.Technologies, HTTPTechnology{Name: match[1], Version: match[2], Header: header})
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
}
func (o HTTPObservation) Summary() string {
	return fmt.Sprintf("실제 통제 요청 %s %s: 전송 시도=%t, HTTP 상태=%d, 응답 완료=%t, 승인 판단=%s, 결과=%s. 이 관찰만으로 취약점이나 구현 기술을 확정하지 않는다.", o.Method, o.Endpoint, o.ForwardAttempted, o.StatusCode, o.ResponseComplete, o.Decision, o.Reason)
}
