package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/oracle"
)

// 제품 접근통제 지도(docs/contracts/product-access-map.md). 저장된 제품 판정과 회차의 정책 선언 동결본(준비 서비스 IPC
// policy_source)으로 수평 지도(자원 종류 × 소유 A·교차 B)를 읽을 때 만든다. KG 결속·새 표를 쓰지 않고, 평가 지도
// (access_map.go)와 응답 형식·상태 값을 섞지 않는다. 행 진위 확인은 제품 판정 목록과 같은 함수(productVerdictListRow)다.

const (
	productAccessMapFormat = "jindo.workbench-product-access-map/v1"
	productAccessMapAxis   = "horizontal"

	productAccessMapInvalidRequest     = "invalid_product_access_map_request"
	productAccessMapForbidden          = "product_access_map_forbidden"
	productAccessMapBindingUnavailable = "product_access_map_binding_unavailable"
	productAccessMapNoDeclaration      = "product_access_map_no_declaration"
	productAccessMapRolesUndeclared    = "product_access_map_roles_undeclared"
	productAccessMapIntegrityFailed    = "product_access_map_integrity_failed"
	productAccessMapUnavailable        = "product_access_map_unavailable"

	productMapRoleOwner = "owner"
	productMapRoleCross = "cross"

	// 셀 상태(§2.3). owner_control·na는 제품 지도에만 있다.
	productMapStateOwnerControl = "owner_control"
	productMapStateNA           = "na"
	productMapStateConfirmed    = "confirmed"
	productMapStateSuspected    = "suspected"
	productMapStateUnknown      = "unknown"

	productMapNABasisShared = "declared_shared"

	// 투영 오류(§2.3). 오라클 사유와 따로 둔다.
	productMapErrSelectionAmbiguous = "selection_ambiguous"
	productMapErrRecordInvalid      = "record_invalid"
	productMapErrRecordHidden       = "record_hidden"
	productMapErrMissingAfterRun    = "record_missing_after_attempt"
	productMapErrObservation        = "observation_unavailable"
	productMapErrNAMismatch         = "na_evidence_mismatch"

	// 확정 단계가 공유 종류를 건너뛸 때 남기는 사유(e3contract run.go). 지금은 "공유 선언"과 같은 뜻이다(§6 O3).
	productMapSkipNotOwnerOnly = "not_owner_only"
	productMapStatusCompleted  = string(e3contract.StatusCompleted)
)

type productAccessMapPolicy struct {
	Ref          string `json:"ref"`
	DigestSHA256 string `json:"digest_sha256"`
}

type productAccessMapPrincipal struct {
	Label string `json:"label"`
	Role  string `json:"role"`
}

type productAccessMapResource struct {
	ResourceType string `json:"resource_type"`
	Sharing      string `json:"sharing"`
	Confirmable  bool   `json:"confirmable"`
}

type productAccessMapCell struct {
	Principal          string                    `json:"principal"`
	ResourceType       string                    `json:"resource_type"`
	State              string                    `json:"state"`
	Applicable         *bool                     `json:"applicable"`
	NABasis            *string                   `json:"na_basis"`
	VerdictID          *string                   `json:"verdict_id"`
	RecordStatus       *string                   `json:"record_status"`
	OracleValue        *string                   `json:"oracle_value"`
	Checks             *productVerdictListChecks `json:"checks"`
	OracleReasonCodes  []string                  `json:"oracle_reason_codes"`
	ProductReasonCodes []string                  `json:"product_reason_codes"`
	CandidateRefs      []string                  `json:"candidate_refs"`
	ObservedAt         *string                   `json:"observed_at"`
	EvidenceSeal       *productEvidenceSeal      `json:"evidence_seal"`
	ProjectionError    *string                   `json:"projection_error"`
}

type productAccessMapEnvelope struct {
	Format        string                          `json:"format"`
	TaskID        string                          `json:"task_id"`
	SourceKind    string                          `json:"source_kind"`
	Axis          string                          `json:"axis"`
	PolicySource  productAccessMapPolicy          `json:"policy_source"`
	Confirmation  *productVerdictListConfirmation `json:"confirmation"`
	Principals    []productAccessMapPrincipal     `json:"principals"`
	Resources     []productAccessMapResource      `json:"resources"`
	HiddenRecords int                             `json:"hidden_records"`
	Cells         []productAccessMapCell          `json:"cells"`
}

func productAccessMapFail(stage string, status int, code string) (productAccessMapEnvelope, int, string) {
	if status >= http.StatusInternalServerError || status == http.StatusUnprocessableEntity {
		log.Printf(logT("[product-access-map] 조회 실패 단계=%s 상태=%d"), stage, status)
	}
	return productAccessMapEnvelope{}, status, code
}

// readProductAccessMap은 제품 판정 목록과 같은 순서로 읽는다: 한 Repeatable Read 읽기 전용 transaction에서 판정 행·봉인 확인
// 기록 → 준비 서비스 IPC 한 번 → 행마다 진위 확인 → 투영. 대상 HTTP·모델 호출·판정 발급·쓰기를 하지 않는다.
func readProductAccessMap(ctx context.Context, pool *sql.DB, taskID int64, jobID string, reader productVerdictContextReader) (productAccessMapEnvelope, int, string) {
	if ctx == nil || pool == nil || taskID <= 0 || jobID == "" || reader == nil {
		return productAccessMapFail("input", http.StatusBadRequest, productAccessMapInvalidRequest)
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return productAccessMapFail("begin", http.StatusServiceUnavailable, productAccessMapUnavailable)
	}
	defer tx.Rollback()
	exploration, rows, status, _ := loadProductVerdictRows(ctx, tx, taskID)
	switch status {
	case http.StatusOK:
	case http.StatusUnprocessableEntity:
		return productAccessMapFail("load", status, productAccessMapIntegrityFailed)
	default:
		return productAccessMapFail("load", http.StatusServiceUnavailable, productAccessMapUnavailable)
	}
	seals, err := loadProductSealChecks(ctx, tx, taskID, productVerdictRunRefs(rows))
	if err != nil {
		return productAccessMapFail("seal-checks", http.StatusServiceUnavailable, productAccessMapUnavailable)
	}
	if tx.Commit() != nil {
		return productAccessMapFail("commit", http.StatusServiceUnavailable, productAccessMapUnavailable)
	}
	got, err := reader.read(ctx)
	if err != nil {
		return productAccessMapFail("policy-source", http.StatusServiceUnavailable, productAccessMapUnavailable)
	}
	got.jobID = jobID
	return assembleProductAccessMap(taskID, exploration, rows, got, seals)
}

// assembleProductAccessMap은 축을 동결본에서만 만든다. 동결본을 확인할 수 없으면 격자를 만들지 않는다(§2.4): 빈 격자·현재
// 선언·이름순 계정으로 대신하지 않는다. DB를 다시 읽지 않는다.
func assembleProductAccessMap(taskID, exploration int64, rows []verdictDetailRow, got productVerdictContext, seals productSealChecks) (productAccessMapEnvelope, int, string) {
	src := got.source
	switch {
	case src == nil:
		// 준비 서비스가 회차를 찾지 못했거나 동결본을 읽지 못했다(당시 원본 미확인). 축을 확인할 수 없다.
		return productAccessMapFail("policy-source", http.StatusServiceUnavailable, productAccessMapUnavailable)
	case !src.Present:
		return productAccessMapFail("declaration", http.StatusConflict, productAccessMapNoDeclaration)
	case !diagnosisprep.ValidProductPolicySourceValue(*src):
		// IPC 응답 검사를 통과했어야 하는 값이다. 여기서 어긋나면 받는 쪽도 fail-closed로 막는다.
		return productAccessMapFail("policy-source-shape", http.StatusServiceUnavailable, productAccessMapUnavailable)
	case src.OwnerAccountLabel == "" || src.CrossAccountLabel == "":
		return productAccessMapFail("roles", http.StatusConflict, productAccessMapRolesUndeclared)
	}
	conf := got.confirmation
	if conf != nil && conf.Present && !diagnosisprep.ValidProductConfirmationState(*conf) {
		return productAccessMapFail("confirmation-shape", http.StatusServiceUnavailable, productAccessMapUnavailable)
	}

	declared := make(map[string]diagnosisprep.ProductDeclaredType, len(src.Types))
	for _, t := range src.Types {
		declared[t.Type] = t
	}
	status := ""
	var skipped []diagnosisprep.ProductSkippedType
	if conf != nil && conf.Present {
		status, skipped = conf.Status, conf.Skipped
	}
	skippedReason := make(map[string]string, len(skipped))
	for _, s := range skipped {
		t, ok := declared[s.ResourceType]
		// 건너뛴 종류가 동결본에 없거나 시험 대상이거나 겹치면 결과와 동결본이 서로 다른 회차를 가리킨다.
		if _, dup := skippedReason[s.ResourceType]; !ok || t.Confirmable || dup {
			return productAccessMapFail("skipped", http.StatusUnprocessableEntity, productAccessMapIntegrityFailed)
		}
		skippedReason[s.ResourceType] = s.Reason
	}

	hidden := 0
	verified := map[string][]productVerdictListItem{}
	for _, r := range rows {
		item, ok := productVerdictListRow(taskID, exploration, r, got, seals)
		if !ok {
			return productAccessMapFail("array-limit", http.StatusUnprocessableEntity, productAccessMapIntegrityFailed)
		}
		// 진위 확인을 통과한 행만 칸에 놓는다. 숨긴 행과 동결본에 없는 종류를 가리키는 행은 종류를 믿을 수 없어 세기만 한다.
		if item.RecordStatus != productVerdictVerified || item.ResourceType == nil {
			hidden++
			continue
		}
		if _, ok := declared[*item.ResourceType]; !ok {
			hidden++
			continue
		}
		verified[*item.ResourceType] = append(verified[*item.ResourceType], item.productVerdictListItem)
	}

	env := productAccessMapEnvelope{Format: productAccessMapFormat, TaskID: strconv.FormatInt(taskID, 10), SourceKind: db.E3SourceProduct,
		Axis: productAccessMapAxis, PolicySource: productAccessMapPolicy{Ref: verdictDetailProductPolicyRefPrefix + got.jobID, DigestSHA256: src.SHA256},
		Principals: []productAccessMapPrincipal{{Label: src.OwnerAccountLabel, Role: productMapRoleOwner}, {Label: src.CrossAccountLabel, Role: productMapRoleCross}},
		Resources:  make([]productAccessMapResource, 0, len(src.Types)), HiddenRecords: hidden, Cells: make([]productAccessMapCell, 0, 2*len(src.Types))}
	if conf != nil && conf.Present {
		env.Confirmation = &productVerdictListConfirmation{Status: conf.Status}
		if conf.Reason != "" {
			reason := conf.Reason
			env.Confirmation.Reason = &reason
		}
	}
	check := productMapSkipCheck{completed: status == productMapStatusCompleted, any: len(skipped) > 0, reasons: skippedReason}
	for _, t := range src.Types {
		env.Resources = append(env.Resources, productAccessMapResource{ResourceType: t.Type, Sharing: t.Sharing, Confirmable: t.Confirmable})
		env.Cells = append(env.Cells, productMapOwnerCell(src.OwnerAccountLabel, t.Type))
		if t.Confirmable {
			env.Cells = append(env.Cells, productMapCrossCell(src.CrossAccountLabel, t.Type, verified[t.Type], hidden, status))
		} else {
			env.Cells = append(env.Cells, productMapSharedCell(src.CrossAccountLabel, t.Type, verified[t.Type], check))
		}
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return productAccessMapFail("encode", http.StatusServiceUnavailable, productAccessMapUnavailable)
	}
	if len(raw) > maxProductVerdictListBytes {
		return productAccessMapFail("size-limit", http.StatusUnprocessableEntity, productAccessMapIntegrityFailed)
	}
	return env, http.StatusOK, ""
}

// productMapSkipCheck는 공유 종류 칸의 적용불가 근거를 확정 결과의 건너뜀과 대조한다(§2.3). completed면 건너뜀이 공유 종류
// 집합과 같아야 하고, 그 밖의 상태는 건너뜀이 비어 있을 수 있어 비어 있지 않을 때만 대조한다.
type productMapSkipCheck struct {
	completed bool
	any       bool
	reasons   map[string]string
}

func (c productMapSkipCheck) mismatch(resourceType string) bool {
	if !c.completed && !c.any {
		return false
	}
	reason, ok := c.reasons[resourceType]
	return !ok || reason != productMapSkipNotOwnerOnly
}

func productMapCell(principal, resourceType, state string) productAccessMapCell {
	return productAccessMapCell{Principal: principal, ResourceType: resourceType, State: state,
		OracleReasonCodes: []string{}, ProductReasonCodes: []string{}, CandidateRefs: []string{}}
}

func (c *productAccessMapCell) fail(code string) {
	c.State = productMapStateUnknown
	c.ProjectionError = &code
}

// productMapOwnerCell은 A 열이다. 선언이 이 계정을 소유 계정으로 정했다는 뜻뿐이고 A 조회 결과가 아니다(§2.3, O1).
func productMapOwnerCell(owner, resourceType string) productAccessMapCell {
	return productMapCell(owner, resourceType, productMapStateOwnerControl)
}

// productMapSharedCell은 공유 종류의 B 칸이다. 선언의 공유 명시가 적용불가의 양의 근거다.
func productMapSharedCell(cross, resourceType string, items []productVerdictListItem, check productMapSkipCheck) productAccessMapCell {
	c := productMapCell(cross, resourceType, productMapStateNA)
	applicable := false
	c.Applicable = &applicable
	switch {
	case len(items) > 0:
		// 확정 단계는 공유 종류에 판정을 만들지 않는다. 판정이 있으면 모순이다.
		c.fail(productMapErrRecordInvalid)
	case check.mismatch(resourceType):
		c.fail(productMapErrNAMismatch)
	default:
		basis := productMapNABasisShared
		c.NABasis = &basis
	}
	return c
}

// productMapCrossCell은 시험 대상 종류의 B 칸이다. 상태는 §2.3 표의 위에서부터 처음 맞는 것이다. 색을 추정하지 않는다.
func productMapCrossCell(cross, resourceType string, items []productVerdictListItem, hidden int, status string) productAccessMapCell {
	c := productMapCell(cross, resourceType, productMapStateUnknown)
	applicable := true
	c.Applicable = &applicable
	switch {
	case len(items) > 1:
		c.fail(productMapErrSelectionAmbiguous)
		return c
	case len(items) == 0 && hidden > 0:
		// 숨긴 행이 이 종류일 수 있어 "판정 없음"이라 하지 않는다.
		c.fail(productMapErrRecordHidden)
		return c
	case len(items) == 0 && status == productMapStatusCompleted:
		c.fail(productMapErrMissingAfterRun)
		return c
	case len(items) == 0:
		c.fail(productMapErrObservation)
		return c
	}
	item := items[0]
	if item.OracleValue == nil || item.Checks == nil {
		c.fail(productMapErrRecordInvalid)
		return c
	}
	switch oracle.VerdictValue(*item.OracleValue) {
	case oracle.Confirmed:
		ch := item.Checks
		if ch.Policy != string(oracle.StateYes) || ch.Identity != string(oracle.StateYes) || ch.Data != string(oracle.StateYes) {
			c.fail(productMapErrRecordInvalid)
			return c
		}
		c.State = productMapStateConfirmed
	case oracle.Suspected:
		c.State = productMapStateSuspected
	case oracle.Undecidable:
		// 저장된 판단불가 판정이다. 관측 누락(observation_unavailable)과 합치지 않는다.
		c.State = productMapStateUnknown
	default:
		// owner_only는 기대 접근이 거부라 ALLOWED가 나올 수 없다.
		c.fail(productMapErrRecordInvalid)
		return c
	}
	recordStatus := item.RecordStatus
	c.VerdictID, c.RecordStatus, c.OracleValue, c.Checks = item.VerdictID, &recordStatus, item.OracleValue, item.Checks
	c.OracleReasonCodes, c.ProductReasonCodes, c.CandidateRefs = productMapList(item.OracleReasonCodes), productMapList(item.ProductReasonCodes), productMapList(item.CandidateRefs)
	c.ObservedAt, c.EvidenceSeal = item.ObservedAt, item.EvidenceSeal
	return c
}

// productMapList는 응답 배열을 null 대신 []로 둔다(화면 파서가 null 배열을 받지 않는다).
func productMapList(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
