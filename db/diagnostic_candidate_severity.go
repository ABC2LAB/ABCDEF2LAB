package db

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Autumn-27/artex/dlp"
)

const WorkerEstimate = "worker_estimate"
const MaxSeverityRationaleBytes = 1024

// MaxSeverityRationaleInputBytes는 가림 전에 받는 근거 길이다. 도구 스키마의 maxLength는 글자 수(1,024자)이고
// harness는 그 값을 검사하지 않으므로, 한 글자 최대 4바이트로 잡은 입력 상한을 넘을 때만 평가를 생략한다.
const MaxSeverityRationaleInputBytes = 4 * MaxSeverityRationaleBytes

// SeverityRationaleCutMarker는 저장 상한에 맞춰 근거를 자른 표시다. 화면·보고서에 그대로 보인다.
const SeverityRationaleCutMarker = "…(이하 생략)"

// NormalizedSeverity는 평가만 정규화한다. 후보의 관찰·판정·증거 지문은 바꾸지 않는다.
// 실제 HTTP 응답과 마감 근거는 평가 가능 조건이며 모델이 설명한 영향의 확정 증거는 아니다.
func (c DiagnosticCandidate) NormalizedSeverity() DiagnosticCandidate {
	severity, rationale, source := c.SuggestedSeverity, c.SeverityRationale, c.SeveritySource
	c.SuggestedSeverity, c.SeverityRationale, c.SeveritySource = "", "", ""
	if source != WorkerEstimate || !validEstimatedSeverity(severity) || c.Validate() != nil || c.OutputTruncated || c.Classification != "completed" {
		return c
	}
	if len(rationale) > MaxSeverityRationaleInputBytes || !utf8.ValidString(rationale) {
		return c
	}
	// 가린 뒤에 자른다. 먼저 자르면 경계에 걸친 비밀 조각이 가림 패턴에 맞지 않고 남을 수 있다.
	rationale = cutSeverityRationale(strings.TrimSpace(dlp.DiagnosticText(rationale)))
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

// cutSeverityRationale은 저장 상한을 넘는 근거를 글자 경계에서 잘라 잘림 표시를 붙인다. 한국어 근거는 한 글자가
// 3바이트라 약 340자에서 상한에 닿는다. 상한 안이면 그대로 둔다(이미 자른 근거를 다시 읽어도 바뀌지 않는다).
func cutSeverityRationale(s string) string {
	if len(s) <= MaxSeverityRationaleBytes {
		return s
	}
	end := MaxSeverityRationaleBytes - len(SeverityRationaleCutMarker)
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return strings.TrimRightFunc(s[:end], unicode.IsSpace) + SeverityRationaleCutMarker
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
