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
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisapi"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/dlp"
	"github.com/Autumn-27/artex/report"
)

type candidateBusinessAnswer struct {
	QuestionIndex int       `json:"question_index"`
	Question      string    `json:"question"`
	Answer        string    `json:"answer"`
	AnsweredAt    time.Time `json:"answered_at"`
}

func candidateTextDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func readCandidateBusinessAnswers(ctx context.Context, tx *sql.Tx, id int64, analysis candidateAnalysisView) ([]candidateBusinessAnswer, error) {
	rows, err := tx.QueryContext(ctx, `SELECT question_index,analysis_digest,question_digest,answer,answer_digest,answered_at
FROM diagnostic_candidate_verification_answers WHERE request_id=$1 ORDER BY question_index`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidateBusinessAnswer
	for rows.Next() {
		var item candidateBusinessAnswer
		var analysisDigest, questionDigest, answerDigest string
		if rows.Scan(&item.QuestionIndex, &analysisDigest, &questionDigest, &item.Answer, &answerDigest, &item.AnsweredAt) != nil ||
			item.QuestionIndex < 1 || item.QuestionIndex > len(analysis.Proposal.BusinessQuestions) ||
			analysisDigest != analysis.Digest || questionDigest != candidateTextDigest(analysis.Proposal.BusinessQuestions[item.QuestionIndex-1]) ||
			answerDigest != candidateTextDigest(item.Answer) || strings.TrimSpace(item.Answer) == "" ||
			len(item.Answer) > 4096 || !utf8.ValidString(item.Answer) || item.AnsweredAt.IsZero() {
			return nil, ErrCandidateAnalysisState
		}
		item.Question = analysis.Proposal.BusinessQuestions[item.QuestionIndex-1]
		out = append(out, item)
	}
	if rows.Err() != nil {
		return nil, ErrCandidateAnalysisState
	}
	return out, nil
}

// A business answer is an attributed statement only. This endpoint never
// creates an E² plan, grants execution, calls a model, or sends target traffic.
func candidateVerificationAnswerHTTP(w http.ResponseWriter, r *http.Request, pool *sql.DB, authenticate diagnosisapi.Authenticate, endpoint diagnosisprep.Endpoint) {
	route, requestID, ok := parseCandidateDecisionRouteAt(r, "answers")
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
		QuestionIndex  int    `json:"question_index"`
		Answer         string `json:"answer"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	if err != nil || strictJSON(raw, &input) != nil || !candidateVerificationDigest.MatchString(input.AnalysisDigest) ||
		input.QuestionIndex < 1 || input.QuestionIndex > 4 || !utf8.ValidString(input.Answer) ||
		strings.TrimSpace(input.Answer) == "" || len(input.Answer) > 4096 || strings.ContainsRune(input.Answer, 0) {
		candidateVerificationError(w, 400, "invalid_verification_request")
		return
	}
	answer := dlp.DiagnosticText(input.Answer)
	if strings.TrimSpace(answer) == "" || len(answer) > 4096 {
		candidateVerificationError(w, 400, "invalid_verification_request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	ownerReq := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-answer-owner", Command: diagnosisprep.AuthorizeTask,
		Principal: owner, TaskID: strconv.FormatInt(route.task, 10)}
	ownerResp, err := endpoint.Call(ctx, ownerReq)
	if err != nil || diagnosisprep.ValidateResponse(ownerReq, ownerResp) != nil || ownerResp.Authorized == nil || !*ownerResp.Authorized ||
		ownerResp.Job == nil || ownerResp.Job.TaskID != ownerReq.TaskID {
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
	if err != nil || run == nil || run.State != report.DiagnosticRunCompleted || run.VerificationVersion != diagnosisVerificationSelected || run.RunRef != candidate.RunRef {
		candidateVerificationError(w, 409, "diagnosis_not_completed")
		return
	}
	var requester, state, digest string
	var step int
	err = tx.QueryRowContext(ctx, `SELECT q.requester,q.original_evidence_digest,e.step,e.process_state FROM diagnostic_candidate_verification_requests q
JOIN LATERAL (SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.id=$1 AND q.task_id=$2 AND q.candidate_id=$3 AND q.method='llm/v1' FOR UPDATE OF q`, requestID, route.task, route.candidate).
		Scan(&requester, &digest, &step, &state)
	if errors.Is(err, sql.ErrNoRows) {
		candidateVerificationError(w, 404, "verification_not_found")
		return
	}
	if err != nil || requester != owner || digest != candidate.EvidenceDigest {
		candidateVerificationError(w, 403, "verification_scope_denied")
		return
	}
	if step != 3 || state != "analysis_ready" {
		candidateVerificationError(w, 409, "analysis_not_ready")
		return
	}
	analysis, err := readCandidateAnalysis(ctx, tx, requestID, candidate.ID)
	if err != nil || analysis == nil {
		candidateVerificationError(w, 503, "analysis_integrity_failed")
		return
	}
	if analysis.Digest != input.AnalysisDigest || input.QuestionIndex > len(analysis.Proposal.BusinessQuestions) {
		candidateVerificationError(w, 409, "analysis_changed")
		return
	}
	question := analysis.Proposal.BusinessQuestions[input.QuestionIndex-1]
	var existingAnswer string
	err = tx.QueryRowContext(ctx, `SELECT answer FROM diagnostic_candidate_verification_answers WHERE request_id=$1 AND question_index=$2`, requestID, input.QuestionIndex).Scan(&existingAnswer)
	if err == nil && existingAnswer != answer {
		candidateVerificationError(w, 409, "business_answer_conflict")
		return
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		candidateVerificationError(w, 503, "verification_storage_unavailable")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_answers
(request_id,question_index,analysis_digest,question_digest,answer,answer_digest,answered_by)
VALUES($1,$2,$3,$4,$5,$6,$7)`, requestID, input.QuestionIndex, analysis.Digest,
			candidateTextDigest(question), answer, candidateTextDigest(answer), owner)
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
	writeJSON(w, 200, map[string]any{"request_id": strconv.FormatInt(requestID, 10), "question_index": input.QuestionIndex, "analysis_digest": analysis.Digest, "answered": true})
}
