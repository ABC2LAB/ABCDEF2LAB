package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestGetReportRejectsFindingOverflowWithoutPartialMarkdown(t *testing.T) {
	s, _, req := trafficEvidenceServer(t)
	task := s.m.ActiveTask()
	if task == nil {
		t.Fatal("missing active task")
	}

	for _, tc := range []struct {
		name       string
		findings   int
		wantStatus int
	}{
		{name: "below", findings: 999, wantStatus: http.StatusOK},
		{name: "exact", findings: 1000, wantStatus: http.StatusOK},
		{name: "overflow", findings: 1001, wantStatus: http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.m.pg.Exec(`DELETE FROM exploration_nodes WHERE exploration_id=$1 AND kind=$2`, task.ExpID, "finding"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.m.pg.Exec(`
INSERT INTO exploration_nodes(exploration_id, kind, payload, priority, state, origin)
SELECT $1, 'finding', jsonb_build_object(
    'vulnclass', 'synthetic',
    'name', 'synthetic-report-finding-' || i,
    'severity', 'low',
    'summary', 'synthetic-report-summary',
    'evidence', ''), 0, 'confirmed', 'report-bounds'
FROM generate_series(1, $2) AS series(i)`, task.ExpID, tc.findings); err != nil {
				t.Fatal(err)
			}

			w := req("GET", fmt.Sprintf("/api/report?task=%s", task.ID), "")
			if w.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%q", w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.wantStatus == http.StatusOK {
				if w.Header().Get("Content-Type") != "text/markdown; charset=utf-8" || !strings.HasPrefix(w.Body.String(), "# ") {
					t.Fatalf("successful response was not Markdown: %q", w.Body.String()[:min(len(w.Body.String()), 80)])
				}
				return
			}
			if strings.HasPrefix(w.Body.String(), "# ") ||
				!strings.Contains(w.Body.String(), "report export limit exceeded") {
				t.Fatalf("overflow emitted partial Markdown or wrong error: %q", w.Body.String())
			}
		})
	}
}

func TestGetReportRejectsAssetTypeOverflowWithoutPartialMarkdown(t *testing.T) {
	s, finding, req := trafficEvidenceServer(t)
	f, err := s.m.pg.GetFinding(finding.FindingID)
	if err != nil || f == nil || f.TaskID == nil {
		t.Fatal("missing fixture task")
	}
	taskID := *f.TaskID
	taskIDText := strconv.FormatInt(taskID, 10)
	const prefix = "http://synthetic-report-asset-boundary-"
	t.Cleanup(func() {
		if _, err := s.m.pg.Exec(`DELETE FROM assets WHERE type='endpoint' AND url LIKE $1`, prefix+"%"); err != nil {
			t.Error("asset boundary cleanup failed")
		}
	})

	for _, tc := range []struct {
		name       string
		wantTotal  int
		wantStatus int
	}{
		{name: "exact", wantTotal: 100000, wantStatus: http.StatusOK},
		{name: "overflow", wantTotal: 100001, wantStatus: http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.m.pg.Exec(`DELETE FROM assets WHERE type='endpoint' AND url LIKE $1`, prefix+"%"); err != nil {
				t.Fatal(err)
			}
			var existing int
			if err := s.m.pg.QueryRow(`SELECT count(*) FROM assets WHERE type='endpoint'`).Scan(&existing); err != nil {
				t.Fatal(err)
			}
			if existing >= tc.wantTotal {
				t.Fatalf("fixture already has %d endpoint assets; cannot create exact boundary", existing)
			}
			need := tc.wantTotal - existing
			if _, err := s.m.pg.Exec(`
INSERT INTO assets(type, task_ids, url, method)
SELECT 'endpoint', ARRAY[$1::bigint], $2 || i::text || '/resource', 'GET'
FROM generate_series(1, $3) AS series(i)`, taskID, prefix, need); err != nil {
				t.Fatal(err)
			}

			w := req("GET", fmt.Sprintf("/api/report?task=%s", taskIDText), "")
			if w.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%q", w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.wantStatus == http.StatusOK {
				if w.Header().Get("Content-Type") != "text/markdown; charset=utf-8" || !strings.HasPrefix(w.Body.String(), "# ") {
					t.Fatalf("successful response was not Markdown")
				}
				return
			}
			if strings.HasPrefix(w.Body.String(), "# ") || !strings.Contains(w.Body.String(), "report export limit exceeded") {
				t.Fatalf("overflow emitted partial Markdown or wrong error: %q", w.Body.String())
			}
		})
	}
}
