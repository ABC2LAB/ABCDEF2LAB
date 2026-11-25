package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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

func parseCandidateDecisionRoute(r *http.Request) (candidateVerificationRoute, int64, bool) {
	return parseCandidateDecisionRouteAt(r, "decision")
}

// This endpoint records a decision on an immutable plan. It never creates a
// task, obtains an E² lease, or sends a target request. The protected runtime
// must recheck the decision and the current target before executing.
func candidateVerificationDecisionHTTP(w http.ResponseWriter, r *http.Request, pool *sql.DB, authenticate diagnosisapi.Authenticate, endpoint diagnosisprep.Endpoint) {
	route, requestID, ok := parseCandidateDecisionRoute(r)
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
		PlanDigest string `json:"plan_digest"`
		Decision   string `json:"decision"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if err != nil || strictJSON(raw, &input) != nil || !candidateVerificationDigest.MatchString(input.PlanDigest) || input.Decision != "approved" && input.Decision != "rejected" {
		candidateVerificationError(w, 400, "invalid_verification_request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	ownerReq := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-decision-owner", Command: diagnosisprep.AuthorizeTask, Principal: owner, TaskID: strconv.FormatInt(route.task, 10)}
	ownerResp, err := endpoint.Call(ctx, ownerReq)
	if err != nil || diagnosisprep.ValidateResponse(ownerReq, ownerResp) != nil {
		candidateVerificationError(w, 503, "verification_owner_unavailable")
		return
	}
	if ownerResp.Authorized == nil || !*ownerResp.Authorized || ownerResp.Job == nil || ownerResp.Job.Scope == nil || ownerResp.Job.TaskID != ownerReq.TaskID {
		candidateVerificationError(w, 403, "verification_scope_denied")
		return
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		candidateVerificationError(w, 503, "verification_storage_unavailable")
		return
	}
	defer tx.Rollback()
	var exploration int64
	err = tx.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, route.task).Scan(&exploration)
	if errors.Is(err, sql.ErrNoRows) {
		candidateVerificationError(w, 404, "task_not_found")
		return
	}
	if err != nil || exploration <= 0 {
		candidateVerificationError(w, 503, "verification_storage_unavailable")
		return
	}
	candidate, found, err := db.ReadDiagnosticCandidateWithFence(ctx, tx, exploration, route.task, route.candidate)
	if err != nil || !found {
		candidateVerificationError(w, 404, "candidate_not_found")
		return
	}
	run, err := readDiagnosticRun(ctx, tx, route.task)
	if err != nil || run == nil || run.State != report.DiagnosticRunCompleted || run.VerificationVersion != diagnosisVerificationV2 || run.RunRef != candidate.RunRef {
		candidateVerificationError(w, 409, "diagnosis_not_completed")
		return
	}
	validAutomatic := false
	for _, assessment := range run.CandidateAssessments {
		if assessment.CandidateID == candidate.ID && validStoredAutomaticCandidateAssessment(candidate, assessment) {
			validAutomatic = true
		}
	}
	if !validAutomatic {
		candidateVerificationError(w, 503, "verification_evidence_invalid")
		return
	}
	var method, requester, originalDigest, originalRunRef, storedPlanDigest, state string
	var planJSON []byte
	var originalVersion int64
	var step int
	err = tx.QueryRowContext(ctx, `SELECT q.method,q.requester,q.original_evidence_digest,q.original_evidence_version,q.original_run_ref,p.plan,p.plan_digest,e.step,e.process_state
FROM diagnostic_candidate_verification_requests q JOIN diagnostic_candidate_verification_plans p ON p.request_id=q.id
JOIN LATERAL (SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.id=$1 AND q.task_id=$2 AND q.candidate_id=$3 FOR UPDATE OF q`, requestID, route.task, route.candidate).
		Scan(&method, &requester, &originalDigest, &originalVersion, &originalRunRef, &planJSON, &storedPlanDigest, &step, &state)
	if errors.Is(err, sql.ErrNoRows) {
		candidateVerificationError(w, 404, "verification_not_found")
		return
	}
	if err != nil || method != "active/v1" || requester != owner || originalDigest != candidate.EvidenceDigest || originalVersion != candidate.EvidenceVersion || originalRunRef != candidate.RunRef {
		candidateVerificationError(w, 503, "verification_evidence_invalid")
		return
	}
	var plan candidateActivePlan
	if strictJSON(planJSON, &plan) != nil || !candidatePlanMatchesEvidence(plan, candidate) {
		candidateVerificationError(w, 503, "verification_evidence_invalid")
		return
	}
	digest, err := candidatePlanDigest(plan)
	if err != nil || digest != storedPlanDigest || digest != input.PlanDigest {
		candidateVerificationError(w, 409, "verification_plan_changed")
		return
	}
	if step == 2 && state == input.Decision {
		executionState := "not_requested"
		if state == "approved" {
			executionState = triggerCandidateExecution(ctx, endpoint, owner, requestID, digest)
		}
		writeJSON(w, 200, map[string]any{"request_id": strconv.FormatInt(requestID, 10), "process_state": state, "plan_digest": digest, "execution_start": executionState})
		return
	}
	if step != 1 || state != "awaiting_approval" {
		candidateVerificationError(w, 409, "verification_already_decided")
		return
	}
	if !time.Now().UTC().Before(plan.ExpiresAt) {
		emptySources := []byte("[]")
		sum := sha256.Sum256(emptySources)
		_, err := tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources,sources_digest,rule_version) VALUES($1,2,'expired','plan_expired',$2,$3,'candidate-active/v1')`, requestID, emptySources, hex.EncodeToString(sum[:]))
		if err != nil || tx.Commit() != nil {
			candidateVerificationError(w, 503, "verification_storage_unavailable")
			return
		}
		candidateVerificationError(w, 409, "verification_plan_expired")
		return
	}
	targetReq := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-decision-target", Command: diagnosisprep.ProductControl, Principal: owner,
		Product: &diagnosisprep.ProductRequest{Operation: "target", TargetID: plan.TargetID}}
	targetResp, err := endpoint.Call(ctx, targetReq)
	if err != nil || diagnosisprep.ValidateResponse(targetReq, targetResp) != nil || targetResp.Error != "" || targetResp.Product == nil || targetResp.Product.Target == nil || targetResp.Product.Reason != "" {
		candidateVerificationError(w, 503, "verification_target_unavailable")
		return
	}
	if !candidatePlanStillApproved(time.Now().UTC(), plan, ownerResp.Job.Input, *ownerResp.Job.Scope, *targetResp.Product.Target) {
		candidateVerificationError(w, 409, "verification_plan_expired")
		return
	}
	if current, e := authenticate(r); e != nil || current != owner {
		candidateVerificationError(w, 401, "authentication_required")
		return
	}
	if recheck, e := endpoint.Call(ctx, ownerReq); e != nil || diagnosisprep.ValidateResponse(ownerReq, recheck) != nil || recheck.Authorized == nil || !*recheck.Authorized || recheck.Job == nil || recheck.Job.TaskID != ownerReq.TaskID {
		candidateVerificationError(w, 403, "verification_scope_denied")
		return
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_decisions(request_id,plan_digest,decision,decided_by) VALUES($1,$2,$3,$4)`, requestID, digest, input.Decision, owner)
	if err != nil {
		candidateVerificationError(w, 503, "verification_storage_unavailable")
		return
	}
	emptySources := []byte("[]")
	sum := sha256.Sum256(emptySources)
	reason := "user_approved"
	approvalRef := fmt.Sprintf("approval-candidate-%d-%s", requestID, digest[:16])
	if input.Decision == "rejected" {
		reason, approvalRef = "user_rejected", ""
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources,sources_digest,rule_version,approval_ref) VALUES($1,2,$2,$3,$4,$5,'candidate-active/v1',$6)`, requestID, input.Decision, reason, emptySources, hex.EncodeToString(sum[:]), approvalRef)
	if err != nil || tx.Commit() != nil {
		candidateVerificationError(w, 503, "verification_storage_unavailable")
		return
	}
	executionState := "not_requested"
	if input.Decision == "approved" {
		executionState = triggerCandidateExecution(ctx, endpoint, owner, requestID, digest)
	}
	writeJSON(w, 200, map[string]any{"request_id": strconv.FormatInt(requestID, 10), "process_state": input.Decision, "plan_digest": digest, "approval_ref": approvalRef, "execution_start": executionState})
}

func triggerCandidateExecution(ctx context.Context, endpoint diagnosisprep.Endpoint, owner string, requestID int64, digest string) string {
	request := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-execution-schedule", Command: diagnosisprep.ProductControl, Principal: owner,
		Product: &diagnosisprep.ProductRequest{Operation: "candidate_execute", JobID: strconv.FormatInt(requestID, 10), CommandKey: digest}}
	response, err := endpoint.Call(ctx, request)
	if err != nil || diagnosisprep.ValidateResponse(request, response) != nil || response.Error != "" || response.Product == nil || response.Product.Reason != "scheduled" {
		return "unavailable"
	}
	return "scheduled"
}
