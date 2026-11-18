package server

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/oracle"
)

// 제품 확정 경로 판정(source_kind=product)은 같은 작업에 있어도 평가 경로의 보고서·접근통제 지도·수정 근거에 섞이지 않는다
// (2026-11-18 사용자 결정 ①(가)). 제품 판정의 표시는 출처를 붙여 따로 읽는다(db.ProductVerdicts).
func storeProductVerdict(t *testing.T, pg *db.DB, taskID int64, run string, value oracle.VerdictValue) db.ProductVerdictResult {
	t.Helper()
	v := db.ProductVerdict{TaskID: taskID,
		Binding: db.ProductBinding{JobRef: "job-synthetic", RunRef: run, E2PolicyRef: "e2-policy-synthetic", GenerationRef: "generation-synthetic",
			InstanceRef: "instance-synthetic", Policy: db.ProductPolicySource{Ref: "declaration-job-synthetic",
				DigestSHA256: sha256.Sum256([]byte("synthetic declaration")), TargetApprovalRevision: "target-revision-1"}},
		Journal:      db.ProductJournal{Ref: "journal-synthetic", DigestSHA256: sha256.Sum256([]byte("synthetic journal"))},
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
