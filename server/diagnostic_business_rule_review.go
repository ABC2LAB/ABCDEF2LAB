package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
)

// 후보 분류는 질문의 종류만 고른다. 계정 역할이나 실제 권한은 분류와 HTTP 상태로 추정하지 않는다.
func diagnosticBusinessRuleReview(c db.DiagnosticCandidate) *report.BusinessRuleReview {
	class := strings.NewReplacer("-", " ", "_", " ").Replace(strings.ToLower(c.VulnClass))
	var question string
	switch strings.Join(strings.Fields(class), " ") {
	case "idor", "bola", "cwe 639", "insecure direct object reference", "broken object level authorization":
		question = "이 요청의 계정은 해당 자원에 접근할 수 있나요? 소유권이나 공유 규칙을 확인해 주세요."
	case "bfla", "broken function level authorization", "broken access control", "access control":
		question = "이 요청의 계정에 해당 기능을 사용할 권한이 있나요?"
	case "business logic", "business logic flaw", "cwe 840", "cwe 841":
		question = "이 작업이 해당 계정과 현재 업무 상태에서 허용되는 동작인가요?"
	default:
		return nil
	}

	type evidence struct {
		Trusted struct {
			Call struct {
				Observations []struct {
					Sequence         uint64 `json:"sequence"`
					Method           string `json:"method"`
					Decision         string `json:"decision"`
					Status           int    `json:"status_code"`
					Complete         bool   `json:"response_complete"`
					ForwardAttempted bool   `json:"forward_attempted"`
					AuditComplete    bool   `json:"audit_complete"`
				} `json:"observations"`
			} `json:"call"`
		} `json:"trusted"`
	}
	var observed struct {
		evidence
		Related []evidence `json:"related_calls"`
	}
	// 보고서 후보 조회가 현재 호출과 관련 호출의 작업·회차·의도 결속을 검증한 뒤 사용한다.
	if json.Unmarshal([]byte(c.Evidence), &observed) != nil {
		return nil
	}
	for _, call := range append([]evidence{observed.evidence}, observed.Related...) {
		for _, o := range call.Trusted.Call.Observations {
			if o.Sequence == 0 || o.Decision != "allowed" || o.Status < 200 || o.Status >= 300 ||
				!o.Complete || !o.ForwardAttempted || !o.AuditComplete {
				continue
			}
			switch o.Method {
			case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
				return &report.BusinessRuleReview{
					Status: "deferred", Reason: "business_rule_unverified",
					Observation: fmt.Sprintf("저장된 요청에서 %s 응답 HTTP %d을 관찰했습니다.", o.Method, o.Status),
					Question:    question,
				}
			}
		}
	}
	return nil
}

func writeDiagnosticBusinessRuleReviews(w http.ResponseWriter, data report.DiagnosticReport) {
	type entry struct {
		ID string `json:"id"`
		report.BusinessRuleReview
	}
	entries := []entry{}
	for _, candidate := range data.Entries {
		if candidate.BusinessRuleReview != nil {
			entries = append(entries, entry{ID: candidate.ID, BusinessRuleReview: *candidate.BusinessRuleReview})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeJSON(w, http.StatusOK, struct {
		TaskID  string  `json:"task_id"`
		Entries []entry `json:"entries"`
	}{TaskID: strconv.FormatInt(data.TaskID, 10), Entries: entries})
}
