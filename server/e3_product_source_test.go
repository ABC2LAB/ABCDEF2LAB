package server

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/report"
)

// 제품 확정 경로 판정(source_kind=product)은 같은 작업에 있어도 평가 경로의 보고서·접근통제 지도·수정 근거에 섞이지 않는다
// (2026-11-18 사용자 결정 ①(가)). 제품 판정의 표시는 출처를 붙여 따로 읽는다(db.ProductVerdicts).
func storeProductVerdict(t *testing.T, pg *db.DB, taskID int64, run string, value oracle.VerdictValue) db.ProductVerdictResult {
	t.Helper()
	v := db.ProductVerdict{TaskID: taskID,
		Binding: db.ProductBinding{JobRef: "job-synthetic", RunRef: run, E2PolicyRef: "e2-policy-synthetic", GenerationRef: "generation-synthetic",
			InstanceRef: "instance-synthetic", Policy: db.ProductPolicySource{Ref: "declaration-job-synthetic",
				DigestSHA256: sha256.Sum256([]byte("synthetic declaration")), TargetApprovalRevision: "target-revision-1"}},
		Checkpoint:   &db.ProductCheckpoint{JournalID: "journal-synthetic", Sequence: 12, MAC: strings.Repeat("c", 64)},
		ResourceType: "document", ResourceRef: "document:" + strings.Repeat("b", 64), ModelTag: "synthetic-model",
		Value: value, PolicyCheck: oracle.StateYes, IdentityCheck: oracle.StateYes, DataCheck: oracle.StateYes,
		OracleReasons:  []oracle.JudgeReason{oracle.ReasonPolicyDenyResolved},
		ProductReasons: []string{}, CandidateRefs: []string{"candidate-1"},
		Evidence:   []db.ProductEvidence{{CallRef: "call-1", AttemptID: "attempt-1", Sequence: 1}},
		ObservedAt: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	out, err := pg.RecordProductVerdict(t.Context(), v)
	if err != nil || !out.Created {
		t.Fatal("제품 판정 저장", err)
	}
	return out
}

func TestDiagnosticReportExcludesProductVerdicts(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	for _, value := range []oracle.VerdictValue{oracle.Confirmed, oracle.Undecidable} {
		t.Run(string(value), func(t *testing.T) {
			f := diagnosticE3FixtureFor(t, s.m.pg, value, true)
			if _, err := s.m.pg.RecordControlledFinding(t.Context(), f.prepared, f.markers); err != nil {
				t.Fatal(err)
			}
			before, err := loadDiagnosticReport(t.Context(), s.m.pg, f.taskID, time.Now())
			if err != nil || len(before.Entries) != 1 {
				t.Fatal("평가 판정 보고서", err)
			}
			product := storeProductVerdict(t, s.m.pg, f.taskID, "product-run-report", oracle.Confirmed)
			after, err := loadDiagnosticReport(t.Context(), s.m.pg, f.taskID, time.Now())
			if err != nil {
				t.Fatal("제품 판정이 보고서 읽기를 깨뜨림", err)
			}
			if len(after.Entries) != 1 || after.Entries[0].ID != before.Entries[0].ID || after.Entries[0].Outcome != before.Entries[0].Outcome ||
				after.SourceFailures != before.SourceFailures {
				t.Fatalf("제품 판정이 평가 보고서에 섞임: %d %+v", len(after.Entries), after.Entries)
			}
			// 제품 판정 항목은 평가 수정 근거 경로로 읽히지 않는다.
			if _, err := db.LoadDiagnosticRemediationSource(t.Context(), s.m.pg.DB, f.taskID, "verdict-"+itoa(product.IssuanceID)); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("제품 판정을 평가 수정 근거로 읽음", err)
			}
			listed, err := s.m.pg.ProductVerdicts(t.Context(), f.taskID, 10)
			if err != nil || len(listed) != 1 || listed[0].IssuanceID != product.IssuanceID || listed[0].SourceKind != db.E3SourceProduct {
				t.Fatal("제품 판정 출처 읽기", err)
			}
		})
	}
}

func TestAccessMapExcludesProductVerdicts(t *testing.T) {
	f := newAccessMapTestFixture(t)
	// 같은 실행 참조에 제품 판정을 먼저 둔다(지도 조회 범위와 같은 execution_ref).
	storeProductVerdict(t, f.pg, f.b.TaskID, f.b.Scope.RunRef, oracle.Confirmed)
	f.source.EvidenceRevision = 2
	first := f.save(t)
	result, err := readAccessMap(t.Context(), f.pg.DB, f.b.TaskID)
	if err != nil {
		t.Fatal("제품 판정이 지도 읽기를 깨뜨림", err)
	}
	c := accessMapFind(t, result, "reader")
	if c.State != "confirmed" || c.RecordID == nil || *c.RecordID != first.VerdictID || c.ProjectionError != nil {
		t.Fatal("제품 판정이 지도 셀에 섞임", c.State, c.ProjectionError)
	}
}

// 보고서 용량 사전 검사도 평가 판정만 센다. 같은 작업의 제품 판정 합계가 상한을 넘어도 평가 보고서는 읽힌다(#714 이동찬 리뷰).
func TestDiagnosticReportByteCheckExcludesProductVerdicts(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	f := diagnosticE3FixtureFor(t, s.m.pg, oracle.Confirmed, true)
	if _, err := s.m.pg.RecordControlledFinding(t.Context(), f.prepared, f.markers); err != nil {
		t.Fatal(err)
	}
	seed := storeProductVerdict(t, s.m.pg, f.taskID, "product-run-bytes", oracle.Undecidable)
	// 제품 판정 행을 큰 정본으로 상한(16MiB)을 넘게 늘린다. 근거 묶음 하나(64KiB)를 여러 판정이 함께 가리킨다.
	const blob = 64 << 10
	big := `"` + strings.Repeat("a", blob-2) + `"`
	var manifest int64
	if err := s.m.pg.QueryRow(`INSERT INTO e3_evidence_manifests(task_id,execution_ref,evidence_revision,canonical,digest)
VALUES($1,'product-run-big',1,convert_to($2,'UTF8'),sha256(convert_to($2,'UTF8'))) RETURNING id`, f.taskID, big).Scan(&manifest); err != nil {
		t.Fatal(err)
	}
	rows := int(report.MaxDiagnosticReportBytes/(2*blob)) + 8
	for i := range rows {
		_, err := s.m.pg.Exec(`WITH v AS (INSERT INTO e3_oracle_verdicts(record_version,payload_canonical,payload,payload_digest,oracle_value)
VALUES(2,convert_to($3,'UTF8'),$3::jsonb,sha256(convert_to($3,'UTF8')),'undecidable') RETURNING id)
INSERT INTO e3_verdict_issuances(verdict_id,manifest_id,task_id,exploration_id,intent_id,mode_version,purpose,execution_ref,case_ref,resource_ref,
eval_run_id,model_tag,reset_generation,kg_snapshot_ref,kg_policy_version,kg_snapshot_digest,harness_generation_ref,target_instance_ref,operation_key,input_digest,
binding_canonical,source_kind,policy_source_ref,policy_source_digest,policy_target_approval_revision,product_reason_codes,candidate_refs)
SELECT v.id,$2,task_id,exploration_id,NULL,NULL,purpose,'product-run-big',case_ref||'-'||$4::text,resource_ref,NULL,model_tag,reset_generation,NULL,NULL,NULL,
harness_generation_ref,target_instance_ref,sha256(operation_key||convert_to($4::text,'UTF8')),input_digest,binding_canonical,source_kind,policy_source_ref,
policy_source_digest,policy_target_approval_revision,product_reason_codes,candidate_refs FROM e3_verdict_issuances, v WHERE e3_verdict_issuances.id=$1`,
			seed.IssuanceID, manifest, big, strconv.Itoa(i))
		if err != nil {
			t.Fatal(err)
		}
	}
	var productBytes int64
	if err := s.m.pg.QueryRow(`SELECT sum(octet_length(m.canonical)+octet_length(v.payload_canonical)) FROM e3_verdict_issuances i
JOIN e3_oracle_verdicts v ON v.id=i.verdict_id JOIN e3_evidence_manifests m ON m.id=i.manifest_id WHERE i.task_id=$1 AND i.source_kind='product'`, f.taskID).Scan(&productBytes); err != nil ||
		productBytes <= report.MaxDiagnosticReportBytes {
		t.Fatalf("제품 판정 합계가 상한을 넘지 않음(시험이 공허함): %d %v", productBytes, err)
	}
	data, err := loadDiagnosticReport(t.Context(), s.m.pg, f.taskID, time.Now())
	if err != nil || len(data.Entries) != 1 || data.Entries[0].Outcome != "confirmed" {
		t.Fatalf("제품 판정 용량 때문에 평가 보고서를 못 읽음: %v %d", err, len(data.Entries))
	}
}
