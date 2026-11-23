package server

import (
	"context"
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
		name, job                         string
		reader                            productVerdictContextReader
		confirmed, unverified, unreadable int
		wantText                          string
	}{
		{"matching", productListJob, fixedProductContext{productListContext(matchedProductSource(), nil)}, 1, 0, 0, "독립 확정 1건"},
		{"missing", productListJob, failingProductContext{}, 0, 0, 1, "제품 확정 단계 판정을 읽지 못함(1건, 확정에 세지 않음)"},
		{"other job", "different-job", fixedProductContext{productListContext(matchedProductSource(), nil)}, 0, 1, 0, "확인 실패 1건(확정에 안 셈)"},
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
			if err != nil || prepared.Confirmed != tc.confirmed || len(prepared.ProductConfirmed) != tc.confirmed || prepared.ProductUnverified != tc.unverified || prepared.ProductUnreadable != tc.unreadable {
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
				if err != nil || !strings.Contains(diagnosticWordText(t, wordParts(t, doc)["word/document.xml"]), tc.wantText) {
					t.Fatalf("product verdict missing from DOCX (shared=%t): %v", shared, err)
				}
				if tc.unreadable > 0 && strings.Contains(string(html), "확인 실패") {
					t.Fatal("unreadable source was mislabeled as an integrity failure")
				}
			}
		})
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
