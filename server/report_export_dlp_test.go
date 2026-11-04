package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
)

const reportExportCanary = "report-export-private-canary-8f31"

func reportExportFixture() *db.DBFinding {
	return &db.DBFinding{
		ID: 41, VulnClass: reportExportCanary, Name: reportExportCanary,
		Severity: "high", Status: "confirmed", Summary: reportExportCanary,
		Evidence: reportExportCanary, Report: reportExportCanary,
		TaskDescription: reportExportCanary,
		AssetIDs:        []int64{77},
		CreatedAt:       time.Date(2026, 11, 2, 1, 2, 3, 0, time.UTC),
		EvidenceVersion: 7, ReportEvidenceVersion: 6, TrafficCount: 1,
		TrafficBindings: []db.FindingTrafficBinding{{
			ID: 9, FindingID: 41, SnapshotID: reportExportCanary,
			Role: "proof", Note: reportExportCanary,
			Snapshot: db.TrafficEvidenceSnapshot{
				ID: reportExportCanary, SourceTrafficID: reportExportCanary,
				CapturedAt: 123, URL: "https://" + reportExportCanary + "/?token=" + reportExportCanary,
				Method: "POST", Status: 200, ContentType: "application/" + reportExportCanary,
				ReqHead: reportExportCanary, RespHead: reportExportCanary,
				ReqHash: reportExportCanary, RespHash: reportExportCanary,
				ReqLen: 12, RespLen: 34,
			},
		}},
	}
}

func invokeReportExport(t *testing.T, format string, f *db.DBFinding) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/exploration/findings/export", nil)
	w := httptest.NewRecorder()
	writeFindingsExport(w, req, []*db.DBFinding{f}, format, time.Date(2026, 11, 2, 2, 3, 4, 0, time.UTC))
	return w
}

func TestReportExportDLPAllFormats(t *testing.T) {
	for _, format := range []string{"md-single", "csv", "json", "md-zip"} {
		t.Run(format, func(t *testing.T) {
			f := reportExportFixture()
			before, err := json.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			w := invokeReportExport(t, format, f)
			if w.Code != http.StatusOK {
				t.Fatalf("export status=%d", w.Code)
			}
			disposition, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
			ext := map[string]string{"md-single": "md", "csv": "csv", "json": "json", "md-zip": "zip"}[format]
			if err != nil || disposition != "attachment" || params["filename"] != "findings-20261102-020304."+ext {
				t.Fatal("export has an invalid download filename header")
			}
			if bytes.Contains(w.Body.Bytes(), []byte(reportExportCanary)) {
				t.Fatal("export contains a free-text canary")
			}
			after, err := json.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("export mutated the source finding")
			}
			switch format {
			case "md-single":
				assertSafeMetadata(t, w.Body.String())
			case "csv":
				assertSafeCSV(t, w.Body.Bytes())
			case "json":
				var docs []map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &docs); err != nil {
					t.Fatal(err)
				}
				if len(docs) != 1 {
					t.Fatalf("unexpected JSON finding count: %d", len(docs))
				}
				doc := docs[0]
				if got, ok := doc["severity"].(string); !ok || got != "high" {
					t.Fatalf("safe severity missing: %#v", doc["severity"])
				}
				if got, ok := doc["report_stale"].(bool); !ok || !got {
					t.Fatalf("stale report marker missing: %#v", doc["report_stale"])
				}
				assets, ok := doc["assets"].([]any)
				if !ok || len(assets) != 1 {
					t.Fatalf("unexpected assets: %#v", doc["assets"])
				}
				asset, ok := assets[0].(map[string]any)
				if !ok || asset["id"] != "77" || asset["type"] != "[REDACTED]" || asset["label"] != "[REDACTED]" {
					t.Fatalf("unsafe asset projection: %#v", assets[0])
				}
			case "md-zip":
				archive, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
				if err != nil {
					t.Fatal(err)
				}
				if len(archive.File) == 0 {
					t.Fatal("empty export archive")
				}
				for _, entry := range archive.File {
					if strings.HasSuffix(entry.Name, ".bin") {
						t.Fatalf("raw body attachment present: %s", entry.Name)
					}
					r, err := entry.Open()
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(r)
					r.Close()
					if err != nil {
						t.Fatal(err)
					}
					if bytes.Contains(data, []byte(reportExportCanary)) {
						t.Fatal("archive entry contains a free-text canary")
					}
					if strings.HasSuffix(entry.Name, "manifest.json") {
						var manifest db.FindingTrafficBinding
						if err := json.Unmarshal(data, &manifest); err != nil {
							t.Fatal(err)
						}
						if manifest.ID != 9 || manifest.Role != "proof" || manifest.Snapshot.Status != 200 {
							t.Fatalf("unsafe or missing manifest metadata: %#v", manifest)
						}
					}
					if strings.HasSuffix(entry.Name, ".http") && string(data) != "[REDACTED]\n" {
						t.Fatalf("unexpected HTTP attachment marker: %q", data)
					}
				}
			}
		})
	}
}

func assertSafeMetadata(t *testing.T, output string) {
	t.Helper()
	for _, want := range []string{"HIGH", "confirmed", "POST", "200"} {
		if !strings.Contains(output, want) {
			t.Errorf("safe metadata %q missing", want)
		}
	}
}

func assertSafeCSV(t *testing.T, raw []byte) {
	t.Helper()
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(raw, []byte("\xEF\xBB\xBF"))))
	records, err := r.ReadAll()
	if err != nil || len(records) != 2 {
		t.Fatalf("unexpected CSV: err=%v records=%d", err, len(records))
	}
	if records[1][0] != "41" || records[1][3] != "high" || records[1][4] != "confirmed" || records[1][8] != "1" {
		t.Fatalf("safe CSV metadata missing: %#v", records[1])
	}
}

func TestReportExportDLPRejectsUnknownFormatBeforeDownload(t *testing.T) {
	f := reportExportFixture()
	w := invokeReportExport(t, "unknown", f)
	if w.Code < 400 || strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("unknown format was not rejected before download: status=%d", w.Code)
	}
	if bytes.Contains(w.Body.Bytes(), []byte(reportExportCanary)) {
		t.Fatal("error response leaked a free-text canary")
	}
}

func TestReportExportDLPRejectsInvalidMetadataAndCancellation(t *testing.T) {
	for name, mutate := range map[string]func(*db.DBFinding){
		"negative-status": func(f *db.DBFinding) { f.TrafficBindings[0].Snapshot.Status = -1 },
		"invalid-time":    func(f *db.DBFinding) { f.CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
		"negative-length": func(f *db.DBFinding) { f.TrafficBindings[0].Snapshot.RespLen = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			f := reportExportFixture()
			mutate(f)
			w := invokeReportExport(t, "json", f)
			if w.Code < 400 || bytes.Contains(w.Body.Bytes(), []byte(reportExportCanary)) {
				t.Fatalf("invalid metadata was not fail-closed: status=%d", w.Code)
			}
		})
	}
	f := reportExportFixture()
	req := httptest.NewRequest(http.MethodGet, "/api/exploration/findings/export", nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	writeFindingsExport(w, req, []*db.DBFinding{f}, "json", time.Now())
	if w.Code < 400 || strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("cancelled export was not blocked: status=%d", w.Code)
	}
}

func TestReportExportDLPRejectsInvalidRouteInputs(t *testing.T) {
	for name, target := range map[string]string{
		"format": "/api/exploration/findings/export?format=unknown",
		"scope":  "/api/exploration/findings/export?format=json&scope=invalid",
		"id":     "/api/exploration/findings/export?format=json&scope=selected&ids=bad",
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			w := httptest.NewRecorder()
			var s *Server
			s.findingsExport(w, req)
			if w.Code != http.StatusBadRequest || bytes.Contains(w.Body.Bytes(), []byte("unknown")) || bytes.Contains(w.Body.Bytes(), []byte("invalid")) {
				t.Fatalf("invalid route input not fixed 400: status=%d", w.Code)
			}
		})
	}
}

func TestReportExportDLPRejectsExcessSelectedBeforeDB(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/exploration/findings/export?format=json&scope=selected&ids="+strings.Repeat("1,", report.MaxFindings)+"1", nil)
	w := httptest.NewRecorder()
	var s *Server
	s.findingsExport(w, req)
	if w.Code != http.StatusUnprocessableEntity || w.Header().Get("Content-Disposition") != "" || !strings.Contains(w.Body.String(), "report export limit exceeded") {
		t.Fatal("excess selected IDs were not blocked before DB access")
	}
}

func TestReportExportBoundedReaderRejectsInvalidLimit(t *testing.T) {
	for _, limit := range []int{0, -1} {
		var pg *db.DB
		rows, err := pg.ListFindingsForExportBounded(db.FindingFilter{}, nil, limit)
		if err == nil || rows != nil {
			t.Fatal("invalid limit reached the database")
		}
	}
}

func TestReportExportEvidenceErrorClassifiesWithoutEcho(t *testing.T) {
	const secret = "evidence-internal-secret-4c9a"
	cases := []struct {
		name string
		err  error
		want int
	}{
		{name: "unknown", err: errors.New(secret), want: http.StatusUnprocessableEntity},
		{name: "conflict-joined", err: errors.Join(db.ErrEvidenceConflict, errors.New(secret)), want: http.StatusConflict},
		{name: "archive-state", err: db.ErrTaskArchiveState, want: http.StatusConflict},
		{name: "finding-missing", err: db.ErrFindingNotFound, want: http.StatusNotFound},
		{name: "evidence-missing", err: db.ErrEvidenceNotFound, want: http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			reportExportEvidenceError(w, tc.err)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d", w.Code, tc.want)
			}
			if strings.Contains(w.Body.String(), secret) || strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
				t.Fatal("error response exposed internal data or download headers")
			}
			if got := strings.TrimSpace(w.Body.String()); got != `{"error":"report export unavailable"}` {
				t.Fatalf("unexpected fixed error body: %q", got)
			}
		})
	}
}

func TestReportExportDLPZipDoesNotReadRawStageAndDisambiguatesNames(t *testing.T) {
	first, second := reportExportFixture(), reportExportFixture()
	second.ID = 42
	second.TrafficBindings[0].FindingID = 42
	path := t.TempDir() + "/findings.zip"
	if err := buildFindingsEvidenceZip(path, []*db.DBFinding{first, second}, "/path/that/does/not/exist", time.Now()); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	seen := map[string]bool{}
	for _, entry := range archive.File {
		if !strings.HasSuffix(entry.Name, ".md") {
			continue
		}
		if seen[entry.Name] {
			t.Fatalf("duplicate ZIP filename: %s", entry.Name)
		}
		seen[entry.Name] = true
	}
	if len(seen) != 2 {
		t.Fatalf("expected two finding markdown files, got %d", len(seen))
	}
	_ = os.Remove(path)
}
