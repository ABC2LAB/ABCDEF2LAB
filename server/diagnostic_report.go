package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/remediation"
	"github.com/Autumn-27/artex/report"
	"github.com/Autumn-27/artex/reproduction"
)

var errDiagnosticReportScope = errors.New("diagnostic report scope invalid")

func diagnosticReadFailureCode(err error) int {
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound
	}
	if errors.Is(err, errDiagnosticReportScope) || errors.Is(err, report.ErrExportBlocked) || errors.Is(err, db.ErrDiagnosticRemediation) || errors.Is(err, db.ErrDiagnosticCandidate) || errors.Is(err, remediation.ErrInvalid) {
		return http.StatusUnprocessableEntity
	}
	return http.StatusServiceUnavailable
}
func unavailableDiagnosticEntry(entry *report.DiagnosticEntry) {
	entry.SourceLoadFailed = true
	entry.BusinessRuleReview = nil
	entry.Severity, entry.SeveritySource, entry.SeverityRationale = "미평가", "", ""
	entry.Steps = nil
	entry.CurrentSource = nil
	entry.RemediationBundle, entry.RemediationSource = nil, nil
	entry.RemediationStale = entry.RemediationArtifactID > 0
	entry.Outcome, entry.Verification = "unknown", "manual_review_required"
}

// 공유 기본값을 보존한다. 상세 사본은 인증된 관리자가 audience=internal을 명시해야 한다.
func (s *Server) diagnosticReport(w http.ResponseWriter, r *http.Request) {
	q, queryErr := url.ParseQuery(r.URL.RawQuery)
	if queryErr != nil {
		writeErr(w, 400, "보고서 조회 형식이 올바르지 않습니다.")
		return
	}
	for key, values := range q {
		if len(values) != 1 || (key != "task" && key != "audience" && key != "format" && key != "download" && key != "run") {
			writeErr(w, 400, "보고서 조회 형식이 올바르지 않습니다.")
			return
		}
	}
	taskID, err := strconv.ParseInt(r.URL.Query().Get("task"), 10, 64)
	audience := r.URL.Query().Get("audience")
	format := r.URL.Query().Get("format")
	reviewJSON := format == "review-json"
	if (format != "" && format != "html" && format != "docx" && !reviewJSON) || (reviewJSON && audience != "internal") ||
		err != nil || taskID <= 0 || r.URL.Query().Get("run") != "" || (audience != "" && audience != "shared" && audience != "internal") {
		writeErr(w, 400, "작업 ID와 보고서 공개 범위를 확인하세요. 회차 필터는 지원하지 않습니다.")
		return
	}
	if s.m == nil || s.m.pg == nil {
		writeErr(w, 503, "보고서 저장소를 사용할 수 없습니다.")
		return
	}
	data, err := loadDiagnosticReportWithCaptures(r.Context(), s.m.pg, taskID, time.Now(), audience == "internal" && !reviewJSON)
	if err != nil {
		code := diagnosticReadFailureCode(err)
		writeErr(w, code, "보고서 범위 또는 저장 증거를 확인할 수 없습니다.")
		return
	}
	if reviewJSON {
		writeDiagnosticBusinessRuleReviews(w, data)
		return
	}
	// v2의 정상 완료 보고서는 원본 후보와 검증 결과를 함께 싣는다. 조회 전에
	// 종료 기록·정책·후보 근거를 검증했으며 원본 스냅샷은 변경하지 않는다.
	if data.Run != nil && (data.Run.VerificationVersion == "passive/v1" || data.Run.VerificationVersion == diagnosisVerificationV2) {
		if data.Run.VerificationVersion == diagnosisVerificationV2 && data.Run.State == report.DiagnosticRunRunning {
			writeErr(w, http.StatusConflict, "점검 진행 중이며 최종 보고서는 아직 없습니다. 내부 검토 기록을 확인하세요.")
			return
		}
		if data.Run.VerificationVersion != diagnosisVerificationV2 || data.Run.State != report.DiagnosticRunCompleted {
			// 기존 v1 및 정상 완료가 아닌 v2 회차의 출력 의미는 유지한다.
			if !projectConfirmedReport(&data) {
				if data.Run.State == report.DiagnosticRunRunning {
					writeErr(w, http.StatusConflict, "점검 진행 중이며 확정된 취약점 보고서는 아직 없습니다. 내부 검토 기록을 확인하세요.")
					return
				}
				if data.Run.VerificationSourceUnavailable {
					writeErr(w, http.StatusServiceUnavailable, "검증 원천을 확인할 수 없어 취약점 보고서를 생성하지 않았습니다.")
					return
				}
				writeErr(w, http.StatusNotFound, "확정된 취약점이 없어 보고서를 생성하지 않았습니다. 점검 완료 기록에서 미완료 후보를 확인하세요.")
				return
			}
		}
	}
	data.Shared = audience != "internal"
	var body []byte
	if format == "docx" {
		body, err = report.DiagnosticDOCX(data)
	} else {
		body, err = report.DiagnosticHTML(data)
	}
	if err != nil {
		writeErr(w, 422, "보고서를 안전하게 출력할 수 없습니다.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if format == "docx" {
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"diagnostic-task-%d.docx\"", taskID))
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src data:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("download") == "1" && format != "docx" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"diagnostic-task-%d.html\"", taskID))
	}
	if r.Context().Err() != nil {
		return
	}
	_, _ = w.Write(body)
}

// 출력 사본만 추린다. 이행점검용 저장 근거는 변경하지 않는다.
func projectConfirmedReport(data *report.DiagnosticReport) bool {
	if data == nil {
		return false
	}
	confirmed := len(data.ProductConfirmed)
	entries := make([]report.DiagnosticEntry, 0, len(data.Entries))
	for _, entry := range data.Entries {
		if entry.Outcome == "confirmed" {
			entries = append(entries, entry)
			confirmed++
		}
	}
	if confirmed == 0 {
		return false
	}
	data.Entries = entries
	return true
}

// 모든 조회를 같은 PostgreSQL 스냅샷에서 수행한다. 원문 파일·대상 URL은 열지 않는다.
func loadDiagnosticReport(ctx context.Context, pg *db.DB, taskID int64, at time.Time) (report.DiagnosticReport, error) {
	return loadDiagnosticReportWithCaptures(ctx, pg, taskID, at, true)
}
func loadDiagnosticReportWithCaptures(ctx context.Context, pg *db.DB, taskID int64, at time.Time, images bool) (report.DiagnosticReport, error) {
	return loadDiagnosticSnapshot(ctx, pg.DB, taskID, func(tx *sql.Tx) (report.DiagnosticReport, error) {
		return readDiagnosticReportWithCaptures(ctx, tx, taskID, at, images)
	})
}

func loadDiagnosticSnapshot(ctx context.Context, pool *sql.DB, taskID int64, read func(*sql.Tx) (report.DiagnosticReport, error)) (report.DiagnosticReport, error) {
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return report.DiagnosticReport{}, err
	}
	defer tx.Rollback()
	out, err := read(tx)
	if err != nil {
		return out, err
	}
	product, err := loadDiagnosticProductSnapshot(ctx, tx, taskID, len(out.Entries))
	if err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	err = product.addToReport(ctx, &out)
	return out, err
}
func readDiagnosticReport(ctx context.Context, tx *sql.Tx, taskID int64, at time.Time) (report.DiagnosticReport, error) {
	return readDiagnosticReportWithCaptures(ctx, tx, taskID, at, true)
}
func readDiagnosticReportWithCaptures(ctx context.Context, tx *sql.Tx, taskID int64, at time.Time, images bool) (report.DiagnosticReport, error) {
	out, err := readDiagnosticEntries(ctx, tx, taskID, at, images, false)
	if err == nil {
		err = readDiagnosticOverview(ctx, tx, taskID, &out)
	}
	if err == nil {
		err = readWorkflowReport(ctx, tx, taskID, &out)
	}
	return out, err
}

func readDiagnosticEntries(ctx context.Context, tx *sql.Tx, taskID int64, at time.Time, images, includeMissingActions bool) (report.DiagnosticReport, error) {
	out := report.DiagnosticReport{TaskID: taskID, GeneratedAt: at}
	if taskID <= 0 {
		return out, errDiagnosticReportScope
	}
	var expID int64
	if err := tx.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL`, taskID).Scan(&expID); err != nil {
		return out, err
	}
	// 후보 판정은 종료 기록에 결속된다. 이행점검도 이 entry 읽기 경로를 공유한다.
	// 개요는 entry 조립 뒤에 읽으므로 후보를 평가하기 전에 먼저 종료 기록을 읽는다.
	run, err := readDiagnosticRun(ctx, tx, taskID)
	if err != nil {
		return out, err
	}
	out.Run = run
	// 원문 TEXT를 드라이버로 읽기 전에 DB에서 총 바이트를 검사한다.
	var storedBytes int64
	if err := tx.QueryRowContext(ctx, `SELECT
 COALESCE((SELECT sum(octet_length(COALESCE(name,''))+octet_length(COALESCE(vulnclass,''))+octet_length(COALESCE(severity,''))+octet_length(COALESCE(status,''))+octet_length(COALESCE(summary,''))+octet_length(COALESCE(evidence,''))+octet_length(COALESCE(report,''))) FROM findings WHERE task_id=$1),0)
 + COALESCE((SELECT sum(octet_length(m.canonical)+octet_length(v.payload_canonical)+octet_length(i.execution_ref)) FROM e3_verdict_issuances i JOIN e3_oracle_verdicts v ON v.id=i.verdict_id JOIN e3_evidence_manifests m ON m.id=i.manifest_id AND m.task_id=i.task_id WHERE i.task_id=$1 AND i.source_kind='eval'),0)`, taskID).Scan(&storedBytes); err != nil {
		return out, err
	}
	if storedBytes > report.MaxDiagnosticReportBytes {
		return out, report.ErrExportBlocked
	}
	candidates, err := db.ListDiagnosticReportCandidates(ctx, tx, expID, taskID, report.MaxFindings+1)
	if err != nil {
		return out, err
	}
	assessments := make(map[int64]report.CandidateAssessment)
	if out.Run != nil && (out.Run.VerificationVersion == "passive/v1" || out.Run.VerificationVersion == diagnosisVerificationV2) {
		for _, a := range out.Run.CandidateAssessments {
			if a.CandidateID <= 0 || assessments[a.CandidateID].CandidateID != 0 {
				return out, errDiagnosticReportScope
			}
			assessments[a.CandidateID] = a
		}
	}
	for _, row := range candidates {
		c := row.Candidate.NormalizedSeverity()
		if row.Unavailable {
			log.Printf("diagnostic source unavailable: task=%d entry=candidate-%d reason=source_invalid", taskID, c.ID)
			out.Entries = append(out.Entries, report.DiagnosticEntry{ID: fmt.Sprintf("candidate-%d", c.ID), Kind: "candidate", Title: "저장 후보 근거 확인 실패", Outcome: "unknown", Verification: "manual_review_required", Severity: "미평가", SourceLoadFailed: true})
			continue
		}
		storedBytes += int64(len(c.Evidence) + len(c.Name) + len(c.VulnClass) + len(c.Summary) + len(c.Remediation) + len(c.RunRef) + len(c.CallRef) + len(c.SeverityRationale) + len(c.SuggestedSeverity) + len(c.SeveritySource))
		if storedBytes > report.MaxDiagnosticReportBytes {
			return out, report.ErrExportBlocked
		}
		if c.TaskID != taskID || c.Validate() != nil {
			return out, errDiagnosticReportScope
		}
		outcome := "review"
		switch c.Classification {
		case "policy_not_executed", "unsupported", "forward_not_attempted", "isolation_unavailable":
			outcome = "not_executed"
		case "result_unknown", "tool_failure", "transient_failure", "terminated":
			outcome = "unknown"
		}
		severity := "미평가"
		if c.SuggestedSeverity != "" {
			severity = c.SuggestedSeverity
		}
		entry := report.DiagnosticEntry{ID: fmt.Sprintf("candidate-%d", c.ID), Kind: "candidate", LanguageMixed: c.LanguageMixed, Run: c.RunRef, IntentID: c.IntentID, Title: c.Name, VulnClass: c.VulnClass, Severity: severity, SeveritySource: c.SeveritySource, SeverityRationale: c.SeverityRationale, Status: c.Classification, Verification: c.Verification, Outcome: outcome, Summary: c.Summary, Remediation: c.Remediation, Evidence: c.Evidence, EvidenceVersion: c.EvidenceVersion, Digest: c.EvidenceDigest, ObservedAt: c.ObservedAt, Truncated: c.OutputTruncated}
		if out.Run != nil && (out.Run.VerificationVersion == "passive/v1" || out.Run.VerificationVersion == diagnosisVerificationV2) && out.Run.State != report.DiagnosticRunRunning && c.RunRef == out.Run.RunRef {
			a, found := assessments[c.ID]
			if !found || a.EvidenceDigest != c.EvidenceDigest {
				return out, errDiagnosticReportScope
			}
			check := candidateverify.Assess(c)
			if out.Run.VerificationVersion == diagnosisVerificationV2 {
				check = candidateverify.Result{Status: a.Status, Reason: a.Reason, Attempts: a.AttemptRefs, Sources: a.Sources}
				if !candidateverify.ValidateStoredV2(c, check) {
					return out, errDiagnosticReportScope
				}
			}
			if check.Status != a.Status || check.Reason != a.Reason || !equalStringSlices(check.Attempts, a.AttemptRefs) {
				return out, errDiagnosticReportScope
			}
			entry.AssessmentStatus, entry.AssessmentReason = check.Status, check.Reason
			if out.Run.VerificationVersion == "passive/v1" {
				entry.Status = check.Reason // 기존 v1 출력 표현 유지
			}
			switch check.Status {
			case candidateverify.Confirmed:
				entry.Outcome, entry.Verification = "confirmed", "passive_observation_v1"
				if out.Run.VerificationVersion == diagnosisVerificationV2 {
					entry.Verification = "passive_observation_v2"
				}
				entry.Severity, entry.SeveritySource, entry.SeverityRationale = "info", "", ""
				if c.VerificationClaim == "html_nosniff_missing" {
					entry.Title = "HTML 문서 보안 헤더 누락"
					entry.Summary = "동일한 승인 경로 패턴의 독립적인 HTML 조회 두 건 이상에서 X-Content-Type-Options: nosniff 설정이 없었습니다. 관찰한 설정 누락을 확정하며 악용 가능성은 별도 판단 대상입니다."
					entry.Remediation = "해당 HTML 응답에 X-Content-Type-Options: nosniff를 설정하고 배포 범위를 확인하세요."
					entry.Report = "수정 후 해당 경로 패턴의 정상 HTML 응답에서 nosniff 헤더가 일관되게 반환되는지 확인하세요."
				} else {
					entry.Title = "Server 응답 헤더의 버전 노출"
					entry.Summary = "동일한 승인 경로 패턴의 독립적인 응답 두 건 이상에서 Server 헤더의 소프트웨어 버전 표기가 확인됐습니다. 버전 노출 사실만 확정하며 해당 버전의 취약성은 주장하지 않습니다."
					entry.Remediation = "Server 응답 헤더에서 상세 버전 표기를 제거할지 운영 정책에 따라 검토하고 설정을 적용하세요."
					entry.Report = "수정 후 해당 경로 패턴의 정상 응답에서 Server 헤더에 상세 버전이 남지 않는지 확인하세요."
				}
			case candidateverify.Negative:
				entry.Outcome = "negative"
			case candidateverify.Insufficient:
				if check.Reason == "source_unavailable" {
					entry.Outcome = "unknown"
				} else {
					entry.Outcome = "review"
				}
			case candidateverify.Unsupported:
				entry.Outcome = "review"
			default:
				return out, errDiagnosticReportScope
			}
		}
		out.Entries = append(out.Entries, entry)
	}
	if len(out.Entries) > report.MaxFindings {
		return out, report.ErrExportBlocked
	}
	rows, err := tx.QueryContext(ctx, `SELECT f.id,COALESCE(f.name,''),COALESCE(f.vulnclass,''),COALESCE(f.severity,''),COALESCE(f.status,'pending'),COALESCE(f.summary,''),COALESCE(f.evidence,''),COALESCE(f.report,''),f.evidence_version,f.report_evidence_version,f.created_at,
 COALESCE(l.evidence_version,0),COALESCE(v.oracle_value,''),COALESCE(i.execution_ref,''),COALESCE(i.intent_id,0),COALESCE(m.canonical,''::bytea),COALESCE(m.digest,''::bytea),COALESCE(v.payload_canonical,''::bytea),COALESCE(v.payload_digest,''::bytea)
 FROM findings f LEFT JOIN e3_verdict_links l ON l.finding_id=f.id AND l.task_id=f.task_id
 LEFT JOIN e3_verdict_issuances i ON i.id=l.issuance_id AND i.task_id=f.task_id AND i.source_kind='eval'
 LEFT JOIN e3_oracle_verdicts v ON v.id=i.verdict_id
 LEFT JOIN e3_evidence_manifests m ON m.id=i.manifest_id AND m.task_id=f.task_id
 WHERE f.task_id=$1 ORDER BY f.id LIMIT $2`, taskID, report.MaxFindings+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var e report.DiagnosticEntry
		var id, reportVersion, initialVersion int64
		var value string
		var manifest, digest, payload, payloadDigest []byte
		err = rows.Scan(&id, &e.Title, &e.VulnClass, &e.Severity, &e.Status, &e.Summary, &e.Evidence, &e.Report, &e.EvidenceVersion, &reportVersion, &e.ObservedAt, &initialVersion, &value, &e.Run, &e.IntentID, &manifest, &digest, &payload, &payloadDigest)
		if err != nil {
			rows.Close()
			return out, err
		}
		e.ID = fmt.Sprintf("finding-%d", id)
		e.Kind = "finding"
		e.Outcome = "review"
		e.Verification = "manual_review_required"
		e.Stale = e.Report != "" && reportVersion != e.EvidenceVersion
		if value != "" {
			mh, ph := sha256.Sum256(manifest), sha256.Sum256(payload)
			if !equalDigest(mh[:], digest) || !equalDigest(ph[:], payloadDigest) {
				rows.Close()
				return out, errDiagnosticReportScope
			}
			e.Digest = hex.EncodeToString(digest)
			if value == "confirmed" && initialVersion == e.EvidenceVersion && !e.Stale {
				e.Outcome = "confirmed"
				e.Verification = "stored_verification"
			}
		}
		if e.Outcome != "confirmed" {
			e.Severity = "미평가"
		}
		if e.Run == "" {
			e.Run = "저장 회차 귀속 미확인"
		}
		e.Remediation = "저장된 상세 보고서의 수정 권고를 검토하세요. 없으면 유형·대상 기술·관찰 증거를 바탕으로 권한 있는 담당자의 검토가 필요합니다."
		out.Entries = append(out.Entries, e)
		if len(out.Entries) > report.MaxFindings {
			rows.Close()
			return out, report.ErrExportBlocked
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	// finding으로 승격되지 않은 E③ 판정도 진단 한계에서 사라지지 않는다.
	rows, err = tx.QueryContext(ctx, `SELECT i.id,i.intent_id,i.execution_ref,v.oracle_value,i.issued_at,m.evidence_revision,m.canonical,m.digest,v.payload_canonical,v.payload_digest
 FROM e3_verdict_issuances i JOIN e3_oracle_verdicts v ON v.id=i.verdict_id
 JOIN e3_evidence_manifests m ON m.id=i.manifest_id AND m.task_id=i.task_id
 LEFT JOIN e3_verdict_links l ON l.issuance_id=i.id AND l.task_id=i.task_id
 WHERE i.task_id=$1 AND i.source_kind='eval' AND l.id IS NULL ORDER BY i.id LIMIT $2`, taskID, report.MaxFindings+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var e report.DiagnosticEntry
		var id int64
		var value string
		var manifest, digest, payload, pd []byte
		if err = rows.Scan(&id, &e.IntentID, &e.Run, &value, &e.ObservedAt, &e.EvidenceVersion, &manifest, &digest, &payload, &pd); err != nil {
			rows.Close()
			return out, err
		}
		mh, ph := sha256.Sum256(manifest), sha256.Sum256(payload)
		if !equalDigest(mh[:], digest) || !equalDigest(ph[:], pd) {
			rows.Close()
			return out, errDiagnosticReportScope
		}
		e.ID = fmt.Sprintf("verdict-%d", id)
		e.Kind = "verdict"
		e.Title = "등록 조회 구조화 판정"
		e.VulnClass = "IDOR"
		e.Severity = "미평가"
		e.Status = value
		e.Verification = "stored_verification"
		e.Outcome = "review"
		if value == "allowed" {
			e.Outcome = "negative"
		}
		if value == "undecidable" {
			e.Outcome = "unknown"
		}
		e.Digest = hex.EncodeToString(digest)
		e.Summary = "기존 E③ 저장 판정입니다. 확정 finding에 연결되지 않은 결과를 보존합니다."
		e.Evidence = "원문 manifest는 보호된 저장소에 보존됩니다. 이 요약만으로 재현 완료나 다른 자원의 안전을 보장하지 않습니다."
		out.Entries = append(out.Entries, e)
		if len(out.Entries) > report.MaxFindings {
			rows.Close()
			return out, report.ErrExportBlocked
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	if err := attachDiagnosticRemediations(ctx, tx, taskID, &out, includeMissingActions); err != nil {
		return out, err
	}
	var captureBytes int64
	for i := range out.Entries {
		entry := &out.Entries[i]
		if entry.SourceLoadFailed {
			entry.RemediationBundle, entry.RemediationSource = nil, nil
			entry.RemediationStale = entry.RemediationArtifactID > 0
			out.SourceFailures++
			continue
		}
		source, err := db.LoadDiagnosticRemediationSourceIsolated(ctx, tx, taskID, entry.ID)
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, db.ErrDiagnosticRemediation) {
				return out, err
			}
			log.Printf("diagnostic source unavailable: task=%d entry=%s reason=source_invalid", taskID, entry.ID)
			unavailableDiagnosticEntry(entry)
			out.SourceFailures++
			continue
		}
		entry.CurrentSource = &source
		if entry.Stale {
			continue
		}
		entry.Steps = reproduction.FromStored(taskID, entry.ID, entry.EvidenceVersion, entry.Digest, source.Context)
		bound := true
		for _, step := range entry.Steps {
			if step.RunRef != entry.Run || reproduction.Validate(step, entry.EvidenceVersion, entry.Digest) != nil {
				bound = false
				break
			}
		}
		if !bound {
			log.Printf("diagnostic source unavailable: task=%d entry=%s reason=observation_binding_invalid", taskID, entry.ID)
			unavailableDiagnosticEntry(entry)
			out.SourceFailures++
			continue
		}
		entry.StepsTruncated = source.Context.ObservationsTruncated
		if entry.Kind == "candidate" && entry.Outcome == "review" {
			entry.BusinessRuleReview = diagnosticBusinessRuleReview(entry)
		}
		if err := attachDiagnosticCaptures(ctx, tx, taskID, entry, &captureBytes, images); err != nil {
			return out, err
		}
	}
	return out, nil
}
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func equalDigest(a, b []byte) bool {
	return len(a) == sha256.Size && len(b) == sha256.Size && strings.EqualFold(hex.EncodeToString(a), hex.EncodeToString(b))
}
