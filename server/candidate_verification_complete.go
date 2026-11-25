package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
)

// CompleteCandidateActiveVerification commits bounded projections and one
// append-only verdict together, after the E² host has drained and its sealed
// audit checkpoint has been stored. A failed or unknown execution cannot call
// this path to manufacture a negative verdict.
func CompleteCandidateActiveVerification(ctx context.Context, pool *sql.DB, requestID, followupTaskID int64, planDigest, runRef string, observations []candidateverify.StoredObservationV2) (candidateverify.Result, error) {
	if ctx == nil || ctx.Err() != nil || pool == nil || requestID <= 0 || followupTaskID <= 0 || !candidateVerificationDigest.MatchString(planDigest) || runRef == "" || len(observations) != 2 {
		return candidateverify.Result{}, ErrCandidateExecutionBinding
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return candidateverify.Result{}, ErrCandidateExecutionBinding
	}
	defer tx.Rollback()
	var taskID, candidateID, explorationID int64
	var evidenceDigest, originalRun, method, decision, state, storedDigest string
	var planJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT q.task_id,q.candidate_id,q.original_evidence_digest,q.original_run_ref,q.method,
 p.plan,p.plan_digest,d.decision,e.process_state,x.followup_task_id,t.exploration_id
 FROM diagnostic_candidate_verification_requests q
 JOIN diagnostic_candidate_verification_plans p ON p.request_id=q.id
 JOIN diagnostic_candidate_verification_decisions d ON d.request_id=q.id AND d.plan_digest=p.plan_digest
 JOIN LATERAL (SELECT process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
 JOIN diagnostic_candidate_verification_executions x ON x.request_id=q.id AND x.plan_digest=p.plan_digest AND x.run_ref=$2
 JOIN tasks t ON t.id=q.task_id AND t.deleted_at IS NULL
 WHERE q.id=$1 AND x.followup_task_id=$3 FOR UPDATE OF q`, requestID, runRef, followupTaskID).
		Scan(&taskID, &candidateID, &evidenceDigest, &originalRun, &method, &planJSON, &storedDigest, &decision, &state, &followupTaskID, &explorationID)
	if err != nil || method != "active/v1" || decision != "approved" || state != "running" || storedDigest != planDigest {
		return candidateverify.Result{}, ErrCandidateExecutionBinding
	}
	var followupStatus string
	var completedAt sql.NullTime
	if tx.QueryRowContext(ctx, `SELECT status,completed_at FROM tasks WHERE id=$1 AND deleted_at IS NULL AND paused`, followupTaskID).Scan(&followupStatus, &completedAt) != nil || followupStatus != "done" || !completedAt.Valid {
		return candidateverify.Result{}, ErrCandidateExecutionBinding
	}
	seal, ok, err := db.ReadSealCheck(ctx, tx, followupTaskID, runRef)
	if err != nil || !ok || len(seal.Checks) < 2 {
		return candidateverify.Result{}, ErrCandidateExecutionBinding
	}
	for _, check := range seal.Checks {
		if !check.Included {
			return candidateverify.Result{}, ErrCandidateExecutionBinding
		}
	}
	var plan CandidateActivePlan
	if strictJSON(planJSON, &plan) != nil || plan.TaskID != taskID || plan.CandidateID != candidateID || plan.EvidenceDigest != evidenceDigest || plan.OriginalRunRef != originalRun {
		return candidateverify.Result{}, ErrCandidateExecutionBinding
	}
	digest, err := candidatePlanDigest(plan)
	if err != nil || digest != planDigest {
		return candidateverify.Result{}, ErrCandidateExecutionBinding
	}
	candidate, found, err := db.ReadDiagnosticCandidateWithFence(ctx, tx, explorationID, taskID, candidateID)
	if err != nil || !found || !candidatePlanMatchesEvidence(plan, candidate) {
		return candidateverify.Result{}, ErrCandidateExecutionBinding
	}
	result := candidateverify.AssessActiveStoredV1(candidate, candidateverify.ActiveBindingV1{TaskID: followupTaskID, RunRef: runRef, ExactPath: plan.ExactPath, CaseRef: planDigest}, observations)
	if result.Status == "" || !candidateverify.ValidSourcesV2(result) {
		return candidateverify.Result{}, ErrCandidateExecutionBinding
	}
	if err := storeCandidateObservations(ctx, tx, followupTaskID, runRef, observations); err != nil {
		return candidateverify.Result{}, ErrCandidateExecutionBinding
	}
	refs := candidateVerificationSourceRefs(result.Sources)
	raw, err := json.Marshal(refs)
	if err != nil || len(raw) > 16<<10 {
		return candidateverify.Result{}, ErrCandidateExecutionBinding
	}
	sum := sha256.Sum256(raw)
	_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,verdict,reason,sources,sources_digest,rule_version,execution_ref)
VALUES($1,4,'completed',$2,$3,$4,$5,'candidate-active/v1',$6)`, requestID, result.Status, result.Reason, raw, hex.EncodeToString(sum[:]), runRef)
	if err != nil || tx.Commit() != nil {
		return candidateverify.Result{}, errors.Join(ErrCandidateExecutionBinding, err)
	}
	return result, nil
}
