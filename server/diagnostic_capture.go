package server

import (
	"context"
	"database/sql"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
	"github.com/Autumn-27/artex/reportcapture"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

func attachDiagnosticCaptures(ctx context.Context, tx *sql.Tx, task int64, entry *report.DiagnosticEntry, storedBytes *int64) error {
	if len(entry.Steps) == 0 {
		return nil
	}
	var readable bool
	if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN to_regclass('diagnostic_captures_v1') IS NULL THEN false ELSE has_table_privilege(current_user,to_regclass('diagnostic_captures_v1'),'SELECT') END`).Scan(&readable); err != nil {
		return err
	}
	for i := range entry.Steps {
		if !readable {
			entry.Steps[i].Capture.Status = "storage_unavailable"
			continue
		}
		capture, err := db.ReadDiagnosticCapture(ctx, tx, task, entry.ID, entry.Steps[i])
		if err != nil {
			return err
		}
		*storedBytes += int64((len(capture.PNG) + 2) / 3 * 4)
		if *storedBytes > report.MaxDiagnosticReportBytes {
			return report.ErrExportBlocked
		}
		entry.Steps[i].Capture = capture
	}
	return nil
}

// 내부 모드에서 같은 작업·항목·단계·현재 증거 digest에 결속한 이미지만 반환한다.
func (s *Server) diagnosticCaptureImage(w http.ResponseWriter, r *http.Request) {
	q, queryErr := url.ParseQuery(r.URL.RawQuery)
	if queryErr != nil {
		writeErr(w, 400, "이미지 참조 형식이 올바르지 않습니다.")
		return
	}
	task, e := strconv.ParseInt(q.Get("task"), 10, 64)
	if e != nil || task <= 0 || strconv.FormatInt(task, 10) != q.Get("task") || q.Get("audience") != "internal" || len(q) != 5 {
		writeErr(w, 403, "내부용으로 동일 작업·항목·단계·이미지 digest를 지정하세요.")
		return
	}
	for k, v := range q {
		if len(v) != 1 || (k != "task" && k != "audience" && k != "entry" && k != "step" && k != "digest") {
			writeErr(w, 400, "이미지 참조 형식이 올바르지 않습니다.")
			return
		}
	}
	if s.m == nil || s.m.pg == nil {
		writeErr(w, 503, "보고서 저장소를 사용할 수 없습니다.")
		return
	}
	data, e := loadDiagnosticReport(r.Context(), s.m.pg, task, time.Now())
	if e != nil {
		writeErr(w, 422, "현재 증거 버전을 확인할 수 없습니다.")
		return
	}
	for _, entry := range data.Entries {
		if entry.ID != q.Get("entry") || entry.Stale {
			continue
		}
		for _, step := range entry.Steps {
			c := step.Capture
			if step.ID != q.Get("step") || c.Digest != q.Get("digest") || reportcapture.Verify(c) != nil {
				continue
			}
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Length", strconv.Itoa(len(c.PNG)))
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
			_, _ = w.Write(c.PNG)
			return
		}
	}
	writeErr(w, 404, "현재 단계에 결속된 검증된 스크린샷이 없습니다.")
}
