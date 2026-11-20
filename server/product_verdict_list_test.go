package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/oracle"
)

// 제품 판정 목록 조회 시험(docs/contracts/product-verdict-list-proposal.md, #801). 행마다 판정 상세(OD06)와 같은 진위 확인을
// 하고, 한 행의 실패가 다른 행을 막지 않는다. 변조는 소유 시험 연결의 transaction 안에서만 재현하고 반드시 롤백한다.

const productListJob = "job-synthetic"

func productListDeclaration() [32]byte { return sha256.Sum256([]byte("synthetic declaration")) }

// storeProductVerdictKind는 storeProductVerdict와 같은 결속으로 자원 종류·대상 승인 판을 바꿔 저장한다.
func storeProductVerdictKind(t *testing.T, pg *db.DB, taskID int64, kind, revision string, value oracle.VerdictValue) db.ProductVerdictResult {
	t.Helper()
	v := db.ProductVerdict{TaskID: taskID,
		Binding: db.ProductBinding{JobRef: productListJob, RunRef: "product-run-list", E2PolicyRef: "e2-policy-synthetic", GenerationRef: "generation-synthetic",
			InstanceRef: "instance-synthetic", Policy: db.ProductPolicySource{Ref: "declaration-" + productListJob,
				DigestSHA256: productListDeclaration(), TargetApprovalRevision: revision}},
		Checkpoint:   &db.ProductCheckpoint{JournalID: "journal-synthetic", Sequence: 12, MAC: strings.Repeat("c", 64)},
		ResourceType: kind, ResourceRef: kind + ":" + strings.Repeat("b", 64), ModelTag: "synthetic-model",
		Value: value, PolicyCheck: oracle.StateYes, IdentityCheck: oracle.StateYes, DataCheck: oracle.StateYes,
		OracleReasons:  []oracle.JudgeReason{oracle.ReasonPolicyDenyResolved},
		ProductReasons: []string{}, CandidateRefs: []string{"candidate-1"},
		Evidence:   []db.ProductEvidence{{CallRef: "call-" + kind, AttemptID: "attempt-" + kind, Sequence: 1}},
		ObservedAt: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	out, err := pg.RecordProductVerdict(t.Context(), v)
	if err != nil || !out.Created {
		t.Fatal("제품 판정 저장", err)
	}
	return out
}

func productListTask(t *testing.T, pg *db.DB) int64 {
	t.Helper()
	task, err := pg.CreateTask("synthetic product verdict list", "synthetic only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Exec(`DELETE FROM task_archives WHERE task_id=$1`, task.ID) })
	return task.ID
}

func productListContext(source *diagnosisprep.ProductPolicySource, confirmation *diagnosisprep.ProductConfirmationState) productVerdictContext {
	return productVerdictContext{jobID: productListJob, source: source, confirmation: confirmation}
}

func matchedProductSource() *diagnosisprep.ProductPolicySource {
	d := productListDeclaration()
	return &diagnosisprep.ProductPolicySource{Present: true, SHA256: hex.EncodeToString(d[:]), OwnerAccountLabel: "tester-a", CrossAccountLabel: "tester-b"}
}

// forgeProductRow는 판정 하나의 정본·발급 결속만 바꿔 다시 서명한다. 같은 회차 판정들이 함께 쓰는 근거 manifest는 그대로
// 둔다(manifest를 다시 쓰면 그 회차 판정 모두의 근거가 바뀐다 — 그건 공유 manifest 변조라 모두 실패하는 게 맞다).
func forgeProductRow(t *testing.T, tx *sql.Tx, issuance int64, payload, binding func(map[string]any)) {
	t.Helper()
	var verdict int64
	var payloadRaw, bindingRaw, manifestDigest []byte
	if err := tx.QueryRowContext(t.Context(), `SELECT i.verdict_id,v.payload_canonical,i.binding_canonical,m.digest FROM e3_verdict_issuances i
JOIN e3_oracle_verdicts v ON v.id=i.verdict_id JOIN e3_evidence_manifests m ON m.id=i.manifest_id WHERE i.id=$1`, issuance).
		Scan(&verdict, &payloadRaw, &bindingRaw, &manifestDigest); err != nil {
		t.Fatal(err)
	}
	p, b := verdictDetailDecode(t, payloadRaw), verdictDetailDecode(t, bindingRaw)
	if payload != nil {
		payload(p)
	}
	if binding != nil {
		binding(b)
	}
	payloadRaw, bindingRaw = verdictDetailEncode(t, p), verdictDetailEncode(t, b)
	pd, bd := sha256.Sum256(payloadRaw), sha256.Sum256(bindingRaw)
	var md [32]byte
	copy(md[:], manifestDigest)
	input := productVerdictInputDigest(pd, md, bd)
	if _, err := tx.ExecContext(t.Context(), `UPDATE e3_oracle_verdicts SET payload_canonical=$2,payload_digest=$3 WHERE id=$1`, verdict, payloadRaw, pd[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `UPDATE e3_verdict_issuances SET binding_canonical=$2,input_digest=$3 WHERE id=$1`, issuance, bindingRaw, input[:]); err != nil {
		t.Fatal(err)
	}
}

// productListInTx는 한 transaction에서 변조를 적용하고 목록과, 같은 snapshot의 판정마다 상세를 함께 읽은 뒤 롤백한다.
func productListInTx(t *testing.T, pg *db.DB, task int64, got productVerdictContext, forge func(*sql.Tx)) (productVerdictListEnvelope, int, map[int64]verdictDetailEnvelope) {
	t.Helper()
	tx, err := pg.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(t.Context(), `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	if forge != nil {
		forge(tx)
	}
	exploration, rows, status, _ := loadProductVerdictRows(t.Context(), tx, task)
	if status != http.StatusOK {
		return productVerdictListEnvelope{}, status, nil
	}
	seals, err := loadProductSealChecks(t.Context(), tx, task, productVerdictRunRefs(rows))
	if err != nil {
		t.Fatal(err)
	}
	env, status, _ := assembleProductVerdictList(task, exploration, productListJob, rows, got, seals)
	details := map[int64]verdictDetailEnvelope{}
	for _, r := range rows {
		if !r.verdictID.Valid {
			continue
		}
		sel := verdictDetailSelector{verdictDetailByVerdict, r.verdictID.Int64}
		loaded, s, _ := loadVerdictDetail(t.Context(), tx, task, sel)
		if s != http.StatusOK {
			t.Fatalf("상세 읽기 %d", s)
		}
		loaded.seals = seals
		d, s, _ := assembleVerdictDetail(t.Context(), task, sel, loaded, got)
		switch s {
		case http.StatusOK:
			details[r.verdictID.Int64] = d
		case http.StatusUnprocessableEntity:
			// 상세는 결속 이상(제품 판정의 link 등)을 응답 전체 422로 낸다. 목록에서는 그 행의 진위 실패와 같다.
			details[r.verdictID.Int64] = verdictDetailEnvelope{RecordChecks: &verdictDetailRecordChecks{ReasonCodes: []string{verdictDetailRecordIntegrityFailed}}}
		default:
			t.Fatalf("상세 조립 %d", s)
		}
	}
	return env, status, details
}

func productListItem(t *testing.T, env productVerdictListEnvelope, verdict int64) productVerdictListItem {
	t.Helper()
	for _, item := range env.Items {
		if item.VerdictID != nil && *item.VerdictID == strconv.FormatInt(verdict, 10) {
			return item
		}
	}
	t.Fatalf("목록에 판정 %d 없음", verdict)
	return productVerdictListItem{}
}

// 같은 작업에 정상 행·진위 실패 행을 섞으면 실패 행만 값·검사를 숨기고 사유를 보이며, 나머지 행은 그대로다. 목록과 상세는
// 같은 행에 같은 진위 결과를 낸다. 같은 작업의 평가 판정은 섞이지 않는다.
func TestProductVerdictListRowAuthenticity(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	f := diagnosticE3FixtureFor(t, pg, oracle.Confirmed, true)
	if _, err := pg.RecordControlledFinding(t.Context(), f.prepared, f.markers); err != nil {
		t.Fatal(err)
	}
	task := f.taskID
	doc := storeProductVerdictKind(t, pg, task, "document", "target-revision-1", oracle.Confirmed)
	note := storeProductVerdictKind(t, pg, task, "note", "target-revision-1", oracle.Undecidable)
	img := storeProductVerdictKind(t, pg, task, "image", "target-revision-1", oracle.Confirmed)
	got := productListContext(matchedProductSource(), &diagnosisprep.ProductConfirmationState{Present: true, Status: "completed"})

	for name, tc := range map[string]struct {
		forge func(*testing.T, *sql.Tx)
	}{
		// 다시 서명한 위조: 정본 칸을 바꾸면 발급 칸과 어긋난다.
		"payload_case_ref": {func(t *testing.T, tx *sql.Tx) {
			forgeProductRow(t, tx, note.IssuanceID, func(p map[string]any) { p["case_ref"] = "e3-product:other" }, nil)
		}},
		// 정책 출처 digest가 당시 동결본과 다르다(정본·발급 칸은 서로 맞게 바꿈).
		"policy_digest": {func(t *testing.T, tx *sql.Tx) {
			other := strings.Repeat("d", 64)
			forgeProductRow(t, tx, note.IssuanceID, func(p map[string]any) { p["policy_source"].(map[string]any)["digest_sha256"] = other },
				func(b map[string]any) { b["policy"].(map[string]any)["digest_sha256"] = other })
			if _, err := tx.ExecContext(t.Context(), `UPDATE e3_verdict_issuances SET policy_source_digest=decode($2,'hex') WHERE id=$1`, note.IssuanceID, other); err != nil {
				t.Fatal(err)
			}
		}},
		// 제품 판정에 finding 연결이 붙음(제품 판정은 finding을 만들지 않는다, 결정 §9). 상세는 422, 목록은 그 행만 숨긴다.
		"product_link": {func(t *testing.T, tx *sql.Tx) {
			if _, err := tx.ExecContext(t.Context(), `INSERT INTO e3_verdict_links(issuance_id,task_id,finding_id,purpose,evidence_version) VALUES($1,$2,$3,'initial',1)`,
				note.IssuanceID, task, 9_000_000_000+note.IssuanceID); err != nil {
				t.Fatal(err)
			}
		}},
		// 저장 해시만 깨짐(다시 서명하지 않음).
		"payload_digest": {func(t *testing.T, tx *sql.Tx) {
			if _, err := tx.ExecContext(t.Context(), `UPDATE e3_oracle_verdicts SET payload_digest=decode(repeat('00',32),'hex') WHERE id=$1`, note.VerdictID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			env, status, details := productListInTx(t, pg, task, got, func(tx *sql.Tx) { tc.forge(t, tx) })
			if status != http.StatusOK || len(env.Items) != 3 || env.SourceKind != "product" {
				t.Fatalf("목록: %d %+v", status, env)
			}
			bad := productListItem(t, env, note.VerdictID)
			if bad.RecordStatus != verdictDetailRecordIntegrityFailed || bad.OracleValue != nil || bad.Checks != nil || bad.ResourceRef != nil ||
				bad.EvidenceCalls != nil || len(bad.OracleReasonCodes) != 0 || len(bad.CandidateRefs) != 0 || bad.EvidenceSeal != nil {
				t.Fatalf("진위 실패 행이 값을 보임: %+v", bad)
			}
			for _, ok := range []db.ProductVerdictResult{doc, img} {
				item := productListItem(t, env, ok.VerdictID)
				if item.RecordStatus != productVerdictVerified || item.OracleValue == nil || *item.OracleValue != "confirmed" || item.Checks == nil ||
					item.Checks.Policy != "yes" || *item.EvidenceCalls != 1 || !slices.Equal(item.CandidateRefs, []string{"candidate-1"}) {
					t.Fatalf("정상 행이 실패 행에 막힘: %+v", item)
				}
			}
			// 목록 = 상세(같은 snapshot·같은 대조).
			for verdict, d := range details {
				item := productListItem(t, env, verdict)
				detailFailed := d.Record == nil && d.RecordChecks != nil && slices.Equal(d.RecordChecks.ReasonCodes, []string{verdictDetailRecordIntegrityFailed})
				if detailFailed != (item.RecordStatus == verdictDetailRecordIntegrityFailed) {
					t.Fatalf("판정 %d 목록 %s ≠ 상세 %+v", verdict, item.RecordStatus, d.RecordChecks)
				}
			}
		})
	}
	env, status, _ := productListInTx(t, pg, task, got, nil)
	if status != http.StatusOK || len(env.Items) != 3 {
		t.Fatalf("변조 없는 목록: %d %+v", status, env)
	}
	for _, item := range env.Items {
		if item.RecordStatus != productVerdictVerified {
			t.Fatalf("변조 없는 행이 검증되지 않음: %+v", item)
		}
	}
	if env.PolicySource == nil || env.PolicySource.Ref != "declaration-"+productListJob || env.PolicySource.TargetApprovalRevision == nil ||
		*env.PolicySource.TargetApprovalRevision != "target-revision-1" || *env.PolicySource.OwnerAccountLabel != "tester-a" ||
		*env.PolicySource.CrossAccountLabel != "tester-b" || env.Confirmation == nil || env.Confirmation.Status != "completed" || env.Confirmation.Reason != nil {
		t.Fatalf("최상위 칸: %+v %+v", env.PolicySource, env.Confirmation)
	}
}

// 당시 동결본을 읽을 수 없으면 행마다 원값을 두고 policy_source_unavailable이다. 라벨·최상위 정책 출처는 없다(현재 등록으로
// 대신하지 않는다). 확정 단계 결과는 있음·없음·unreadable을 섞지 않는다.
func TestProductVerdictListContextStates(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	task := productListTask(t, pg)
	v := storeProductVerdictKind(t, pg, task, "document", "target-revision-1", oracle.Confirmed)
	for name, tc := range map[string]struct {
		got          productVerdictContext
		status       string
		policy       bool
		confirmation *productVerdictListConfirmation
	}{
		"not_found":      {got: productListContext(nil, nil), status: verdictDetailPolicySourceMissing},
		"frozen_absent":  {got: productListContext(&diagnosisprep.ProductPolicySource{}, &diagnosisprep.ProductConfirmationState{}), status: verdictDetailPolicySourceMissing},
		"unreadable":     {got: productListContext(matchedProductSource(), &diagnosisprep.ProductConfirmationState{Present: true, Status: diagnosisprep.ConfirmationUnreadable}), status: productVerdictVerified, policy: true, confirmation: &productVerdictListConfirmation{Status: diagnosisprep.ConfirmationUnreadable}},
		"disabled":       {got: productListContext(matchedProductSource(), &diagnosisprep.ProductConfirmationState{Present: true, Status: "disabled", Reason: "declaration_missing"}), status: productVerdictVerified, policy: true},
		"result_missing": {got: productListContext(matchedProductSource(), &diagnosisprep.ProductConfirmationState{}), status: productVerdictVerified, policy: true},
	} {
		t.Run(name, func(t *testing.T) {
			env, status, _ := productListInTx(t, pg, task, tc.got, nil)
			if status != http.StatusOK || len(env.Items) != 1 {
				t.Fatalf("목록: %d %+v", status, env)
			}
			item := productListItem(t, env, v.VerdictID)
			if item.RecordStatus != tc.status || item.OracleValue == nil || *item.OracleValue != "confirmed" {
				t.Fatalf("행: %+v", item)
			}
			if (env.PolicySource != nil) != tc.policy {
				t.Fatalf("최상위 정책 출처: %+v", env.PolicySource)
			}
			raw, _ := json.Marshal(env)
			wire := map[string]any{}
			_ = json.Unmarshal(raw, &wire)
			switch name {
			case "unreadable":
				if env.Confirmation == nil || env.Confirmation.Status != diagnosisprep.ConfirmationUnreadable || env.Confirmation.Reason != nil {
					t.Fatalf("unreadable: %s", raw)
				}
			case "disabled":
				if env.Confirmation == nil || env.Confirmation.Status != "disabled" || env.Confirmation.Reason == nil || *env.Confirmation.Reason != "declaration_missing" {
					t.Fatalf("disabled: %s", raw)
				}
			default:
				// 결과 파일이 없으면 null이다(꺼짐으로 추정하지 않는다).
				if v, ok := wire["confirmation"]; !ok || v != nil {
					t.Fatalf("confirmation이 null이 아님: %s", raw)
				}
			}
		})
	}
}

// 대상 승인 판은 진위 확인을 통과한 행들이 모두 같을 때만 최상위에 싣는다. 평가 판정만 있는 작업은 빈 목록이다.
func TestProductVerdictListRevisionAndEmpty(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	task := productListTask(t, pg)
	storeProductVerdictKind(t, pg, task, "document", "target-revision-1", oracle.Confirmed)
	storeProductVerdictKind(t, pg, task, "note", "target-revision-2", oracle.Confirmed)
	env, status, _ := productListInTx(t, pg, task, productListContext(matchedProductSource(), &diagnosisprep.ProductConfirmationState{}), nil)
	if status != http.StatusOK || env.PolicySource == nil || env.PolicySource.TargetApprovalRevision != nil {
		t.Fatalf("판이 다른 행들: %d %+v", status, env.PolicySource)
	}
	f := diagnosticE3FixtureFor(t, pg, oracle.Confirmed, true)
	if _, err := pg.RecordControlledFinding(t.Context(), f.prepared, f.markers); err != nil {
		t.Fatal(err)
	}
	env, status, _ = productListInTx(t, pg, f.taskID, productListContext(matchedProductSource(), &diagnosisprep.ProductConfirmationState{}), nil)
	raw, _ := json.Marshal(env)
	if status != http.StatusOK || env.Items == nil || len(env.Items) != 0 || !strings.Contains(string(raw), `"items":[]`) {
		t.Fatalf("평가 판정만 있는 작업: %d %s", status, raw)
	}
}

// 한 작업의 판정이 종류 상한(16)을 넘으면 잘라 보내지 않고 422다.
func TestProductVerdictListCap(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	task := productListTask(t, pg)
	for i := 0; i < maxProductVerdictListItems; i++ {
		storeProductVerdictKind(t, pg, task, "kind"+strconv.Itoa(i), "target-revision-1", oracle.Confirmed)
	}
	got := productListContext(matchedProductSource(), &diagnosisprep.ProductConfirmationState{})
	if env, status, _ := productListInTx(t, pg, task, got, nil); status != http.StatusOK || len(env.Items) != maxProductVerdictListItems {
		t.Fatalf("상한 경계: %d %d", status, len(env.Items))
	}
	storeProductVerdictKind(t, pg, task, "kindover", "target-revision-1", oracle.Confirmed)
	if env, status, _ := productListInTx(t, pg, task, got, nil); status != http.StatusUnprocessableEntity || env.Format != "" {
		t.Fatalf("상한 초과: %d %+v", status, env)
	}
	_, status, code := readProductVerdictList(t.Context(), pg.DB, task, productListJob, fixedProductContext{got})
	if status != http.StatusUnprocessableEntity || code != productVerdictsIntegrityFailed {
		t.Fatalf("상한 초과 코드: %d %s", status, code)
	}
}

type fixedProductContext struct{ got productVerdictContext }

func (f fixedProductContext) read(context.Context) (productVerdictContext, error) { return f.got, nil }

type failingProductContext struct{}

func (failingProductContext) read(context.Context) (productVerdictContext, error) {
	return productVerdictContext{}, errors.New("closed")
}

// 조회는 읽기 전용이다. 판정·발급·manifest·link·finding 행 수가 그대로이고, IPC 장애는 503이다.
func TestProductVerdictListReadOnly(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	task := productListTask(t, pg)
	storeProductVerdictKind(t, pg, task, "document", "target-revision-1", oracle.Confirmed)
	count := func() string {
		var out string
		if err := pg.QueryRow(`SELECT concat_ws(',', (SELECT count(*) FROM e3_oracle_verdicts), (SELECT count(*) FROM e3_verdict_issuances),
 (SELECT count(*) FROM e3_evidence_manifests), (SELECT count(*) FROM e3_verdict_links), (SELECT count(*) FROM findings))`).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := count()
	env, status, _ := readProductVerdictList(t.Context(), pg.DB, task, productListJob, fixedProductContext{productListContext(matchedProductSource(), &diagnosisprep.ProductConfirmationState{})})
	if status != http.StatusOK || len(env.Items) != 1 || count() != before {
		t.Fatalf("읽기 전용: %d %s→%s", status, before, count())
	}
	if _, status, code := readProductVerdictList(t.Context(), pg.DB, task, productListJob, failingProductContext{}); status != http.StatusServiceUnavailable || code != productVerdictsUnavailable {
		t.Fatalf("IPC 장애: %d %s", status, code)
	}
	raw, _ := json.Marshal(env)
	for _, secret := range []string{"journal-synthetic", "call-document", "attempt-document", "generation-synthetic", "instance-synthetic", "e2-policy-synthetic", "product-run-list", "synthetic-model"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("내보내지 않는 칸이 응답에 있음 %q: %s", secret, raw)
		}
	}
}

// 경로는 제품 관리 중계의 GET /api/tasks/{task_id}/product-verdicts 정확한 모양만 받는다.
func TestProductVerdictListRequestShape(t *testing.T) {
	for _, tc := range []struct {
		method, target string
		status         int
	}{
		{http.MethodGet, "/api/tasks/42/product-verdicts", http.StatusOK},
		{http.MethodGet, "/api/tasks/42/product-verdicts?x=1", http.StatusBadRequest},
		{http.MethodGet, "/api/tasks/42/product-verdicts?", http.StatusBadRequest},
		{http.MethodGet, "/api/tasks/042/product-verdicts", http.StatusBadRequest},
		{http.MethodGet, "/api/tasks/0/product-verdicts", http.StatusBadRequest},
		{http.MethodGet, "/api/tasks/42/product-verdicts/1", http.StatusBadRequest},
		{http.MethodGet, "/api/tasks/42/x/product-verdicts", http.StatusBadRequest},
		{http.MethodPost, "/api/tasks/42/product-verdicts", http.StatusMethodNotAllowed},
		{http.MethodHead, "/api/tasks/42/product-verdicts", http.StatusMethodNotAllowed},
	} {
		r := httptest.NewRequest(tc.method, tc.target, nil)
		if !ProductVerdictListPath(r) {
			t.Fatalf("%s %s: 목록 경로로 분기하지 않음", tc.method, tc.target)
		}
		if _, status := productVerdictListRequest(r); status != tc.status {
			t.Fatalf("%s %s: %d", tc.method, tc.target, status)
		}
	}
	if ProductVerdictListPath(httptest.NewRequest(http.MethodGet, "/api/tasks/42/finding-verdict-detail", nil)) {
		t.Fatal("다른 경로를 목록으로 봄")
	}
}

// 인증 → 작업 소유(AuthorizeTask) → 저장 판정 읽기 → IPC 한 번 순서다. AuthorizeTask 실패는 작업이 있든 없든 같은 403이다.
func TestProductVerdictListRoute(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	task := productListTask(t, pg)
	storeProductVerdictKind(t, pg, task, "document", "target-revision-1", oracle.Confirmed)
	taskRef := strconv.FormatInt(task, 10)
	path := "/api/tasks/" + taskRef + "/product-verdicts"
	for name, tc := range map[string]struct {
		method, target string
		authErr        error
		owner          string
		job            *diagnosisprep.Job
		jobErr         error
		noEndpoint     bool
		ipcErr         error
		status         int
		code           string
		calls          int
	}{
		"ok":               {owner: "ARTEX", job: &diagnosisprep.Job{ID: productListJob, TaskID: taskRef}, status: 200, calls: 1},
		"unauthenticated":  {authErr: errors.New("인증이 필요합니다"), status: 401, code: verdictDetailAuthRequired},
		"auth_unavailable": {authErr: errManagementAuthenticationUnavailable, status: 503, code: productVerdictsUnavailable},
		"not_owned":        {owner: "ARTEX", status: 403, code: productVerdictsForbidden},
		"missing_task":     {owner: "ARTEX", target: "/api/tasks/" + strconv.FormatInt(task+1_000_000, 10) + "/product-verdicts", status: 403, code: productVerdictsForbidden},
		"job_task_differs": {owner: "ARTEX", job: &diagnosisprep.Job{ID: productListJob, TaskID: "999"}, status: 409, code: productVerdictsBindingUnavailable},
		"job_without_id":   {owner: "ARTEX", job: &diagnosisprep.Job{TaskID: taskRef}, status: 409, code: productVerdictsBindingUnavailable},
		"job_read_error":   {owner: "ARTEX", jobErr: errors.New("prep down"), status: 503, code: productVerdictsUnavailable},
		"not_configured":   {owner: "ARTEX", noEndpoint: true, status: 503, code: productVerdictsUnavailable},
		"ipc_error":        {owner: "ARTEX", job: &diagnosisprep.Job{ID: productListJob, TaskID: taskRef}, ipcErr: errors.New("closed"), status: 503, code: productVerdictsUnavailable, calls: 1},
		"query":            {owner: "ARTEX", target: path + "?verdict_id=1", status: 400, code: productVerdictsInvalidRequest},
		"post":             {method: http.MethodPost, owner: "ARTEX", status: 405, code: productVerdictsInvalidRequest},
	} {
		t.Run(name, func(t *testing.T) {
			d := productListDeclaration()
			endpoint := &verdictDetailTestEndpoint{source: &diagnosisprep.ProductPolicySource{Present: true, SHA256: hex.EncodeToString(d[:]),
				OwnerAccountLabel: "tester-a", CrossAccountLabel: "tester-b"}, err: tc.ipcErr}
			bridge := &diagnosisManagementBridge{
				authenticate:      func(*http.Request) (string, error) { return tc.owner, tc.authErr },
				readPool:          pg.DB,
				candidateJob:      func(context.Context, string, string) (*diagnosisprep.Job, error) { return tc.job, tc.jobErr },
				candidateEndpoint: endpoint,
			}
			if tc.noEndpoint {
				bridge.candidateEndpoint = nil
			}
			method, target := http.MethodGet, path
			if tc.method != "" {
				method = tc.method
			}
			if tc.target != "" {
				target = tc.target
			}
			w := httptest.NewRecorder()
			bridge.serveProductVerdictList(w, httptest.NewRequest(method, target, nil))
			if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" || len(endpoint.calls) != tc.calls {
				t.Fatalf("상태 %d·IPC %d: %s", w.Code, len(endpoint.calls), w.Body.String())
			}
			if tc.status == http.StatusMethodNotAllowed && w.Header().Get("Allow") != http.MethodGet {
				t.Fatal("405에 Allow가 없음")
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if tc.code != "" && body["error"] != tc.code {
				t.Fatalf("오류 코드: %v", body)
			}
			if tc.status == http.StatusOK {
				items, _ := body["items"].([]any)
				first, _ := items[0].(map[string]any)
				if body["format"] != productVerdictListFormat || body["source_kind"] != "product" || len(items) != 1 || first["record_status"] != "verified" {
					t.Fatalf("응답: %s", w.Body.String())
				}
				// 근거 체크포인트는 있는데 봉인 확인 기록이 없으면 봉인 미확인이다(값은 그대로, E1 계약 §7).
				if seal, _ := first["evidence_seal"].(map[string]any); seal["status"] != "unconfirmed" || seal["reason"] != "seal_not_recorded" {
					t.Fatalf("봉인 확인 표시: %s", w.Body.String())
				}
			}
		})
	}
}

// storeSealVerdict는 근거 체크포인트·근거 호출 유무를 바꿔 제품 판정을 저장한다.
func storeSealVerdict(t *testing.T, pg *db.DB, taskID int64, kind string, checkpoint *db.ProductCheckpoint, evidence bool) db.ProductVerdictResult {
	t.Helper()
	v := db.ProductVerdict{TaskID: taskID,
		Binding: db.ProductBinding{JobRef: productListJob, RunRef: "product-run-list", E2PolicyRef: "e2-policy-synthetic", GenerationRef: "generation-synthetic",
			InstanceRef: "instance-synthetic", Policy: db.ProductPolicySource{Ref: "declaration-" + productListJob,
				DigestSHA256: productListDeclaration(), TargetApprovalRevision: "target-revision-1"}},
		Checkpoint:   checkpoint,
		ResourceType: kind, ResourceRef: kind + ":" + strings.Repeat("b", 64), ModelTag: "synthetic-model",
		Value: oracle.Undecidable, PolicyCheck: oracle.StateYes, IdentityCheck: oracle.StateUnknown, DataCheck: oracle.StateUnknown,
		OracleReasons: []oracle.JudgeReason{oracle.ReasonSubjectNotVerified}, ProductReasons: []string{}, CandidateRefs: []string{},
		Evidence:   []db.ProductEvidence{},
		ObservedAt: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	if evidence {
		v.Evidence = []db.ProductEvidence{{CallRef: "call-" + kind, AttemptID: "attempt-" + kind, Sequence: 1}}
	}
	out, err := pg.RecordProductVerdict(t.Context(), v)
	if err != nil || !out.Created {
		t.Fatal("제품 판정 저장", err)
	}
	return out
}

// 판정 행은 바꾸지 않고 회차의 봉인 확인 기록과 근거 체크포인트를 대조해 세 단계로 보인다(E1 계약 §7). 포함 실패는 진위
// 실패라 값을 숨긴다. 목록과 상세가 같은 표시를 낸다.
func TestProductVerdictListEvidenceSeal(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	cp := &db.ProductCheckpoint{JournalID: "journal-synthetic", Sequence: 12, MAC: strings.Repeat("c", 64)}
	head := db.ProductCheckpoint{JournalID: "journal-synthetic", Sequence: 20, MAC: strings.Repeat("e", 64)}
	got := productListContext(matchedProductSource(), &diagnosisprep.ProductConfirmationState{})
	for name, tc := range map[string]struct {
		checkpoint *db.ProductCheckpoint
		evidence   bool
		seal       *db.SealCheckRecord
		forge      string
		status     string
		reason     string
		hidden     bool
	}{
		"confirmed": {checkpoint: cp, evidence: true, seal: &db.SealCheckRecord{Head: head, Checks: []db.SealCheck{{Sequence: 5, MAC: strings.Repeat("a", 64), Included: true}, {Sequence: 12, MAC: cp.MAC, Included: true}}},
			status: productEvidenceSealConfirmed},
		"seal_not_recorded":   {checkpoint: cp, evidence: true, status: productEvidenceSealUnconfirmed, reason: productEvidenceSealNotRecorded},
		"checkpoint_missing":  {evidence: true, status: productEvidenceSealUnconfirmed, reason: productEvidenceSealCheckpointMissing},
		"no_evidence":         {checkpoint: cp},
		"no_evidence_no_seal": {},
		"not_included": {checkpoint: cp, evidence: true, seal: &db.SealCheckRecord{Head: head, Checks: []db.SealCheck{{Sequence: 12, MAC: cp.MAC, Included: false}}},
			status: productEvidenceSealInclusionFailed, hidden: true},
		"not_listed": {checkpoint: cp, evidence: true, seal: &db.SealCheckRecord{Head: head, Checks: []db.SealCheck{{Sequence: 12, MAC: strings.Repeat("d", 64), Included: true}}},
			status: productEvidenceSealInclusionFailed, hidden: true},
		"other_journal": {checkpoint: cp, evidence: true, seal: &db.SealCheckRecord{Head: db.ProductCheckpoint{JournalID: "journal-other", Sequence: 20, MAC: head.MAC},
			Checks: []db.SealCheck{{Sequence: 12, MAC: cp.MAC, Included: true}}}, status: productEvidenceSealInclusionFailed, hidden: true},
		"stored_invalid": {checkpoint: cp, evidence: true, seal: &db.SealCheckRecord{Head: head, Checks: []db.SealCheck{{Sequence: 12, MAC: cp.MAC, Included: true}}},
			forge: `UPDATE e3_seal_checks SET checks='[{"sequence":0,"mac":"x","included":true}]'::jsonb WHERE task_id=$1`, status: productEvidenceSealInclusionFailed, hidden: true},
	} {
		t.Run(name, func(t *testing.T) {
			task := productListTask(t, pg)
			v := storeSealVerdict(t, pg, task, "document", tc.checkpoint, tc.evidence)
			if tc.seal != nil {
				record := *tc.seal
				record.TaskID, record.RunRef = task, "product-run-list"
				if err := pg.RecordSealCheck(t.Context(), record); err != nil {
					t.Fatal(err)
				}
			}
			env, status, details := productListInTx(t, pg, task, got, func(tx *sql.Tx) {
				if tc.forge != "" {
					if _, err := tx.ExecContext(t.Context(), tc.forge, task); err != nil {
						t.Fatal(err)
					}
				}
			})
			if status != http.StatusOK || len(env.Items) != 1 {
				t.Fatalf("목록: %d %+v", status, env)
			}
			item := productListItem(t, env, v.VerdictID)
			seal := item.EvidenceSeal
			switch {
			case tc.status == "":
				if seal != nil {
					t.Fatalf("근거 없는 판정에 봉인 확인 표시: %+v", seal)
				}
			case seal == nil || seal.Status != tc.status || (seal.Reason == nil) != (tc.reason == "") || tc.reason != "" && *seal.Reason != tc.reason:
				t.Fatalf("봉인 확인 표시: %+v", seal)
			}
			if hidden := item.RecordStatus == verdictDetailRecordIntegrityFailed; hidden != tc.hidden || hidden && item.OracleValue != nil || !hidden && item.OracleValue == nil {
				t.Fatalf("값 숨김: %+v", item)
			}
			d := details[v.VerdictID]
			if (d.EvidenceSeal == nil) != (seal == nil) || seal != nil && (d.EvidenceSeal.Status != seal.Status || (d.EvidenceSeal.Reason == nil) != (seal.Reason == nil)) ||
				tc.hidden != (d.Record == nil) {
				t.Fatalf("목록과 상세가 다름: 목록 %+v 상세 %+v", seal, d.EvidenceSeal)
			}
			raw, _ := json.Marshal(item)
			if tc.status != "" && !strings.Contains(string(raw), `"evidence_seal":{"status":"`+tc.status+`","reason":`) {
				t.Fatalf("칸 모양: %s", raw)
			}
		})
	}
}
