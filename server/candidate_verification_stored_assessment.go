package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"reflect"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/report"
)

// revalidateStoredCandidateEvidence is called under the immutable request row
// lock both before the final model send fence and before committing its result.
func revalidateStoredCandidateEvidence(ctx context.Context, tx *sql.Tx, c CandidateAssessmentContext) error {
	if !c.StoredOnly || c.Original.RequestID <= 0 || c.Original.OriginalObservation == nil ||
		c.FollowupTaskID != c.Original.TaskID || c.FollowupRunRef != c.Original.RunRef ||
		c.Plan.Version != "" || len(c.Followup) == 0 {
		return ErrCandidateStoredLLMEvidence
	}
	var explorationID int64
	if tx.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL`, c.Original.TaskID).Scan(&explorationID) != nil {
		return ErrCandidateStoredLLMEvidence
	}
	candidate, found, err := db.ReadDiagnosticCandidateWithFence(ctx, tx, explorationID, c.Original.TaskID, c.Original.Candidate.ID)
	linkedE3, linkErr := candidateClaimedByE3(ctx, tx, c.Original.TaskID, c.Original.Candidate.ID)
	if err != nil || !found || linkErr != nil || linkedE3 || ownershipIDORCandidate(candidate) ||
		candidate.EvidenceDigest != c.Original.Candidate.EvidenceDigest || candidate.RunRef != c.Original.RunRef ||
		!reflect.DeepEqual(candidate.SaveFence, c.Original.Candidate.SaveFence) {
		return ErrCandidateStoredLLMEvidence
	}
	run, err := readDiagnosticRun(ctx, tx, candidate.TaskID)
	if err != nil || run == nil || run.State != report.DiagnosticRunCompleted ||
		run.RunRef != candidate.RunRef || run.VerificationVersion != diagnosisVerificationSelected || !run.VerificationObservationsStored {
		return ErrCandidateStoredLLMEvidence
	}
	rows, err := readCandidateObservations(ctx, tx, candidate.TaskID, candidate.RunRef)
	if err != nil || len(rows) != run.VerificationObservationsCount || !reflect.DeepEqual(rows, c.Original.Observations) {
		return ErrCandidateStoredLLMEvidence
	}
	digest, err := candidateObservationsDigest(rows)
	if err != nil || digest != run.VerificationObservationsDigest {
		return ErrCandidateStoredLLMEvidence
	}
	primary, err := selectedOriginalObservation(candidate, rows)
	if err != nil || primary == nil || !reflect.DeepEqual(*primary, *c.Original.OriginalObservation) {
		return ErrCandidateStoredLLMEvidence
	}
	seal, ok, err := db.ReadSealCheck(ctx, tx, candidate.TaskID, candidate.RunRef)
	if err != nil || !ok {
		return ErrCandidateStoredLLMEvidence
	}
	comparisons, err := selectedStoredComparisons(c.Original, seal)
	if err != nil || !reflect.DeepEqual(comparisons, c.Followup) {
		return ErrCandidateStoredLLMEvidence
	}
	analysis, err := readCandidateAnalysis(ctx, tx, c.Original.RequestID, candidate.ID)
	if err != nil || analysis == nil || !reflect.DeepEqual(*analysis, c.Analysis) {
		return ErrCandidateStoredLLMEvidence
	}
	answers, err := readCandidateBusinessAnswers(ctx, tx, c.Original.RequestID, *analysis)
	if err != nil || !reflect.DeepEqual(answers, c.Answers) {
		return ErrCandidateStoredLLMEvidence
	}
	return nil
}

func BeginStoredCandidateAssessment(ctx context.Context, pool *sql.DB, c CandidateAssessmentContext) error {
	if ctx == nil || ctx.Err() != nil || pool == nil || !c.StoredOnly {
		return ErrCandidateStoredLLMEvidence
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ErrCandidateStoredLLMEvidence
	}
	defer tx.Rollback()
	var step int
	var state string
	if tx.QueryRowContext(ctx, `SELECT e.step,e.process_state FROM diagnostic_candidate_verification_requests q
JOIN LATERAL (SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.id=$1 AND q.method='llm/v1' FOR UPDATE OF q`, c.Original.RequestID).Scan(&step, &state) != nil ||
		step != 3 || state != "analysis_ready" || revalidateStoredCandidateEvidence(ctx, tx, c) != nil {
		return ErrCandidateStoredLLMEvidence
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events
(request_id,step,process_state,reason,sources,sources_digest,rule_version)
VALUES($1,4,'assessing','stored_final_model_started','[]'::json,$2,'selected/llm-v1')`,
		c.Original.RequestID, emptyCandidateSourcesDigest); err != nil {
		return ErrCandidateStoredLLMEvidence
	}
	return tx.Commit()
}

func CompleteStoredCandidateAssessment(ctx context.Context, pool *sql.DB, c CandidateAssessmentContext,
	profileID int64, proposal CandidateFinalVerdict, usage diagnosisprep.ModelUsage) error {
	if ctx == nil || ctx.Err() != nil || pool == nil || !c.StoredOnly || profileID <= 0 || profileID != c.Analysis.ProfileID ||
		!usage.UsageKnown || usage.Requests != 1 || usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CostUSDMicros < 0 {
		return ErrCandidateStoredLLMEvidence
	}
	raw, err := json.Marshal(proposal)
	if err != nil || len(raw) > 16<<10 {
		return ErrCandidateStoredLLMEvidence
	}
	parsed, err := ParseCandidateFinalVerdict(raw, c)
	if err != nil {
		return ErrCandidateStoredLLMEvidence
	}
	raw, err = json.Marshal(parsed)
	if err != nil || len(raw) > 16<<10 {
		return ErrCandidateStoredLLMEvidence
	}
	resultDigest := sha256.Sum256(raw)
	refs, err := json.Marshal(parsed.Sources)
	if err != nil || len(refs) > 16<<10 {
		return ErrCandidateStoredLLMEvidence
	}
	refsDigest := sha256.Sum256(refs)
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ErrCandidateStoredLLMEvidence
	}
	defer tx.Rollback()
	var step int
	var state string
	if tx.QueryRowContext(ctx, `SELECT e.step,e.process_state FROM diagnostic_candidate_verification_requests q
JOIN LATERAL (SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.id=$1 AND q.method='llm/v1' FOR UPDATE OF q`, c.Original.RequestID).Scan(&step, &state) != nil ||
		step != 4 || state != "assessing" || revalidateStoredCandidateEvidence(ctx, tx, c) != nil {
		return ErrCandidateStoredLLMEvidence
	}
	var maxRequests, maxInput, maxOutput, maxCost int64
	if tx.QueryRowContext(ctx, `SELECT model_requests,input_tokens,output_tokens,cost_usd_micros
FROM diagnostic_candidate_budget_reservations WHERE request_id=$1 AND task_id=$2 AND evidence_digest=$3`,
		c.Original.RequestID, c.Original.TaskID, c.Original.Candidate.EvidenceDigest).
		Scan(&maxRequests, &maxInput, &maxOutput, &maxCost) != nil ||
		int64(c.Analysis.ModelRequests)+usage.Requests > maxRequests ||
		c.Analysis.InputTokens+usage.InputTokens > maxInput || c.Analysis.OutputTokens+usage.OutputTokens > maxOutput ||
		c.Analysis.CostUSDMicros+usage.CostUSDMicros > maxCost {
		return ErrCandidateStoredLLMEvidence
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_verdicts
(request_id,result,result_digest,profile_id,model_requests,input_tokens,output_tokens,cost_usd_micros)
VALUES($1,$2::jsonb,$3,$4,1,$5,$6,$7)`, c.Original.RequestID, raw, hex.EncodeToString(resultDigest[:]),
		profileID, usage.InputTokens, usage.OutputTokens, usage.CostUSDMicros); err != nil {
		return ErrCandidateStoredLLMEvidence
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events
(request_id,step,process_state,verdict,reason,sources,sources_digest,rule_version)
VALUES($1,5,'completed',$2,'stored_model_evidence_checked',$3,$4,'selected/llm-v1')`,
		c.Original.RequestID, parsed.Verdict, refs, hex.EncodeToString(refsDigest[:])); err != nil {
		return ErrCandidateStoredLLMEvidence
	}
	return tx.Commit()
}
