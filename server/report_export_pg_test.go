package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
)

// This integration test only uses the dedicated test database configured by
// ARTEX_PG_DSN. The fixture skips when that database is not available.
func TestReportExportDLPDatabaseAndSourcePreserved(t *testing.T) {
	s, finding, req := trafficEvidenceServer(t)
	body := []byte(reportExportCanary + "\x00\xff")
	seedServerEvidenceFlow(t, s, "report-export-source", body)
	if _, err := s.m.pg.Exec(`UPDATE findings SET name=$1,vulnclass=$1,summary=$1,evidence=$1,report=$1 WHERE id=$2`, reportExportCanary, finding.FindingID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.evidenceStore().Bind(context.Background(), finding.FindingID, []db.TrafficRef{{TrafficID: "report-export-source", Role: "proof", Note: reportExportCanary}}); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := s.m.pg.QueryRow(`SELECT row_to_json(f)::text FROM findings f WHERE id=$1`, finding.FindingID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	bindings, err := s.m.pg.GetFindingTraffic(context.Background(), finding.FindingID)
	if err != nil {
		t.Fatal(err)
	}
	beforeBindings, err := json.Marshal(bindings)
	if err != nil {
		t.Fatal(err)
	}
	source, _, err := s.evidenceStore().OpenBody(bindings.Bindings[0].Snapshot, "response")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(source); err != nil || !bytes.Equal(got, body) {
		source.Close()
		t.Fatal("source fixture differs")
	}
	source.Close()
	for _, format := range []string{"md-single", "csv", "json", "md-zip"} {
		t.Run(format, func(t *testing.T) {
			w := req("GET", fmt.Sprintf("/api/exploration/findings/export?scope=selected&ids=%d&format=%s", finding.FindingID, format), "")
			if w.Code != 200 {
				t.Fatalf("export status=%d", w.Code)
			}
			if strings.Contains(w.Body.String(), reportExportCanary) {
				t.Fatal("raw finding leaked")
			}
			if format == "md-zip" {
				z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range z.File {
					r, err := entry.Open()
					if err != nil {
						t.Fatal(err)
					}
					v, err := io.ReadAll(r)
					r.Close()
					if err != nil {
						t.Fatal(err)
					}
					if bytes.Contains(v, body) || bytes.Contains(v, []byte(reportExportCanary)) || strings.HasSuffix(entry.Name, ".bin") {
						t.Fatal("raw source leaked into ZIP")
					}
				}
			}
		})
	}
	var after string
	if err := s.m.pg.QueryRow(`SELECT row_to_json(f)::text FROM findings f WHERE id=$1`, finding.FindingID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	afterList, err := s.m.pg.GetFindingTraffic(context.Background(), finding.FindingID)
	if err != nil {
		t.Fatal(err)
	}
	afterBindings, err := json.Marshal(afterList)
	if err != nil {
		t.Fatal(err)
	}
	if before != after || !bytes.Equal(beforeBindings, afterBindings) {
		t.Fatal("export changed stored finding or binding metadata")
	}
	source, _, err = s.evidenceStore().OpenBody(bindings.Bindings[0].Snapshot, "response")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	afterBody, err := io.ReadAll(source)
	if err != nil || !bytes.Equal(afterBody, body) {
		t.Fatal("export changed source body")
	}
}

func TestReportExportDLPBoundsDatabaseRead(t *testing.T) {
	s, finding, req := trafficEvidenceServer(t)
	f, err := s.m.pg.GetFinding(finding.FindingID)
	if err != nil || f == nil || f.TaskID == nil {
		t.Fatal("missing fixture task")
	}
	taskID := *f.TaskID
	if _, err := s.m.pg.Exec(`INSERT INTO findings(task_id,name,severity,status)
SELECT $1,$2,'low','pending' FROM generate_series(1,$3)`, taskID, reportExportCanary, report.MaxFindings+1); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.m.pg.Exec(`DELETE FROM findings WHERE task_id=$1 AND name=$2`, taskID, reportExportCanary); err != nil {
			t.Error("bulk fixture cleanup failed")
		}
	})
	filter := db.FindingFilter{TaskID: fmt.Sprint(taskID)}
	rows, err := s.m.pg.ListFindingsForExportBounded(filter, nil, report.MaxFindings+1)
	if err != nil || len(rows) != report.MaxFindings+1 {
		t.Fatal("bounded reader did not return the overflow sentinel row")
	}
	rows, err = s.m.pg.ListFindingsForExportBounded(filter, nil, 1)
	if err != nil || len(rows) != 1 {
		t.Fatal("filtered SQL row limit was not enforced")
	}
	var bulkID int64
	if err := s.m.pg.QueryRow(`SELECT min(id) FROM findings WHERE task_id=$1 AND name=$2`, taskID, reportExportCanary).Scan(&bulkID); err != nil {
		t.Fatal(err)
	}
	rows, err = s.m.pg.ListFindingsForExportBounded(db.FindingFilter{}, []int64{finding.FindingID, bulkID}, 1)
	if err != nil || len(rows) != 1 {
		t.Fatal("selected SQL row limit was not enforced")
	}
	rows, err = s.m.pg.ListFindingsForExport(db.FindingFilter{}, []int64{finding.FindingID})
	if err != nil || len(rows) != 1 {
		t.Fatal("legacy reader compatibility changed")
	}
	for _, scope := range []string{"filtered", "all"} {
		t.Run(scope, func(t *testing.T) {
			w := req("GET", fmt.Sprintf("/api/exploration/findings/export?format=json&scope=%s&task_id=%d", scope, taskID), "")
			if w.Code != http.StatusUnprocessableEntity || w.Header().Get("Content-Disposition") != "" || !strings.Contains(w.Body.String(), "report export limit exceeded") {
				t.Fatal("excess rows were not rejected before download")
			}
		})
	}
}
