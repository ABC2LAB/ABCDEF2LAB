package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/Autumn-27/artex/candidateverify"
)

var ErrCandidateAssessmentContext = errors.New("candidate assessment context unavailable")

// CandidateAssessmentContext contains only the original sealed projection,
// the newly sealed E² projection and immutable user business statements.
// Reading it cannot send a model or target request.
type CandidateAssessmentContext struct {
	Original       CandidateSelectedContext
	Plan           CandidateLLMPlan
	Analysis       candidateAnalysisView
	Answers        []candidateBusinessAnswer
	FollowupTaskID int64
	FollowupRunRef string
	Followup       []candidateverify.StoredObservationV2
	EvidenceRefs   []candidateVerificationSourceRef
}

func ReadCandidateAssessmentContext(ctx context.Context, pool *sql.DB, requestID int64) (CandidateAssessmentContext, error) {
	original, err := ReadCandidateAssessmentOriginal(ctx, pool, requestID)
	if err != nil {
		return CandidateAssessmentContext{}, ErrCandidateAssessmentContext
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return CandidateAssessmentContext{}, ErrCandidateAssessmentContext
	}
	defer tx.Rollback()
	var out CandidateAssessmentContext
	out.Original = original
	var planRaw, sourceRaw []byte
	var decision, state, reason, planDigest, ruleVersion, executionRef string
	var step int
	err = tx.QueryRowContext(ctx, `SELECT p.plan,p.plan_digest,d.decision,x.followup_task_id,x.run_ref,e.step,e.process_state,e.reason,e.rule_version,e.execution_ref,e.sources
FROM diagnostic_candidate_verification_requests q
JOIN diagnostic_candidate_verification_plans p ON p.request_id=q.id
JOIN diagnostic_candidate_verification_decisions d ON d.request_id=q.id AND d.plan_digest=p.plan_digest
JOIN diagnostic_candidate_verification_executions x ON x.request_id=q.id AND x.plan_digest=p.plan_digest
JOIN LATERAL (SELECT * FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.id=$1 AND q.method='llm/v1' AND q.task_id=$2 AND q.candidate_id=$3 AND q.original_evidence_digest=$4`, requestID,
		original.TaskID, original.Candidate.ID, original.Candidate.EvidenceDigest).
		Scan(&planRaw, &planDigest, &decision, &out.FollowupTaskID, &out.FollowupRunRef, &step, &state, &reason, &ruleVersion, &executionRef, &sourceRaw)
	if err != nil || decision != "approved" || step != 7 || state != "evidence_ready" || reason != "sealed_observations_available" ||
		ruleVersion != diagnosisVerificationSelected || executionRef != out.FollowupRunRef ||
		strictJSON(planRaw, &out.Plan) != nil || out.Plan.TaskID != original.TaskID || out.Plan.CandidateID != original.Candidate.ID ||
		out.Plan.EvidenceDigest != original.Candidate.EvidenceDigest || out.Plan.OriginalRunRef != original.RunRef ||
		out.FollowupTaskID == original.TaskID || out.FollowupTaskID <= 0 {
		return CandidateAssessmentContext{}, ErrCandidateAssessmentContext
	}
	digest, err := candidateLLMPlanDigest(out.Plan)
	if err != nil || digest != planDigest {
		return CandidateAssessmentContext{}, ErrCandidateAssessmentContext
	}
	analysis, err := readCandidateAnalysis(ctx, tx, requestID, original.Candidate.ID)
	if err != nil || analysis == nil || !candidateLLMPlanMatchesAnalysis(out.Plan, *analysis) {
		return CandidateAssessmentContext{}, ErrCandidateAssessmentContext
	}
	out.Analysis = *analysis
	out.Answers, err = readCandidateBusinessAnswers(ctx, tx, requestID, out.Analysis)
	if err != nil {
		return CandidateAssessmentContext{}, ErrCandidateAssessmentContext
	}
	out.Followup, err = readCandidateObservations(ctx, tx, out.FollowupTaskID, out.FollowupRunRef)
	if err != nil || len(out.Followup) != len(out.Plan.Actions) {
		return CandidateAssessmentContext{}, ErrCandidateAssessmentContext
	}
	if json.Unmarshal(sourceRaw, &out.EvidenceRefs) != nil || len(out.EvidenceRefs) != len(out.Followup) {
		return CandidateAssessmentContext{}, ErrCandidateAssessmentContext
	}
	row := candidateVerificationAttempt{ID: strconv.FormatInt(requestID, 10), Method: "llm/v1", LLMPlan: &out.Plan,
		Latest: candidateVerificationEvent{State: state, ExecutionRef: executionRef, Sources: sourceRaw}}
	binding := candidateVerificationBinding{FollowupTask: sql.NullInt64{Int64: out.FollowupTaskID, Valid: true},
		RunRef: sql.NullString{String: out.FollowupRunRef, Valid: true}}
	if validateCandidateLLMEvidence(ctx, tx, row, binding) != nil {
		return CandidateAssessmentContext{}, ErrCandidateAssessmentContext
	}
	if tx.Commit() != nil {
		return CandidateAssessmentContext{}, ErrCandidateAssessmentContext
	}
	return out, nil
}
