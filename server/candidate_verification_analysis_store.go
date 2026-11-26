package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/Autumn-27/artex/diagnosisprep"
)

var ErrCandidateAnalysisState = errors.New("selected candidate analysis state invalid")

type candidateAnalysisView struct {
	Proposal      CandidateAnalysisProposal `json:"proposal"`
	Digest        string                    `json:"digest"`
	ProfileID     int64                     `json:"profile_id"`
	ModelRequests int                       `json:"model_requests"`
	InputTokens   int64                     `json:"input_tokens"`
	OutputTokens  int64                     `json:"output_tokens"`
	CostUSDMicros int64                     `json:"cost_usd_micros"`
}

func readCandidateAnalysis(ctx context.Context, tx *sql.Tx, requestID, candidateID int64) (*candidateAnalysisView, error) {
	var raw []byte
	var out candidateAnalysisView
	err := tx.QueryRowContext(ctx, `SELECT proposal,proposal_digest,profile_id,model_requests,input_tokens,output_tokens,cost_usd_micros
FROM diagnostic_candidate_verification_analyses WHERE request_id=$1`, requestID).
		Scan(&raw, &out.Digest, &out.ProfileID, &out.ModelRequests, &out.InputTokens, &out.OutputTokens, &out.CostUSDMicros)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil || len(raw) > 16<<10 || strictJSON(raw, &out.Proposal) != nil || out.Proposal.Version != "selected/analysis-v1" ||
		out.Proposal.CandidateID != candidateID || out.ProfileID <= 0 || out.ModelRequests != 1 || out.InputTokens < 0 || out.OutputTokens < 0 || out.CostUSDMicros < 0 {
		return nil, ErrCandidateAnalysisState
	}
	canonical, err := json.Marshal(out.Proposal)
	if err != nil {
		return nil, ErrCandidateAnalysisState
	}
	sum := sha256.Sum256(canonical)
	if out.Digest != hex.EncodeToString(sum[:]) {
		return nil, ErrCandidateAnalysisState
	}
	return &out, nil
}

// BeginCandidateAnalysis is the durable send fence. A restart that sees this
// state must fail it, because whether the model received the request is unknown.
func BeginCandidateAnalysis(ctx context.Context, pool *sql.DB, selected CandidateSelectedContext) error {
	if ctx == nil || ctx.Err() != nil || pool == nil || selected.RequestID <= 0 || selected.TaskID <= 0 {
		return ErrCandidateAnalysisState
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var taskID, candidateID int64
	var digest, runRef, state string
	var step int
	err = tx.QueryRowContext(ctx, `SELECT q.task_id,q.candidate_id,q.original_evidence_digest,q.original_run_ref,e.step,e.process_state
FROM diagnostic_candidate_verification_requests q
JOIN LATERAL (SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.id=$1 AND q.method='llm/v1' FOR UPDATE OF q`, selected.RequestID).
		Scan(&taskID, &candidateID, &digest, &runRef, &step, &state)
	if err != nil || taskID != selected.TaskID || candidateID != selected.Candidate.ID || digest != selected.Candidate.EvidenceDigest || runRef != selected.RunRef || step != 1 || state != "requested" {
		return ErrCandidateAnalysisState
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources,sources_digest,rule_version)
VALUES($1,2,'checking_stored_evidence','analysis_started','[]'::json,$2,'selected/llm-v1')`, selected.RequestID, emptyCandidateSourcesDigest)
	if err != nil {
		return err
	}
	return tx.Commit()
}

const emptyCandidateSourcesDigest = "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"

// CompleteCandidateAnalysis appends a validated, bounded proposal and then
// publishes the processing state in the same transaction. This is not a
// vulnerability verdict or approval for any proposed action.
func CompleteCandidateAnalysis(ctx context.Context, pool *sql.DB, selected CandidateSelectedContext, profileID int64, proposal CandidateAnalysisProposal, usage diagnosisprep.ModelUsage) error {
	if ctx == nil || ctx.Err() != nil || pool == nil || profileID <= 0 || !usage.UsageKnown || usage.Requests != 1 ||
		usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CostUSDMicros < 0 {
		return ErrCandidateAnalysisState
	}
	raw, err := json.Marshal(proposal)
	if err != nil || len(raw) > 16<<10 {
		return ErrCandidateAnalysisState
	}
	parsed, err := ParseCandidateAnalysisProposal(raw, selected)
	if err != nil {
		return ErrCandidateAnalysisState
	}
	raw, err = json.Marshal(parsed)
	if err != nil || len(raw) > 16<<10 {
		return ErrCandidateAnalysisState
	}
	sum := sha256.Sum256(raw)
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	var step int
	err = tx.QueryRowContext(ctx, `SELECT e.step,e.process_state FROM diagnostic_candidate_verification_requests q
JOIN LATERAL (SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.id=$1 AND q.task_id=$2 AND q.candidate_id=$3 AND q.original_evidence_digest=$4 AND q.method='llm/v1' FOR UPDATE OF q`,
		selected.RequestID, selected.TaskID, selected.Candidate.ID, selected.Candidate.EvidenceDigest).Scan(&step, &state)
	if err != nil || step != 2 || state != "checking_stored_evidence" {
		return ErrCandidateAnalysisState
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_analyses(request_id,proposal,proposal_digest,profile_id,model_requests,input_tokens,output_tokens,cost_usd_micros)
VALUES($1,$2::jsonb,$3,$4,1,$5,$6,$7)`, selected.RequestID, raw, hex.EncodeToString(sum[:]), profileID, usage.InputTokens, usage.OutputTokens, usage.CostUSDMicros)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources,sources_digest,rule_version)
VALUES($1,3,'analysis_ready','analysis_available','[]'::json,$2,'selected/llm-v1')`, selected.RequestID, emptyCandidateSourcesDigest)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func FailCandidateAnalysis(ctx context.Context, pool *sql.DB, requestID int64, reason string) error {
	if ctx == nil || ctx.Err() != nil || pool == nil || requestID <= 0 || !candidateVerificationReason.MatchString(reason) {
		return ErrCandidateAnalysisState
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var step int
	var state string
	err = tx.QueryRowContext(ctx, `SELECT e.step,e.process_state FROM diagnostic_candidate_verification_requests q
JOIN LATERAL (SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.id=$1 AND q.method='llm/v1' FOR UPDATE OF q`, requestID).Scan(&step, &state)
	if err != nil {
		return ErrCandidateAnalysisState
	}
	if (step == 2 || step == 3) && (state == "failed" || state == "cancelled" || state == "analysis_ready") {
		return tx.Commit()
	}
	if step != 1 && step != 2 || step == 1 && state != "requested" || step == 2 && state != "checking_stored_evidence" {
		return ErrCandidateAnalysisState
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources,sources_digest,rule_version)
VALUES($1,$2,'failed',$3,'[]'::json,$4,'selected/llm-v1')`, requestID, step+1, reason, emptyCandidateSourcesDigest)
	if err != nil {
		return err
	}
	return tx.Commit()
}
