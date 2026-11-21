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
	// StatusInterrupted는 회차 중지로 확정 단계가 도중에 끊긴 경우다. 끝낸 종류의 판정만 있다. 확정 단계를 시작하기 전에
	// 실행이 중지·취소로 끝났고 비활성 근거가 없었던 경우(RunStopped)도 이것이고, 그때는 판정·요청이 0이다.
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
	// DisabledShareUnavailable은 확정 단계 몫(종류 수·요청 시간 상한)을 받지 못한 경우다.
	DisabledShareUnavailable  DisabledReason = "confirmation_share_unavailable"
	DisabledNoConfirmableType DisabledReason = "no_confirmable_type"
	// DisabledAccountRolesUndeclared는 선언에 소유·교차 계정 역할이 없는 경우다(계약 §4).
	DisabledAccountRolesUndeclared DisabledReason = "account_roles_undeclared"
	// DisabledPrivilegeUnconfirmed는 두 계정이 같은 권한 등급이라는 운영자 확인이 없는 경우다(계약 §4).
	DisabledPrivilegeUnconfirmed DisabledReason = "accounts_privilege_unconfirmed"
	// DisabledNoCandidates는 모델 단계가 E② 실행을 한 번도 잡지 않은 채(대상 요청 0) 스스로 끝나 확정할 후보가 없는 경우다
	// (사용자 결정 2026-11-20 분기점 2). 요청 없이 끝낸다.
	DisabledNoCandidates DisabledReason = "no_confirmation_candidates"
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
	// IntegrityJournalIntegrityFailed는 Journal.Sealed가 저널을 검증하지 못한 경우다(ErrJournalIntegrity와 그 밖의 오류).
	IntegrityJournalIntegrityFailed IntegrityReason = "journal_integrity_failed"
	IntegrityJournalEventMismatch   IntegrityReason = "journal_event_mismatch"
	IntegrityAuthorityRevoked       IntegrityReason = "authority_revoked"
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
	// ReasonRequestTimeout은 확정 요청이 전용 시간 상한을 넘겨 결과 불명인 경우다(사용자 결정 2026-11-18). 결과 불명의
	// 한 갈래이지만, 상한 값(VAmPI 파일럿에서 확정)을 정할 근거가 되도록 따로 센다.
	ReasonRequestTimeout ProductReason = "product_request_timeout"
	// ReasonWriteRecordUnavailable은 생성 요청 전에 쓰기 기록을 남기지 못해 보내지 않은 경우다(기록하고 나서 보내기).
	ReasonWriteRecordUnavailable ProductReason = "product_write_record_unavailable"
	// ReasonCreateRequestUnavailable은 생성 요청을 만들 수 없어(객체 ID 난수 실패·표식과 겹침, 본문 틀에 표식·ID를 넣을 수
	// 없음) 보내지 않은 경우다.
	ReasonCreateRequestUnavailable ProductReason = "product_create_request_unavailable"
	// ReasonDataCheckContradiction은 교차(B) 조회가 거부 응답(401·403·404)인데 표식 검사가 "있음"인 경우다. 거부와 데이터
	// 노출이 함께 나온 모순이라 어느 쪽으로도 판정하지 않는다(사용자 결정 2026-11-20).
	ReasonDataCheckContradiction ProductReason = "product_data_check_contradiction"
	// ReasonSessionExpired는 교차(B) 조회가 거부(401·403·404)로 끝난 뒤 B 신원을 다시 확인했더니 신원 응답도 401·403인
	// 경우다. 거부 원인이 접근 통제가 아니라 세션 만료일 수 있어 판정하지 않는다(사용자 결정 2026-11-20 S21 (나)).
	ReasonSessionExpired ProductReason = "product_session_expired"
	// ReasonCrossDenialUnverified는 교차 조회 거부 뒤 B 신원을 다시 확인하지 못한 경우다(보내지 못함·결과 불명·시간 초과·
	// 확정 몫 끝·중지·처음과 다른 주체·그 밖의 비정상 응답). 원인을 모르는 거부를 접근 거부(data_no_match)로 적지 않는다.
	ReasonCrossDenialUnverified ProductReason = "product_cross_denial_unverified"
)

// CrossDenied는 교차 조회 응답이 접근 거부(401·403·404)인지다. 거부 응답이면 표식 검사보다 상태를 먼저 본다
// (사용자 결정 2026-11-20): 표식 검사 결과가 "있음"이면 모순(ReasonDataCheckContradiction)이다. 아니면 B 신원을 한 번
// 더 확인해(S21 (나)) 살아 있을 때만 정상 데이터가 아닌 것으로(cross_normal_data no) 오라클에 넘겨 data_no_match가 된다.
func CrossDenied(status int) bool { return status == 401 || status == 403 || status == 404 }

// WriteOutcome은 확정 단계 생성 요청이 대상에 남긴 결과다.
type WriteOutcome string

const (
	// WriteCreated는 생성 응답을 다 받았고 객체 ID를 꺼낸 경우다.
	WriteCreated WriteOutcome = "created"
	// WriteUnknown은 생성 요청을 보냈거나 보냈을 수 있는데 객체가 생겼는지·ID가 무엇인지 모르는 경우다(시간 초과·결과
	// 불명·비정상 응답·ID 없음). 객체가 생겼다고 보고 리셋 확인 대상에 넣는다.
	WriteUnknown WriteOutcome = "unknown"
	// WriteNotSent는 쓰기 기록 갱신(WriteRecorder.Settle)에만 쓴다: 생성 요청을 보내지 않은 것이 확실하다. E²가 대상으로
	// 연결을 시도하기 전에 거부한 경우(sent=false와 연결 전 거부 사유 넷: 정책 거부·승인 거절·승인 시간 초과·확정 몫 소진)와
	// 회차가 멈춰 생성 요청을 부르지 않은 경우뿐이다. 연결 뒤 실패·결과 불명(E② 오류, 보냈지만 응답 미완료, 시간 초과)은
	// 해당하지 않는다. E²가 그런 경우를 sent=false로 돌려주면 관찰 계약 위반(진위 실패)이고 기록은 ID 모름으로 남는다.
	// 미리 남긴 기록을 거둔다. RunResult.Writes에는 넣지 않는다.
	WriteNotSent WriteOutcome = "not_sent"
)

// ConfirmationWrite는 확정 단계 생성 요청 하나가 대상에 남겼을 수 있는 쓰기다. 서버는 이것으로 대상의 쓰기 기록
// (confirmation_writes)을 남기고, 운영자 리셋 확인 전까지 다음 회차 확정 단계를 시작하지 않는다. 보내지 않은 것이 확실한
// 생성(NotSent)은 넣지 않는다.
type ConfirmationWrite struct {
	ResourceType string       `json:"resource_type"`
	Outcome      WriteOutcome `json:"outcome"`
	// Ref는 생성 관찰의 참조다. 관찰을 받지 못했으면 비어 있다.
	Ref CallRef `json:"ref,omitzero"`
	// ObjectIDDigest는 created일 때만 있다.
	ObjectIDDigest string `json:"object_id_digest,omitempty"`
	// ObjectID는 요청 본문 칸 ID(선언 create.id_field)일 때 확정 단계가 만들어 보낸 ID다. 보내기 전에 알므로 결과와 관계없이
	// 남고, 리셋 확인 때 이 ID로 남은 객체를 조회해 볼 수 있다. 비밀이 아니다(표식과 다른 난수). 응답 경로 ID(id_path)면 비어 있다.
	ObjectID string `json:"object_id,omitempty"`
}

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
	// ObjectIDDigest는 이 종류의 생성 요청이 만든 객체 ID의 실행별 키 digest다(생성이 정상일 때만). 저장 판정의 자원 참조
	// (<종류>:<digest>)가 된다(E1). 객체 ID 원문은 싣지 않는다.
	ObjectIDDigest string           `json:"object_id_digest,omitempty"`
	ObservedAt     time.Time        `json:"observed_at"`
	Case           oracle.CaseInput `json:"-"`
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
	// Writes는 이 회차 생성 요청이 대상에 남겼을 수 있는 쓰기다. 상태와 관계없이(진위 실패로 판정을 버려도) 싣는다.
	Writes []ConfirmationWrite `json:"writes"`
}

// RefusedAtRunStart는 서버가 회차 시작 때(모델 단계 전) 리셋 게이트로 회차를 거절했을 때의 확정 단계 결과다. 요청 0건이고
// 결속·판정·후보·쓰기가 없다. 결과 파일이 없으면 화면·집계가 거절 사유를 모른다(S12 제품 직접 실행, 2026-11-20).
func RefusedAtRunStart(refusal StartRefusal) RunResult {
	return RunResult{Version: RunResultVersion, Status: StatusStartRefused, Reason: string(refusal), Verdicts: []Verdict{},
		Skipped: []SkippedType{}, Candidates: []CandidateConfirmation{}, Writes: []ConfirmationWrite{}}
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
