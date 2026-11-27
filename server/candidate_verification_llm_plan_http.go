package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisapi"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/learnedlogin"
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
	for _, action := range analysis.Proposal.Actions {
		if action.Method != http.MethodGet && action.Method != http.MethodHead {
			candidateVerificationError(w, 409, "candidate_action_method_unsupported")
			return
		}
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
	originalContext, contextErr := readCandidatePlanOriginal(ctx, pool, requestID)
	if contextErr != nil || originalContext.Candidate.ID != candidate.ID || originalContext.Candidate.EvidenceDigest != candidate.EvidenceDigest ||
		originalContext.OriginalObservation == nil {
		candidateVerificationError(w, 409, "candidate_original_observation_unavailable")
		return
	}
	var auth *CandidatePlanAuth
	if accountRef := originalContext.OriginalObservation.Source.Observation.RequestAccountRef; accountRef != "" {
		// 선택 검증은 실행마다 로그인을 한 번만 보내고 다시 보내지 않는다. 같은 계정의 최근 선택 실행이 인증 실패·결과 불명으로
		// 연달아 상한만큼 끝났으면 새 계획을 만들지 않는다(계정 잠금 방지, 모델 로그인 계약 §5의 연속 실패 상한과 같은 수).
		failures, failureErr := candidateRecentAuthFailures(ctx, tx, ownerResp.Job.Scope.TargetRef, accountRef, candidateSelectedLoginMaxFailures)
		if failureErr != nil {
			candidateVerificationError(w, 503, "verification_storage_unavailable")
			return
		}
		if failures >= candidateSelectedLoginMaxFailures {
			candidateAuthPreparationError(w, string(learnedlogin.ReasonAttemptsExhausted))
			return
		}
		var reason string
		auth, reason = readCandidatePlanAuth(ctx, endpoint, owner, ownerResp.Job.Scope.TargetRef, accountRef)
		if auth == nil {
			candidateAuthPreparationError(w, reason)
			return
		}
	}
	plan, digest, err := makeCandidateLLMPlan(time.Now().UTC(), candidate, originalContext.OriginalObservation, *analysis, ownerResp.Job.Input, *ownerResp.Job.Scope, *targetResp.Product.Target, auth)
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
		// 인증 준비 원천(9칸 digest·학습 방법 digest·준비 경로·출처 수준)이 승인 대기 중에 바뀌면 같은 계획으로 두지 않는다.
		if strictJSON(stored, &prior) != nil || prior.AnalysisDigest != analysis.Digest || prior.AccountRef != plan.AccountRef ||
			prior.Version != plan.Version || !reflect.DeepEqual(prior.AuthPreparation, plan.AuthPreparation) ||
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

// readCandidatePlanAuth는 원본 계정의 인증 준비 원천을 보호 준비 서비스에서 읽는다(docs/contracts/selected-candidate-learned-login.md).
// 출처를 확인한 학습 방법이 먼저이고, 학습 방법이 없을 때만 로그인 설정(9칸)이다. 학습 방법이 있는데 믿을 수 없으면 그 고정
// 사유로 막는다(9칸으로 대신하지 않음). 실패면 nil과 응답 오류 사유다.
func readCandidatePlanAuth(ctx context.Context, endpoint diagnosisprep.Endpoint, owner, targetID, accountRef string) (*CandidatePlanAuth, string) {
	req := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-llm-plan-login", Command: diagnosisprep.ProductControl,
		Principal: owner, Product: &diagnosisprep.ProductRequest{Operation: "target_login_candidate", TargetID: targetID, AccountRef: accountRef}}
	resp, err := endpoint.Call(ctx, req)
	if err != nil || diagnosisprep.ValidateResponse(req, resp) != nil || resp.Error != "" || resp.Product == nil || resp.Product.CandidateLogin == nil {
		return nil, ""
	}
	choice := resp.Product.CandidateLogin
	switch choice.Source {
	case diagnosisprep.CandidateLoginLearned:
		return &CandidatePlanAuth{Learned: &CandidateLearnedLogin{Method: choice.Method.Clone(), Trust: choice.Trust}}, ""
	case diagnosisprep.CandidateLoginConfigured:
		login := *choice.Login
		login.Cookies = append([]string(nil), login.Cookies...)
		return &CandidatePlanAuth{Configured: &login}, ""
	}
	return nil, choice.Reason
}

// candidateAuthPreparationError는 인증 준비를 계획할 수 없는 까닭이다. 쓸 방법이 아예 없으면 기존 사유, 학습 방법이 있지만
// 쓸 수 없으면(출처·판본·인계·실패 상한) 그 고정 사유를 reason으로 같이 준다(값 없음).
func candidateAuthPreparationError(w http.ResponseWriter, reason string) {
	if reason == "" || reason == string(learnedlogin.ReasonNone) {
		candidateVerificationError(w, 409, "candidate_auth_preparation_unavailable")
		return
	}
	writeJSON(w, 409, map[string]string{"error": "candidate_learned_login_unusable", "reason": reason})
}

// candidateSelectedLoginMaxFailures는 같은 계정 선택 검증의 연속 인증 실패 상한이다(모델 로그인 계약 §5 기본 3과 같은 값,
// 사용자 결정 2026-11-26).
const candidateSelectedLoginMaxFailures = 3

// candidateRecentAuthFailures는 그 대상·계정 참조의 가장 최근 선택 검증 실행부터 연달아 인증 실패·결과 불명으로 끝난 수다.
func candidateRecentAuthFailures(ctx context.Context, tx *sql.Tx, targetID, accountRef string, limit int) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.reason FROM diagnostic_candidate_verification_plans p
JOIN diagnostic_candidate_verification_executions x ON x.request_id=p.request_id
JOIN LATERAL (SELECT reason FROM diagnostic_candidate_verification_events WHERE request_id=p.request_id ORDER BY step DESC LIMIT 1) e ON true
WHERE p.target_id=$1 AND p.plan->>'account_ref'=$2 ORDER BY p.request_id DESC LIMIT $3`, targetID, accountRef, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var reasons []string
	for rows.Next() {
		var reason string
		if err := rows.Scan(&reason); err != nil {
			return 0, err
		}
		reasons = append(reasons, reason)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return consecutiveCandidateAuthFailures(reasons), nil
}

// consecutiveCandidateAuthFailures는 최근 실행부터 연달아 인증 실패·결과 불명으로 끝난 수다(진행 중·성공·다른 실패에서 멈춤).
func consecutiveCandidateAuthFailures(latestFirst []string) int {
	count := 0
	for _, reason := range latestFirst {
		if reason != "candidate_auth_failed" && reason != "candidate_auth_result_unknown" {
			break
		}
		count++
	}
	return count
}
