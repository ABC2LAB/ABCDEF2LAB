package server

import (
	"context"
	"database/sql"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
	"github.com/Autumn-27/artex/reportcapture"
	"github.com/Autumn-27/artex/reproduction"
	"net/http"
	"net/url"
	"strconv"
)

func attachDiagnosticCaptures(ctx context.Context, tx *sql.Tx, task int64, entry *report.DiagnosticEntry, imageBytes *int64, pixels bool) error {
	if len(entry.Steps) == 0 {
		return nil
	}
	var readable bool
	if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN to_regclass('diagnostic_captures_v1') IS NULL THEN false ELSE has_table_privilege(current_user,to_regclass('diagnostic_captures_v1'),'SELECT') END`).Scan(&readable); err != nil {
		return err
	}
	for i := range entry.Steps {
		step := &entry.Steps[i]
		if !readable {
			step.Capture.Status = "storage_unavailable"
			continue
		}
		capture, n, err := db.ReadDiagnosticCaptureMetadata(ctx, tx, task, entry.ID, *step)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			step.Capture = reproduction.Capture{Status: "binding_invalid"}
			continue
		}
		if pixels && capture.Status == "captured" {
			encoded := (n + 2) / 3 * 4
			if *imageBytes+encoded > report.MaxDiagnosticCaptureBytes {
				capture = reproduction.Capture{Status: "capacity_omitted"}
			} else {
				capture, err = db.ReadDiagnosticCapture(ctx, tx, task, entry.ID, *step)
				if err != nil {
					capture = reproduction.Capture{Status: "binding_invalid"}
				} else {
					*imageBytes += encoded
				}
			}
		}
		step.Capture = capture
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
	tx, e := s.m.pg.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		writeErr(w, 503, "보고서 저장소를 사용할 수 없습니다.")
		return
	}
	defer tx.Rollback()
	source, e := db.LoadDiagnosticRemediationSource(r.Context(), tx, task, q.Get("entry"))
	if e != nil {
		writeErr(w, 422, "현재 증거 버전을 확인할 수 없습니다.")
		return
	}
	version, e := strconv.ParseInt(source.EvidenceVersion, 10, 64)
	if e != nil {
		writeErr(w, 422, "현재 증거 버전을 확인할 수 없습니다.")
		return
	}
	for _, step := range reproduction.FromStored(task, q.Get("entry"), version, source.EvidenceDigest, source.Context) {
		if step.ID != q.Get("step") {
			continue
		}
		c, err := db.ReadDiagnosticCapture(r.Context(), tx, task, q.Get("entry"), step)
		if err != nil {
			writeErr(w, 422, "현재 이미지 무결성을 확인할 수 없습니다.")
			return
		}
		if c.Digest != q.Get("digest") || reportcapture.Verify(c) != nil {
			break
		}
		if tx.Commit() != nil {
			writeErr(w, 503, "보고서 저장소를 사용할 수 없습니다.")
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", strconv.Itoa(len(c.PNG)))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		_, _ = w.Write(c.PNG)
		return
	}

	writeErr(w, 404, "현재 단계에 결속된 검증된 스크린샷이 없습니다.")
}
