package server

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
)

var ErrCandidateStoredLLMEvidence = errors.New("candidate stored evidence unavailable")

// ReadCandidateStoredAssessmentContext is an evidence read, never a model
// send fence. The final caller must authorize the requester and lock the
// request again immediately before invoking the installed model.
func ReadCandidateStoredAssessmentContext(ctx context.Context, pool *sql.DB, requestID int64) (CandidateAssessmentContext, error) {
	original, err := ReadCandidateStoredAssessmentOriginal(ctx, pool, requestID)
	if err != nil {
		return CandidateAssessmentContext{}, ErrCandidateStoredLLMEvidence
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return CandidateAssessmentContext{}, ErrCandidateStoredLLMEvidence
	}
	defer tx.Rollback()
	var step int
	var state string
	if tx.QueryRowContext(ctx, `SELECT e.step,e.process_state FROM diagnostic_candidate_verification_requests q
JOIN LATERAL (SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.id=$1 AND q.task_id=$2 AND q.candidate_id=$3 AND q.original_evidence_digest=$4 AND q.method='llm/v1'`,
		requestID, original.TaskID, original.Candidate.ID, original.Candidate.EvidenceDigest).Scan(&step, &state) != nil ||
		step != 3 || state != "analysis_ready" {
		return CandidateAssessmentContext{}, ErrCandidateStoredLLMEvidence
	}
	analysis, err := readCandidateAnalysis(ctx, tx, requestID, original.Candidate.ID)
	if err != nil || analysis == nil {
		return CandidateAssessmentContext{}, ErrCandidateStoredLLMEvidence
	}
	answers, err := readCandidateBusinessAnswers(ctx, tx, requestID, *analysis)
	if err != nil {
		return CandidateAssessmentContext{}, ErrCandidateStoredLLMEvidence
	}
	seal, ok, err := db.ReadSealCheck(ctx, tx, original.TaskID, original.RunRef)
	if err != nil || !ok {
		return CandidateAssessmentContext{}, ErrCandidateStoredLLMEvidence
	}
	comparisons, err := selectedStoredComparisons(original, seal)
	if err != nil {
		return CandidateAssessmentContext{}, err
	}
	if tx.Commit() != nil {
		return CandidateAssessmentContext{}, ErrCandidateStoredLLMEvidence
	}
	return CandidateAssessmentContext{Original: original, Analysis: *analysis, Answers: answers,
		StoredOnly: true, FollowupTaskID: original.TaskID, FollowupRunRef: original.RunRef,
		Followup: comparisons, EvidenceRefs: candidateVerificationSourceRefs(storedSources(comparisons))}, nil
}

func storedSources(rows []candidateverify.StoredObservationV2) []candidateverify.SourceV2 {
	out := make([]candidateverify.SourceV2, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Source)
	}
	return out
}

// selectedStoredComparisons chooses only independent, sealed E² calls from
// the original completed run. It never treats a second copy of the candidate
// response, a different function, or an observation before the save fence as
// a separate verification. No vulnerability-type rule is applied here.
func selectedStoredComparisons(c CandidateSelectedContext, seal db.SealCheckRecord) ([]candidateverify.StoredObservationV2, error) {
	if c.OriginalObservation == nil || c.Candidate.SaveFence == nil || c.Candidate.SaveFence.AttemptHighWater == 0 ||
		!candidateverify.ValidSelectedStoredObservation(c.TaskID, c.RunRef, *c.OriginalObservation) ||
		!selectedObservationSealed(*c.OriginalObservation, seal) {
		return nil, ErrCandidateStoredLLMEvidence
	}
	primary := c.OriginalObservation.Source
	base := primary.Observation
	if !base.ExactFunction || base.Endpoint == "" || base.Method == "" || base.RuleRef == "" || base.RequestAuthContext == "" {
		return nil, ErrCandidateStoredLLMEvidence
	}
	var comparisons []candidateverify.StoredObservationV2
	for _, row := range c.Observations {
		// 분석 입력의 관찰 관계(candidateAnalysisRelation)도 같은 조건을 쓴다.
		if !storedComparisonEligible(c, *c.OriginalObservation, row) {
			continue
		}
		if !candidateverify.ValidSelectedStoredObservation(c.TaskID, c.RunRef, row) || !selectedObservationSealed(row, seal) {
			return nil, ErrCandidateStoredLLMEvidence
		}
		comparisons = append(comparisons, row)
		if len(comparisons) > 3 {
			// Do not cherry-pick a bounded subset of contradictory observations.
			return nil, ErrCandidateStoredLLMEvidence
		}
	}
	if len(comparisons) == 0 {
		return nil, ErrCandidateStoredLLMEvidence
	}
	return comparisons, nil
}
