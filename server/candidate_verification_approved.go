package server

import (
	"context"
	"database/sql"
	"time"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
)

// ReadApprovedCandidatePlan is for the protected runtime only. The caller
// holds a lock on the request row and must still check the original Job,
// current target, fresh task, and installation before issuing E² authority.
func ReadApprovedCandidatePlan(ctx context.Context, tx *sql.Tx, requestID int64, requestedDigest string) (CandidateActivePlan, error) {
	if ctx == nil || ctx.Err() != nil || tx == nil || requestID <= 0 || !candidateVerificationDigest.MatchString(requestedDigest) {
		return CandidateActivePlan{}, ErrCandidateExecutionBinding
	}
	var taskID, candidateID, evidenceVersion, explorationID int64
	var evidenceDigest, runRef, method, requester, storedDigest, decision, decisionState string
	var planJSON []byte
	var expiresAt time.Time
	err := tx.QueryRowContext(ctx, `SELECT q.task_id,q.candidate_id,q.original_evidence_version,q.original_evidence_digest,q.original_run_ref,q.method,q.requester,
 p.plan,p.plan_digest,p.expires_at,d.decision,e.process_state,t.exploration_id
 FROM diagnostic_candidate_verification_requests q
 JOIN diagnostic_candidate_verification_plans p ON p.request_id=q.id
 JOIN diagnostic_candidate_verification_decisions d ON d.request_id=q.id AND d.plan_digest=p.plan_digest
 JOIN diagnostic_candidate_verification_events e ON e.request_id=q.id AND e.step=2
 JOIN tasks t ON t.id=q.task_id AND t.deleted_at IS NULL
 LEFT JOIN diagnostic_candidate_verification_executions x ON x.request_id=q.id
 WHERE q.id=$1 AND x.request_id IS NULL`, requestID).Scan(&taskID, &candidateID, &evidenceVersion, &evidenceDigest, &runRef, &method, &requester,
		&planJSON, &storedDigest, &expiresAt, &decision, &decisionState, &explorationID)
	if err != nil || method != "active/v1" || requester != "ARTEX" || storedDigest != requestedDigest || decision != "approved" || decisionState != "approved" || !time.Now().Before(expiresAt) {
		return CandidateActivePlan{}, ErrCandidateExecutionBinding
	}
	var plan CandidateActivePlan
	if strictJSON(planJSON, &plan) != nil || plan.TaskID != taskID || plan.CandidateID != candidateID || plan.EvidenceDigest != evidenceDigest || plan.OriginalRunRef != runRef || plan.ExpiresAt.Sub(expiresAt).Abs() > time.Microsecond {
		return CandidateActivePlan{}, ErrCandidateExecutionBinding
	}
	digest, err := candidatePlanDigest(plan)
	if err != nil || digest != requestedDigest {
		return CandidateActivePlan{}, ErrCandidateExecutionBinding
	}
	candidate, found, err := db.ReadDiagnosticCandidateWithFence(ctx, tx, explorationID, taskID, candidateID)
	if err != nil || !found || candidate.EvidenceVersion != evidenceVersion || !candidatePlanMatchesEvidence(plan, candidate) {
		return CandidateActivePlan{}, ErrCandidateExecutionBinding
	}
	run, err := readDiagnosticRun(ctx, tx, taskID)
	if err != nil || run == nil || run.State != report.DiagnosticRunCompleted || run.RunRef != runRef || run.VerificationVersion != diagnosisVerificationV2 {
		return CandidateActivePlan{}, ErrCandidateExecutionBinding
	}
	insufficient := false
	for _, a := range run.CandidateAssessments {
		if a.CandidateID == candidateID && a.Status == candidateverify.Insufficient && validStoredAutomaticCandidateAssessment(candidate, a) {
			insufficient = true
		}
	}
	if !insufficient {
		return CandidateActivePlan{}, ErrCandidateExecutionBinding
	}
	return plan, nil
}
