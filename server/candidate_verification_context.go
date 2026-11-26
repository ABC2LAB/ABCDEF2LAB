package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2runner"
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
	// OriginalObservation is present only when the candidate's own call is
	// independently present in the sealed E² projection and both copies agree.
	// A nil value must not be replaced with a guessed replay path.
	OriginalObservation *candidateverify.StoredObservationV2
}

func selectedOriginalObservation(candidate db.DiagnosticCandidate, rows []candidateverify.StoredObservationV2) (*candidateverify.StoredObservationV2, error) {
	var original struct {
		Trusted e2runner.TrustedResult `json:"trusted"`
	}
	if json.Unmarshal([]byte(candidate.Evidence), &original) != nil {
		return nil, ErrCandidateSelectedContext
	}
	var bound *candidateverify.StoredObservationV2
	for i := range rows {
		source := rows[i].Source
		if source.CallRef != candidate.CallRef {
			continue
		}
		if len(original.Trusted.Call.Observations) != 1 {
			return nil, ErrCandidateSelectedContext
		}
		storedObservation, storedErr := json.Marshal(source.Observation)
		candidateObservation, candidateErr := json.Marshal(original.Trusted.Call.Observations[0])
		if bound != nil || source.IntentID != candidate.IntentID || source.TaskRef != strconv.FormatInt(candidate.TaskID, 10) ||
			source.RunRef != candidate.RunRef || original.Trusted.Call.CallRef != candidate.CallRef ||
			storedErr != nil || candidateErr != nil || !bytes.Equal(storedObservation, candidateObservation) ||
			source.PolicyRevision != original.Trusted.Execution.PolicyRevision ||
			rows[i].HeaderMode != original.Trusted.Execution.HeaderMode {
			return nil, ErrCandidateSelectedContext
		}
		scope, err := json.Marshal(original.Trusted.Execution.Scope)
		if err != nil || source.ScopeDigest != db.DiagnosticEvidenceDigest(string(scope)) {
			return nil, ErrCandidateSelectedContext
		}
		bound = &rows[i]
	}
	return bound, nil
}

func sealedCandidateObservations(rows []candidateverify.StoredObservationV2, seal db.SealCheckRecord) []candidateverify.StoredObservationV2 {
	sealed := make([]candidateverify.StoredObservationV2, 0, len(rows))
	for _, observation := range rows {
		if selectedObservationSealed(observation, seal) {
			sealed = append(sealed, observation)
		}
	}
	return sealed
}

// ReadCandidateSelectedContext reads one selected request and the immutable
// original run in a single snapshot. Its caller must separately authorize the
// current requester and recheck the request state immediately before any model
// or E² work; a read-only snapshot is never an execution authority.
func ReadCandidateSelectedContext(ctx context.Context, pool *sql.DB, requestID int64) (CandidateSelectedContext, error) {
	return readCandidateSelectedContext(ctx, pool, requestID, 1, "requested")
}

// The assessment phase reads the same immutable original after the new E²
// evidence is sealed. This still grants neither model nor target authority.
func ReadCandidateAssessmentOriginal(ctx context.Context, pool *sql.DB, requestID int64) (CandidateSelectedContext, error) {
	return readCandidateSelectedContext(ctx, pool, requestID, 7, "evidence_ready")
}

func ReadCandidateStoredAssessmentOriginal(ctx context.Context, pool *sql.DB, requestID int64) (CandidateSelectedContext, error) {
	return readCandidateSelectedContext(ctx, pool, requestID, 3, "analysis_ready")
}

func readCandidatePlanOriginal(ctx context.Context, pool *sql.DB, requestID int64) (CandidateSelectedContext, error) {
	return readCandidateSelectedContext(ctx, pool, requestID, 0, "")
}

func readCandidateSelectedContext(ctx context.Context, pool *sql.DB, requestID int64, wantStep int, wantState string) (CandidateSelectedContext, error) {
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
	if err != nil || (wantStep != 0 && (step != wantStep || state != wantState)) ||
		(wantStep == 0 && !(step == 3 && state == "analysis_ready" || step == 4 && state == "awaiting_approval")) {
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
		if len(out.Observations) > 0 {
			seal, ok, sealErr := db.ReadSealCheck(ctx, tx, out.TaskID, run.RunRef)
			if sealErr != nil {
				return CandidateSelectedContext{}, ErrCandidateSelectedContext
			}
			// Older completed runs may contain bounded observations without a
			// Host checkpoint (for example, when the proof exceeded the row limit).
			// Keep those rows in the immutable store but never show them to the
			// model or use them to authorize a follow-up plan.
			if ok {
				out.Observations = sealedCandidateObservations(out.Observations, seal)
			} else {
				out.Observations = nil
			}
		}
		out.OriginalObservation, err = selectedOriginalObservation(candidate, out.Observations)
		if err != nil {
			return CandidateSelectedContext{}, ErrCandidateSelectedContext
		}
	}
	out.RequestID, out.Candidate, out.RunRef = requestID, candidate, run.RunRef
	out.SourceUnavailable = run.VerificationSourceUnavailable || run.VerificationObservationsStored && out.OriginalObservation == nil
	if err := tx.Commit(); err != nil {
		return CandidateSelectedContext{}, ErrCandidateSelectedContext
	}
	return out, nil
}
