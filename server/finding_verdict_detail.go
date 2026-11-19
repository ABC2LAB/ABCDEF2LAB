package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

// 판정 상세 조회 1조각 코어(docs/contracts/finding-verdict-detail-decision.md OD01~OD11,
// docs/contracts/workbench-finding-verdict-detail-proposal.md §2.2·§4·§6·§6.1·§8).
// 경로 설치(OD10 제품 관리 중계)는 finding_verdict_detail_route.go다. 여기서는 저장된 판정을 읽고 검증해 공개 칸만 투영한다.
// 대상 HTTP·모델 호출·판정 생성·재검증 실행·쓰기를 하지 않는다. 평가 manifest 결속 대조는 기존 지도 읽기
// 검사(accessMapManifest.matches)를 그대로 쓰며, 그 안의 오라클 재계산은 저장값 일관성 대조일 뿐 새 판정이 아니다.

const (
	verdictDetailFormat = "jindo.workbench-finding-verdict-detail/v1"
	// OD08: 응답 전체 상한과 배열 상한. 넘으면 잘라 보내지 않고 422다.
	maxVerdictDetailBytes         = 256 << 10
	maxVerdictDetailReasons       = 32
	maxVerdictDetailCanaryFields  = 32
	maxVerdictDetailSteps         = 8
	maxVerdictDetailCandidateRefs = 64
	verdictDetailCanonicalTime    = "2006-01-02T15:04:05.000000Z"
	// 판정 선택은 유일 제약(issuance당 link 하나, finding당 link 하나)으로 한 행이다. 둘째 행은 결속 이상이다.
	verdictDetailRowLimit = 2
)

// OD08·초안 §8.1의 오류 코드.
const (
	verdictDetailInvalidRequest  = "invalid_verdict_detail_request"
	verdictDetailNotFound        = "verdict_detail_not_found"
	verdictDetailIntegrityFailed = "verdict_detail_integrity_failed"
	verdictDetailUnavailable     = "verdict_detail_unavailable"
)

// record_checks.reason_codes와 transmission.reason_codes의 고정 어휘.
const (
	verdictDetailRecordIntegrityFailed  = "record_integrity_failed"
	verdictDetailPolicySourceMissing    = "policy_source_unavailable"
	verdictDetailKGSnapshotUnavailable  = "kg_snapshot_unavailable"
	verdictDetailJournalV3Unavailable   = "journal_v3_unavailable"
	verdictDetailEvalTitle              = "수평 IDOR 구조화 판정"
	verdictDetailProductTitle           = "제품 확정 단계 판정"
	verdictDetailProductRecordVersion   = 2
	verdictDetailEvalRecordVersion      = 1
	verdictDetailProductFormat          = "e3-product-verdict/v1"
	verdictDetailProductManifestFormat  = "e3-product-manifest/v1"
	verdictDetailProductBindingFormat   = "e3-product-binding/v1"
	verdictDetailProductPolicyKind      = "product_declaration"
	verdictDetailProductCasePrefix      = "e3-product:"
	verdictDetailProductEvidenceVersion = 1
)

var (
	// 제품 정본 칸의 모양(db/e3_product_verdicts.go 저장 검사와 같은 규칙).
	verdictDetailProductRef  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	verdictDetailProductCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	verdictDetailHexDigest   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type verdictDetailSelectorKind string

const (
	verdictDetailByVerdict   verdictDetailSelectorKind = "verdict"
	verdictDetailByFinding   verdictDetailSelectorKind = "finding"
	verdictDetailByCandidate verdictDetailSelectorKind = "candidate"
)

// verdictDetailSelector는 서로 배타인 selector 하나다(OD01). candidate_id는 2조각(OD05·OD09)이다.
type verdictDetailSelector struct {
	kind verdictDetailSelectorKind
	id   int64
}

// parseVerdictDetailSelector는 query에서 selector 정확히 하나를 읽는다. 중복 키·selector 여럿·모르는 키·
// 정규 십진(1..MaxInt64)이 아닌 값은 거부한다.
// 경로 설치 쪽(findingVerdictDetailRequest)은 url.ParseQuery 오류(예: ';')도 400으로 거부한다.
func parseVerdictDetailSelector(q url.Values) (verdictDetailSelector, bool) {
	if len(q) != 1 {
		return verdictDetailSelector{}, false
	}
	for key, values := range q {
		var kind verdictDetailSelectorKind
		switch key {
		case "verdict_id":
			kind = verdictDetailByVerdict
		case "finding_id":
			kind = verdictDetailByFinding
		case "candidate_id":
			kind = verdictDetailByCandidate
		default:
			return verdictDetailSelector{}, false
		}
		if len(values) != 1 {
			return verdictDetailSelector{}, false
		}
		id, ok := verdictDetailID(values[0])
		if !ok {
			return verdictDetailSelector{}, false
		}
		return verdictDetailSelector{kind: kind, id: id}, true
	}
	return verdictDetailSelector{}, false
}

func verdictDetailID(raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0 && strconv.FormatInt(id, 10) == raw
}

// verdictPolicySourceResult는 당시 정책 동결본 대조 결과다(OD06).
type verdictPolicySourceResult int

const (
	verdictPolicySourceMatched verdictPolicySourceResult = iota + 1
	verdictPolicySourceUnavailable
	verdictPolicySourceMismatch
	verdictPolicySourceError
)

// verdictPolicySourceRequest는 발급 행과 정본이 가리키는 정책 출처다. 응답에는 나가지 않는다.
type verdictPolicySourceRequest struct {
	SourceKind             string
	JobRef, RunRef         string
	Ref                    string
	DigestSHA256           [32]byte
	TargetApprovalRevision string
}

// policySourceReader는 제품 판정의 당시 정책 동결본(실행 결속의 e3-declaration.json, #708)을 읽어 digest를 대조한다.
// 동결본 파일을 읽을 수 없으면 Unavailable, 읽었는데 다르면 Mismatch, 읽기 경로 자체의 장애는 Error다.
// 제품 구현은 준비 서비스 IPC로 묻는 productPolicySourceReader다. 현재 등록·정책을 대신 대입하지 않는다.
type policySourceReader interface {
	CheckPolicySource(context.Context, verdictPolicySourceRequest) verdictPolicySourceResult
}

type verdictDetailSelection struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type verdictDetailIDs struct {
	VerdictID   *string `json:"verdict_id"`
	IssuanceID  *string `json:"issuance_id"`
	ManifestID  *string `json:"manifest_id"`
	FindingID   *string `json:"finding_id"`
	CandidateID *string `json:"candidate_id"`
}

type verdictDetailPolicySource struct {
	Kind                   string `json:"kind"`
	Ref                    string `json:"ref"`
	DigestSHA256           string `json:"digest_sha256"`
	TargetApprovalRevision string `json:"target_approval_revision"`
}

type verdictDetailRecordChecks struct {
	Status        string   `json:"status"`
	BindingStatus string   `json:"binding_status"`
	ReasonCodes   []string `json:"reason_codes"`
}

type verdictDetailPresentation struct {
	Title    *string `json:"title"`
	Severity *string `json:"severity"`
	Mock     bool    `json:"mock"`
}

type verdictDetailView struct {
	DisplayVerdict               *string  `json:"display_verdict"`
	ConfirmationEvidenceComplete *bool    `json:"confirmation_evidence_complete"`
	MissingRequirements          []string `json:"missing_requirements"`
}

type verdictDetailStep struct {
	Role             string `json:"role"`
	AttemptRef       string `json:"attempt_ref"`
	ForwardAttempted *bool  `json:"forward_attempted"`
	RequestWritten   *bool  `json:"request_written"`
	ResponseReceived *bool  `json:"response_received"`
	ResponseComplete *bool  `json:"response_complete"`
}

type verdictDetailTransmission struct {
	State       string              `json:"state"`
	Source      string              `json:"source"`
	ScopeRole   *string             `json:"scope_role"`
	Coverage    string              `json:"coverage"`
	AuditStatus string              `json:"audit_status"`
	ReasonCodes []string            `json:"reason_codes"`
	Steps       []verdictDetailStep `json:"steps"`
}

type verdictDetailFinding struct {
	ID           string  `json:"id"`
	TriageStatus *string `json:"triage_status"`
}

// verdictDetailEvalRecord는 평가 판정(record_version 1) 정본의 공개 칸 allowlist다(초안 §8.2 예시).
type verdictDetailEvalRecord struct {
	CaseRef              string   `json:"case_ref"`
	Axis                 string   `json:"axis"`
	VulnClass            string   `json:"vuln_class"`
	ResourceRef          string   `json:"resource_ref"`
	OracleValue          string   `json:"oracle_value"`
	VulnVerdict          *string  `json:"vuln_verdict"`
	AccessPolicyResult   string   `json:"access_policy_result"`
	PolicyCheck          string   `json:"policy_check"`
	IdentityCheck        string   `json:"identity_check"`
	DataCheck            string   `json:"data_check"`
	ReasonCodes          []string `json:"reason_codes"`
	SameResource         string   `json:"same_resource"`
	OwnerNormalData      string   `json:"owner_normal_data"`
	CrossNormalData      string   `json:"cross_normal_data"`
	NontrivialDataMatch  string   `json:"nontrivial_data_match"`
	ReflectionExcluded   string   `json:"reflection_excluded"`
	TemplateExcluded     string   `json:"template_excluded"`
	CanaryFields         []string `json:"canary_fields"`
	OwnerRef             string   `json:"owner_ref"`
	EvaluatedRef         string   `json:"evaluated_ref"`
	OwnerStatus          string   `json:"owner_status"`
	EvaluatedStatus      string   `json:"evaluated_status"`
	PolicyVersion        string   `json:"policy_version"`
	ExpectedAccess       string   `json:"expected_access"`
	Sharing              string   `json:"sharing"`
	Delegation           string   `json:"delegation"`
	HintMatchesCanonical bool     `json:"hint_matches_canonical"`
	PolicyValidFrom      string   `json:"policy_valid_from"`
	PolicyValidUntil     string   `json:"policy_valid_until"`
	ResetGeneration      string   `json:"reset_generation"`
	ObservedAt           string   `json:"observed_at"`
	CreatedAt            string   `json:"created_at"`
	ModelTag             string   `json:"model_tag"`
	TrafficEvidenceRefs  []string `json:"traffic_evidence_refs"`
}

// verdictDetailProductRecord는 제품 판정(record_version 2) 정본의 공개 칸 allowlist다. 근거 호출은 개수만 싣는다.
type verdictDetailProductRecord struct {
	CaseRef            string   `json:"case_ref"`
	ResourceType       string   `json:"resource_type"`
	ResourceRef        string   `json:"resource_ref"`
	OracleValue        string   `json:"oracle_value"`
	PolicyCheck        string   `json:"policy_check"`
	IdentityCheck      string   `json:"identity_check"`
	DataCheck          string   `json:"data_check"`
	ReasonCodes        []string `json:"reason_codes"`
	ProductReasonCodes []string `json:"product_reason_codes"`
	CandidateRefs      []string `json:"candidate_refs"`
	EvidenceCount      int      `json:"evidence_count"`
	ObservedAt         string   `json:"observed_at"`
	ModelTag           string   `json:"model_tag"`
}

// verdictDetailEnvelope는 초안 §8.1 envelope에 OD08의 최상위 source_kind를 더한 응답이다.
type verdictDetailEnvelope struct {
	Format           string                     `json:"format"`
	TaskID           string                     `json:"task_id"`
	Selection        verdictDetailSelection     `json:"selection"`
	IDs              verdictDetailIDs           `json:"ids"`
	SourceKind       *string                    `json:"source_kind"`
	PolicySource     *verdictDetailPolicySource `json:"policy_source"`
	CandidateLink    *struct{}                  `json:"candidate_link"`
	Availability     string                     `json:"availability"`
	EvaluationStatus string                     `json:"evaluation_status"`
	Record           any                        `json:"record"`
	RecordChecks     *verdictDetailRecordChecks `json:"record_checks"`
	Presentation     verdictDetailPresentation  `json:"presentation"`
	View             verdictDetailView          `json:"view"`
	Transmission     verdictDetailTransmission  `json:"transmission"`
	Finding          *verdictDetailFinding      `json:"finding"`
	Candidate        *verdictDetailCandidate    `json:"candidate"`
}

// verdictDetailCandidate는 candidate selector가 고른 후보의 최소 식별·검토 상태다(초안 §8.2 예시 ④). 후보 원문·근거는 싣지
// 않는다. 연결된 판정이 있어도 후보 상태는 manual_review_required 그대로다(OD05).
type verdictDetailCandidate struct {
	ID           string `json:"id"`
	Verification string `json:"verification"`
}

// 제품 정본의 읽기 형식(db/e3_product_verdicts.go의 저장 형식과 같은 칸).
type verdictDetailProductPolicy struct {
	Kind                   string `json:"kind"`
	Ref                    string `json:"ref"`
	DigestSHA256           string `json:"digest_sha256"`
	TargetApprovalRevision string `json:"target_approval_revision"`
}

type verdictDetailProductEvidence struct {
	CallRef   string `json:"call_ref"`
	AttemptID string `json:"attempt_id"`
	Sequence  uint64 `json:"sequence"`
}

type verdictDetailProductPayload struct {
	Format         string                         `json:"format"`
	SourceKind     string                         `json:"source_kind"`
	CaseRef        string                         `json:"case_ref"`
	ResourceType   string                         `json:"resource_type"`
	ResourceRef    string                         `json:"resource_ref"`
	OracleValue    string                         `json:"oracle_value"`
	PolicyCheck    string                         `json:"policy_check"`
	IdentityCheck  string                         `json:"identity_check"`
	DataCheck      string                         `json:"data_check"`
	OracleReasons  []string                       `json:"oracle_reason_codes"`
	ProductReasons []string                       `json:"product_reason_codes"`
	CandidateRefs  []string                       `json:"candidate_refs"`
	Evidence       []verdictDetailProductEvidence `json:"evidence"`
	ObservedAt     string                         `json:"observed_at"`
	ModelTag       string                         `json:"model_tag"`
	Policy         verdictDetailProductPolicy     `json:"policy_source"`
}

type verdictDetailProductManifest struct {
	Format        string `json:"format"`
	RunRef        string `json:"run_ref"`
	JournalRef    string `json:"journal_ref"`
	JournalDigest string `json:"journal_digest_sha256"`
}

type verdictDetailProductBinding struct {
	Format        string                     `json:"format"`
	TaskID        int64                      `json:"task_id"`
	JobRef        string                     `json:"job_ref"`
	RunRef        string                     `json:"run_ref"`
	E2PolicyRef   string                     `json:"e2_policy_ref"`
	GenerationRef string                     `json:"generation_ref"`
	InstanceRef   string                     `json:"instance_ref"`
	Policy        verdictDetailProductPolicy `json:"policy"`
}

// verdictDetailRow는 한 읽기 snapshot의 LEFT JOIN 한 행이다. 없는 행은 NULL로 남아 부재를 숨기지 않는다.
type verdictDetailRow struct {
	findingID, findingTask, findingEvidence                    sql.NullInt64
	findingStatus                                              sql.NullString
	linkID, linkTask, linkEvidence                             sql.NullInt64
	linkPurpose                                                sql.NullString
	issuanceID, issTask, issExploration                        sql.NullInt64
	issIntent, issMode                                         sql.NullInt64
	issPurpose, issExecution, issCase, issResource, issEvalRun sql.NullString
	issModel, issReset, issKGRef, issKGPolicy, issGeneration   sql.NullString
	issInstance, issSource, issPolicyRef, issPolicyRevision    sql.NullString
	issProductReasons, issCandidates                           sql.NullString
	issKGDigest, issInput, issBinding, issPolicyDigest         []byte
	verdictID, recordVersion                                   sql.NullInt64
	payload, payloadDigest                                     []byte
	oracleValue                                                sql.NullString
	manifestID, manifestTask, manifestRevision                 sql.NullInt64
	manifestExecution                                          sql.NullString
	manifest, manifestDigest                                   []byte
}

type verdictDetailLoaded struct {
	exploration int64
	row         verdictDetailRow
	// candidate는 candidate selector가 먼저 읽은 후보다(그 밖의 selector는 nil).
	candidate *db.DiagnosticCandidate
}

const verdictDetailColumns = `f.id,f.task_id,f.status,f.evidence_version,
 l.id,l.task_id,l.purpose,l.evidence_version,
 i.id,i.task_id,i.exploration_id,i.intent_id,i.mode_version,i.purpose,i.execution_ref,i.case_ref,i.resource_ref,
 i.eval_run_id,i.model_tag,i.reset_generation,i.kg_snapshot_ref,i.kg_policy_version,i.harness_generation_ref,i.target_instance_ref,
 i.source_kind,i.policy_source_ref,i.policy_target_approval_revision,i.product_reason_codes::text,i.candidate_refs::text,
 i.kg_snapshot_digest,i.input_digest,i.binding_canonical,i.policy_source_digest,
 v.id,v.record_version,v.payload_canonical,v.payload_digest,v.oracle_value,
 m.id,m.task_id,m.evidence_revision,m.execution_ref,m.canonical,m.digest`

func (r *verdictDetailRow) scan(rows *sql.Rows) error {
	return rows.Scan(&r.findingID, &r.findingTask, &r.findingStatus, &r.findingEvidence,
		&r.linkID, &r.linkTask, &r.linkPurpose, &r.linkEvidence,
		&r.issuanceID, &r.issTask, &r.issExploration, &r.issIntent, &r.issMode, &r.issPurpose, &r.issExecution, &r.issCase, &r.issResource,
		&r.issEvalRun, &r.issModel, &r.issReset, &r.issKGRef, &r.issKGPolicy, &r.issGeneration, &r.issInstance,
		&r.issSource, &r.issPolicyRef, &r.issPolicyRevision, &r.issProductReasons, &r.issCandidates,
		&r.issKGDigest, &r.issInput, &r.issBinding, &r.issPolicyDigest,
		&r.verdictID, &r.recordVersion, &r.payload, &r.payloadDigest, &r.oracleValue,
		&r.manifestID, &r.manifestTask, &r.manifestRevision, &r.manifestExecution, &r.manifest, &r.manifestDigest)
}

// readFindingVerdictDetail은 한 Repeatable Read 읽기 전용 transaction에서 판정 상세를 읽는다(§6.1).
// 반환 상태가 200이 아니면 envelope는 비어 있고 code가 오류 코드다.
func readFindingVerdictDetail(ctx context.Context, pool *sql.DB, taskID int64, sel verdictDetailSelector, policy policySourceReader) (verdictDetailEnvelope, int, string) {
	if ctx == nil || pool == nil || taskID <= 0 || sel.id <= 0 {
		return verdictDetailFail("input", http.StatusBadRequest, verdictDetailInvalidRequest)
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return verdictDetailFail("begin", http.StatusServiceUnavailable, verdictDetailUnavailable)
	}
	defer tx.Rollback()
	loaded, status, code := loadVerdictDetail(ctx, tx, taskID, sel)
	if status != http.StatusOK {
		return verdictDetailFail("load", status, code)
	}
	if tx.Commit() != nil {
		return verdictDetailFail("commit", http.StatusServiceUnavailable, verdictDetailUnavailable)
	}
	return assembleVerdictDetail(ctx, taskID, sel, loaded, policy)
}

// verdictDetailFail은 단계와 상태만 남긴다. 요청 값·SQL·정본·경로는 기록하지 않는다.
func verdictDetailFail(stage string, status int, code string) (verdictDetailEnvelope, int, string) {
	if status >= http.StatusInternalServerError || status == http.StatusUnprocessableEntity {
		log.Printf(logT("[verdict-detail] 조회 실패 단계=%s 상태=%d"), stage, status)
	}
	return verdictDetailEnvelope{}, status, code
}

// loadVerdictDetail은 selector 소속을 확인하고 issuance·verdict·manifest·link·finding을 LEFT JOIN 한 행으로 읽는다.
func loadVerdictDetail(ctx context.Context, q reportReadQuery, taskID int64, sel verdictDetailSelector) (verdictDetailLoaded, int, string) {
	var out verdictDetailLoaded
	err := q.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL`, taskID).Scan(&out.exploration)
	if errors.Is(err, sql.ErrNoRows) {
		return out, http.StatusNotFound, verdictDetailNotFound
	}
	if err != nil {
		return out, http.StatusServiceUnavailable, verdictDetailUnavailable
	}
	var query string
	arg := any(sel.id)
	switch sel.kind {
	case verdictDetailByVerdict:
		// 다른 task의 판정과 없는 판정은 같은 404다. link·finding은 task로 거르지 않고 읽어 불일치를 422로 드러낸다.
		query = `SELECT ` + verdictDetailColumns + `
 FROM e3_verdict_issuances i LEFT JOIN e3_oracle_verdicts v ON v.id=i.verdict_id
 LEFT JOIN e3_evidence_manifests m ON m.id=i.manifest_id
 LEFT JOIN e3_verdict_links l ON l.issuance_id=i.id
 LEFT JOIN findings f ON f.id=l.finding_id
 WHERE i.verdict_id=$1 AND i.task_id=$2 LIMIT $3`
	case verdictDetailByFinding:
		query = `SELECT ` + verdictDetailColumns + `
 FROM findings f LEFT JOIN e3_verdict_links l ON l.finding_id=f.id
 LEFT JOIN e3_verdict_issuances i ON i.id=l.issuance_id
 LEFT JOIN e3_oracle_verdicts v ON v.id=i.verdict_id
 LEFT JOIN e3_evidence_manifests m ON m.id=i.manifest_id
 WHERE f.id=$1 AND f.task_id=$2 LIMIT $3`
	case verdictDetailByCandidate:
		// 후보를 먼저 읽는다. 다른 작업의 후보와 없는 후보는 같은 404, 저장 형식이 어긋난 후보는 422다.
		c, found, err := db.ReadDiagnosticCandidate(ctx, q, out.exploration, taskID, sel.id)
		if errors.Is(err, db.ErrDiagnosticCandidate) {
			return out, http.StatusUnprocessableEntity, verdictDetailIntegrityFailed
		}
		if err != nil {
			return out, http.StatusServiceUnavailable, verdictDetailUnavailable
		}
		if !found {
			return out, http.StatusNotFound, verdictDetailNotFound
		}
		out.candidate = &c
		// 후보를 참조하는 제품 판정만 고른다(OD05). 참조는 확정 단계가 저장 때 검증한 candidate_refs이고 값은 후보 번호의
		// 정규 십진 문자열이다. 유사도로 찾지 않는다. link·finding은 제품 판정에 없어야 하므로 같이 읽어 드러낸다.
		query = `SELECT ` + verdictDetailColumns + `
 FROM e3_verdict_issuances i LEFT JOIN e3_oracle_verdicts v ON v.id=i.verdict_id
 LEFT JOIN e3_evidence_manifests m ON m.id=i.manifest_id
 LEFT JOIN e3_verdict_links l ON l.issuance_id=i.id
 LEFT JOIN findings f ON f.id=l.finding_id
 WHERE i.candidate_refs @> jsonb_build_array($1::text) AND i.task_id=$2 AND i.source_kind='product' LIMIT $3`
		arg = strconv.FormatInt(sel.id, 10)
	default:
		return out, http.StatusBadRequest, verdictDetailInvalidRequest
	}
	rows, err := q.QueryContext(ctx, query, arg, taskID, verdictDetailRowLimit)
	if err != nil {
		return out, http.StatusServiceUnavailable, verdictDetailUnavailable
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if count > 1 {
			return out, http.StatusUnprocessableEntity, verdictDetailIntegrityFailed
		}
		if out.row.scan(rows) != nil {
			return out, http.StatusServiceUnavailable, verdictDetailUnavailable
		}
	}
	if rows.Err() != nil {
		return out, http.StatusServiceUnavailable, verdictDetailUnavailable
	}
	if count == 0 {
		if out.candidate != nil {
			// 후보는 있고 그 후보를 참조하는 판정이 없다(no_link).
			return out, http.StatusOK, ""
		}
		return out, http.StatusNotFound, verdictDetailNotFound
	}
	return out, http.StatusOK, ""
}

// verdictDetailVerified는 진위 검사를 통과한 판정의 표시 재료다.
type verdictDetailVerified struct {
	record     any
	value      oracle.VerdictValue
	states     []verdictDetailState
	severity   *string
	complete   bool
	steps      []verdictDetailStep
	sent       bool
	policyReq  *verdictPolicySourceRequest
	policyView *verdictDetailPolicySource
	arrays     [][]string
	canary     int
	candidates int
}

type verdictDetailState struct {
	name  string
	state oracle.CheckState
}

// assembleVerdictDetail은 읽은 행을 검증하고 공개 칸만 투영한다. DB를 다시 읽지 않는다.
func assembleVerdictDetail(ctx context.Context, taskID int64, sel verdictDetailSelector, loaded verdictDetailLoaded, policy policySourceReader) (verdictDetailEnvelope, int, string) {
	r := loaded.row
	env := verdictDetailEnvelope{Format: verdictDetailFormat, TaskID: strconv.FormatInt(taskID, 10),
		Selection: verdictDetailSelection{Kind: string(sel.kind), ID: strconv.FormatInt(sel.id, 10)}}
	env.View = verdictDetailView{MissingRequirements: []string{}}
	env.Transmission = verdictDetailTransmission{State: "unknown", Source: "unbound", Coverage: "unavailable", AuditStatus: "unavailable",
		ReasonCodes: []string{}, Steps: []verdictDetailStep{}}

	if c := loaded.candidate; c != nil {
		env.IDs.CandidateID = verdictDetailString(c.ID)
		env.Candidate = &verdictDetailCandidate{ID: *env.IDs.CandidateID, Verification: c.Verification}
	}
	linked := r.linkID.Valid
	if linked {
		// §6: 기존 link의 task·purpose·finding 결속이 어긋나면 잘못 결속된 응답을 내지 않는다.
		// link↔finding·link↔issuance 번호는 JOIN 조건이 같게 만든다. 남은 것은 행 부재와 task 결속이다(없는 행의 NULL은
		// 0이라 task 대조에서 걸린다). purpose는 표 검사(CHECK)가 막지만 결속 규칙이라 다시 본다.
		if r.linkTask.Int64 != taskID || r.linkPurpose.String != "initial" || r.findingTask.Int64 != taskID || r.issTask.Int64 != taskID {
			return verdictDetailFail("link", http.StatusUnprocessableEntity, verdictDetailIntegrityFailed)
		}
	}
	if r.findingID.Valid {
		env.IDs.FindingID = verdictDetailString(r.findingID.Int64)
		env.Finding = &verdictDetailFinding{ID: *env.IDs.FindingID}
		if db.ValidFindingStatus(r.findingStatus.String) {
			status := r.findingStatus.String
			env.Finding.TriageStatus = &status
		}
	}
	if !r.issuanceID.Valid {
		// finding selector(일반 finding만 있고 E③ link가 없음)와 candidate selector(후보를 참조하는 판정이 없음)에서만 생긴다
		// (issuance 없는 link는 위에서 422). 판정 미실행 증명이 아니므로 unknown이다(§6).
		env.Availability, env.EvaluationStatus = "no_link", "unknown"
		return verdictDetailFinish(env)
	}
	env.IDs.IssuanceID = verdictDetailString(r.issuanceID.Int64)
	// 행이 실제로 있을 때만 ID를 싣는다. 발급 칸이 가리키는 없는 행 번호를 내보내지 않는다.
	if r.verdictID.Valid {
		env.IDs.VerdictID = verdictDetailString(r.verdictID.Int64)
	}
	if r.manifestID.Valid {
		env.IDs.ManifestID = verdictDetailString(r.manifestID.Int64)
	}
	source := r.issSource.String
	switch source {
	case db.E3SourceEval:
		// 최초 저장 경로의 confirmed는 finding·link와 한 transaction으로만 생긴다. link 유실은 저장 불변식 위반이다.
		if !linked && r.oracleValue.String == string(oracle.Confirmed) {
			return verdictDetailFail("confirmed-link", http.StatusUnprocessableEntity, verdictDetailIntegrityFailed)
		}
	case db.E3SourceProduct:
		// 제품 판정은 finding을 만들지 않는다(결정 §9). link가 있으면 저장 불변식 위반이다.
		if linked {
			return verdictDetailFail("product-link", http.StatusUnprocessableEntity, verdictDetailIntegrityFailed)
		}
	default:
		return verdictDetailFail("source-kind", http.StatusUnprocessableEntity, verdictDetailIntegrityFailed)
	}
	env.SourceKind = &source
	env.Availability, env.EvaluationStatus = "recorded", "recorded"
	title := verdictDetailEvalTitle
	if source == db.E3SourceProduct {
		title = verdictDetailProductTitle
	}
	env.Presentation.Title = &title
	binding := "current"
	// link가 없으면 두 칸 모두 NULL(0)이라 current다.
	if r.findingEvidence.Int64 != r.linkEvidence.Int64 {
		binding = "historical"
	}
	checks := &verdictDetailRecordChecks{BindingStatus: binding}
	env.RecordChecks = checks

	var verified verdictDetailVerified
	var ok bool
	if source == db.E3SourceEval {
		verified, ok = verifyEvalVerdictDetail(taskID, loaded.exploration, r)
	} else {
		verified, ok = verifyProductVerdictDetail(taskID, loaded.exploration, r)
	}
	// candidate selector로 고른 제품 판정은 그 후보와 같은 실행이어야 한다. 저장 때 검증한 연결이 다른 실행을 가리키면
	// 저장 불변식 위반이다(OD05, §9.1 "동일 실행").
	if ok && loaded.candidate != nil && (verified.policyReq == nil || verified.policyReq.RunRef != loaded.candidate.RunRef) {
		return verdictDetailFail("candidate-run", http.StatusUnprocessableEntity, verdictDetailIntegrityFailed)
	}
	policyMissing := false
	if ok && verified.policyReq != nil {
		result := verdictPolicySourceUnavailable
		if policy != nil {
			result = policy.CheckPolicySource(ctx, *verified.policyReq)
		}
		switch result {
		case verdictPolicySourceMatched:
		case verdictPolicySourceUnavailable:
			policyMissing = true
		case verdictPolicySourceMismatch:
			ok = false
		default:
			return verdictDetailFail("policy-source", http.StatusServiceUnavailable, verdictDetailUnavailable)
		}
	}
	if !ok {
		// §6: 저장 정본·발급 결속·당시 정책 digest 불일치. 그 판정만 표시를 보류하고 잘못 연결된 자료는 싣지 않는다.
		checks.Status, checks.ReasonCodes = "unavailable", []string{verdictDetailRecordIntegrityFailed}
		env.Transmission = verdictDetailUnverifiedTransmission(source)
		return verdictDetailFinish(env)
	}
	if len(verified.steps) > maxVerdictDetailSteps || verified.canary > maxVerdictDetailCanaryFields || verified.candidates > maxVerdictDetailCandidateRefs {
		return verdictDetailFail("array-limit", http.StatusUnprocessableEntity, verdictDetailIntegrityFailed)
	}
	for _, list := range verified.arrays {
		if len(list) > maxVerdictDetailReasons {
			return verdictDetailFail("array-limit", http.StatusUnprocessableEntity, verdictDetailIntegrityFailed)
		}
	}
	env.Record = verified.record
	env.Presentation.Severity = verified.severity
	env.PolicySource = verified.policyView
	switch {
	case source == db.E3SourceEval:
		// 평가 KG snapshot 본문(근거 위치용)은 이 조각에서 읽지 않는다. 진위 검사는 통과했으므로 verified다.
		checks.Status, checks.ReasonCodes = "verified", []string{verdictDetailKGSnapshotUnavailable}
	case policyMissing:
		checks.Status, checks.ReasonCodes = "unavailable", []string{verdictDetailPolicySourceMissing}
	default:
		checks.Status, checks.ReasonCodes = "verified", []string{}
	}
	display := string(verified.value)
	complete := verified.complete
	env.View.ConfirmationEvidenceComplete = &complete
	if policyMissing || binding == "historical" {
		// 당시 정책 원본 미확인이나 현재 finding 증거 변경이면 확정 증거 충분성을 주장하지 않는다(§4, 예시 ⑨).
		env.View.ConfirmationEvidenceComplete = nil
	}
	if binding == "historical" {
		display = string(oracle.Undecidable)
	}
	env.View.DisplayVerdict = &display
	if verified.value != oracle.Allowed {
		// allowed는 정책상 허용 판단이라 확정 증거 누락으로 다루지 않는다(§4, 예시 ③).
		for _, s := range verified.states {
			if s.state != oracle.StateYes {
				env.View.MissingRequirements = append(env.View.MissingRequirements, s.name)
			}
		}
	}
	if source == db.E3SourceEval {
		role := "cross_read"
		state := "unknown"
		if verified.sent {
			state = "sent"
		}
		// 독립 감사 checkpoint 대조 reader가 붙기 전에는 영수증만으로 sent를 정하고 감사 확인은 따로 unavailable이다(OD03).
		env.Transmission = verdictDetailTransmission{State: state, Source: "registered_get", ScopeRole: &role, Coverage: "complete",
			AuditStatus: "unavailable", ReasonCodes: []string{}, Steps: verified.steps}
	} else {
		env.Transmission = verdictDetailUnverifiedTransmission(source)
	}
	return verdictDetailFinish(env)
}

// verdictDetailUnverifiedTransmission은 송신 근거 원천이 없거나 믿을 수 없을 때의 표현이다.
func verdictDetailUnverifiedTransmission(source string) verdictDetailTransmission {
	if source == db.E3SourceProduct {
		// 제품 송신 근거(E② 저널 v3)는 3조각이다(OD03·OD09).
		return verdictDetailTransmission{State: "unknown", Source: "e2proxy", Coverage: "unavailable", AuditStatus: "unavailable",
			ReasonCodes: []string{verdictDetailJournalV3Unavailable}, Steps: []verdictDetailStep{}}
	}
	role := "cross_read"
	return verdictDetailTransmission{State: "unknown", Source: "registered_get", ScopeRole: &role, Coverage: "unavailable", AuditStatus: "unavailable",
		ReasonCodes: []string{verdictDetailRecordIntegrityFailed}, Steps: []verdictDetailStep{}}
}

// verdictDetailFinish는 OD08 전체 상한을 확인한다. 넘으면 잘라 보내지 않는다.
func verdictDetailFinish(env verdictDetailEnvelope) (verdictDetailEnvelope, int, string) {
	raw, err := json.Marshal(env)
	if err != nil {
		return verdictDetailFail("encode", http.StatusServiceUnavailable, verdictDetailUnavailable)
	}
	if len(raw) > maxVerdictDetailBytes {
		return verdictDetailFail("size-limit", http.StatusUnprocessableEntity, verdictDetailIntegrityFailed)
	}
	return env, http.StatusOK, ""
}

func verdictDetailString(id int64) *string {
	s := strconv.FormatInt(id, 10)
	return &s
}

func verdictDetailBool(v bool) *bool { return &v }

// verdictDetailCommonRow는 두 출처가 같은 규칙으로 보는 행 모양이다(§6.1 2단계). 정본 길이(1..64KiB)와 manifest 판 양수는
// 표 검사(CHECK)가 막으므로 다시 세지 않는다. purpose는 같은 CHECK가 있지만 결속 규칙이라 다시 본다.
// 판정·manifest 행 부재는 LEFT JOIN의 NULL(0)이라 판·task 대조에서 걸린다.
func verdictDetailCommonRow(exploration int64, r verdictDetailRow, taskID, version int64) bool {
	return r.recordVersion.Int64 == version && r.issPurpose.String == "initial" &&
		r.manifestTask.Int64 == taskID && r.issExploration.Int64 == exploration && r.manifestExecution.String == r.issExecution.String &&
		verdictDetailDigestIs(r.payloadDigest, sha256.Sum256(r.payload)) && verdictDetailDigestIs(r.manifestDigest, sha256.Sum256(r.manifest))
}

func verdictDetailDigestIs(stored []byte, actual [32]byte) bool {
	return len(stored) == len(actual) && string(stored) == string(actual[:])
}

// evalVerdictInputDigest는 e3record.Prepare의 발급 입력 digest(세 해시를 묶은 canonical JSON)를 다시 만든다.
// canonical은 키 정렬·공백 없음·정수 배열이라 {"Binding":[..],"Manifest":[..],"Payload":[..]}다.
func evalVerdictInputDigest(payload, manifest, binding [32]byte) [32]byte {
	out := []byte{'{'}
	for i, part := range []struct {
		name   string
		digest [32]byte
	}{{"Binding", binding}, {"Manifest", manifest}, {"Payload", payload}} {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, '"')
		out = append(out, part.name...)
		out = append(out, `":[`...)
		for j, b := range part.digest {
			if j > 0 {
				out = append(out, ',')
			}
			out = strconv.AppendUint(out, uint64(b), 10)
		}
		out = append(out, ']')
	}
	return sha256.Sum256(append(out, '}'))
}

// productVerdictInputDigest는 db.RecordProductVerdict의 입력 digest(세 해시 hex 배열의 JSON)를 다시 만든다.
func productVerdictInputDigest(payload, manifest, binding [32]byte) [32]byte {
	raw, _ := json.Marshal([3]string{hex.EncodeToString(payload[:]), hex.EncodeToString(manifest[:]), hex.EncodeToString(binding[:])})
	return sha256.Sum256(raw)
}

// verdictDetailExactKeys는 정본 최상위 객체의 키가 형식의 칸과 정확히 같은지 본다. 누락 키를 0값으로 받지 않는다.
func verdictDetailExactKeys(raw []byte, model any) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	names := verdictDetailJSONNames(reflect.TypeOf(model))
	if len(object) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := object[name]; !ok {
			return false
		}
	}
	return true
}

func verdictDetailJSONNames(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			out = append(out, verdictDetailJSONNames(f.Type)...)
			continue
		}
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" {
			name = f.Name
		}
		out = append(out, name)
	}
	return out
}

// verifyEvalVerdictDetail은 평가 판정(record_version 1)의 정본 해시·발급 입력 digest·발급 칸↔Source·
// manifest 결속(지도 읽기와 같은 검사)·판정 열을 대조한다. 하나라도 어긋나면 false다.
func verifyEvalVerdictDetail(taskID, exploration int64, r verdictDetailRow) (verdictDetailVerified, bool) {
	var out verdictDetailVerified
	if !verdictDetailCommonRow(exploration, r, taskID, verdictDetailEvalRecordVersion) {
		return out, false
	}
	var record accessMapRecord
	var source e3record.Source
	var manifest accessMapManifest
	if strictJSON(r.payload, &record) != nil || strictJSON(r.issBinding, &source) != nil || strictJSON(r.manifest, &manifest) != nil ||
		!verdictDetailExactKeys(r.payload, accessMapRecord{}) || !verdictDetailExactKeys(r.issBinding, e3record.Source{}) ||
		!verdictDetailExactKeys(r.manifest, e3record.Manifest{}) {
		return out, false
	}
	payloadDigest, manifestDigest := sha256.Sum256(r.payload), sha256.Sum256(r.manifest)
	if !verdictDetailDigestIs(r.issInput, evalVerdictInputDigest(payloadDigest, manifestDigest, sha256.Sum256(r.issBinding))) {
		return out, false
	}
	evalRun := ""
	if r.issEvalRun.Valid {
		evalRun = r.issEvalRun.String
		if evalRun == "" {
			return out, false
		}
	}
	e := source.Expected
	if e.Scope.TaskRef != strconv.FormatInt(taskID, 10) || source.ExplorationID != exploration ||
		e.IntentID != r.issIntent.Int64 || source.ModeVersion != uint64(r.issMode.Int64) ||
		e.Scope.RunRef != r.issExecution.String || e.Scope.CaseRef != r.issCase.String || record.CaseRef != r.issCase.String ||
		e.ResourceRef != r.issResource.String || record.ResourceRef != r.issResource.String ||
		source.EvalRunID != evalRun || source.ModelTag != r.issModel.String || record.ModelTag != r.issModel.String ||
		source.ResetGeneration == "" || source.ResetGeneration != r.issReset.String || record.ResetGeneration != r.issReset.String ||
		source.KGSnapshotRef != r.issKGRef.String || !verdictDetailDigestIs(r.issKGDigest, source.KGSnapshotDigest) ||
		e.KGPolicyVersion != r.issKGPolicy.String || record.PolicyVersion != r.issKGPolicy.String ||
		source.GenerationRef != r.issGeneration.String || source.InstanceRef != r.issInstance.String ||
		source.EvidenceRevision != uint64(r.manifestRevision.Int64) ||
		source.FinalCheckpointRef == "" || source.FinalCheckpointDigest == ([32]byte{}) ||
		e.Owner.Label != record.OwnerRef || e.Evaluated.Label != record.EvaluatedRef ||
		string(record.OracleValue) != r.oracleValue.String || !db.ValidSeverity(source.Severity) ||
		!record.valid(source) || !manifest.matches(source, record) {
		return out, false
	}
	signals := record.accessMapSignals
	out.value = record.OracleValue
	out.states = []verdictDetailState{{"policy_check", signals.PolicyCheck}, {"identity_check", signals.IdentityCheck}, {"data_check", signals.DataCheck},
		{"same_resource", signals.SameResource}, {"owner_normal_data", signals.OwnerNormalData}, {"cross_normal_data", signals.CrossNormalData},
		{"nontrivial_data_match", signals.NontrivialDataMatch}, {"reflection_excluded", signals.ReflectionExcluded}, {"template_excluded", signals.TemplateExcluded}}
	out.complete = record.OracleValue == oracle.Confirmed
	for _, s := range out.states {
		out.complete = out.complete && s.state == oracle.StateYes
	}
	severity := source.Severity
	out.severity = &severity
	reasons := make([]string, len(record.ReasonCodes))
	for i, reason := range record.ReasonCodes {
		reasons[i] = string(reason)
	}
	traffic := make([]string, len(record.TrafficEvidenceRefs))
	for i, id := range record.TrafficEvidenceRefs {
		traffic[i] = strconv.FormatInt(id, 10)
	}
	var vuln *string
	if record.VulnVerdict != nil {
		value := string(*record.VulnVerdict)
		vuln = &value
	}
	out.arrays = [][]string{reasons}
	out.canary = len(record.CanaryFields)
	out.record = verdictDetailEvalRecord{CaseRef: record.CaseRef, Axis: record.Axis, VulnClass: record.VulnClass, ResourceRef: record.ResourceRef,
		OracleValue: string(record.OracleValue), VulnVerdict: vuln, AccessPolicyResult: record.AccessPolicyResult,
		PolicyCheck: string(signals.PolicyCheck), IdentityCheck: string(signals.IdentityCheck), DataCheck: string(signals.DataCheck), ReasonCodes: reasons,
		SameResource: string(signals.SameResource), OwnerNormalData: string(signals.OwnerNormalData), CrossNormalData: string(signals.CrossNormalData),
		NontrivialDataMatch: string(signals.NontrivialDataMatch), ReflectionExcluded: string(signals.ReflectionExcluded), TemplateExcluded: string(signals.TemplateExcluded),
		CanaryFields: append([]string{}, record.CanaryFields...), OwnerRef: record.OwnerRef, EvaluatedRef: record.EvaluatedRef,
		OwnerStatus: string(record.OwnerStatus), EvaluatedStatus: string(record.EvaluatedStatus), PolicyVersion: record.PolicyVersion,
		ExpectedAccess: string(record.ExpectedAccess), Sharing: string(record.Sharing), Delegation: string(record.Delegation),
		HintMatchesCanonical: record.HintMatchesCanonical, PolicyValidFrom: verdictDetailTime(record.PolicyValidFrom),
		PolicyValidUntil: verdictDetailTime(record.PolicyValidUntil), ResetGeneration: record.ResetGeneration,
		ObservedAt: verdictDetailTime(record.ObservedAt), CreatedAt: verdictDetailTime(record.CreatedAt), ModelTag: record.ModelTag, TrafficEvidenceRefs: traffic}
	owner, cross := manifest.Evidence.Owner.Receipt, manifest.Evidence.Cross.Receipt
	out.steps = []verdictDetailStep{verdictDetailReceiptStep("owner_read", owner), verdictDetailReceiptStep("cross_read", cross)}
	out.sent = cross.RequestWritten || cross.ResponseReceived
	return out, true
}

// verdictDetailReceiptStep은 등록 GET 영수증의 송신 사실만 옮긴다. 영수증에 없는 칸은 false로 채우지 않고 null이다.
// 세션 handle·계정 라벨·상태 코드는 싣지 않는다.
func verdictDetailReceiptStep(role string, receipt transmit.Receipt) verdictDetailStep {
	return verdictDetailStep{Role: role, AttemptRef: strconv.FormatUint(receipt.Attempt, 10),
		RequestWritten: verdictDetailBool(receipt.RequestWritten), ResponseReceived: verdictDetailBool(receipt.ResponseReceived)}
}

func verdictDetailTime(at time.Time) string {
	return accessMapCanonicalTime(at).Format(verdictDetailCanonicalTime)
}

// verifyProductVerdictDetail은 제품 판정(record_version 2)의 정본 해시·발급 입력 digest·발급 결속(binding↔발급 칸)·
// 정책 출처 칸(참조·digest·대상 승인 판)·제품 사유·후보 칸을 대조한다(OD06).
func verifyProductVerdictDetail(taskID, exploration int64, r verdictDetailRow) (verdictDetailVerified, bool) {
	var out verdictDetailVerified
	if !verdictDetailCommonRow(exploration, r, taskID, verdictDetailProductRecordVersion) || r.manifestRevision.Int64 != verdictDetailProductEvidenceVersion {
		return out, false
	}
	var payload verdictDetailProductPayload
	var binding verdictDetailProductBinding
	var manifest verdictDetailProductManifest
	var reasons, candidates []string
	if strictJSON(r.payload, &payload) != nil || strictJSON(r.issBinding, &binding) != nil || strictJSON(r.manifest, &manifest) != nil ||
		!verdictDetailExactKeys(r.payload, payload) || !verdictDetailExactKeys(r.issBinding, binding) || !verdictDetailExactKeys(r.manifest, manifest) ||
		strictJSON([]byte(r.issProductReasons.String), &reasons) != nil || strictJSON([]byte(r.issCandidates.String), &candidates) != nil {
		return out, false
	}
	payloadDigest, manifestDigest := sha256.Sum256(r.payload), sha256.Sum256(r.manifest)
	if !verdictDetailDigestIs(r.issInput, productVerdictInputDigest(payloadDigest, manifestDigest, sha256.Sum256(r.issBinding))) {
		return out, false
	}
	p := payload.Policy
	if payload.Format != verdictDetailProductFormat || payload.SourceKind != db.E3SourceProduct ||
		manifest.Format != verdictDetailProductManifestFormat || binding.Format != verdictDetailProductBindingFormat ||
		binding.TaskID != taskID || binding.RunRef != r.issExecution.String || manifest.RunRef != r.issExecution.String ||
		!verdictDetailProductRef.MatchString(binding.JobRef) || !verdictDetailProductRef.MatchString(binding.E2PolicyRef) ||
		!verdictDetailProductRef.MatchString(manifest.JournalRef) || !verdictDetailHexDigest.MatchString(manifest.JournalDigest) ||
		binding.GenerationRef == "" || binding.GenerationRef != r.issGeneration.String || binding.GenerationRef != r.issReset.String ||
		binding.InstanceRef == "" || binding.InstanceRef != r.issInstance.String ||
		!verdictDetailProductCode.MatchString(payload.ResourceType) || payload.CaseRef != verdictDetailProductCasePrefix+payload.ResourceType ||
		payload.CaseRef != r.issCase.String || payload.ResourceRef != r.issResource.String ||
		!strings.HasPrefix(payload.ResourceRef, payload.ResourceType+":") || !verdictDetailProductRef.MatchString(payload.ResourceRef) ||
		payload.ModelTag != r.issModel.String || payload.OracleValue != r.oracleValue.String ||
		p != binding.Policy || p.Kind != verdictDetailProductPolicyKind || !verdictDetailProductRef.MatchString(p.Ref) ||
		!verdictDetailProductRef.MatchString(p.TargetApprovalRevision) ||
		p.Ref != r.issPolicyRef.String || p.TargetApprovalRevision != r.issPolicyRevision.String ||
		p.DigestSHA256 != hex.EncodeToString(r.issPolicyDigest) ||
		payload.ProductReasons == nil || payload.CandidateRefs == nil || payload.OracleReasons == nil || payload.Evidence == nil ||
		!slices.Equal(reasons, payload.ProductReasons) || !slices.Equal(candidates, payload.CandidateRefs) {
		return out, false
	}
	// 정본 시각은 UTC 마이크로초 6자리 형식만 받는다(자리 수가 다르면 Parse가 거부한다).
	if _, err := time.Parse(verdictDetailCanonicalTime, payload.ObservedAt); err != nil {
		return out, false
	}
	value := oracle.VerdictValue(payload.OracleValue)
	states := []verdictDetailState{{"policy_check", oracle.CheckState(payload.PolicyCheck)}, {"identity_check", oracle.CheckState(payload.IdentityCheck)},
		{"data_check", oracle.CheckState(payload.DataCheck)}}
	for _, s := range states {
		if s.state != oracle.StateYes && s.state != oracle.StateNo && s.state != oracle.StateUnknown {
			return out, false
		}
	}
	for _, reason := range payload.OracleReasons {
		if !accessMapReason(oracle.JudgeReason(reason)) {
			return out, false
		}
	}
	for _, reason := range payload.ProductReasons {
		if !verdictDetailProductCode.MatchString(reason) {
			return out, false
		}
	}
	for _, ref := range payload.CandidateRefs {
		if !verdictDetailProductRef.MatchString(ref) {
			return out, false
		}
	}
	// 저장 검사와 같은 불변식: 확정은 세 검사 모두 yes, 근거 호출 하나 이상, 제품 사유 없음.
	out.complete = value == oracle.Confirmed && len(payload.Evidence) > 0 && len(payload.ProductReasons) == 0
	for _, s := range states {
		out.complete = out.complete && s.state == oracle.StateYes
	}
	if value == oracle.Confirmed && !out.complete {
		return out, false
	}
	var digest [32]byte
	copy(digest[:], r.issPolicyDigest)
	out.value, out.states = value, states
	out.policyReq = &verdictPolicySourceRequest{SourceKind: db.E3SourceProduct, JobRef: binding.JobRef, RunRef: binding.RunRef,
		Ref: p.Ref, DigestSHA256: digest, TargetApprovalRevision: p.TargetApprovalRevision}
	out.policyView = &verdictDetailPolicySource{Kind: p.Kind, Ref: p.Ref, DigestSHA256: p.DigestSHA256, TargetApprovalRevision: p.TargetApprovalRevision}
	out.arrays = [][]string{payload.OracleReasons, payload.ProductReasons}
	out.candidates = len(payload.CandidateRefs)
	out.record = verdictDetailProductRecord{CaseRef: payload.CaseRef, ResourceType: payload.ResourceType, ResourceRef: payload.ResourceRef,
		OracleValue: payload.OracleValue, PolicyCheck: payload.PolicyCheck, IdentityCheck: payload.IdentityCheck, DataCheck: payload.DataCheck,
		ReasonCodes: append([]string{}, payload.OracleReasons...), ProductReasonCodes: append([]string{}, payload.ProductReasons...),
		CandidateRefs: append([]string{}, payload.CandidateRefs...), EvidenceCount: len(payload.Evidence), ObservedAt: payload.ObservedAt, ModelTag: payload.ModelTag}
	return out, true
}
