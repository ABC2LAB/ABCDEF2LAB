package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/report"
)

// 후보 유형은 자유 입력이다. 단어 경계를 확인해 CWE-6390 같은 다른 유형을 제외한다.
var (
	objectReviewClass   = regexp.MustCompile(`\b(idor|bola|cwe 639|insecure direct object reference|broken object level authori[sz]ation)\b`)
	accessReviewClass   = regexp.MustCompile(`\b(bfla|broken function level authori[sz]ation|access control|(missing|improper) authori[sz]ation|authori[sz]ation bypass)\b`)
	businessReviewClass = regexp.MustCompile(`\b(business ?logic|cwe 84[01]|price manipulation|payment bypass|negative quantity)\b`)
)

func diagnosticBusinessRuleReview(entry *report.DiagnosticEntry) *report.BusinessRuleReview {
	class := strings.Join(strings.Fields(strings.NewReplacer("-", " ", "_", " ").Replace(strings.ToLower(entry.VulnClass))), " ")
	var question string
	switch {
	case objectReviewClass.MatchString(class):
		question = "성공 응답을 받은 요청의 계정은 해당 자원에 접근할 수 있나요? 소유권이나 공유 규칙을 확인해 주세요."
	case accessReviewClass.MatchString(class):
		question = "성공 응답을 받은 요청의 계정에 해당 기능을 사용할 권한이 있나요?"
	case businessReviewClass.MatchString(class):
		question = "성공 응답을 받은 작업은 해당 계정과 당시 업무 상태에서 허용되나요?"
	default:
		return nil
	}

	var evidence struct {
		Trusted struct {
			Call e2proxy.CallResult `json:"call"`
		} `json:"trusted"`
	}
	if json.Unmarshal([]byte(entry.Evidence), &evidence) != nil {
		return nil
	}
	// 관련 호출과 주 호출은 같은 시도 번호를 가질 수 있다. 보고서가 검증한 주 호출의 단계만 연결한다.
	call := evidence.Trusted.Call
	steps := make(map[string]int)
	for _, step := range entry.Steps {
		if step.CallRef == call.CallRef {
			steps[step.SourceStepID] = step.Number
		}
	}

	var facts []string
	hasSuccess := false
	for _, o := range call.Observations {
		step := steps[fmt.Sprintf("attempt-%d", o.Sequence)]
		if step == 0 || o.Decision != "allowed" || !o.ResponseComplete || !o.ForwardAttempted || !o.AuditComplete || o.StatusCode < 100 || o.StatusCode > 599 {
			continue
		}
		switch o.Method {
		case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
			facts = append(facts, fmt.Sprintf("재현 단계 %d (%s, HTTP %d)", step, o.Method, o.StatusCode))
			hasSuccess = hasSuccess || (o.StatusCode >= 200 && o.StatusCode < 300)
		}
	}
	if !hasSuccess {
		return nil
	}
	return &report.BusinessRuleReview{
		Status: "deferred", Reason: "business_rule_unverified",
		Observation: strings.Join(facts, ", "), Question: question,
	}
}

func writeDiagnosticBusinessRuleReviews(w http.ResponseWriter, data report.DiagnosticReport) {
	type entry struct {
		ID string `json:"id"`
		report.BusinessRuleReview
	}
	entries := []entry{}
	type observation struct {
		Step    int    `json:"step"`
		Method  string `json:"method"`
		Summary string `json:"summary"`
	}
	type verification struct {
		ID             string                     `json:"id"`
		Status         string                     `json:"status"`
		Reason         string                     `json:"reason"`
		EvidenceDigest string                     `json:"evidence_digest,omitempty"`
		AttemptRefs    []string                   `json:"attempt_refs,omitempty"`
		Observations   []observation              `json:"observations"`
		Sources        []candidateverify.SourceV2 `json:"sources,omitempty"`
	}
	verifications := []verification{}
	assessments := map[int64]report.CandidateAssessment{}
	if data.Run != nil && (data.Run.VerificationVersion == "passive/v1" || data.Run.VerificationVersion == diagnosisVerificationV2) {
		for _, assessment := range data.Run.CandidateAssessments {
			assessments[assessment.CandidateID] = assessment
		}
	}
	for _, candidate := range data.Entries {
		if candidate.BusinessRuleReview != nil {
			entries = append(entries, entry{ID: candidate.ID, BusinessRuleReview: *candidate.BusinessRuleReview})
		}
		if candidate.Kind != "candidate" || data.Run == nil || (data.Run.VerificationVersion != "passive/v1" && data.Run.VerificationVersion != diagnosisVerificationV2) {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(candidate.ID, "candidate-"), 10, 64)
		if err != nil {
			continue
		}
		assessment, found := assessments[id]
		if !found && data.Run.State == report.DiagnosticRunRunning {
			verifications = append(verifications, verification{ID: candidate.ID, Status: "insufficient", Reason: "verification_not_complete", EvidenceDigest: candidate.Digest, Observations: []observation{}})
			continue
		}
		if !found && !candidate.SourceLoadFailed {
			continue
		}
		row := verification{ID: candidate.ID, Status: assessment.Status, Reason: assessment.Reason, EvidenceDigest: candidate.Digest, AttemptRefs: append([]string(nil), assessment.AttemptRefs...), Observations: []observation{}, Sources: append([]candidateverify.SourceV2(nil), assessment.Sources...)}
		if candidate.SourceLoadFailed {
			row.Status, row.Reason = "insufficient", "source_unavailable"
		}
		for _, step := range candidate.Steps {
			if len(row.Observations) >= 2 {
				break
			}
			summary := report.DiagnosticText(step.Observation)
			if len(summary) > 512 {
				summary = "관찰 요약 일부 생략"
			}
			row.Observations = append(row.Observations, observation{Step: step.Number, Method: step.Method, Summary: summary})
		}
		verifications = append(verifications, row)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeJSON(w, http.StatusOK, struct {
		TaskID                 string         `json:"task_id"`
		Entries                []entry        `json:"entries"`
		CandidateVerifications []verification `json:"candidate_verifications,omitempty"`
	}{TaskID: strconv.FormatInt(data.TaskID, 10), Entries: entries, CandidateVerifications: verifications})
}
