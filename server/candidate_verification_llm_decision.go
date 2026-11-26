package server

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisapi"
	"github.com/Autumn-27/artex/diagnosisprep"
)

func candidateVerificationLLMDecision(w http.ResponseWriter, r *http.Request, ctx context.Context, tx *sql.Tx,
	authenticate diagnosisapi.Authenticate, endpoint diagnosisprep.Endpoint, owner string, ownerReq diagnosisprep.Request,
	job diagnosisprep.Job, requestID int64, candidate db.DiagnosticCandidate, planJSON []byte, storedDigest string,
	step int, state, submittedDigest, decision string) {
	linkedE3, linkErr := candidateClaimedByE3(ctx, tx, candidate.TaskID, candidate.ID)
	if job.Scope == nil || ownershipIDORCandidate(candidate) || linkErr != nil || linkedE3 {
		candidateVerificationError(w, 403, "verification_scope_denied")
		return
	}
	var plan CandidateLLMPlan
	if strictJSON(planJSON, &plan) != nil || plan.TaskID != candidate.TaskID || plan.CandidateID != candidate.ID ||
		plan.EvidenceDigest != candidate.EvidenceDigest || plan.OriginalRunRef != candidate.RunRef {
		candidateVerificationError(w, 503, "verification_plan_invalid")
		return
	}
	digest, err := candidateLLMPlanDigest(plan)
	if err != nil || digest != storedDigest || digest != submittedDigest {
		candidateVerificationError(w, 409, "verification_plan_changed")
		return
	}
	analysis, err := readCandidateAnalysis(ctx, tx, requestID, candidate.ID)
	if err != nil || analysis == nil || !candidateLLMPlanMatchesAnalysis(plan, *analysis) {
		candidateVerificationError(w, 503, "analysis_integrity_failed")
		return
	}
	if _, err := readCandidateBusinessAnswers(ctx, tx, requestID, *analysis); err != nil {
		candidateVerificationError(w, 503, "business_answer_integrity_failed")
		return
	}
	if step == 5 && state == decision {
		start := "not_requested"
		if decision == "approved" {
			start = triggerCandidateLLMExecution(ctx, endpoint, owner, requestID, digest)
		}
		writeJSON(w, 200, map[string]any{"request_id": strconv.FormatInt(requestID, 10), "process_state": state,
			"plan_digest": digest, "execution_start": start})
		return
	}
	if step != 4 || state != "awaiting_approval" {
		candidateVerificationError(w, 409, "verification_already_decided")
		return
	}
	if !time.Now().UTC().Before(plan.ExpiresAt) {
		_, err := tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events
(request_id,step,process_state,reason,sources,sources_digest,rule_version)
VALUES($1,5,'expired','plan_expired','[]'::json,$2,'selected/llm-v1')`, requestID, emptyCandidateSourcesDigest)
		if err != nil || tx.Commit() != nil {
			candidateVerificationError(w, 503, "verification_storage_unavailable")
			return
		}
		candidateVerificationError(w, 409, "verification_plan_expired")
		return
	}
	targetReq := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-llm-decision-target", Command: diagnosisprep.ProductControl,
		Principal: owner, Product: &diagnosisprep.ProductRequest{Operation: "target", TargetID: plan.TargetID}}
	targetResp, err := endpoint.Call(ctx, targetReq)
	if err != nil || diagnosisprep.ValidateResponse(targetReq, targetResp) != nil || targetResp.Error != "" || targetResp.Product == nil ||
		targetResp.Product.Target == nil || targetResp.Product.Reason != "" {
		candidateVerificationError(w, 503, "verification_target_unavailable")
		return
	}
	if !candidateLLMPlanStillApproved(time.Now().UTC(), plan, job.Input, *job.Scope, *targetResp.Product.Target) {
		candidateVerificationError(w, 409, "verification_plan_expired")
		return
	}
	if current, e := authenticate(r); e != nil || current != owner {
		candidateVerificationError(w, 401, "authentication_required")
		return
	}
	recheck, e := endpoint.Call(ctx, ownerReq)
	if e != nil || diagnosisprep.ValidateResponse(ownerReq, recheck) != nil || recheck.Authorized == nil || !*recheck.Authorized ||
		recheck.Job == nil || recheck.Job.TaskID != ownerReq.TaskID {
		candidateVerificationError(w, 403, "verification_scope_denied")
		return
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_decisions(request_id,plan_digest,decision,decided_by)
VALUES($1,$2,$3,$4)`, requestID, digest, decision, owner); err != nil {
		candidateVerificationError(w, 503, "verification_storage_unavailable")
		return
	}
	reason, approvalRef := "user_rejected", ""
	if decision == "approved" {
		reason, approvalRef = "user_approved", fmt.Sprintf("approval-candidate-%d-%s", requestID, digest[:16])
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events
(request_id,step,process_state,reason,sources,sources_digest,rule_version,approval_ref)
VALUES($1,5,$2,$3,'[]'::json,$4,'selected/llm-v1',$5)`, requestID, decision, reason, emptyCandidateSourcesDigest, approvalRef); err != nil || tx.Commit() != nil {
		candidateVerificationError(w, 503, "verification_storage_unavailable")
		return
	}
	start := "not_requested"
	if decision == "approved" {
		start = triggerCandidateLLMExecution(ctx, endpoint, owner, requestID, digest)
	}
	writeJSON(w, 200, map[string]any{"request_id": strconv.FormatInt(requestID, 10), "process_state": decision, "plan_digest": digest,
		"approval_ref": approvalRef, "execution_start": start})
}

func triggerCandidateLLMExecution(ctx context.Context, endpoint diagnosisprep.Endpoint, owner string, requestID int64, digest string) string {
	req := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-llm-execution-schedule", Command: diagnosisprep.ProductControl,
		Principal: owner, Product: &diagnosisprep.ProductRequest{Operation: "candidate_llm_execute", JobID: strconv.FormatInt(requestID, 10), CommandKey: digest}}
	response, err := endpoint.Call(ctx, req)
	if err != nil || diagnosisprep.ValidateResponse(req, response) != nil || response.Error != "" || response.Product == nil || response.Product.Reason != "scheduled" {
		return "unavailable"
	}
	return "scheduled"
}
