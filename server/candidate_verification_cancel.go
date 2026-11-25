package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/diagnosisapi"
	"github.com/Autumn-27/artex/diagnosisprep"
)

func candidateVerificationCancelHTTP(w http.ResponseWriter, r *http.Request, pool *sql.DB, authenticate diagnosisapi.Authenticate, endpoint diagnosisprep.Endpoint) {
	route, requestID, ok := parseCandidateDecisionRouteAt(r, "cancel")
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
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if err != nil || strictJSON(raw, &input) != nil || input.PlanDigest != "" && !candidateVerificationDigest.MatchString(input.PlanDigest) {
		candidateVerificationError(w, 400, "invalid_verification_request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	ownerReq := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-cancel-owner", Command: diagnosisprep.AuthorizeTask,
		Principal: owner, TaskID: strconv.FormatInt(route.task, 10)}
	ownerResp, err := endpoint.Call(ctx, ownerReq)
	if err != nil || diagnosisprep.ValidateResponse(ownerReq, ownerResp) != nil || ownerResp.Authorized == nil || !*ownerResp.Authorized || ownerResp.Job == nil || ownerResp.Job.TaskID != ownerReq.TaskID {
		candidateVerificationError(w, 403, "verification_scope_denied")
		return
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		candidateVerificationError(w, 503, "verification_storage_unavailable")
		return
	}
	defer tx.Rollback()
	var requester, method, digest, state string
	var step int
	err = tx.QueryRowContext(ctx, `SELECT q.requester,q.method,COALESCE(p.plan_digest,q.original_evidence_digest),e.step,e.process_state
FROM diagnostic_candidate_verification_requests q
LEFT JOIN diagnostic_candidate_verification_plans p ON p.request_id=q.id
JOIN LATERAL (SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.id=$1 AND q.task_id=$2 AND q.candidate_id=$3 AND q.method IN ('active/v1','llm/v1') FOR UPDATE OF q`, requestID, route.task, route.candidate).
		Scan(&requester, &method, &digest, &step, &state)
	if errors.Is(err, sql.ErrNoRows) {
		candidateVerificationError(w, 404, "verification_not_found")
		return
	}
	if err != nil || requester != owner {
		candidateVerificationError(w, 403, "verification_scope_denied")
		return
	}
	if method == "llm/v1" {
		if input.PlanDigest != "" && input.PlanDigest != digest {
			candidateVerificationError(w, 409, "verification_plan_changed")
			return
		}
		if state != "requested" && state != "checking_stored_evidence" && state != "analysis_ready" && state != "awaiting_approval" &&
			!(state == "assessing" && step == 4) &&
			state != "approved" && state != "running" && state != "cancelled" {
			candidateVerificationError(w, 409, "verification_cancel_unavailable")
			return
		}
		storedAssessing := state == "assessing" && step == 4
		if state != "cancelled" && (step == 1 || step == 2 || step == 3 || step == 4) {
			emptySources := []byte("[]")
			sum := sha256.Sum256(emptySources)
			_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources,sources_digest,rule_version)
VALUES($1,$2,'cancelled','user_cancelled',$3,$4,'selected/llm-v1')`, requestID, step+1, emptySources, hex.EncodeToString(sum[:]))
			if err != nil {
				candidateVerificationError(w, 503, "verification_storage_unavailable")
				return
			}
			state = "cancelled"
		}
		if tx.Commit() != nil {
			candidateVerificationError(w, 503, "verification_storage_unavailable")
			return
		}
		if step == 2 && state == "cancelled" {
			command := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-analysis-cancel", Command: diagnosisprep.ProductControl,
				Principal: owner, Product: &diagnosisprep.ProductRequest{Operation: "candidate_analysis_cancel", JobID: strconv.FormatInt(requestID, 10), CommandKey: digest}}
			_, _ = endpoint.Call(ctx, command)
		}
		if storedAssessing && state == "cancelled" {
			command := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-stored-cancel", Command: diagnosisprep.ProductControl,
				Principal: owner, Product: &diagnosisprep.ProductRequest{Operation: "candidate_stored_cancel", JobID: strconv.FormatInt(requestID, 10), CommandKey: digest}}
			_, _ = endpoint.Call(ctx, command)
		}
		if state == "approved" || state == "running" {
			command := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-llm-cancel-runtime", Command: diagnosisprep.ProductControl,
				Principal: owner, Product: &diagnosisprep.ProductRequest{Operation: "candidate_llm_cancel", JobID: strconv.FormatInt(requestID, 10), CommandKey: digest}}
			response, callErr := endpoint.Call(ctx, command)
			if callErr != nil || diagnosisprep.ValidateResponse(command, response) != nil || response.Error != "" || response.Product == nil || response.Product.Reason == "" {
				candidateVerificationError(w, 503, "verification_cancel_unavailable")
				return
			}
			state = response.Product.Reason
		}
		writeJSON(w, 200, map[string]string{"request_id": strconv.FormatInt(requestID, 10), "process_state": state})
		return
	}
	if !candidateVerificationDigest.MatchString(input.PlanDigest) {
		candidateVerificationError(w, 400, "invalid_verification_request")
		return
	}
	if digest != input.PlanDigest {
		candidateVerificationError(w, 409, "verification_plan_changed")
		return
	}
	if state == "awaiting_approval" && step == 1 {
		emptySources := []byte("[]")
		sum := sha256.Sum256(emptySources)
		_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources,sources_digest,rule_version)
VALUES($1,2,'cancelled','user_cancelled',$2,$3,'candidate-active/v1')`, requestID, emptySources, hex.EncodeToString(sum[:]))
		if err != nil || tx.Commit() != nil {
			candidateVerificationError(w, 503, "verification_storage_unavailable")
			return
		}
		writeJSON(w, 200, map[string]string{"request_id": strconv.FormatInt(requestID, 10), "process_state": "cancelled"})
		return
	}
	if tx.Commit() != nil {
		candidateVerificationError(w, 503, "verification_storage_unavailable")
		return
	}
	if state == "approved" || state == "running" {
		command := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-cancel-runtime", Command: diagnosisprep.ProductControl,
			Principal: owner, Product: &diagnosisprep.ProductRequest{Operation: "candidate_cancel", JobID: strconv.FormatInt(requestID, 10), CommandKey: digest}}
		response, err := endpoint.Call(ctx, command)
		if err != nil || diagnosisprep.ValidateResponse(command, response) != nil || response.Error != "" || response.Product == nil || response.Product.Reason == "" {
			candidateVerificationError(w, 503, "verification_cancel_unavailable")
			return
		}
		state = response.Product.Reason
	}
	writeJSON(w, 200, map[string]string{"request_id": strconv.FormatInt(requestID, 10), "process_state": state})
}

func parseCandidateDecisionRouteAt(r *http.Request, action string) (candidateVerificationRoute, int64, bool) {
	if r.Method != http.MethodPost || r.URL.RawQuery != "" || r.URL.RawPath != "" || !strings.HasPrefix(r.URL.Path, candidateVerificationPrefix) {
		return candidateVerificationRoute{}, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, candidateVerificationPrefix), "/")
	if len(parts) != 6 || parts[1] != "candidates" || parts[3] != "verifications" || parts[5] != action {
		return candidateVerificationRoute{}, 0, false
	}
	ids := make([]int64, 3)
	for i, part := range []string{parts[0], parts[2], parts[4]} {
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != part {
			return candidateVerificationRoute{}, 0, false
		}
		ids[i] = id
	}
	return candidateVerificationRoute{task: ids[0], candidate: ids[1]}, ids[2], true
}
