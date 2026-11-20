package db

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/oracle"
)

func productVerdictFor(taskID int64, run, resourceType string, value oracle.VerdictValue) ProductVerdict {
	v := ProductVerdict{TaskID: taskID,
		Binding: ProductBinding{JobRef: "job-synthetic", RunRef: run, E2PolicyRef: "e2-policy-synthetic", GenerationRef: "generation-synthetic",
			InstanceRef: "instance-synthetic", Policy: ProductPolicySource{Ref: "declaration-job-synthetic",
				DigestSHA256: sha256.Sum256([]byte("synthetic declaration")), TargetApprovalRevision: "target-revision-3"}},
		Checkpoint:   &ProductCheckpoint{JournalID: "journal-synthetic", Sequence: 12, MAC: strings.Repeat("c", 64)},
		ResourceType: resourceType, ResourceRef: resourceType + ":" + strings.Repeat("a", 64), ModelTag: "synthetic-model",
		Value: value, PolicyCheck: oracle.StateYes, IdentityCheck: oracle.StateYes, DataCheck: oracle.StateYes,
		OracleReasons:  []oracle.JudgeReason{oracle.ReasonPolicyDenyResolved, oracle.ReasonSubjectsDistinctVerified, oracle.ReasonDataNontrivialMatch},
		ProductReasons: []string{}, CandidateRefs: []string{"candidate-1"},
		Evidence:   []ProductEvidence{{CallRef: "call-1", AttemptID: "attempt-1", Sequence: 1}, {CallRef: "call-2", AttemptID: "attempt-2", Sequence: 2}},
		ObservedAt: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	if value == oracle.Undecidable {
		v.DataCheck = oracle.StateUnknown
		v.OracleReasons = []oracle.JudgeReason{oracle.ReasonDataUnverifiable}
		v.ProductReasons = []string{"product_request_result_unknown"}
	}
	return v
}

// 확정은 세 검사가 모두 yes, 근거 호출 하나 이상, 제품 사유 없음일 때만 받는다(증거 없는 확정 금지). DB 없이 돈다.
func TestProductVerdictInputValidation(t *testing.T) {
	if !validProductVerdict(productVerdictFor(1, "run-1", "document", oracle.Confirmed)) ||
		!validProductVerdict(productVerdictFor(1, "run-1", "document", oracle.Undecidable)) {
		t.Fatal("양성 대조 거부")
	}
	for name, change := range map[string]func(*ProductVerdict){
		"confirmed data unknown":     func(v *ProductVerdict) { v.DataCheck = oracle.StateUnknown },
		"confirmed policy no":        func(v *ProductVerdict) { v.PolicyCheck = oracle.StateNo },
		"confirmed identity unknown": func(v *ProductVerdict) { v.IdentityCheck = oracle.StateUnknown },
		"confirmed without evidence": func(v *ProductVerdict) { v.Evidence = nil },
		"confirmed with product reason": func(v *ProductVerdict) {
			v.ProductReasons = []string{"product_request_result_unknown"}
		},
		"unknown value":            func(v *ProductVerdict) { v.Value = "maybe" },
		"unknown check":            func(v *ProductVerdict) { v.DataCheck = "partial" },
		"zero policy digest":       func(v *ProductVerdict) { v.Binding.Policy.DigestSHA256 = [32]byte{} },
		"zero checkpoint sequence": func(v *ProductVerdict) { v.Checkpoint.Sequence = 0 },
		"upper checkpoint mac":     func(v *ProductVerdict) { v.Checkpoint.MAC = strings.Repeat("C", 64) },
		"short checkpoint mac":     func(v *ProductVerdict) { v.Checkpoint.MAC = strings.Repeat("c", 63) },
		"bad checkpoint journal":   func(v *ProductVerdict) { v.Checkpoint.JournalID = "../journal" },
		"no approval revision":     func(v *ProductVerdict) { v.Binding.Policy.TargetApprovalRevision = "" },
		"no policy ref":            func(v *ProductVerdict) { v.Binding.Policy.Ref = "" },
		"bad run ref":              func(v *ProductVerdict) { v.Binding.RunRef = "../run" },
		"resource prefix mismatch": func(v *ProductVerdict) { v.ResourceRef = "other:" + strings.Repeat("a", 64) },
		"bad resource type":        func(v *ProductVerdict) { v.ResourceType = "Document"; v.ResourceRef = "Document:x" },
		"duplicate candidate":      func(v *ProductVerdict) { v.CandidateRefs = []string{"c", "c"} },
		"duplicate evidence":       func(v *ProductVerdict) { v.Evidence[1].CallRef = v.Evidence[0].CallRef },
		"zero sequence":            func(v *ProductVerdict) { v.Evidence[0].Sequence = 0 },
		"bad product reason": func(v *ProductVerdict) {
			v.Value = oracle.Undecidable
			v.DataCheck = oracle.StateUnknown
			v.ProductReasons = []string{"Bad Reason"}
		},
		"no observed time": func(v *ProductVerdict) { v.ObservedAt = time.Time{} },
		"no task":          func(v *ProductVerdict) { v.TaskID = 0 },
		"no model tag":     func(v *ProductVerdict) { v.ModelTag = "" },
	} {
		v := productVerdictFor(1, "run-1", "document", oracle.Confirmed)
		change(&v)
		if validProductVerdict(v) {
			t.Fatal("받으면 안 되는 입력:", name)
		}
	}
}

func TestProductVerdictStoredSeparatelyFromEval(t *testing.T) {
	d := e3TestDB(t)
	f := e3FixtureFor(t, d, oracle.Confirmed, true)
	eval, err := d.RecordControlledFinding(t.Context(), f.prepared, f.markers)
	if err != nil {
		t.Fatal(err)
	}
	// 제품 회차는 자기 E² 실행 참조를 쓴다. 평가와 같은 회차·근거 판을 쓰면 근거 묶음 충돌로 거부된다(아래 시험).
	if _, err := d.RecordProductVerdict(t.Context(), productVerdictFor(f.taskID, f.source.Expected.Scope.RunRef, "document", oracle.Confirmed)); !errors.Is(err, ErrProductVerdictConflict) {
		t.Fatal("평가 근거 묶음 자리에 제품 근거를 넣음", err)
	}
	run := "product-run-1"
	var wg sync.WaitGroup
	results := make(chan ProductVerdictResult, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			out, err := d.RecordProductVerdict(t.Context(), productVerdictFor(f.taskID, run, "document", oracle.Confirmed))
			results <- out
			errs <- err
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	created := 0
	var product ProductVerdictResult
	for out := range results {
		if product.IssuanceID == 0 {
			product = out
		}
		if out.IssuanceID != product.IssuanceID || out.VerdictID != product.VerdictID {
			t.Fatal("같은 회차·종류 판정이 둘 생김")
		}
		if out.Created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created=%d", created)
	}
	second, err := d.RecordProductVerdict(t.Context(), productVerdictFor(f.taskID, run, "invoice", oracle.Undecidable))
	if err != nil || !second.Created || second.ManifestID != product.ManifestID {
		t.Fatal("같은 회차의 다른 종류가 같은 근거 묶음을 쓰지 않음", err)
	}
	// 기존 평가 행은 출처 종류 eval로 채워지고 평가 칸이 그대로다. 제품 행은 KG·intent·통제 모드 판이 없고 정책 출처가 있다.
	var source string
	var intent, mode sql.NullInt64
	var kg sql.NullString
	var policyRef sql.NullString
	if err := d.QueryRow(`SELECT source_kind,intent_id,mode_version,kg_snapshot_ref,policy_source_ref FROM e3_verdict_issuances WHERE id=$1`,
		eval.IssuanceID).Scan(&source, &intent, &mode, &kg, &policyRef); err != nil || source != E3SourceEval || !intent.Valid || !mode.Valid || !kg.Valid || policyRef.Valid {
		t.Fatal("평가 행 출처·칸 불일치", err, source)
	}
	var reasons, candidates string
	var version int
	if err := d.QueryRow(`SELECT i.source_kind,i.intent_id,i.mode_version,i.kg_snapshot_ref,i.policy_source_ref,i.product_reason_codes::text,i.candidate_refs::text,v.record_version
 FROM e3_verdict_issuances i JOIN e3_oracle_verdicts v ON v.id=i.verdict_id WHERE i.id=$1`, second.IssuanceID).
		Scan(&source, &intent, &mode, &kg, &policyRef, &reasons, &candidates, &version); err != nil ||
		source != E3SourceProduct || intent.Valid || mode.Valid || kg.Valid || policyRef.String != "declaration-job-synthetic" ||
		reasons != `["product_request_result_unknown"]` || candidates != `["candidate-1"]` || version != productVerdictRecordVersion {
		t.Fatal("제품 행 출처·칸 불일치", err, source, reasons, candidates, version)
	}
	// 제품 판정은 finding을 만들지 않는다(후보를 확정으로 바꾸지 않음).
	if got := e3Counts(t, d, f); got != ([5]int{2, 3, 1, 1, 1}) {
		t.Fatalf("counts=%v", got)
	}
	listed, err := d.ProductVerdicts(t.Context(), f.taskID, 10)
	if err != nil || len(listed) != 2 {
		t.Fatal("제품 판정 읽기", err, len(listed))
	}
	for _, v := range listed {
		if v.SourceKind != E3SourceProduct || v.IssuanceID == eval.IssuanceID || v.PolicyDigest != sha256.Sum256([]byte("synthetic declaration")) {
			t.Fatal("제품 읽기에 평가 판정이 섞임")
		}
	}
	if _, err := d.ProductVerdicts(t.Context(), f.taskID, 1); !errors.Is(err, ErrProductVerdictInput) {
		t.Fatal("상한 넘는 읽기를 잘라서 돌려줌", err)
	}
	// 평가 경로 읽기는 그대로다.
	if _, err := d.ControlledFindingRecord(t.Context(), eval.FindingID); err != nil {
		t.Fatal(err)
	}
	// 같은 회차·종류에 다른 판정은 충돌이다. 같은 회차의 다른 종류가 다른 저널을 가리켜도 충돌이다.
	changed := productVerdictFor(f.taskID, run, "document", oracle.Undecidable)
	if _, err := d.RecordProductVerdict(t.Context(), changed); !errors.Is(err, ErrProductVerdictConflict) {
		t.Fatal("다른 입력을 같은 판정으로 받음", err)
	}
	otherJournal := productVerdictFor(f.taskID, run, "receipt", oracle.Allowed)
	otherJournal.Checkpoint = &ProductCheckpoint{JournalID: "journal-synthetic", Sequence: 13, MAC: strings.Repeat("d", 64)}
	if _, err := d.RecordProductVerdict(t.Context(), otherJournal); !errors.Is(err, ErrProductVerdictConflict) {
		t.Fatal("같은 회차에 다른 근거 묶음", err)
	}
	if got := e3Counts(t, d, f); got != ([5]int{2, 3, 1, 1, 1}) {
		t.Fatalf("거부한 저장이 행을 남김: %v", got)
	}
}

// 표 검사: 평가 행은 KG·intent·통제 모드 판이 필수이고 제품 칸을 가질 수 없다. 제품 행은 그 반대다.
func TestProductVerdictSchemaKeepsEvalConditions(t *testing.T) {
	d := e3TestDB(t)
	f := e3FixtureFor(t, d, oracle.Allowed, true)
	eval, err := d.RecordControlledFinding(t.Context(), f.prepared, f.markers)
	if err != nil {
		t.Fatal(err)
	}
	product, err := d.RecordProductVerdict(t.Context(), productVerdictFor(f.taskID, "product-run", "document", oracle.Confirmed))
	if err != nil {
		t.Fatal(err)
	}
	// 기존 행을 복사해 칸 하나씩 바꿔 넣는다. 판정 행은 새로 만든다(verdict_id는 발급 하나에 하나).
	insertCopy := func(from int64, overrides string, recordVersion int) error {
		tx, err := d.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var verdict int64
		if err := tx.QueryRow(`INSERT INTO e3_oracle_verdicts(record_version,payload_canonical,payload,payload_digest,oracle_value)
SELECT $2,payload_canonical,payload,payload_digest,oracle_value FROM e3_oracle_verdicts WHERE id=(SELECT verdict_id FROM e3_verdict_issuances WHERE id=$1) RETURNING id`,
			from, recordVersion).Scan(&verdict); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO e3_verdict_issuances(verdict_id,manifest_id,task_id,exploration_id,intent_id,mode_version,purpose,execution_ref,case_ref,resource_ref,
eval_run_id,model_tag,reset_generation,kg_snapshot_ref,kg_policy_version,kg_snapshot_digest,harness_generation_ref,target_instance_ref,operation_key,input_digest,
binding_canonical,source_kind,policy_source_ref,policy_source_digest,policy_target_approval_revision,product_reason_codes,candidate_refs)
SELECT $2,manifest_id,task_id,exploration_id,intent_id,mode_version,purpose,execution_ref,case_ref,resource_ref,eval_run_id,model_tag,reset_generation,
kg_snapshot_ref,kg_policy_version,kg_snapshot_digest,harness_generation_ref,target_instance_ref,sha256(operation_key),input_digest,binding_canonical,
source_kind,policy_source_ref,policy_source_digest,policy_target_approval_revision,product_reason_codes,candidate_refs FROM e3_verdict_issuances WHERE id=$1`, from, verdict)
		if err != nil {
			return err
		}
		if overrides != "" {
			if _, err := tx.Exec(`ALTER TABLE e3_verdict_issuances DISABLE TRIGGER trg_e3_issuance_immutable`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`UPDATE e3_verdict_issuances SET `+overrides+` WHERE verdict_id=$1`, verdict); err != nil {
				return err
			}
		}
		return nil
	}
	// 양성 대조: 그대로 복사하면 들어간다(아래 거부가 검사 때문임을 보인다).
	if err := insertCopy(eval.IssuanceID, "", 1); err != nil {
		t.Fatal("평가 행 복사 양성 대조", err)
	}
	if err := insertCopy(product.IssuanceID, "", 2); err != nil {
		t.Fatal("제품 행 복사 양성 대조", err)
	}
	for name, c := range map[string]struct {
		from      int64
		overrides string
		version   int
	}{
		"eval without kg snapshot": {eval.IssuanceID, "kg_snapshot_ref=NULL", 1},
		"eval without kg policy":   {eval.IssuanceID, "kg_policy_version=NULL", 1},
		"eval without kg digest":   {eval.IssuanceID, "kg_snapshot_digest=NULL", 1},
		"eval without intent":      {eval.IssuanceID, "intent_id=NULL", 1},
		"eval without mode":        {eval.IssuanceID, "mode_version=NULL", 1},
		"eval with policy source":  {eval.IssuanceID, "policy_source_ref='declaration-x'", 1},
		"eval with candidates":     {eval.IssuanceID, `candidate_refs='[]'::jsonb`, 1},
		"product with kg":          {product.IssuanceID, "kg_snapshot_ref='kg'", 2},
		"product with intent":      {product.IssuanceID, "intent_id=" + strconv.FormatInt(f.intentID, 10), 2},
		"product with mode":        {product.IssuanceID, "mode_version=1", 2},
		"product with eval run":    {product.IssuanceID, "eval_run_id='eval-run'", 2},
		"product without policy":   {product.IssuanceID, "policy_source_ref=NULL", 2},
		"product short digest":     {product.IssuanceID, `policy_source_digest='\x00'::bytea`, 2},
		"product without revision": {product.IssuanceID, "policy_target_approval_revision=NULL", 2},
		"product reasons object":   {product.IssuanceID, `product_reason_codes='{}'::jsonb`, 2},
		"product without cands":    {product.IssuanceID, "candidate_refs=NULL", 2},
		"product without reasons":  {product.IssuanceID, "product_reason_codes=NULL", 2},
		"product without digest":   {product.IssuanceID, "policy_source_digest=NULL", 2},
		"unknown source":           {product.IssuanceID, "source_kind='other'", 2},
		"record version 3":         {eval.IssuanceID, "", 3},
	} {
		err := insertCopy(c.from, c.overrides, c.version)
		if err == nil || !strings.Contains(err.Error(), "violates check constraint") {
			t.Fatal("표 검사가 막지 않음:", name, err)
		}
	}
}
