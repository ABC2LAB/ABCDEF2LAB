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
	"github.com/Autumn-27/artex/e2proxy"
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
	want := map[string]int{}
	for _, action := range row.LLMPlan.Actions {
		want[fmt.Sprintf("%s %s", action.Method, action.ExactPath)]++
	}
	sources := make([]candidateverify.SourceV2, 0, len(observations))
	for _, stored := range observations {
		obs := stored.Source.Observation
		key := fmt.Sprintf("%s %s", obs.Method, obs.Endpoint)
		if !candidateverify.ValidSelectedStoredObservation(binding.FollowupTask.Int64, binding.RunRef.String, stored) ||
			!selectedObservationSealed(stored, seal) ||
			stored.Source.ScopeDigest != scopeDigest ||
			stored.HeaderMode != e2proxy.HeaderPreserve || !obs.ExactFunction || obs.Decision != "allowed" ||
			!obs.ResponseComplete || !obs.AuditComplete || want[key] == 0 {
			return db.ErrDiagnosticCandidate
		}
		want[key]--
		sources = append(sources, stored.Source)
	}
	for _, count := range want {
		if count != 0 {
			return db.ErrDiagnosticCandidate
		}
	}
	var refs []candidateVerificationSourceRef
	if json.Unmarshal(row.Latest.Sources, &refs) != nil || !reflect.DeepEqual(refs, candidateVerificationSourceRefs(sources)) {
		return db.ErrDiagnosticCandidate
	}
	return nil
}
