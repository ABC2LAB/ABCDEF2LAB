package server

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
)

var ErrCandidateSelectedContext = errors.New("selected candidate context unavailable")

// CandidateSelectedContext is a read-only, task-bound input for the future
// verification model. Observations are E²'s bounded safe projections; no
// model opinion or extra target request is produced by this read.
type CandidateSelectedContext struct {
	RequestID         int64
	TaskID            int64
	Candidate         db.DiagnosticCandidate
	RunRef            string
	SourceUnavailable bool
	Observations      []candidateverify.StoredObservationV2
}

// ReadCandidateSelectedContext reads one selected request and the immutable
// original run in a single snapshot. Its caller must separately authorize the
// current requester and recheck the request state immediately before any model
// or E² work; a read-only snapshot is never an execution authority.
func ReadCandidateSelectedContext(ctx context.Context, pool *sql.DB, requestID int64) (CandidateSelectedContext, error) {
	if ctx == nil || ctx.Err() != nil || pool == nil || requestID <= 0 {
		return CandidateSelectedContext{}, ErrCandidateSelectedContext
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return CandidateSelectedContext{}, ErrCandidateSelectedContext
	}
	defer tx.Rollback()
	var out CandidateSelectedContext
	var candidateID, explorationID, originalVersion int64
	var originalDigest, originalRun, state string
	var step int
	err = tx.QueryRowContext(ctx, `SELECT q.task_id,q.candidate_id,t.exploration_id,q.original_evidence_version,q.original_evidence_digest,q.original_run_ref,e.step,e.process_state
FROM diagnostic_candidate_verification_requests q
JOIN tasks t ON t.id=q.task_id AND t.deleted_at IS NULL
JOIN LATERAL (SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.id=$1 AND q.method='llm/v1'`, requestID).Scan(&out.TaskID, &candidateID, &explorationID, &originalVersion, &originalDigest, &originalRun, &step, &state)
	if err != nil || step != 1 || state != "requested" {
		return CandidateSelectedContext{}, ErrCandidateSelectedContext
	}
	candidate, found, err := db.ReadDiagnosticCandidateWithFence(ctx, tx, explorationID, out.TaskID, candidateID)
	if err != nil || !found || candidate.Validate() != nil || candidate.EvidenceVersion != originalVersion ||
		candidate.EvidenceDigest != originalDigest || candidate.RunRef != originalRun {
		return CandidateSelectedContext{}, ErrCandidateSelectedContext
	}
	run, err := readDiagnosticRun(ctx, tx, out.TaskID)
	if err != nil || run == nil || run.State != report.DiagnosticRunCompleted ||
		run.VerificationVersion != diagnosisVerificationSelected || run.RunRef != originalRun {
		return CandidateSelectedContext{}, ErrCandidateSelectedContext
	}
	assessed := false
	for _, assessment := range run.CandidateAssessments {
		if assessment.CandidateID == candidate.ID && assessment.EvidenceDigest == candidate.EvidenceDigest &&
			assessment.Status == candidateverify.Insufficient &&
			(assessment.Reason == "selected_verification_required" || assessment.Reason == "source_unavailable") {
			if assessed {
				return CandidateSelectedContext{}, ErrCandidateSelectedContext
			}
			assessed = true
		}
	}
	if !assessed {
		return CandidateSelectedContext{}, ErrCandidateSelectedContext
	}
	if run.VerificationObservationsStored {
		out.Observations, err = readCandidateObservations(ctx, tx, out.TaskID, run.RunRef)
		if err != nil || len(out.Observations) != run.VerificationObservationsCount {
			return CandidateSelectedContext{}, ErrCandidateSelectedContext
		}
		digest, err := candidateObservationsDigest(out.Observations)
		if err != nil || digest != run.VerificationObservationsDigest {
			return CandidateSelectedContext{}, ErrCandidateSelectedContext
		}
		for _, observation := range out.Observations {
			if !candidateverify.ValidSelectedStoredObservation(out.TaskID, run.RunRef, observation) {
				return CandidateSelectedContext{}, ErrCandidateSelectedContext
			}
		}
	}
	out.RequestID, out.Candidate, out.RunRef, out.SourceUnavailable = requestID, candidate, run.RunRef, run.VerificationSourceUnavailable
	if err := tx.Commit(); err != nil {
		return CandidateSelectedContext{}, ErrCandidateSelectedContext
	}
	return out, nil
}
