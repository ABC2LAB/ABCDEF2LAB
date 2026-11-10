package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/dlp"
	"github.com/Autumn-27/artex/remediation"
	"github.com/Autumn-27/artex/report"
	"github.com/Autumn-27/artex/reproduction"
)

type diagnosticRemediationDTO struct {
	ID        int64               `json:"id"`
	EntryID   string              `json:"entry_id"`
	CreatedAt time.Time           `json:"created_at"`
	Stale     bool                `json:"stale"`
	Bundle    *remediation.Bundle `json:"bundle,omitempty"`
}
type diagnosticRemediationEntryDTO struct {
	VulnClass         string                    `json:"vuln_class"`
	Severity          string                    `json:"severity"`
	Steps             []reproduction.Step       `json:"steps,omitempty"`
	StepsTruncated    bool                      `json:"steps_truncated,omitempty"`
	Context           *remediation.Context      `json:"context,omitempty"`
	Technologies      []remediation.Technology  `json:"technologies,omitempty"`
	EntryID           string                    `json:"entry_id"`
	Title             string                    `json:"title"`
	Outcome           string                    `json:"outcome"`
	Status            string                    `json:"status"`
	Verification      string                    `json:"verification"`
	EvidenceVersion   int64                     `json:"evidence_version"`
	SourceDigest      string                    `json:"source_digest"`
	EvidenceDigest    string                    `json:"evidence_digest"`
	ReportVersion     string                    `json:"report_version"`
	Available         bool                      `json:"available"`
	UnavailableReason string                    `json:"unavailable_reason"`
	Summary           string                    `json:"summary"`
	Evidence          string                    `json:"evidence"`
	Report            string                    `json:"report"`
	Remediation       string                    `json:"remediation"`
	Run               string                    `json:"run"`
	IntentID          int64                     `json:"intent_id"`
	ObservedAt        time.Time                 `json:"observed_at"`
	Record            *diagnosticRemediationDTO `json:"record,omitempty"`
}

func attachDiagnosticRemediations(ctx context.Context, tx *sql.Tx, taskID int64, out *report.DiagnosticReport) error {
	records, err := db.ListDiagnosticRemediationRecords(ctx, tx, taskID, report.MaxFindings+1)
	if err != nil {
		return err
	}
	if len(records) > report.MaxFindings {
		return report.ErrExportBlocked
	}
	entries := map[string]*report.DiagnosticEntry{}
	for i := range out.Entries {
		entries[out.Entries[i].ID] = &out.Entries[i]
	}
	for _, record := range records {
		entry := entries[record.EntryID]
		if entry == nil {
			continue
		}
		entry.RemediationArtifactID = record.ID
		entry.RemediationCreatedAt = record.CreatedAt
		entry.RemediationStale = record.Stale
		if record.Stale {
			continue
		}
		bundle, source := record.Bundle, record.Source
		entry.RemediationBundle = &bundle
		entry.RemediationSource = &source
	}
	return nil
}
func remediationScope(r *http.Request) (int64, bool, error) {
	id, err := strconv.ParseInt(r.URL.Query().Get("task"), 10, 64)
	audience := r.URL.Query().Get("audience")
	if err != nil || id <= 0 || r.URL.Query().Get("run") != "" || (audience != "" && audience != "shared" && audience != "internal") {
		return 0, false, errDiagnosticReportScope
	}
	return id, audience == "internal", nil
}
func remediationError(w http.ResponseWriter, err error) {
	code := 422
	if errors.Is(err, sql.ErrNoRows) {
		code = 404
	}
	if errors.Is(err, db.ErrDiagnosticRemediationStale) {
		code = 409
	}
	writeErr(w, code, "조치 자료의 범위·버전·입력을 확인할 수 없습니다. 새로고침 후 확인하세요.")
}
func writeRemediationJSON(w http.ResponseWriter, value any) {
	payload, err := json.Marshal(value)
	if err != nil || len(payload) > report.MaxDiagnosticReportBytes {
		writeErr(w, 422, "조치 자료가 출력 한도를 넘었습니다.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(payload)
}

// 기본 공유 응답에는 자유문장·생성 프롬프트·원본 digest를 제공하지 않는다.
func (s *Server) listDiagnosticRemediation(w http.ResponseWriter, r *http.Request) {
	taskID, internal, err := remediationScope(r)
	if err != nil {
		writeErr(w, 400, "명시적인 작업과 공개 범위를 선택하세요.")
		return
	}
	if s.m == nil || s.m.pg == nil {
		writeErr(w, 503, "보고서 저장소를 사용할 수 없습니다.")
		return
	}
	tx, err := s.m.pg.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		remediationError(w, err)
		return
	}
	defer tx.Rollback()
	data, err := readDiagnosticReport(r.Context(), tx, taskID, time.Now())
	if err != nil {
		remediationError(w, err)
		return
	}

	entries := make([]diagnosticRemediationEntryDTO, 0, len(data.Entries))
	for _, e := range data.Entries {
		dto := diagnosticRemediationEntryDTO{EntryID: e.ID, VulnClass: dlp.Redacted, Severity: "미평가", Title: dlp.Redacted, Outcome: e.Outcome, Status: dlp.Redacted, Verification: e.Verification, EvidenceVersion: e.EvidenceVersion, Summary: dlp.Redacted, Evidence: dlp.Redacted, Report: dlp.Redacted, Remediation: dlp.Redacted, Run: dlp.Redacted, IntentID: e.IntentID, ObservedAt: e.ObservedAt, UnavailableReason: "내부 상세 모드를 명시적으로 선택하세요."}
		if internal {
			dto.Steps = e.Steps
			dto.StepsTruncated = e.StepsTruncated
			dto.Title = dlp.DiagnosticText(e.Title)
			dto.VulnClass = dlp.DiagnosticText(e.VulnClass)
			dto.Severity = dlp.DiagnosticText(e.Severity)
			dto.Status = dlp.DiagnosticText(e.Status)
			dto.Summary = dlp.DiagnosticText(e.Summary)
			dto.Evidence = dlp.DiagnosticText(e.Evidence)
			dto.Report = dlp.DiagnosticText(e.Report)
			dto.Remediation = dlp.DiagnosticText(e.Remediation)
			dto.Run = dlp.DiagnosticText(e.Run)
			source, loadErr := db.LoadDiagnosticRemediationSource(r.Context(), tx, taskID, e.ID)
			if loadErr != nil {
				remediationError(w, loadErr)
				return
			}
			context := source.Context
			dto.Context = &context
			dto.SourceDigest = source.SourceDigest
			dto.ReportVersion = source.ReportVersion
			dto.EvidenceDigest = source.EvidenceDigest
			preview, buildErr := remediation.Build(source)
			if buildErr == nil {
				dto.Technologies = preview.Technologies
				dto.Context = &preview.Context
			}
			dto.Available = buildErr == nil
			dto.UnavailableReason = ""
			if !dto.Available {
				dto.UnavailableReason = "현재 근거로 조치를 생성할 수 없습니다. 검증된 증거 스냅샷을 확인하세요."
			}
		}
		if e.RemediationArtifactID > 0 {
			dto.Record = &diagnosticRemediationDTO{ID: e.RemediationArtifactID, EntryID: e.ID, CreatedAt: e.RemediationCreatedAt, Stale: e.RemediationStale}
			if internal && !e.RemediationStale {
				dto.Record.Bundle = e.RemediationBundle
			}
		}
		entries = append(entries, dto)
	}
	if err = tx.Commit(); err != nil {
		remediationError(w, err)
		return
	}
	writeRemediationJSON(w, struct {
		TaskID  int64                           `json:"task_id"`
		Entries []diagnosticRemediationEntryDTO `json:"entries"`
	}{taskID, entries})
}
func (s *Server) createDiagnosticRemediation(w http.ResponseWriter, r *http.Request) {
	taskID, internal, err := remediationScope(r)
	if err != nil {
		writeErr(w, 400, "명시적인 작업과 공개 범위를 선택하세요.")
		return
	}
	if !internal {
		writeErr(w, 403, "조치 생성은 내부 상세 모드에서 명시적으로 요청하세요.")
		return
	}
	if s.m == nil || s.m.pg == nil {
		writeErr(w, 503, "보고서 저장소를 사용할 수 없습니다.")
		return
	}
	var input struct {
		EntryID              string                   `json:"entry_id"`
		ExpectedSourceDigest string                   `json:"expected_source_digest"`
		Technologies         []remediation.Technology `json:"technologies"`
		Context              *remediation.Context     `json:"context"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&input); err != nil {
		writeErr(w, 400, "조치 입력 형식이 올바르지 않습니다.")
		return
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF || len(input.Technologies) > 32 {
		writeErr(w, 400, "조치 입력 형식이 올바르지 않습니다.")
		return
	}
	for i := range input.Technologies {
		input.Technologies[i].Source = "user_provided"
		input.Technologies[i].EvidenceRef = ""
		input.Technologies[i].ObservedAt = ""
	}
	var contexts []remediation.Context
	if input.Context != nil {
		// 관측 출처와 단계 귀속은 저장 원본만 발급한다. 사용자 보완은 상단 문자열만 받는다.
		input.Context.Source = "user_provided"
		input.Context.Observations = nil
		input.Context.ObservationsTruncated = false
		contexts = append(contexts, *input.Context)
	}
	record, err := s.m.pg.SaveDiagnosticRemediation(r.Context(), taskID, input.EntryID, input.ExpectedSourceDigest, input.Technologies, contexts...)
	if err != nil {
		remediationError(w, err)
		return
	}
	writeRemediationJSON(w, diagnosticRemediationDTO{ID: record.ID, EntryID: record.EntryID, CreatedAt: record.CreatedAt, Bundle: &record.Bundle})
}
func (s *Server) copyDiagnosticRemediation(w http.ResponseWriter, r *http.Request) {
	taskID, internal, err := remediationScope(r)
	if err != nil {
		writeErr(w, 400, "명시적인 작업과 공개 범위를 선택하세요.")
		return
	}
	if !internal {
		writeErr(w, 403, "프롬프트 복사는 내부 상세 모드에서만 가능합니다.")
		return
	}
	recordID, err := strconv.ParseInt(r.URL.Query().Get("record"), 10, 64)
	if err != nil || recordID <= 0 || r.URL.Query().Get("entry") == "" || r.URL.Query().Get("digest") == "" {
		writeErr(w, 400, "복사할 조치의 버전을 지정하세요.")
		return
	}
	if s.m == nil || s.m.pg == nil {
		writeErr(w, 503, "보고서 저장소를 사용할 수 없습니다.")
		return
	}
	tx, err := s.m.pg.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		remediationError(w, err)
		return
	}
	defer tx.Rollback()
	record, err := db.GetDiagnosticRemediationRecord(r.Context(), tx, taskID, r.URL.Query().Get("entry"))
	if err != nil {
		remediationError(w, err)
		return
	}
	if record == nil {
		remediationError(w, sql.ErrNoRows)
		return
	}
	if record.Stale || record.ID != recordID || record.Bundle.InputDigest != r.URL.Query().Get("digest") {
		remediationError(w, db.ErrDiagnosticRemediationStale)
		return
	}
	if err = tx.Commit(); err != nil {
		remediationError(w, err)
		return
	}
	writeRemediationJSON(w, struct {
		EntryID     string `json:"entry_id"`
		RecordID    int64  `json:"record_id"`
		InputDigest string `json:"input_digest"`
		AIPrompt    string `json:"ai_prompt"`
	}{record.EntryID, record.ID, record.Bundle.InputDigest, record.Bundle.AIPrompt})
}
