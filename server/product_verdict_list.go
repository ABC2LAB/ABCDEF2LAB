package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e3contract"
)

// 제품 판정 목록 조회(docs/contracts/product-verdict-list-proposal.md, #801). 작업 하나의 제품 판정을 한 번에 읽는다. 행마다
// 판정 상세(OD06)와 같은 진위 확인을 한다: 진위 실패 행은 값·검사를 숨기고 사유만, 당시 동결본을 읽을 수 없으면 원값과
// "당시 원본 미확인", 현재 등록·정책으로 대신하지 않는다. 한 행의 실패가 다른 행을 막지 않는다. 422는 상한 초과뿐이다.
// 대상 HTTP·모델 호출·판정 발급·쓰기를 하지 않는다.

const (
	productVerdictListFormat = "jindo.workbench-product-verdicts/v1"
	// 한 작업의 판정 수는 선언의 자원 종류 수 이하다(쪽 나눔 없음). 넘으면 잘라 보내지 않고 422다.
	maxProductVerdictListItems = e3contract.MaxDeclarationTypes
	// 응답 전체 상한은 판정 상세와 같다(OD08).
	maxProductVerdictListBytes = maxVerdictDetailBytes

	productVerdictsInvalidRequest     = "invalid_product_verdicts_request"
	productVerdictsForbidden          = "product_verdicts_forbidden"
	productVerdictsBindingUnavailable = "product_verdicts_binding_unavailable"
	productVerdictsIntegrityFailed    = "product_verdicts_integrity_failed"
	productVerdictsUnavailable        = "product_verdicts_unavailable"

	productVerdictVerified = "verified"
)

type productVerdictListPolicy struct {
	Ref                    string  `json:"ref"`
	DigestSHA256           string  `json:"digest_sha256"`
	TargetApprovalRevision *string `json:"target_approval_revision"`
	OwnerAccountLabel      *string `json:"owner_account_label"`
	CrossAccountLabel      *string `json:"cross_account_label"`
}

type productVerdictListConfirmation struct {
	Status string  `json:"status"`
	Reason *string `json:"reason"`
}

type productVerdictListChecks struct {
	Policy   string `json:"policy"`
	Identity string `json:"identity"`
	Data     string `json:"data"`
}

type productVerdictListItem struct {
	VerdictID          *string                   `json:"verdict_id"`
	RecordStatus       string                    `json:"record_status"`
	ResourceType       *string                   `json:"resource_type"`
	ResourceRef        *string                   `json:"resource_ref"`
	OracleValue        *string                   `json:"oracle_value"`
	Checks             *productVerdictListChecks `json:"checks"`
	OracleReasonCodes  []string                  `json:"oracle_reason_codes"`
	ProductReasonCodes []string                  `json:"product_reason_codes"`
	CandidateRefs      []string                  `json:"candidate_refs"`
	EvidenceCalls      *int                      `json:"evidence_calls"`
	ObservedAt         *string                   `json:"observed_at"`
	// EvidenceSeal은 판정 근거 체크포인트가 봉인된 저널에 포함되는지다(E1 계약 §7). 근거 호출이 없는 판정은 null이다.
	EvidenceSeal *productEvidenceSeal `json:"evidence_seal"`
}

type productVerdictListEnvelope struct {
	Format       string                          `json:"format"`
	TaskID       string                          `json:"task_id"`
	SourceKind   string                          `json:"source_kind"`
	Title        string                          `json:"title"`
	PolicySource *productVerdictListPolicy       `json:"policy_source"`
	Confirmation *productVerdictListConfirmation `json:"confirmation"`
	Items        []productVerdictListItem        `json:"items"`
}

// productVerdictContextReader는 준비 서비스 IPC policy_source 한 번(동결본 digest·계정 라벨·확정 단계 상태)이다.
type productVerdictContextReader interface {
	read(context.Context) (productVerdictContext, error)
}

func productVerdictListFail(stage string, status int, code string) (productVerdictListEnvelope, int, string) {
	if status >= http.StatusInternalServerError || status == http.StatusUnprocessableEntity {
		log.Printf(logT("[product-verdicts] 조회 실패 단계=%s 상태=%d"), stage, status)
	}
	return productVerdictListEnvelope{}, status, code
}

// readProductVerdictList는 한 Repeatable Read 읽기 전용 transaction에서 작업의 제품 판정 행을 읽고, IPC 한 번으로 받은
// 당시 동결본·확정 단계 상태로 행마다 진위를 확인한다. jobID는 조회를 허가한 작업의 회차다.
func readProductVerdictList(ctx context.Context, pool *sql.DB, taskID int64, jobID string, reader productVerdictContextReader) (productVerdictListEnvelope, int, string) {
	if ctx == nil || pool == nil || taskID <= 0 || jobID == "" || reader == nil {
		return productVerdictListFail("input", http.StatusBadRequest, productVerdictsInvalidRequest)
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return productVerdictListFail("begin", http.StatusServiceUnavailable, productVerdictsUnavailable)
	}
	defer tx.Rollback()
	exploration, rows, status, code := loadProductVerdictRows(ctx, tx, taskID)
	if status != http.StatusOK {
		return productVerdictListFail("load", status, code)
	}
	seals, err := loadProductSealChecks(ctx, tx, taskID, productVerdictRunRefs(rows))
	if err != nil {
		return productVerdictListFail("seal-checks", http.StatusServiceUnavailable, productVerdictsUnavailable)
	}
	if tx.Commit() != nil {
		return productVerdictListFail("commit", http.StatusServiceUnavailable, productVerdictsUnavailable)
	}
	got, err := reader.read(ctx)
	if err != nil {
		return productVerdictListFail("policy-source", http.StatusServiceUnavailable, productVerdictsUnavailable)
	}
	return assembleProductVerdictList(taskID, exploration, jobID, rows, got, seals)
}

// loadProductVerdictRows는 작업의 제품 판정 발급을 판정 상세와 같은 칸으로 읽는다. 상한을 넘으면 잘라 읽지 않고 422다.
func loadProductVerdictRows(ctx context.Context, q reportReadQuery, taskID int64) (int64, []verdictDetailRow, int, string) {
	var exploration int64
	err := q.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL`, taskID).Scan(&exploration)
	if err != nil {
		// 조회는 준비 서비스가 작업 소유를 허가한 뒤라, 작업 행을 못 읽는 것은 저장 장애다.
		return 0, nil, http.StatusServiceUnavailable, productVerdictsUnavailable
	}
	rows, err := q.QueryContext(ctx, `SELECT `+verdictDetailColumns+`
 FROM e3_verdict_issuances i LEFT JOIN e3_oracle_verdicts v ON v.id=i.verdict_id
 LEFT JOIN e3_evidence_manifests m ON m.id=i.manifest_id
 LEFT JOIN e3_verdict_links l ON l.issuance_id=i.id
 LEFT JOIN findings f ON f.id=l.finding_id
 WHERE i.task_id=$1 AND i.source_kind=$2 ORDER BY i.id LIMIT $3`, taskID, db.E3SourceProduct, maxProductVerdictListItems+1)
	if err != nil {
		return 0, nil, http.StatusServiceUnavailable, productVerdictsUnavailable
	}
	defer rows.Close()
	var out []verdictDetailRow
	for rows.Next() {
		if len(out) == maxProductVerdictListItems {
			return 0, nil, http.StatusUnprocessableEntity, productVerdictsIntegrityFailed
		}
		var r verdictDetailRow
		if r.scan(rows) != nil {
			return 0, nil, http.StatusServiceUnavailable, productVerdictsUnavailable
		}
		out = append(out, r)
	}
	if rows.Err() != nil {
		return 0, nil, http.StatusServiceUnavailable, productVerdictsUnavailable
	}
	return exploration, out, http.StatusOK, ""
}

// assembleProductVerdictList는 행마다 판정 상세(OD06)와 같은 검사로 진위를 확인하고 공개 칸만 옮긴다. DB를 다시 읽지 않는다.
func assembleProductVerdictList(taskID, exploration int64, jobID string, rows []verdictDetailRow, got productVerdictContext, seals productSealChecks) (productVerdictListEnvelope, int, string) {
	got.jobID = jobID
	env := productVerdictListEnvelope{Format: productVerdictListFormat, TaskID: strconv.FormatInt(taskID, 10),
		SourceKind: db.E3SourceProduct, Title: verdictDetailProductTitle, Items: make([]productVerdictListItem, 0, len(rows))}
	if c := got.confirmation; c != nil && c.Present {
		env.Confirmation = &productVerdictListConfirmation{Status: c.Status}
		if c.Reason != "" {
			reason := c.Reason
			env.Confirmation.Reason = &reason
		}
	}
	revisions := map[string]bool{}
	for _, r := range rows {
		item, ok := productVerdictListRow(taskID, exploration, r, got, seals)
		if !ok {
			return productVerdictListFail("array-limit", http.StatusUnprocessableEntity, productVerdictsIntegrityFailed)
		}
		if item.RecordStatus != verdictDetailRecordIntegrityFailed && item.revision != "" {
			revisions[item.revision] = true
		}
		env.Items = append(env.Items, item.productVerdictListItem)
	}
	if s := got.source; s != nil && s.Present {
		policy := &productVerdictListPolicy{Ref: verdictDetailProductPolicyRefPrefix + jobID, DigestSHA256: s.SHA256}
		// 대상 승인 판은 IPC가 주지 않는다. 진위 확인을 통과한 행들이 모두 같은 판을 가리킬 때만 싣는다.
		if len(revisions) == 1 {
			for revision := range revisions {
				policy.TargetApprovalRevision = &revision
			}
		}
		if s.OwnerAccountLabel != "" && s.CrossAccountLabel != "" {
			owner, cross := s.OwnerAccountLabel, s.CrossAccountLabel
			policy.OwnerAccountLabel, policy.CrossAccountLabel = &owner, &cross
		}
		env.PolicySource = policy
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return productVerdictListFail("encode", http.StatusServiceUnavailable, productVerdictsUnavailable)
	}
	if len(raw) > maxProductVerdictListBytes {
		return productVerdictListFail("size-limit", http.StatusUnprocessableEntity, productVerdictsIntegrityFailed)
	}
	return env, http.StatusOK, ""
}

type productVerdictListRowView struct {
	productVerdictListItem
	revision string
}

// productVerdictListRow는 행 하나를 판정 상세와 같은 규칙으로 확인한다. ok=false는 배열 상한 초과(응답 전체 422)다.
func productVerdictListRow(taskID, exploration int64, r verdictDetailRow, got productVerdictContext, seals productSealChecks) (productVerdictListRowView, bool) {
	var out productVerdictListRowView
	if r.verdictID.Valid {
		out.VerdictID = verdictDetailString(r.verdictID.Int64)
	}
	out.OracleReasonCodes, out.ProductReasonCodes, out.CandidateRefs = []string{}, []string{}, []string{}
	failed := func() (productVerdictListRowView, bool) {
		out.RecordStatus = verdictDetailRecordIntegrityFailed
		return out, true
	}
	// 제품 판정은 finding을 만들지 않는다(결정 §9). link가 있으면 저장 불변식 위반이다.
	if r.linkID.Valid {
		return failed()
	}
	verified, ok := verifyProductVerdictDetail(taskID, exploration, r)
	if !ok || verified.policyReq == nil {
		return failed()
	}
	status := productVerdictVerified
	switch got.check(*verified.policyReq) {
	case verdictPolicySourceMatched:
	case verdictPolicySourceUnavailable:
		status = verdictDetailPolicySourceMissing
	default:
		return failed()
	}
	if verified.candidates > maxVerdictDetailCandidateRefs {
		return out, false
	}
	for _, list := range verified.arrays {
		if len(list) > maxVerdictDetailReasons {
			return out, false
		}
	}
	record, ok := verified.record.(verdictDetailProductRecord)
	if !ok {
		return failed()
	}
	// 봉인 포함 실패(되감기·다른 분기)는 진위 실패다. 값은 숨기고 봉인 확인 표시만 남긴다.
	seal, sealFailed := productEvidenceSealFor(verified, seals)
	if sealFailed {
		row, ok := failed()
		row.EvidenceSeal = seal
		return row, ok
	}
	out.EvidenceSeal = seal
	out.RecordStatus = status
	out.ResourceType, out.ResourceRef, out.OracleValue = &record.ResourceType, &record.ResourceRef, &record.OracleValue
	out.Checks = &productVerdictListChecks{Policy: record.PolicyCheck, Identity: record.IdentityCheck, Data: record.DataCheck}
	out.OracleReasonCodes, out.ProductReasonCodes, out.CandidateRefs = record.ReasonCodes, record.ProductReasonCodes, record.CandidateRefs
	evidence := record.EvidenceCount
	out.EvidenceCalls = &evidence
	out.ObservedAt = &record.ObservedAt
	out.revision = verified.policyReq.TargetApprovalRevision
	return out, true
}
