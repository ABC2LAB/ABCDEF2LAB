package server

import (
	"context"
	"database/sql"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
)

// ReadApprovedCandidateLLMPlan is a protected-runtime precondition, not an
// execution grant. The caller still verifies today's installation and target.
func ReadApprovedCandidateLLMPlan(ctx context.Context, tx *sql.Tx, requestID int64, requestedDigest string) (CandidateLLMPlan, error) {
	if ctx == nil || ctx.Err() != nil || tx == nil || requestID <= 0 || !candidateVerificationDigest.MatchString(requestedDigest) {
		return CandidateLLMPlan{}, ErrCandidateExecutionBinding
	}
	var taskID, candidateID, evidenceVersion, explorationID int64
	var evidenceDigest, runRef, method, requester, storedDigest, decision, decisionState, latestState string
	var planJSON []byte
	var expiresAt time.Time
	err := tx.QueryRowContext(ctx, `SELECT q.task_id,q.candidate_id,q.original_evidence_version,q.original_evidence_digest,q.original_run_ref,q.method,q.requester,
p.plan,p.plan_digest,p.expires_at,d.decision,e.process_state,latest.process_state,t.exploration_id
FROM diagnostic_candidate_verification_requests q
JOIN diagnostic_candidate_verification_plans p ON p.request_id=q.id
JOIN diagnostic_candidate_verification_decisions d ON d.request_id=q.id AND d.plan_digest=p.plan_digest
JOIN diagnostic_candidate_verification_events e ON e.request_id=q.id AND e.step=5
JOIN LATERAL (SELECT process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) latest ON true
JOIN tasks t ON t.id=q.task_id AND t.deleted_at IS NULL
LEFT JOIN diagnostic_candidate_verification_executions x ON x.request_id=q.id
WHERE q.id=$1 AND x.request_id IS NULL`, requestID).Scan(&taskID, &candidateID, &evidenceVersion, &evidenceDigest, &runRef, &method, &requester,
		&planJSON, &storedDigest, &expiresAt, &decision, &decisionState, &latestState, &explorationID)
	if err != nil || method != "llm/v1" || requester != "ARTEX" || storedDigest != requestedDigest || decision != "approved" ||
		decisionState != "approved" || latestState != "approved" || !time.Now().Before(expiresAt) {
		return CandidateLLMPlan{}, ErrCandidateExecutionBinding
	}
	var plan CandidateLLMPlan
	if strictJSON(planJSON, &plan) != nil || plan.TaskID != taskID || plan.CandidateID != candidateID || plan.EvidenceDigest != evidenceDigest ||
		plan.OriginalRunRef != runRef || plan.ExpiresAt.Sub(expiresAt).Abs() > time.Microsecond {
		return CandidateLLMPlan{}, ErrCandidateExecutionBinding
	}
	digest, err := candidateLLMPlanDigest(plan)
	if err != nil || digest != requestedDigest {
		return CandidateLLMPlan{}, ErrCandidateExecutionBinding
	}
	candidate, found, err := db.ReadDiagnosticCandidateWithFence(ctx, tx, explorationID, taskID, candidateID)
	linkedE3, linkErr := candidateClaimedByE3(ctx, tx, taskID, candidateID)
	if err != nil || !found || candidate.EvidenceVersion != evidenceVersion || candidate.Validate() != nil || ownershipIDORCandidate(candidate) || linkErr != nil || linkedE3 {
		return CandidateLLMPlan{}, ErrCandidateExecutionBinding
	}
	run, err := readDiagnosticRun(ctx, tx, taskID)
	if err != nil || run == nil || run.State != report.DiagnosticRunCompleted || run.RunRef != runRef ||
		run.VerificationVersion != diagnosisVerificationSelected {
		return CandidateLLMPlan{}, ErrCandidateExecutionBinding
	}
	analysis, err := readCandidateAnalysis(ctx, tx, requestID, candidateID)
	if err != nil || analysis == nil || !candidateLLMPlanMatchesAnalysis(plan, *analysis) {
		return CandidateLLMPlan{}, ErrCandidateExecutionBinding
	}
	return plan, nil
}
