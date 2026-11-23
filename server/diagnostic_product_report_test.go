package server

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

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
		{"other job", "different-job", fixedProductContext{productListContext(matchedProductSource(), nil)}, 0, 1, 0, "무결성 또는 출처 미확인 1건(확정에 세지 않음)"},
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
				if tc.unreadable > 0 && strings.Contains(string(html), "무결성 또는 출처 미확인") {
					t.Fatal("unreadable source was mislabeled as an integrity failure")
				}
			}
		})
	}
}
