package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strconv"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/report"
)

// BeginCandidateAssessment is a durable, one-shot model send fence. A process
// restart after this event must report unknown outcome, never resend.
func BeginCandidateAssessment(ctx context.Context, pool *sql.DB, selected CandidateAssessmentContext) error {
	if ctx == nil || ctx.Err() != nil || pool == nil || selected.Original.RequestID <= 0 {
		return ErrCandidateAssessmentContext
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ErrCandidateAssessmentContext
	}
	defer tx.Rollback()
	var step int
	var state, runRef string
	if tx.QueryRowContext(ctx, `SELECT e.step,e.process_state,x.run_ref FROM diagnostic_candidate_verification_requests q
JOIN LATERAL (SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
JOIN diagnostic_candidate_verification_executions x ON x.request_id=q.id
WHERE q.id=$1 AND q.method='llm/v1' FOR UPDATE OF q`, selected.Original.RequestID).Scan(&step, &state, &runRef) != nil ||
		step != 7 || state != "evidence_ready" || runRef != selected.FollowupRunRef ||
		revalidateCandidateAssessmentEvidence(ctx, tx, selected) != nil {
		return ErrCandidateAssessmentContext
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events
(request_id,step,process_state,reason,sources,sources_digest,rule_version,execution_ref)
VALUES($1,8,'assessing','final_model_started','[]'::json,$2,'selected/llm-v1',$3)`,
		selected.Original.RequestID, emptyCandidateSourcesDigest, runRef); err != nil {
		return ErrCandidateAssessmentContext
	}
	return tx.Commit()
}

func revalidateCandidateAssessmentEvidence(ctx context.Context, tx *sql.Tx, selected CandidateAssessmentContext) error {
	c := selected.Original
	var explorationID int64
	if tx.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL`, c.TaskID).Scan(&explorationID) != nil {
		return ErrCandidateAssessmentContext
	}
	candidate, found, err := db.ReadDiagnosticCandidateWithFence(ctx, tx, explorationID, c.TaskID, c.Candidate.ID)
	linkedE3, linkErr := candidateClaimedByE3(ctx, tx, c.TaskID, c.Candidate.ID)
	if err != nil || !found || candidate.EvidenceDigest != c.Candidate.EvidenceDigest || candidate.RunRef != c.RunRef || ownershipIDORCandidate(candidate) || linkErr != nil || linkedE3 {
		return ErrCandidateAssessmentContext
	}
	run, err := readDiagnosticRun(ctx, tx, c.TaskID)
	if err != nil || run == nil || run.State != report.DiagnosticRunCompleted || run.RunRef != c.RunRef ||
		run.VerificationVersion != diagnosisVerificationSelected {
		return ErrCandidateAssessmentContext
	}
	if run.VerificationObservationsStored {
		original, err := readCandidateObservations(ctx, tx, c.TaskID, c.RunRef)
		if err != nil || len(original) != run.VerificationObservationsCount || !reflect.DeepEqual(original, c.Observations) {
			return ErrCandidateAssessmentContext
		}
		digest, err := candidateObservationsDigest(original)
		if err != nil || digest != run.VerificationObservationsDigest {
			return ErrCandidateAssessmentContext
		}
		bound, err := selectedOriginalObservation(candidate, original)
		if err != nil || (bound == nil) != (c.OriginalObservation == nil) ||
			bound != nil && !reflect.DeepEqual(*bound, *c.OriginalObservation) {
			return ErrCandidateAssessmentContext
		}
	} else if !run.VerificationSourceUnavailable || len(c.Observations) != 0 || c.OriginalObservation != nil {
		return ErrCandidateAssessmentContext
	}
	var rawPlan, rawRefs []byte
	var planDigest, executionRef, state string
	if tx.QueryRowContext(ctx, `SELECT p.plan,p.plan_digest,x.run_ref,e.process_state,e.sources
FROM diagnostic_candidate_verification_plans p
JOIN diagnostic_candidate_verification_executions x ON x.request_id=p.request_id AND x.plan_digest=p.plan_digest
JOIN diagnostic_candidate_verification_events e ON e.request_id=p.request_id AND e.step=7
WHERE p.request_id=$1 AND x.followup_task_id=$2`, c.RequestID, selected.FollowupTaskID).
		Scan(&rawPlan, &planDigest, &executionRef, &state, &rawRefs) != nil || state != "evidence_ready" || executionRef != selected.FollowupRunRef {
		return ErrCandidateAssessmentContext
	}
	var plan CandidateLLMPlan
	if strictJSON(rawPlan, &plan) != nil || !reflect.DeepEqual(plan, selected.Plan) {
		return ErrCandidateAssessmentContext
	}
	digest, err := candidateLLMPlanDigest(plan)
	if err != nil || digest != planDigest {
		return ErrCandidateAssessmentContext
	}
	row := candidateVerificationAttempt{ID: strconv.FormatInt(c.RequestID, 10), Method: "llm/v1", LLMPlan: &plan,
		Latest: candidateVerificationEvent{State: "evidence_ready", ExecutionRef: executionRef, Sources: rawRefs}}
	binding := candidateVerificationBinding{FollowupTask: sql.NullInt64{Int64: selected.FollowupTaskID, Valid: true},
		RunRef: sql.NullString{String: selected.FollowupRunRef, Valid: true}}
	if validateCandidateLLMEvidence(ctx, tx, row, binding) != nil {
		return ErrCandidateAssessmentContext
	}
	followup, err := readCandidateObservations(ctx, tx, selected.FollowupTaskID, selected.FollowupRunRef)
	if err != nil || !reflect.DeepEqual(followup, selected.Followup) {
		return ErrCandidateAssessmentContext
	}
	return nil
}

func CompleteCandidateAssessment(ctx context.Context, pool *sql.DB, selected CandidateAssessmentContext, profileID int64,
	proposal CandidateFinalVerdict, usage diagnosisprep.ModelUsage) error {
	if ctx == nil || ctx.Err() != nil || pool == nil || profileID <= 0 || profileID != selected.Analysis.ProfileID ||
		!usage.UsageKnown || usage.Requests != 1 || usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CostUSDMicros < 0 {
		return ErrCandidateFinalVerdict
	}
	raw, err := json.Marshal(proposal)
	if err != nil || len(raw) > 16<<10 {
		return ErrCandidateFinalVerdict
	}
	parsed, err := ParseCandidateFinalVerdict(raw, selected)
	if err != nil {
		return ErrCandidateFinalVerdict
	}
	raw, err = json.Marshal(parsed)
	if err != nil || len(raw) > 16<<10 {
		return ErrCandidateFinalVerdict
	}
	sum := sha256.Sum256(raw)
	refs, err := json.Marshal(parsed.Sources)
	if err != nil || len(refs) > 16<<10 {
		return ErrCandidateFinalVerdict
	}
	refsSum := sha256.Sum256(refs)
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ErrCandidateFinalVerdict
	}
	defer tx.Rollback()
	var step int
	var state string
	if tx.QueryRowContext(ctx, `SELECT e.step,e.process_state FROM diagnostic_candidate_verification_requests q
JOIN LATERAL (SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.id=$1 AND q.method='llm/v1' FOR UPDATE OF q`, selected.Original.RequestID).Scan(&step, &state) != nil ||
		step != 8 || state != "assessing" || revalidateCandidateAssessmentEvidence(ctx, tx, selected) != nil {
		return ErrCandidateFinalVerdict
	}
	var reservedRequests, reservedInput, reservedOutput, reservedCost int64
	if tx.QueryRowContext(ctx, `SELECT model_requests,input_tokens,output_tokens,cost_usd_micros
FROM diagnostic_candidate_budget_reservations WHERE request_id=$1 AND task_id=$2 AND evidence_digest=$3`,
		selected.Original.RequestID, selected.Original.TaskID, selected.Original.Candidate.EvidenceDigest).
		Scan(&reservedRequests, &reservedInput, &reservedOutput, &reservedCost) != nil ||
		int64(selected.Analysis.ModelRequests)+usage.Requests > reservedRequests ||
		selected.Analysis.InputTokens+usage.InputTokens > reservedInput ||
		selected.Analysis.OutputTokens+usage.OutputTokens > reservedOutput ||
		selected.Analysis.CostUSDMicros+usage.CostUSDMicros > reservedCost {
		return ErrCandidateFinalVerdict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_verdicts
(request_id,result,result_digest,profile_id,model_requests,input_tokens,output_tokens,cost_usd_micros)
VALUES($1,$2::jsonb,$3,$4,1,$5,$6,$7)`, selected.Original.RequestID, raw, hex.EncodeToString(sum[:]), profileID,
		usage.InputTokens, usage.OutputTokens, usage.CostUSDMicros); err != nil {
		return ErrCandidateFinalVerdict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events
(request_id,step,process_state,verdict,reason,sources,sources_digest,rule_version,execution_ref)
VALUES($1,9,'completed',$2,'model_evidence_checked',$3,$4,'selected/llm-v1',$5)`, selected.Original.RequestID,
		parsed.Verdict, refs, hex.EncodeToString(refsSum[:]), selected.FollowupRunRef); err != nil {
		return ErrCandidateFinalVerdict
	}
	return tx.Commit()
}

// References are verified against both immutable runs; no vulnerability-type
// heuristic, request count, or model confidence score grants confirmation.
func verifiedCandidateFinalSources(c CandidateAssessmentContext, verdict CandidateFinalVerdict) []candidateverify.SourceV2 {
	var out []candidateverify.SourceV2
	for _, ref := range verdict.Sources {
		if c.Original.OriginalObservation != nil {
			s := c.Original.OriginalObservation.Source
			if ref == (candidateVerificationSourceRef{s.CallRef, s.Observation.AttemptRef, s.Digest}) {
				out = append(out, s)
				continue
			}
		}
		for _, row := range c.Followup {
			s := row.Source
			if ref == (candidateVerificationSourceRef{s.CallRef, s.Observation.AttemptRef, s.Digest}) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}
