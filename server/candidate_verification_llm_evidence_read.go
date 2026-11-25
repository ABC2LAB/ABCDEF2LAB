package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
)

// Reconstruct a ready observation from the sealed follow-up task on every
// read. An event row by itself cannot make an unaudited response evidence.
func validateCandidateLLMEvidence(ctx context.Context, tx *sql.Tx, row candidateVerificationAttempt, binding candidateVerificationBinding) error {
	if row.Method != "llm/v1" || row.Latest.State != "evidence_ready" {
		return nil
	}
	if row.LLMPlan == nil || !binding.FollowupTask.Valid || !binding.RunRef.Valid ||
		binding.RunRef.String != row.Latest.ExecutionRef {
		return db.ErrDiagnosticCandidate
	}
	requestID, err := strconv.ParseInt(row.ID, 10, 64)
	if err != nil || requestID <= 0 {
		return db.ErrDiagnosticCandidate
	}
	var scopeDigest string
	if tx.QueryRowContext(ctx, `SELECT scope_digest FROM diagnostic_candidate_verification_executions
WHERE request_id=$1 AND followup_task_id=$2 AND run_ref=$3`, requestID, binding.FollowupTask.Int64, binding.RunRef.String).
		Scan(&scopeDigest) != nil || !candidateVerificationDigest.MatchString(scopeDigest) {
		return db.ErrDiagnosticCandidate
	}
	var state string
	var paused bool
	var finished sql.NullTime
	if tx.QueryRowContext(ctx, `SELECT status,paused,completed_at FROM tasks WHERE id=$1 AND deleted_at IS NULL`, binding.FollowupTask.Int64).
		Scan(&state, &paused, &finished) != nil || state != "done" || !paused || !finished.Valid {
		return db.ErrDiagnosticCandidate
	}
	seal, ok, err := db.ReadSealCheck(ctx, tx, binding.FollowupTask.Int64, binding.RunRef.String)
	if err != nil || !ok {
		return db.ErrDiagnosticCandidate
	}
	observations, err := readCandidateObservations(ctx, tx, binding.FollowupTask.Int64, binding.RunRef.String)
	if err != nil || len(observations) != len(row.LLMPlan.Actions) {
		return db.ErrDiagnosticCandidate
	}
	matched := make([]bool, len(row.LLMPlan.Actions))
	sources := make([]candidateverify.SourceV2, 0, len(observations))
	for _, stored := range observations {
		obs := stored.Source.Observation
		if !candidateverify.ValidSelectedStoredObservation(binding.FollowupTask.Int64, binding.RunRef.String, stored) ||
			!selectedObservationSealed(stored, seal) ||
			stored.Source.ScopeDigest != scopeDigest ||
			!selectedPlanObservationMatches(*row.LLMPlan, stored) || !obs.ExactFunction || obs.Decision != "allowed" ||
			!obs.ResponseComplete || !obs.AuditComplete {
			return db.ErrDiagnosticCandidate
		}
		found := false
		for i, action := range row.LLMPlan.Actions {
			if !matched[i] && obs.RuleRef == fmt.Sprintf("candidate-llm-%d", i+1) && selectedActionMatchesObservation(action, obs) {
				matched[i], found = true, true
				break
			}
		}
		if !found {
			return db.ErrDiagnosticCandidate
		}
		sources = append(sources, stored.Source)
	}
	for _, done := range matched {
		if !done {
			return db.ErrDiagnosticCandidate
		}
	}
	var refs []candidateVerificationSourceRef
	if json.Unmarshal(row.Latest.Sources, &refs) != nil || !reflect.DeepEqual(refs, candidateVerificationSourceRefs(sources)) {
		return db.ErrDiagnosticCandidate
	}
	return nil
}
