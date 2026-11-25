package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
)

var errCandidateVerificationHistory = errors.New("candidate verification history invalid")

// 완료 보고서는 같은 PostgreSQL snapshot에서 최근 요청의 처리 상태만 투영한다.
// 최초 자동 판정과 원본 종료 기록을 덮어쓰지 않는다.
func attachLatestCandidateVerifications(ctx context.Context, tx *sql.Tx, taskID int64, candidates []db.DiagnosticReportCandidate, automatic map[int64]report.CandidateAssessment, out *report.DiagnosticReport) error {
	byID := make(map[int64]db.DiagnosticReportCandidate, len(candidates))
	entryIndex := make(map[int64]int, len(candidates))
	for _, row := range candidates {
		if _, exists := byID[row.Candidate.ID]; exists {
			return errCandidateVerificationHistory
		}
		byID[row.Candidate.ID] = row
	}
	for i, entry := range out.Entries {
		if entry.Kind != "candidate" {
			continue
		}
		var id int64
		if _, err := fmt.Sscanf(entry.ID, "candidate-%d", &id); err != nil || id <= 0 {
			return errCandidateVerificationHistory
		}
		entryIndex[id] = i
	}
	rows, err := tx.QueryContext(ctx, `SELECT q.id,q.candidate_id,q.request_key,q.method,q.original_evidence_digest,q.original_evidence_version,q.original_run_ref,q.requested_at,e.step,e.process_state,e.verdict,e.reason,e.sources,e.sources_digest,e.rule_version,e.execution_ref,e.approval_ref,e.event_at
FROM (SELECT DISTINCT ON (candidate_id) * FROM diagnostic_candidate_verification_requests WHERE task_id=$1 ORDER BY candidate_id,id DESC) q
LEFT JOIN LATERAL (SELECT * FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
ORDER BY q.candidate_id LIMIT $2`, taskID, report.MaxFindings+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if count > report.MaxFindings {
			return report.ErrExportBlocked
		}
		row, step, err := scanCandidateVerificationAttempt(rows)
		if err != nil {
			return err
		}
		var candidateID int64
		if _, err := fmt.Sscanf(row.CandidateID, "%d", &candidateID); err != nil {
			return errCandidateVerificationHistory
		}
		candidateRow, found := byID[candidateID]
		assessment, assessed := automatic[candidateID]
		index, included := entryIndex[candidateID]
		if !found || !included || !assessed || candidateRow.Unavailable ||
			!validStoredAutomaticCandidateAssessment(candidateRow.Candidate, assessment) ||
			validateCandidateVerificationAttempt(row, step, candidateRow.Candidate, assessment) != nil {
			return errCandidateVerificationHistory
		}
		verdict := ""
		if row.Latest.Verdict != nil {
			verdict = *row.Latest.Verdict
		}
		out.Entries[index].LatestVerification = &report.DiagnosticLatestVerification{
			State: row.Latest.State, Verdict: verdict, Reason: row.Latest.Reason,
			Method: row.Method, EvidenceDigest: row.OriginalDigest, RequestedAt: row.RequestedAt,
		}
	}
	return rows.Err()
}
