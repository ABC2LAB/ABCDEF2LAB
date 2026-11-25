package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisapi"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/report"
)

// A persisted plan is a review artifact. It cannot issue a fresh lease or send
// a target request; the approval and protected execution paths are separate.
func candidateVerificationLLMPlanHTTP(w http.ResponseWriter, r *http.Request, pool *sql.DB, authenticate diagnosisapi.Authenticate, endpoint diagnosisprep.Endpoint) {
	route, requestID, ok := parseCandidateDecisionRouteAt(r, "plan")
	if !ok {
		candidateVerificationError(w, 400, "invalid_verification_request")
		return
	}
	owner, err := authenticate(r)
	if err != nil || owner == "" {
		candidateVerificationError(w, 401, "authentication_required")
		return
	}
	if pool == nil || endpoint == nil {
		candidateVerificationError(w, 503, "verification_storage_unavailable")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		candidateVerificationError(w, 400, "invalid_verification_request")
		return
	}
	var input struct {
		AnalysisDigest string `json:"analysis_digest"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if err != nil || strictJSON(raw, &input) != nil || !candidateVerificationDigest.MatchString(input.AnalysisDigest) {
		candidateVerificationError(w, 400, "invalid_verification_request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	ownerReq := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-llm-plan-owner", Command: diagnosisprep.AuthorizeTask,
		Principal: owner, TaskID: strconv.FormatInt(route.task, 10)}
	ownerResp, err := endpoint.Call(ctx, ownerReq)
	if err != nil || diagnosisprep.ValidateResponse(ownerReq, ownerResp) != nil || ownerResp.Authorized == nil || !*ownerResp.Authorized ||
		ownerResp.Job == nil || ownerResp.Job.Scope == nil || ownerResp.Job.TaskID != ownerReq.TaskID {
		candidateVerificationError(w, 403, "verification_scope_denied")
		return
	}
	// The analysis worker owns the no-target stored-evidence path. A user
	// opening the plan endpoint immediately after analysis must not race it
	// into a new target execution when sealed comparisons already suffice.
	if _, err := ReadCandidateStoredAssessmentContext(ctx, pool, requestID); err == nil {
		candidateVerificationError(w, 409, "stored_evidence_assessment_pending")
		return
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		candidateVerificationError(w, 503, "verification_storage_unavailable")
		return
	}
	defer tx.Rollback()
	var exploration int64
	if tx.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, route.task).Scan(&exploration) != nil {
		candidateVerificationError(w, 404, "task_not_found")
		return
	}
	candidate, found, err := db.ReadDiagnosticCandidateWithFence(ctx, tx, exploration, route.task, route.candidate)
	if err != nil {
		candidateVerificationError(w, 503, "candidate_integrity_failed")
		return
	}
	if !found {
		candidateVerificationError(w, 404, "candidate_not_found")
		return
	}
	run, err := readDiagnosticRun(ctx, tx, route.task)
	linkedE3, linkErr := candidateClaimedByE3(ctx, tx, route.task, candidate.ID)
	if err != nil || run == nil || run.State != report.DiagnosticRunCompleted || run.VerificationVersion != diagnosisVerificationSelected ||
		run.RunRef != candidate.RunRef || ownershipIDORCandidate(candidate) || linkErr != nil || linkedE3 {
		candidateVerificationError(w, 409, "diagnosis_not_completed")
		return
	}
	var requester, originalDigest, state string
	var step int
	err = tx.QueryRowContext(ctx, `SELECT q.requester,q.original_evidence_digest,e.step,e.process_state FROM diagnostic_candidate_verification_requests q
JOIN LATERAL (SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.id=$1 AND q.task_id=$2 AND q.candidate_id=$3 AND q.method='llm/v1' FOR UPDATE OF q`, requestID, route.task, route.candidate).
		Scan(&requester, &originalDigest, &step, &state)
	if errors.Is(err, sql.ErrNoRows) {
		candidateVerificationError(w, 404, "verification_not_found")
		return
	}
	if err != nil || requester != owner || originalDigest != candidate.EvidenceDigest {
		candidateVerificationError(w, 403, "verification_scope_denied")
		return
	}
	if !(step == 3 && state == "analysis_ready" || step == 4 && state == "awaiting_approval") {
		candidateVerificationError(w, 409, "analysis_not_ready")
		return
	}
	analysis, err := readCandidateAnalysis(ctx, tx, requestID, candidate.ID)
	if err != nil || analysis == nil {
		candidateVerificationError(w, 503, "analysis_integrity_failed")
		return
	}
	if analysis.Digest != input.AnalysisDigest {
		candidateVerificationError(w, 409, "analysis_changed")
		return
	}
	if _, err := readCandidateBusinessAnswers(ctx, tx, requestID, *analysis); err != nil {
		candidateVerificationError(w, 503, "business_answer_integrity_failed")
		return
	}
	targetReq := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-llm-plan-target", Command: diagnosisprep.ProductControl,
		Principal: owner, Product: &diagnosisprep.ProductRequest{Operation: "target", TargetID: ownerResp.Job.Scope.TargetRef}}
	targetResp, err := endpoint.Call(ctx, targetReq)
	if err != nil || diagnosisprep.ValidateResponse(targetReq, targetResp) != nil || targetResp.Error != "" || targetResp.Product == nil || targetResp.Product.Target == nil || targetResp.Product.Reason != "" {
		candidateVerificationError(w, 503, "verification_target_unavailable")
		return
	}
	plan, digest, err := makeCandidateLLMPlan(time.Now().UTC(), candidate, *analysis, ownerResp.Job.Input, *ownerResp.Job.Scope, *targetResp.Product.Target)
	if err != nil {
		candidateVerificationError(w, 409, "candidate_action_plan_unavailable")
		return
	}
	if step == 4 {
		var stored []byte
		var storedDigest string
		if tx.QueryRowContext(ctx, `SELECT plan,plan_digest FROM diagnostic_candidate_verification_plans WHERE request_id=$1`, requestID).Scan(&stored, &storedDigest) != nil {
			candidateVerificationError(w, 503, "verification_plan_invalid")
			return
		}
		var prior CandidateLLMPlan
		if strictJSON(stored, &prior) != nil || prior.AnalysisDigest != analysis.Digest ||
			!candidateLLMPlanStillApproved(time.Now().UTC(), prior, ownerResp.Job.Input, *ownerResp.Job.Scope, *targetResp.Product.Target) {
			// The stored plan is immutable. A changed target or policy needs a
			// newly selected verification, never an in-place plan rewrite.
			candidateVerificationError(w, 409, "verification_plan_changed")
			return
		}
		priorDigest, err := candidateLLMPlanDigest(prior)
		if err != nil || priorDigest != storedDigest {
			candidateVerificationError(w, 503, "verification_plan_invalid")
			return
		}
		plan, digest = prior, storedDigest
	} else {
		encoded, err := json.Marshal(plan)
		if err != nil || len(encoded) > 4096 {
			candidateVerificationError(w, 409, "candidate_action_plan_unavailable")
			return
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_plans(request_id,plan,plan_digest,target_id,target_revision,expires_at)
VALUES($1,$2::jsonb,$3,$4,$5,$6)`, requestID, encoded, digest, plan.TargetID, plan.TargetRevision, plan.ExpiresAt)
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources,sources_digest,rule_version)
VALUES($1,4,'awaiting_approval','action_approval_required','[]'::json,$2,'selected/llm-v1')`, requestID, emptyCandidateSourcesDigest)
		}
		if err != nil {
			candidateVerificationError(w, 503, "verification_storage_unavailable")
			return
		}
	}
	current, err := authenticate(r)
	if err != nil || current != owner {
		candidateVerificationError(w, 401, "authentication_required")
		return
	}
	recheck, err := endpoint.Call(ctx, ownerReq)
	if err != nil || diagnosisprep.ValidateResponse(ownerReq, recheck) != nil || recheck.Authorized == nil || !*recheck.Authorized ||
		recheck.Job == nil || recheck.Job.TaskID != ownerReq.TaskID {
		candidateVerificationError(w, 403, "verification_scope_denied")
		return
	}
	if tx.Commit() != nil {
		candidateVerificationError(w, 503, "verification_storage_unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"request_id": strconv.FormatInt(requestID, 10), "process_state": "awaiting_approval", "plan": plan, "plan_digest": digest})
}
