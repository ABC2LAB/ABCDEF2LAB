package db

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const WorkerEstimate = "worker_estimate"
const MaxSeverityRationaleBytes = 1024

// NormalizedSeverity는 평가만 정규화한다. 후보의 관찰·판정·증거 지문은 바꾸지 않는다.
// 실제 HTTP 응답과 마감 근거는 평가 가능 조건이며 모델이 설명한 영향의 확정 증거는 아니다.
func (c DiagnosticCandidate) NormalizedSeverity() DiagnosticCandidate {
	severity, rationale, source := c.SuggestedSeverity, c.SeverityRationale, c.SeveritySource
	c.SuggestedSeverity, c.SeverityRationale, c.SeveritySource = "", "", ""
	if source != WorkerEstimate || !validEstimatedSeverity(severity) || c.Validate() != nil || c.OutputTruncated || c.Classification != "completed" {
		return c
	}
	if len(rationale) > MaxSeverityRationaleBytes || !utf8.ValidString(rationale) {
		return c
	}
	// 근거 글은 가리지 않는다(절대규칙 2, 2026-11-26 DLP 폐기). 형식 규칙(한국어 · 한 줄 · 길이)만 본다.
	rationale = strings.TrimSpace(rationale)
	hangul := false
	for _, r := range rationale {
		if unicode.IsControl(r) {
			return c
		}
		if unicode.In(r, unicode.Hangul) {
			hangul = true
		}
	}
	if !hangul || utf8.RuneCountInString(rationale) < 8 || len(rationale) > MaxSeverityRationaleBytes || !severityHTTPObservation([]byte(c.Evidence)) {
		return c
	}
	c.SuggestedSeverity, c.SeverityRationale, c.SeveritySource = severity, rationale, WorkerEstimate
	return c
}

func validEstimatedSeverity(s string) bool {
	switch s {
	case "critical", "high", "medium", "low", "info":
		return true
	}
	return false
}

// Bash 성공만으로는 부족하다. 실제 응답을 관찰한 감사 완결 호출만 평가하며
// 관련 호출 중 불명·잘림·미마감이 하나라도 있으면 보수적으로 평가를 생략한다.
func severityHTTPObservation(raw []byte) bool {
	var e struct {
		Related []json.RawMessage `json:"related_calls"`
		Trusted struct {
			Classification string `json:"classification"`
			Final          bool   `json:"final"`
			BashExited     bool   `json:"process_bash_exited"`
			ProcessDrained bool   `json:"process_drained"`
			ProxyDrained   bool   `json:"proxy_drained"`
			Truncated      bool   `json:"output_truncated"`
			Call           struct {
				Final                 bool `json:"final"`
				AuditComplete         bool `json:"audit_complete"`
				ObservationsTruncated bool `json:"observations_truncated"`
				Counts                struct {
					Responses        uint64 `json:"responses"`
					ForwardAttempted uint64 `json:"forward_attempted"`
					Unknown          uint64 `json:"unknown"`
					Withheld         uint64 `json:"withheld"`
				} `json:"counts"`
				Observations []struct {
					Sequence         uint64    `json:"sequence"`
					AttemptRef       string    `json:"attempt_ref"`
					ObservedAt       time.Time `json:"observed_at"`
					Method           string    `json:"method"`
					Endpoint         string    `json:"endpoint"`
					Decision         string    `json:"decision"`
					Status           int       `json:"status_code"`
					Complete         bool      `json:"response_complete"`
					Truncated        bool      `json:"response_truncated"`
					ForwardAttempted bool      `json:"forward_attempted"`
					AuditComplete    bool      `json:"audit_complete"`
				} `json:"observations"`
			} `json:"call"`
		} `json:"trusted"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return false
	}
	t := e.Trusted
	if t.Classification != "completed" || !t.Final || !t.BashExited || !t.ProcessDrained || !t.ProxyDrained || t.Truncated || !t.Call.Final || !t.Call.AuditComplete || t.Call.Counts.Responses == 0 || t.Call.Counts.ForwardAttempted == 0 || t.Call.Counts.Unknown != 0 || t.Call.Counts.Withheld != 0 || t.Call.ObservationsTruncated {
		return false
	}
	found := false
	for _, o := range t.Call.Observations {
		if o.Truncated {
			return false
		}
		if o.Sequence > 0 && o.AttemptRef != "" && !o.ObservedAt.IsZero() && o.Decision == "allowed" && o.Method != "" && o.Endpoint != "" && o.Status >= 100 && o.Status <= 599 && o.Complete && !o.Truncated && o.ForwardAttempted && o.AuditComplete {
			found = true
		}
	}
	if !found {
		return false
	}
	for _, other := range e.Related {
		if !severityHTTPObservation(other) {
			return false
		}
	}
	return true
}
