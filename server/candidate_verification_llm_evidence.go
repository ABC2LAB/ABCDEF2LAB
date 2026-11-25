package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
)

// CompleteCandidateLLMEvidence persists only sealed, audited E² observations.
// It cannot make a vulnerability verdict. The later model receives exactly
// these durable projections and the preserved original observations.
func CompleteCandidateLLMEvidence(ctx context.Context, pool *sql.DB, requestID, followupTaskID int64,
	planDigest, runRef string, observations []candidateverify.StoredObservationV2) error {
	if ctx == nil || ctx.Err() != nil || pool == nil || requestID <= 0 || followupTaskID <= 0 || !candidateVerificationDigest.MatchString(planDigest) ||
		runRef == "" || len(observations) < 1 || len(observations) > 3 {
		return ErrCandidateExecutionBinding
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ErrCandidateExecutionBinding
	}
	defer tx.Rollback()
	var originalTask, candidateID, explorationID int64
	var originalDigest, originalRun, method, decision, state, storedDigest, scopeDigest string
	var planJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT q.task_id,q.candidate_id,q.original_evidence_digest,q.original_run_ref,q.method,
p.plan,p.plan_digest,d.decision,e.process_state,t.exploration_id,x.scope_digest
FROM diagnostic_candidate_verification_requests q
JOIN diagnostic_candidate_verification_plans p ON p.request_id=q.id
JOIN diagnostic_candidate_verification_decisions d ON d.request_id=q.id AND d.plan_digest=p.plan_digest
JOIN LATERAL (SELECT process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
JOIN diagnostic_candidate_verification_executions x ON x.request_id=q.id AND x.plan_digest=p.plan_digest AND x.run_ref=$2
JOIN tasks t ON t.id=q.task_id AND t.deleted_at IS NULL
WHERE q.id=$1 AND x.followup_task_id=$3 FOR UPDATE OF q`, requestID, runRef, followupTaskID).
		Scan(&originalTask, &candidateID, &originalDigest, &originalRun, &method, &planJSON, &storedDigest, &decision, &state, &explorationID, &scopeDigest)
	if err != nil || method != "llm/v1" || decision != "approved" || state != "running" || storedDigest != planDigest ||
		!candidateVerificationDigest.MatchString(scopeDigest) {
		return ErrCandidateExecutionBinding
	}
	var followupStatus string
	var completedAt sql.NullTime
	if tx.QueryRowContext(ctx, `SELECT status,completed_at FROM tasks WHERE id=$1 AND deleted_at IS NULL AND paused`, followupTaskID).
		Scan(&followupStatus, &completedAt) != nil || followupStatus != "done" || !completedAt.Valid {
		return ErrCandidateExecutionBinding
	}
	var plan CandidateLLMPlan
	if strictJSON(planJSON, &plan) != nil || plan.TaskID != originalTask || plan.CandidateID != candidateID || plan.EvidenceDigest != originalDigest ||
		plan.OriginalRunRef != originalRun || len(plan.Actions) != len(observations) {
		return ErrCandidateExecutionBinding
	}
	if digest, err := candidateLLMPlanDigest(plan); err != nil || digest != planDigest {
		return ErrCandidateExecutionBinding
	}
	candidate, found, err := db.ReadDiagnosticCandidateWithFence(ctx, tx, explorationID, originalTask, candidateID)
	linkedE3, linkErr := candidateClaimedByE3(ctx, tx, originalTask, candidateID)
	if err != nil || !found || candidate.EvidenceDigest != originalDigest || candidate.RunRef != originalRun || ownershipIDORCandidate(candidate) || linkErr != nil || linkedE3 {
		return ErrCandidateExecutionBinding
	}
	analysis, err := readCandidateAnalysis(ctx, tx, requestID, candidateID)
	if err != nil || analysis == nil || !candidateLLMPlanMatchesAnalysis(plan, *analysis) {
		return ErrCandidateExecutionBinding
	}
	seal, ok, err := db.ReadSealCheck(ctx, tx, followupTaskID, runRef)
	if err != nil || !ok {
		return ErrCandidateExecutionBinding
	}
	want := map[string]int{}
	for _, action := range plan.Actions {
		want[fmt.Sprintf("%s %s", action.Method, action.ExactPath)]++
	}
	var sources []candidateverify.SourceV2
	for _, row := range observations {
		obs := row.Source.Observation
		if !candidateverify.ValidSelectedStoredObservation(followupTaskID, runRef, row) || row.Source.ScopeDigest != scopeDigest ||
			!selectedObservationSealed(row, seal) ||
			row.HeaderMode != e2proxy.HeaderPreserve ||
			!obs.ExactFunction || obs.Decision != "allowed" || !obs.ResponseComplete || !obs.AuditComplete ||
			obs.RuleRef == "" || want[fmt.Sprintf("%s %s", obs.Method, obs.Endpoint)] == 0 {
			return ErrCandidateExecutionBinding
		}
		want[fmt.Sprintf("%s %s", obs.Method, obs.Endpoint)]--
		sources = append(sources, row.Source)
	}
	for _, count := range want {
		if count != 0 {
			return ErrCandidateExecutionBinding
		}
	}
	if err := storeCandidateObservations(ctx, tx, followupTaskID, runRef, observations); err != nil {
		return ErrCandidateExecutionBinding
	}
	refs := candidateVerificationSourceRefs(sources)
	raw, err := json.Marshal(refs)
	if err != nil || len(raw) > 16<<10 {
		return ErrCandidateExecutionBinding
	}
	sum := sha256.Sum256(raw)
	if _, err := tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events
(request_id,step,process_state,reason,sources,sources_digest,rule_version,execution_ref)
VALUES($1,7,'evidence_ready','sealed_observations_available',$2,$3,'selected/llm-v1',$4)`, requestID, raw, hex.EncodeToString(sum[:]), runRef); err != nil {
		return ErrCandidateExecutionBinding
	}
	return tx.Commit()
}

// selectedObservationSealed requires the exact ordinary result, Host-issued
// checkpoint and later sealed-prefix check to refer to the same source.
func selectedObservationSealed(row candidateverify.StoredObservationV2, seal db.SealCheckRecord) bool {
	if row.SelectedProof == nil {
		return false
	}
	s, proof := row.Source, row.SelectedProof
	e, cp, o := proof.Event, proof.Checkpoint, s.Observation
	scope, err := json.Marshal(e.Scope)
	if err != nil || db.DiagnosticEvidenceDigest(string(scope)) != s.ScopeDigest ||
		e.Scope.TaskRef != s.TaskRef || e.Scope.RunRef != s.RunRef ||
		e.PolicyRevision != s.PolicyRevision || e.IntentID != s.IntentID ||
		e.Stage != "result" || e.Index == 0 || e.Index > cp.Sequence || e.Version != e2proxy.JournalVersionV3 ||
		e.Program != "" || e.E3Audit != nil || e.CallRef != s.CallRef ||
		e.AttemptID != o.AttemptRef || e.Sequence != o.Sequence || e.Method != o.Method ||
		e.RuleRef != o.RuleRef || e.Decision != o.Decision || e.Reason != o.Reason ||
		e.StatusCode != o.StatusCode || e.ForwardAttempted != o.ForwardAttempted ||
		!e.RequestWritten || !e.ResponseReceived || e.ResponseComplete != o.ResponseComplete ||
		e.AuditComplete != o.AuditComplete || e.ResponseTruncated || e.NotSent || !e.At.Equal(o.ObservedAt) ||
		cp.Version != e2proxy.JournalVersionV3 || cp.Scope != e.Scope ||
		cp.JournalID != seal.Head.JournalID || cp.Sequence == 0 || cp.Sequence > seal.Head.Sequence {
		return false
	}
	for _, check := range seal.Checks {
		if check.Sequence == cp.Sequence && check.MAC == cp.MAC && check.Included {
			return true
		}
	}
	return false
}
