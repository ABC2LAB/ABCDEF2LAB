package server

import (
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/responseevidence"
)

// 선택 후보 최종 판정의 응답 근거 게이트다. 프로그램은 관찰 사실(근거 유무·비교 가능성·재현 일관성)만 검사하고, 의미 해석은 모델이
// 한다. 저장 문맥만으로 결정되므로 이력·보고서 재조회(ParseCandidateFinalVerdict)에서 같은 결과를 낸다.
const candidateEvidenceGateVersion = "selected/evidence-gate-v1"

// 근거 요구 범주다.
const (
	// candidateEvidenceHeaders는 응답 헤더 사실로 주장하는 유형이다(보안 헤더·쿠키 속성·CORS·버전 배너).
	candidateEvidenceHeaders = "response_headers"
	// candidateEvidenceStructure는 응답 본문 구조가 필요한 유형이다(오류 노출·업무 규칙·데이터 노출·형식 이상 등).
	candidateEvidenceStructure = "response_structure"
)

// 게이트가 결정적 권고를 막은 고정 사유다.
const (
	evidenceReasonUnavailable    = "response_evidence_unavailable"
	evidenceReasonIncomparable   = "response_evidence_incomparable"
	evidenceReasonInconsistent   = "reproduction_inconsistent"
	evidenceReasonAuthLost       = "reproduction_auth_context_lost"
	evidenceReasonReproductionKO = "reproduction_unsuccessful"
)

type candidateEvidenceComparison struct {
	Left       candidateVerificationSourceRef `json:"left"`
	Right      candidateVerificationSourceRef `json:"right"`
	Comparison responseevidence.Comparison    `json:"comparison"`
}

// CandidateEvidenceReview는 모델 입력과 판정 게이트에 같이 쓰는 프로그램 비교 결과다. 취약점 판정이 아니다.
type CandidateEvidenceReview struct {
	Version         string                        `json:"version"`
	Requirement     string                        `json:"requirement"`
	Comparisons     []candidateEvidenceComparison `json:"comparisons"`
	ConfirmAllowed  bool                          `json:"confirm_allowed"`
	NegativeAllowed bool                          `json:"negative_allowed"`
	ConfirmReason   string                        `json:"confirm_blocked_reason,omitempty"`
	NegativeReason  string                        `json:"negative_blocked_reason,omitempty"`
}

// candidateEvidenceGateActive는 새 응답 근거 봉투가 있는 문맥인지다. 봉투가 하나도 없는 옛 문맥은 기존 #950 규칙만 적용해
// 이미 저장된 판정의 재조회 의미를 바꾸지 않는다.
func candidateEvidenceGateActive(c CandidateAssessmentContext) bool {
	if c.Original.OriginalObservation != nil && c.Original.OriginalObservation.ResponseEvidence != nil {
		return true
	}
	for _, row := range c.Followup {
		if row.ResponseEvidence != nil {
			return true
		}
	}
	return false
}

func candidateEvidenceRequirement(candidate db.DiagnosticCandidate) string {
	class := strings.ToLower(strings.TrimSpace(candidate.VulnClass))
	switch class {
	case "security_headers", "security headers", "session_cookie", "session cookie", "cors", "cors_misconfiguration", "clickjacking":
		return candidateEvidenceHeaders
	}
	if candidate.VerificationClaim == "html_nosniff_missing" || candidate.VerificationClaim == "server_version_banner" {
		return candidateEvidenceHeaders
	}
	return candidateEvidenceStructure
}

func sourceRefOf(row candidateverify.StoredObservationV2) candidateVerificationSourceRef {
	return candidateVerificationSourceRef{row.Source.CallRef, row.Source.Observation.AttemptRef, row.Source.Digest}
}

// followupRole은 계획 행동의 종류로 역할을 정한다. 저장 관찰 평가(계획 없음)의 독립 관찰은 같은 기능의 재현이다.
func followupRole(c CandidateAssessmentContext, row candidateverify.StoredObservationV2) responseevidence.Role {
	for i, action := range c.Plan.Actions {
		if row.Source.Observation.RuleRef == fmt.Sprintf("candidate-llm-%d", i+1) && action.Kind == "normal_control" {
			return responseevidence.RoleNormalControl
		}
	}
	return responseevidence.RoleReproduction
}

func evidenceUsable(ev responseevidence.Evidence, requirement string) bool {
	if responseevidence.Validate(ev) != nil {
		return false
	}
	if requirement == candidateEvidenceHeaders {
		return ev.Headers != nil && (ev.State.Structured() || ev.State == responseevidence.StateUnsupportedMedia)
	}
	if !ev.State.Structured() {
		return false
	}
	// 빈 응답은 구조가 '없음'으로 확인된 것이다. 본문 해석 상한으로 구조를 못 담았으면 쓸 수 없다.
	return ev.Empty || ev.JSON != nil || ev.HTML != nil || ev.Text != nil
}

// authLost는 재현 응답이 원본과 다른 인증 상태로 보이는지다(로그인 화면·인증 요구·재인증 이동).
func authLost(original, reproduction responseevidence.Evidence) bool {
	switch reproduction.StatusCode {
	case 401, 407:
		return original.StatusCode != reproduction.StatusCode
	case 403:
		return original.StatusCode != 403
	}
	if reproduction.StatusCode >= 300 && reproduction.StatusCode < 400 && (original.StatusCode < 300 || original.StatusCode >= 400) {
		return true
	}
	loginNow := reproduction.HTML != nil && reproduction.HTML.PasswordInput
	loginBefore := original.HTML != nil && original.HTML.PasswordInput
	return loginNow && !loginBefore
}

// 확정을 막는 재현 불일치 차이다. 목록 길이·값 변화처럼 정상 데이터 변동일 수 있는 차이는 모델 해석에 맡긴다.
var reproductionBreaking = map[string]bool{responseevidence.DiffStatus: true, responseevidence.DiffMedia: true,
	responseevidence.DiffEmpty: true, responseevidence.DiffRootType: true, responseevidence.DiffErrorShape: true,
	responseevidence.DiffLoginForm: true}

// reviewCandidateEvidence는 원본·재현·정상 대조 근거를 비교하고 결정적 권고 허용 여부를 정한다.
func reviewCandidateEvidence(c CandidateAssessmentContext) *CandidateEvidenceReview {
	if !candidateEvidenceGateActive(c) {
		return nil
	}
	review := &CandidateEvidenceReview{Version: candidateEvidenceGateVersion, Requirement: candidateEvidenceRequirement(c.Original.Candidate),
		Comparisons: []candidateEvidenceComparison{}}
	block := func(confirm, negative string) {
		if confirm != "" && review.ConfirmReason == "" {
			review.ConfirmReason = confirm
		}
		if negative != "" && review.NegativeReason == "" {
			review.NegativeReason = negative
		}
	}
	if c.Original.OriginalObservation == nil {
		block(evidenceReasonUnavailable, evidenceReasonUnavailable)
		return finishReview(review)
	}
	original := *c.Original.OriginalObservation
	originalEv := candidateverify.RowResponseEvidence(original)
	if !evidenceUsable(originalEv, review.Requirement) {
		block(evidenceReasonUnavailable, evidenceReasonUnavailable)
	}
	var firstReproduction *candidateverify.StoredObservationV2
	reproductions := 0
	for i := range c.Followup {
		row := c.Followup[i]
		role := followupRole(c, row)
		ev := candidateverify.RowResponseEvidence(row)
		cmp := responseevidence.Compare(originalEv, ev, responseevidence.RoleOriginal, role)
		review.Comparisons = append(review.Comparisons, candidateEvidenceComparison{Left: sourceRefOf(original), Right: sourceRefOf(row), Comparison: cmp})
		if role != responseevidence.RoleReproduction {
			continue
		}
		reproductions++
		if firstReproduction == nil {
			firstReproduction = &c.Followup[i]
		}
		if !evidenceUsable(ev, review.Requirement) {
			block(evidenceReasonUnavailable, evidenceReasonUnavailable)
			continue
		}
		if cmp.Relation == responseevidence.RelationIncomparable {
			block(evidenceReasonIncomparable, evidenceReasonIncomparable)
			continue
		}
		if authLost(originalEv, ev) {
			// 인증 상태가 바뀐 재현은 확정도 반박도 하지 못한다.
			block(evidenceReasonAuthLost, evidenceReasonAuthLost)
			continue
		}
		for _, d := range cmp.Differences {
			if reproductionBreaking[d.Kind] {
				block(evidenceReasonInconsistent, "")
			}
			if d.Kind == responseevidence.DiffMedia || d.Kind == responseevidence.DiffRootType {
				// 다른 형식의 응답은 같은 기능에 닿았다고 볼 수 없다. 반박 근거로도 쓰지 않는다.
				block("", evidenceReasonInconsistent)
			}
		}
		if ev.StatusCode != originalEv.StatusCode && (ev.StatusCode < 200 || ev.StatusCode >= 300) {
			// 상태가 바뀐 재현이 성공 응답이 아니면(4xx·5xx 등) 요청 실패일 뿐 주장을 반박하는 관찰이 아니다.
			block("", evidenceReasonReproductionKO)
		}
		if review.Requirement == candidateEvidenceStructure && structureCoverageIncomplete(cmp) {
			block(evidenceReasonIncomparable, evidenceReasonIncomparable)
		}
	}
	if reproductions == 0 {
		block(evidenceReasonUnavailable, evidenceReasonUnavailable)
	}
	// 정상 대조는 재현을 대신하지 않는다. 모델이 의미를 읽도록 재현과 대조의 비교도 함께 준다.
	if firstReproduction != nil {
		for _, row := range c.Followup {
			if followupRole(c, row) != responseevidence.RoleNormalControl {
				continue
			}
			cmp := responseevidence.Compare(candidateverify.RowResponseEvidence(*firstReproduction), candidateverify.RowResponseEvidence(row),
				responseevidence.RoleReproduction, responseevidence.RoleNormalControl)
			review.Comparisons = append(review.Comparisons, candidateEvidenceComparison{Left: sourceRefOf(*firstReproduction), Right: sourceRefOf(row), Comparison: cmp})
		}
	}
	return finishReview(review)
}

func finishReview(r *CandidateEvidenceReview) *CandidateEvidenceReview {
	r.ConfirmAllowed, r.NegativeAllowed = r.ConfirmReason == "", r.NegativeReason == ""
	return r
}

// 구조 일부가 상한으로 빠졌으면 누락된 필드·폼·입력에 대해 확정하거나 반박할 수 없다.
// 값 digest와 보호 값의 비교 불가는 별도 사실로 남기되 구조 범위 자체를 잃은 경우에만 막는다.
func structureCoverageIncomplete(c responseevidence.Comparison) bool {
	if c.DifferencesOmitted > 0 {
		return true
	}
	for _, code := range c.NotCompared {
		switch code {
		case "body_structure", "json_duplicate_keys", "json_fields_omitted", "left_fields_omitted", "right_fields_omitted",
			"html_forms_omitted", "html_inputs_omitted", "limit:depth", "limit:fields", "limit:array_items",
			"limit:html_tokens", "limit:forms", "limit:inputs":
			return true
		}
	}
	return false
}
