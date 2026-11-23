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
		name, job             string
		reader                productVerdictContextReader
		confirmed, unverified int
	}{
		{"matching", productListJob, fixedProductContext{productListContext(matchedProductSource(), nil)}, 1, 0},
		{"missing", productListJob, failingProductContext{}, 0, 1},
		{"other job", "different-job", fixedProductContext{productListContext(matchedProductSource(), nil)}, 0, 1},
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
			if err != nil || prepared.Confirmed != tc.confirmed || len(prepared.ProductConfirmed) != tc.confirmed || prepared.ProductUnverified != tc.unverified {
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
			if tc.confirmed != 0 {
				for _, shared := range []bool{false, true} {
					in.Shared = shared
					out, err := report.DiagnosticHTML(in)
					if err != nil || !strings.Contains(string(out), "독립 확정 1건") {
						t.Fatal("product verdict missing from HTML")
					}
				}
			}
		})
	}
}
