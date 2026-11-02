package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/dlp"
	"github.com/Autumn-27/artex/report"
)

func validReportExportFormat(format string) bool {
	switch format {
	case "md-single", "md-zip", "csv", "json":
		return true
	default:
		return false
	}
}

// Export failures must not echo query values, DB errors, or filesystem paths.
func reportExportEvidenceError(w http.ResponseWriter, err error) {
	status := http.StatusUnprocessableEntity
	if errors.Is(err, db.ErrEvidenceConflict) || errors.Is(err, db.ErrTaskArchiveState) {
		status = http.StatusConflict
	}
	if errors.Is(err, db.ErrFindingNotFound) || errors.Is(err, db.ErrEvidenceNotFound) {
		status = http.StatusNotFound
	}
	writeErr(w, status, "report export unavailable")
}

// writeFindingsExport owns the final output boundary for every download format.
// It never opens source evidence blobs or resolves raw asset labels.
func writeFindingsExport(w http.ResponseWriter, r *http.Request, findings []*db.DBFinding, format string, now time.Time) {
	if !validReportExportFormat(format) {
		writeErr(w, 400, "bad export format")
		return
	}
	if r.Context().Err() != nil {
		writeErr(w, 408, "report export cancelled")
		return
	}
	fs, err := report.ProjectFindings(findings)
	if err != nil || now.UTC().Year() < 0 || now.UTC().Year() > 9999 {
		writeErr(w, 422, "report export blocked")
		return
	}
	stamp := now.UTC().Format("20060102-150405")
	setDownload := func(contentType, ext string) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="findings-%s.%s"`, stamp, ext))
	}
	var payload []byte
	var contentType, ext string
	switch format {
	case "md-single":
		payload = []byte(report.FindingsMarkdown(fs, now))
		contentType, ext = "text/markdown; charset=utf-8", "md"
	case "csv":
		payload = report.FindingsCSV(fs)
		contentType, ext = "text/csv; charset=utf-8", "csv"
	case "json":
		out := make([]FindingDTO, 0, len(fs))
		for _, f := range fs {
			d := findingFromDB(f, nil)
			for _, id := range f.AssetIDs {
				d.Assets = append(d.Assets, FindingAssetDTO{ID: i64s(id), Type: dlp.Redacted, Label: dlp.Redacted})
			}
			out = append(out, d)
		}
		payload, err = json.MarshalIndent(out, "", "  ")
		if err != nil {
			writeErr(w, 422, "report export blocked")
			return
		}
		payload = append(payload, '\n')
		contentType, ext = "application/json; charset=utf-8", "json"
	case "md-zip":
		stage, err := os.MkdirTemp("", "artex-finding-export-")
		if err != nil {
			writeErr(w, 500, "report export unavailable")
			return
		}
		defer os.RemoveAll(stage)
		path := filepath.Join(stage, "findings.zip")
		if err := buildFindingsEvidenceZip(path, fs, stage, now); err != nil {
			writeErr(w, 500, "report export unavailable")
			return
		}
		file, err := os.Open(path)
		if err != nil {
			writeErr(w, 500, "report export unavailable")
			return
		}
		defer file.Close()
		if r.Context().Err() != nil {
			writeErr(w, 408, "report export cancelled")
			return
		}
		setDownload("application/zip", "zip")
		http.ServeContent(w, r, "findings.zip", now, file)
		return
	}
	if r.Context().Err() != nil {
		writeErr(w, 408, "report export cancelled")
		return
	}
	setDownload(contentType, ext)
	if n, err := w.Write(payload); err != nil || n != len(payload) {
		log.Print("report export: response write failed")
	}
}
