package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
)

// A completed active verdict is never trusted solely because an event says
// "confirmed". Every history and report read reconstructs the program verdict
// from the two durable E² projections and the closed task's seal.
func validateCandidateActiveEvidence(ctx context.Context, tx *sql.Tx, row candidateVerificationAttempt, binding candidateVerificationBinding, candidate db.DiagnosticCandidate) error {
	if row.Method != "active/v1" || row.Latest.State != "completed" {
		return nil
	}
	if row.Plan == nil || !binding.FollowupTask.Valid || binding.FollowupTask.Int64 <= 0 || !binding.RunRef.Valid ||
		row.Latest.Verdict == nil || !candidatePlanMatchesEvidence(*row.Plan, candidate) {
		return db.ErrDiagnosticCandidate
	}
	var state string
	var paused bool
	var completedAt sql.NullTime
	if tx.QueryRowContext(ctx, `SELECT status,paused,completed_at FROM tasks WHERE id=$1 AND deleted_at IS NULL`, binding.FollowupTask.Int64).
		Scan(&state, &paused, &completedAt) != nil || state != "done" || !paused || !completedAt.Valid {
		return db.ErrDiagnosticCandidate
	}
	seal, ok, err := db.ReadSealCheck(ctx, tx, binding.FollowupTask.Int64, binding.RunRef.String)
	if err != nil || !ok || len(seal.Checks) < 2 {
		return db.ErrDiagnosticCandidate
	}
	for _, check := range seal.Checks {
		if !check.Included {
			return db.ErrDiagnosticCandidate
		}
	}
	observations, err := readCandidateObservations(ctx, tx, binding.FollowupTask.Int64, binding.RunRef.String)
	if err != nil || len(observations) != 2 {
		return db.ErrDiagnosticCandidate
	}
	result := candidateverify.AssessActiveStoredV1(candidate, candidateverify.ActiveBindingV1{
		TaskID: binding.FollowupTask.Int64, RunRef: binding.RunRef.String,
		ExactPath: row.Plan.ExactPath, CaseRef: row.PlanDigest,
	}, observations)
	if result.Status != *row.Latest.Verdict || result.Reason != row.Latest.Reason || !candidateverify.ValidSourcesV2(result) {
		return db.ErrDiagnosticCandidate
	}
	var refs []candidateVerificationSourceRef
	if json.Unmarshal(row.Latest.Sources, &refs) != nil || !reflect.DeepEqual(refs, candidateVerificationSourceRefs(result.Sources)) {
		return db.ErrDiagnosticCandidate
	}
	return nil
}
