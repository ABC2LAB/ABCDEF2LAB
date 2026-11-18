package e3contract

import (
	"time"

	"github.com/Autumn-27/artex/oracle"
)

// RunResultVersion은 확정 단계 결과 형식 판이다.
const RunResultVersion = "e3-confirmation-run/v1"

// ConfirmationStatus는 확정 단계 한 회의 상태다. 회차 상태·결과 화면·공식 회차 run-record에 이 값을 그대로 보인다.
type ConfirmationStatus string

const (
	// StatusDisabled는 쓰는 조건(#596 §3)이 모자라 확정 단계를 돌지 않은 경우다. E² 프로그램 요청은 0건이다.
	StatusDisabled ConfirmationStatus = "disabled"
	// StatusStartRefused는 조건은 갖췄지만 시작 점검(정책 동결본·리셋 확인·표식 등록)에서 멈춘 경우다. 요청 0건이다.
	StatusStartRefused ConfirmationStatus = "start_refused"
	// StatusCompleted는 확정 단계가 끝까지 돈 경우다. 종류마다 판정 행이 있다(UNDECIDABLE 포함).
	StatusCompleted ConfirmationStatus = "completed"
	// StatusInterrupted는 회차 중지로 확정 단계가 도중에 끊긴 경우다. 끝낸 종류의 판정만 있다.
	StatusInterrupted ConfirmationStatus = "interrupted"
	// StatusIntegrityFailed는 결속·진위가 깨진 경우다. 판정 행을 만들지 않는다. "발견 없음"·"판정 없음"으로 보이면 안 된다.
	StatusIntegrityFailed ConfirmationStatus = "confirmation_integrity_failed"
)

// DisabledReason은 StatusDisabled의 사유다.
type DisabledReason string

const (
	// DisabledNotWired는 연결이 아직 없는 경우다(E² Transport·Journal 등). 최종 제품 이미지에서는 나오면 안 된다.
	DisabledNotWired DisabledReason = "not_wired"
	// DisabledNoDeclaration은 유효한 선언 동결본이 없는 회차다(공식 Juice Shop 측정처럼 E③ 없이 도는 회차).
	DisabledNoDeclaration      DisabledReason = "declaration_missing"
	DisabledBindingInvalid     DisabledReason = "binding_invalid"
	DisabledPolicyKind         DisabledReason = "policy_kind_not_product"
	DisabledWriteNotConsented  DisabledReason = "write_not_consented"
	DisabledAccountsUnverified DisabledReason = "test_accounts_unavailable"
	DisabledPolicyWindow       DisabledReason = "policy_window_invalid"
	DisabledNoConfirmableType  DisabledReason = "no_confirmable_type"
)

// IntegrityReason은 StatusIntegrityFailed의 세부 사유다.
type IntegrityReason string

const (
	IntegrityPolicyDigestMismatch IntegrityReason = "policy_digest_mismatch"
	IntegrityPolicyTargetMismatch IntegrityReason = "policy_target_mismatch"
	IntegrityObservationInvalid   IntegrityReason = "observation_invalid"
	IntegrityBindingMismatch      IntegrityReason = "binding_mismatch"
	IntegrityObservationMismatch  IntegrityReason = "observation_mismatch"
	IntegrityObservationReused    IntegrityReason = "observation_reused"
	IntegrityJournalEventMissing  IntegrityReason = "journal_event_missing"
	IntegrityJournalEventMismatch IntegrityReason = "journal_event_mismatch"
	IntegrityAuthorityRevoked     IntegrityReason = "authority_revoked"
)

// ProductReason은 제품 확정 단계의 UNDECIDABLE 세부 사유다(#692 §9.1). 오라클 사유 목록(oracle.JudgeReason)은 바꾸지
// 않고 판정 행의 별도 칸에 둔다.
type ProductReason string

const (
	ReasonEncodingUnsupported       ProductReason = "product_data_encoding_unsupported"
	ReasonPartialResponse           ProductReason = "product_data_partial_response"
	ReasonDataCheckFailed           ProductReason = "product_data_check_failed"
	ReasonOwnerMarkerUnobserved     ProductReason = "product_owner_marker_unobserved"
	ReasonIdentityUnverified        ProductReason = "product_identity_unverified"
	ReasonSubjectsIdentical         ProductReason = "product_subjects_identical"
	ReasonCreateApprovalDenied      ProductReason = "product_create_approval_denied"
	ReasonCreateApprovalTimeout     ProductReason = "product_create_approval_timeout"
	ReasonConfirmationBudgetExhaust ProductReason = "product_confirmation_budget_exhausted"
	// 아래 셋은 #692 §9.1 목록에 더한 것이다. 결과 불명 요청(다시 보내지 않음), E² 정책 거부, 생성 실패(정상 응답이
	// 아니거나 객체 ID를 꺼내지 못함)를 데이터 검사 실패와 섞지 않는다.
	ReasonRequestResultUnknown ProductReason = "product_request_result_unknown"
	ReasonRequestDenied        ProductReason = "product_request_denied"
	ReasonCreateFailed         ProductReason = "product_create_failed"
)

// Verdict는 자원 종류 하나의 판정 행이다. 값은 oracle.Evaluate(Case, ObservedAt)가 만든 그대로다.
type Verdict struct {
	ResourceType   string               `json:"resource_type"`
	Value          oracle.VerdictValue  `json:"oracle_value"`
	PolicyCheck    oracle.CheckState    `json:"policy_check"`
	IdentityCheck  oracle.CheckState    `json:"identity_check"`
	DataCheck      oracle.CheckState    `json:"data_check"`
	OracleReasons  []oracle.JudgeReason `json:"oracle_reason_codes"`
	ProductReasons []ProductReason      `json:"product_reason_codes"`
	CandidateRefs  []string             `json:"candidate_refs"`
	Evidence       []CallRef            `json:"evidence"`
	ObservedAt     time.Time            `json:"observed_at"`
	Case           oracle.CaseInput     `json:"-"`
}

// SkippedType은 확정 대상이 아닌 종류다(owner_only가 아님). 요청을 보내지 않고 판정 행도 만들지 않는다.
type SkippedType struct {
	ResourceType string `json:"resource_type"`
	Reason       string `json:"reason"`
}

// CandidateConfirmation은 모델 단계 후보 하나가 확정 단계에서 어떻게 보이는지다. 후보 자체의 검토 상태
// (manual_review_required)는 바꾸지 않는다. Level은 연결된 판정이 CONFIRMED일 때만 confirmed이고, 그 밖에는 suspected다.
type CandidateConfirmation struct {
	CandidateRef string              `json:"candidate_ref"`
	Level        oracle.VerdictValue `json:"level"`
	ResourceType string              `json:"resource_type,omitempty"`
	// Reason은 진위 실패면 confirmation_integrity_failed, 연결이 안 되면 link_none·link_ambiguous다.
	Reason string `json:"reason,omitempty"`
}

// Candidate는 모델 단계 후보의 비민감 요약이다(메서드와 E² 시도 경로 패턴, pathmask.Readable 모양).
type Candidate struct {
	Ref         string
	Method      string
	PathPattern string
}

// RunResult는 확정 단계 한 회의 결과다. 서버는 회차 상태와 결과 화면에 Status·Reason을 그대로 싣고, 공식 회차 러너는
// run-record에 옮겨 결과 보고에 보인다.
type RunResult struct {
	Version string             `json:"version"`
	Status  ConfirmationStatus `json:"status"`
	Reason  string             `json:"reason,omitempty"`
	// Binding은 검증을 통과한 실행 결속만이다. 결속이 틀려 거부했으면 비운다(검증 전 값을 결과에 남기지 않는다).
	Binding    *RunBinding             `json:"binding,omitempty"`
	Verdicts   []Verdict               `json:"verdicts"`
	Skipped    []SkippedType           `json:"skipped"`
	Candidates []CandidateConfirmation `json:"candidates"`
	// Requests는 E² 프로그램 요청을 부른 횟수다(보내지 않은 것으로 끝난 호출 포함).
	Requests int `json:"requests"`
}

// LeakingSinks는 sinks 중 표식이 어느 모양으로든 들어 있는 이름들이다. 확정 단계를 돈 뒤 저널·영수증·승인 미리보기·
// 로그·평가 내보내기를 이것으로 확인한다. 표식이 계약 모양이 아니면 확인할 수 없으므로 "all"을 돌려준다.
func LeakingSinks(m Marker, sinks map[string][]byte) []string {
	if !m.Valid() {
		return []string{"all"}
	}
	var out []string
	for name, data := range sinks {
		if ContainsMarker(data, m) {
			out = append(out, name)
		}
	}
	return out
}
