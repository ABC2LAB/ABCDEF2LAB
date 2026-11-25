package server

import (
	"context"
	"database/sql"
	"encoding/json"
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
	rows, err := tx.QueryContext(ctx, `SELECT q.id,q.candidate_id,q.request_key,q.method,q.original_evidence_digest,q.original_evidence_version,q.original_run_ref,q.requested_at,e.step,e.process_state,e.verdict,e.reason,e.sources,e.sources_digest,e.rule_version,e.execution_ref,e.approval_ref,e.event_at,p.plan,p.plan_digest,d.decision,d.plan_digest,x.followup_task_id,x.run_ref
FROM (SELECT DISTINCT ON (candidate_id) * FROM diagnostic_candidate_verification_requests WHERE task_id=$1 ORDER BY candidate_id,id DESC) q
LEFT JOIN LATERAL (SELECT * FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
LEFT JOIN diagnostic_candidate_verification_plans p ON p.request_id=q.id
LEFT JOIN diagnostic_candidate_verification_decisions d ON d.request_id=q.id
LEFT JOIN diagnostic_candidate_verification_executions x ON x.request_id=q.id AND x.plan_digest=p.plan_digest
ORDER BY q.candidate_id LIMIT $2`, taskID, report.MaxFindings+1)
	if err != nil {
		return err
	}
	type pendingVerification struct {
		row     candidateVerificationAttempt
		step    int
		binding candidateVerificationBinding
	}
	pending := make([]pendingVerification, 0)
	count := 0
	for rows.Next() {
		count++
		if count > report.MaxFindings {
			rows.Close()
			return report.ErrExportBlocked
		}
		row, step, binding, err := scanCandidateVerificationAttempt(rows)
		if err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, pendingVerification{row, step, binding})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range pending {
		row, step, binding := item.row, item.step, item.binding
		var candidateID int64
		if _, err := fmt.Sscanf(row.CandidateID, "%d", &candidateID); err != nil {
			return errCandidateVerificationHistory
		}
		candidateRow, found := byID[candidateID]
		assessment, assessed := automatic[candidateID]
		index, included := entryIndex[candidateID]
		if !found || !included || !assessed || candidateRow.Unavailable ||
			!validStoredAutomaticCandidateAssessment(candidateRow.Candidate, assessment) ||
			validateCandidateVerificationAttempt(row, step, binding, candidateRow.Candidate, assessment) != nil ||
			validateCandidateActiveEvidence(ctx, tx, row, binding, candidateRow.Candidate) != nil {
			return errCandidateVerificationHistory
		}
		verdict := ""
		if row.Latest.Verdict != nil {
			verdict = *row.Latest.Verdict
		}
		latest := &report.DiagnosticLatestVerification{
			State: row.Latest.State, Verdict: verdict, Reason: row.Latest.Reason,
			Method: row.Method, EvidenceDigest: row.OriginalDigest, RequestedAt: row.RequestedAt,
		}
		entry := &out.Entries[index]
		var latestID int64
		if _, err := fmt.Sscanf(row.ID, "%d", &latestID); err != nil || latestID <= 0 {
			return errCandidateVerificationHistory
		}
		// A later stored review or failed run does not erase a prior sealed
		// confirmation. Read and validate the previous completed active event,
		// then display it separately from the most recent attempt.
		earlier, earlierStep, earlierBinding, earlierErr := scanCandidateVerificationAttempt(tx.QueryRowContext(ctx, `SELECT q.id,q.candidate_id,q.request_key,q.method,q.original_evidence_digest,q.original_evidence_version,q.original_run_ref,q.requested_at,e.step,e.process_state,e.verdict,e.reason,e.sources,e.sources_digest,e.rule_version,e.execution_ref,e.approval_ref,e.event_at,p.plan,p.plan_digest,d.decision,d.plan_digest,x.followup_task_id,x.run_ref
FROM diagnostic_candidate_verification_requests q
JOIN LATERAL (SELECT * FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON e.process_state='completed' AND e.verdict IN ('confirmed','negative')
JOIN diagnostic_candidate_verification_plans p ON p.request_id=q.id
JOIN diagnostic_candidate_verification_decisions d ON d.request_id=q.id
JOIN diagnostic_candidate_verification_executions x ON x.request_id=q.id AND x.plan_digest=p.plan_digest
WHERE q.task_id=$1 AND q.candidate_id=$2 AND q.id<$3 AND q.method='active/v1'
ORDER BY q.id DESC LIMIT 1`, taskID, candidateID, latestID))
		if earlierErr != nil && !errors.Is(earlierErr, sql.ErrNoRows) {
			return errCandidateVerificationHistory
		}
		if earlierErr == nil {
			if err := validateCandidateVerificationAttempt(earlier, earlierStep, earlierBinding, candidateRow.Candidate, assessment); err != nil {
				return errCandidateVerificationHistory
			}
			if err := validateCandidateActiveEvidence(ctx, tx, earlier, earlierBinding, candidateRow.Candidate); err != nil || earlier.Latest.Verdict == nil {
				return errCandidateVerificationHistory
			}
			latest.EarlierVerdict, latest.EarlierReason, latest.EarlierRequestedAt = *earlier.Latest.Verdict, earlier.Latest.Reason, earlier.RequestedAt
			var refs []candidateVerificationSourceRef
			if json.Unmarshal(earlier.Latest.Sources, &refs) != nil || len(refs) != 2 {
				return errCandidateVerificationHistory
			}
			for _, ref := range refs {
				latest.EarlierSources = append(latest.EarlierSources, report.DiagnosticVerificationSourceRef{CallRef: ref.CallRef, AttemptRef: ref.AttemptRef, ObservationDigest: ref.ObservationDigest})
			}
		}
		if row.Method == "active/v1" {
			latest.InitialVerdict, latest.InitialReason = assessment.Status, assessment.Reason
		}
		activeRow := row
		applyActive := row.Method == "active/v1" && row.Latest.State == "completed"
		if row.Method == "stored/v1" && earlierErr == nil {
			activeRow, applyActive = earlier, true
		}
		if applyActive {
			var refs []candidateVerificationSourceRef
			if err := json.Unmarshal(activeRow.Latest.Sources, &refs); err != nil {
				return errCandidateVerificationHistory
			}
			if row.Method == "active/v1" {
				for _, ref := range refs {
					latest.Sources = append(latest.Sources, report.DiagnosticVerificationSourceRef{
						CallRef: ref.CallRef, AttemptRef: ref.AttemptRef, ObservationDigest: ref.ObservationDigest,
					})
				}
			}
			entry.AssessmentStatus, entry.AssessmentReason = *activeRow.Latest.Verdict, activeRow.Latest.Reason
			switch *activeRow.Latest.Verdict {
			case "confirmed":
				entry.Outcome, entry.Verification = "confirmed", "active_observation_v1"
				entry.Severity, entry.SeveritySource, entry.SeverityRationale = "info", "", ""
				if candidateRow.Candidate.VerificationClaim == "html_nosniff_missing" {
					entry.Title = "HTML 문서 보안 헤더 누락"
					entry.Summary = "사용자가 승인한 추가 검증에서 동일한 정확한 HTML 경로를 독립적으로 두 번 조회해 X-Content-Type-Options: nosniff 설정 누락을 확인했습니다. 악용 가능성은 별도 판단 대상입니다."
					entry.Remediation = "해당 HTML 응답에 X-Content-Type-Options: nosniff를 설정하고 배포 범위를 확인하세요."
					entry.Report = "수정 후 해당 경로의 정상 HTML 응답에서 nosniff 헤더가 일관되게 반환되는지 확인하세요."
				} else {
					entry.Title = "Server 응답 헤더의 버전 노출"
					entry.Summary = "사용자가 승인한 추가 검증에서 동일한 정확한 경로의 독립 응답 두 건에서 Server 헤더의 소프트웨어 버전 표기를 확인했습니다. 해당 버전의 취약성은 주장하지 않습니다."
					entry.Remediation = "Server 응답 헤더에서 상세 버전 표기를 제거할지 운영 정책에 따라 검토하고 설정을 적용하세요."
					entry.Report = "수정 후 해당 경로의 정상 응답에서 Server 헤더에 상세 버전이 남지 않는지 확인하세요."
				}
			case "negative":
				entry.Outcome = "negative"
			case "insufficient":
				entry.Outcome = "review"
			default:
				return errCandidateVerificationHistory
			}
		}
		entry.LatestVerification = latest
	}
	return nil
}
