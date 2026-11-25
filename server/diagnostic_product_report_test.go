package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/report"
)

func TestDiagnosticReportCountsOnlyVerifiedProductConfirmations(t *testing.T) {
	s, fixture, _ := trafficEvidenceServer(t)
	finding, err := s.m.pg.GetFinding(fixture.FindingID)
	if err != nil || finding.TaskID == nil {
		t.Fatal("fixture finding is missing its task")
	}
	task := *finding.TaskID
	storeProductVerdictKind(t, s.m.pg, task, "document", "revision-1", oracle.Confirmed)
	diagnosticServerCandidate(t, s, task, "completed")
	for _, tc := range []struct {
		name, job                                   string
		reader                                      productVerdictContextReader
		confirmed, unverified, unreadable, noSource int
		wantText                                    string
	}{
		{"matching", productListJob, fixedProductContext{productListContext(matchedProductSource(), nil)}, 1, 0, 0, 0, "독립 확정 1건"},
		{"IPC failure", productListJob, failingProductContext{}, 0, 0, 1, 0, "제품 확정 단계 판정을 읽지 못함(1건, 확정에 세지 않음)"},
		{"policy source missing", productListJob, fixedProductContext{productListContext(nil, nil)}, 0, 0, 0, 1, "당시 원본 미확인 1건(확정에 세지 않음)"},
		{"other job", "different-job", fixedProductContext{productListContext(matchedProductSource(), nil)}, 0, 1, 0, 0, "확인 실패 1건(확정에 안 셈)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(t.Context(), diagnosticProductContextKey{}, diagnosticProductBinding{
				taskID: strconv.FormatInt(task, 10), jobID: tc.job, reader: tc.reader,
			})
			in, err := loadDiagnosticReport(ctx, s.m.pg, task, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := report.PrepareDiagnostic(in)
			if err != nil || prepared.Confirmed != tc.confirmed || len(prepared.ProductConfirmed) != tc.confirmed ||
				prepared.ProductUnverified != tc.unverified || prepared.ProductUnreadable != tc.unreadable ||
				prepared.ProductSourceUnavailable != tc.noSource {
				t.Fatal("report counts disagree with the verified product verdict")
			}
			candidates := 0
			for _, row := range prepared.Entries {
				if row.Kind == "candidate" {
					candidates++
					if row.Outcome != "review" {
						t.Fatal("candidate was promoted")
					}
				}
			}
			if candidates != 1 {
				t.Fatal("candidate missing from report")
			}
			for _, shared := range []bool{false, true} {
				in.Shared = shared
				html, err := report.DiagnosticHTML(in)
				if err != nil || !strings.Contains(string(html), tc.wantText) {
					t.Fatalf("product verdict missing from HTML (shared=%t): %v", shared, err)
				}
				doc, err := report.DiagnosticDOCX(in)
				if err != nil {
					t.Fatalf("product verdict missing from DOCX (shared=%t): %v", shared, err)
				}
				for _, body := range []string{string(html), diagnosticWordText(t, wordParts(t, doc)["word/document.xml"])} {
					if !strings.Contains(body, tc.wantText) {
						t.Fatalf("보고서에 제품 판정 요약 없음 (공유=%t)", shared)
					}
					if (tc.unreadable > 0 || tc.noSource > 0) && strings.Contains(body, "확인 실패") {
						t.Fatal("읽지 못한 판정이 무결성 확인 실패로 표시됐다")
					}
					if tc.confirmed > 0 && shared && (!strings.Contains(body, "자원 종류 ***") || strings.Contains(body, "자원 종류 document")) {
						t.Fatal("공유 보고서에 자원 종류 원문이 남았다")
					}
				}
			}
		})
	}
}

type diagnosticProductTestReader func(context.Context) (productVerdictContext, error)

func (f diagnosticProductTestReader) read(ctx context.Context) (productVerdictContext, error) {
	return f(ctx)
}

func TestDiagnosticProductReportReleasesDatabaseBeforePolicyRead(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	task := productListTask(t, pg)
	storeProductVerdictKind(t, pg, task, "document", "revision-1", oracle.Confirmed)
	previous := pg.Stats().MaxOpenConnections
	pg.SetMaxOpenConns(1)
	t.Cleanup(func() { pg.SetMaxOpenConns(previous) })
	reader := diagnosticProductTestReader(func(ctx context.Context) (productVerdictContext, error) {
		probe, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := pg.PingContext(probe); err != nil {
			t.Fatalf("정책 원본을 조회할 때 보고서 트랜잭션이 DB 연결을 잡고 있음: %v", err)
		}
		return productListContext(matchedProductSource(), nil), nil
	})
	ctx := context.WithValue(t.Context(), diagnosticProductContextKey{}, diagnosticProductBinding{
		taskID: strconv.FormatInt(task, 10), jobID: productListJob, reader: reader,
	})
	in, err := loadDiagnosticReport(ctx, pg, task, time.Now())
	if err != nil || len(in.ProductConfirmed) != 1 {
		t.Fatalf("정책 원본 확인 후 확정 판정 누락: %v", err)
	}
}

func TestDiagnosticProductReportDistinguishesStorageFailureAndLimit(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	task := productListTask(t, pg)
	ctx := context.WithValue(t.Context(), diagnosticProductContextKey{}, diagnosticProductBinding{
		taskID: strconv.FormatInt(task, 10), jobID: productListJob,
	})
	tx, err := pg.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDiagnosticProductSnapshot(ctx, tx, task, 0); err == nil || diagnosticReadFailureCode(err) != http.StatusServiceUnavailable {
		t.Fatalf("제품 판정 DB 오류는 503이어야 함: %v", err)
	}
	storeProductVerdictKind(t, pg, task, "document", "revision-1", oracle.Confirmed)
	if _, err := loadDiagnosticProductSnapshot(ctx, pg, task, report.MaxFindings); err == nil || diagnosticReadFailureCode(err) != http.StatusUnprocessableEntity {
		t.Fatalf("보고서 행 상한은 422여야 함: %v", err)
	}
}

func TestDiagnosticReportShowsUnconfirmedAndFailedEvidenceSeals(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	for _, tc := range []struct {
		name        string
		failedSeal  bool
		confirmed   int
		unverified  int
		wantSummary string
		wantRow     string
	}{
		{"unconfirmed", false, 1, 0, "그중 근거 봉인 미확인 1건", "근거 봉인 미확인(seal_not_recorded)"},
		{"inclusion failed", true, 0, 1, "확인 실패 1건(확정에 안 셈)", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := productListTask(t, pg)
			storeProductVerdictKind(t, pg, task, "document", "revision-1", oracle.Confirmed)
			if tc.failedSeal {
				record := db.SealCheckRecord{TaskID: task, RunRef: "product-run-list",
					Head:   db.ProductCheckpoint{JournalID: "journal-synthetic", Sequence: 20, MAC: strings.Repeat("e", 64)},
					Checks: []db.SealCheck{{Sequence: 12, MAC: strings.Repeat("c", 64), Included: false}}}
				if err := pg.RecordSealCheck(t.Context(), record); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.WithValue(t.Context(), diagnosticProductContextKey{}, diagnosticProductBinding{
				taskID: strconv.FormatInt(task, 10), jobID: productListJob,
				reader: fixedProductContext{productListContext(matchedProductSource(), nil)},
			})
			in, err := loadDiagnosticReport(ctx, pg, task, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := report.PrepareDiagnostic(in)
			if err != nil || prepared.Confirmed != tc.confirmed || prepared.ProductUnverified != tc.unverified {
				t.Fatal("봉인 상태와 보고서 확정 집계가 다르다", err)
			}
			for _, shared := range []bool{false, true} {
				in.Shared = shared
				html, err := report.DiagnosticHTML(in)
				if err != nil || !strings.Contains(string(html), tc.wantSummary) ||
					tc.wantRow != "" && !strings.Contains(string(html), tc.wantRow) {
					t.Fatal("HTML에 봉인 상태가 없다", err)
				}
				doc, err := report.DiagnosticDOCX(in)
				if err != nil {
					t.Fatal(err)
				}
				word := diagnosticWordText(t, wordParts(t, doc)["word/document.xml"])
				if !strings.Contains(word, tc.wantSummary) || tc.wantRow != "" && !strings.Contains(word, tc.wantRow) {
					t.Fatal("DOCX에 봉인 상태가 없다")
				}
			}
		})
	}
}

func TestDiagnosticReportShowsNonConfirmedProductVerdictCounts(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	task := productListTask(t, pg)
	storeProductVerdictKind(t, pg, task, "document", "revision-1", oracle.Suspected)
	storeProductVerdictKind(t, pg, task, "note", "revision-1", oracle.Undecidable)
	storeProductVerdictKind(t, pg, task, "image", "revision-1", oracle.Undecidable)
	ctx := context.WithValue(t.Context(), diagnosticProductContextKey{}, diagnosticProductBinding{
		taskID: strconv.FormatInt(task, 10), jobID: productListJob,
		reader: fixedProductContext{productListContext(matchedProductSource(), nil)},
	})
	in, err := loadDiagnosticReport(ctx, pg, task, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := report.PrepareDiagnostic(in)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Confirmed != 0 || prepared.ProductUnverified != 0 || prepared.ProductSuspected != 1 || prepared.ProductUndecidable != 2 {
		t.Fatalf("제품 판정 집계: 확정=%d, 확인 실패=%d, 의심=%d, 판정 불가=%d",
			prepared.Confirmed, prepared.ProductUnverified, prepared.ProductSuspected, prepared.ProductUndecidable)
	}
	const summary = "독립 확정 0건 · 의심 1건 · 판정 불가 2건(확정에 세지 않음)"
	for _, shared := range []bool{false, true} {
		in.Shared = shared
		html, err := report.DiagnosticHTML(in)
		if err != nil || !strings.Contains(string(html), summary) {
			t.Fatalf("HTML에 제품 판정 요약 없음 (공유=%t): %v", shared, err)
		}
		doc, err := report.DiagnosticDOCX(in)
		if err != nil || !strings.Contains(diagnosticWordText(t, wordParts(t, doc)["word/document.xml"]), summary) {
			t.Fatalf("DOCX에 제품 판정 요약 없음 (공유=%t): %v", shared, err)
		}
	}
}
