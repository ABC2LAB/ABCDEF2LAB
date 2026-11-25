package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/Autumn-27/artex/candidateverify"
)

var errCandidateStoredObservations = errors.New("candidate stored observations invalid")

func candidateObservationsDigest(rows []candidateverify.StoredObservationV2) (string, error) {
	raw, err := json.Marshal(rows)
	if err != nil {
		return "", errCandidateStoredObservations
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// The finalized run record and these bounded projections commit together.
func storeCandidateObservations(ctx context.Context, tx *sql.Tx, taskID int64, runRef string, observations []candidateverify.StoredObservationV2) error {
	for _, observation := range observations {
		raw, err := json.Marshal(observation)
		if err != nil || len(raw) > 4096 {
			return errCandidateStoredObservations
		}
		sum := sha256.Sum256(raw)
		_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_observations_v2(task_id,run_ref,call_ref,attempt_ref,source,source_digest) VALUES($1,$2,$3,$4,$5::jsonb,$6)`,
			taskID, runRef, observation.Source.CallRef, observation.Source.Observation.AttemptRef, raw, hex.EncodeToString(sum[:]))
		if err != nil {
			return errCandidateStoredObservations
		}
	}
	return nil
}

func readCandidateObservations(ctx context.Context, tx *sql.Tx, taskID int64, runRef string) ([]candidateverify.StoredObservationV2, error) {
	rows, err := tx.QueryContext(ctx, `SELECT call_ref,attempt_ref,source,source_digest FROM diagnostic_candidate_observations_v2 WHERE task_id=$1 AND run_ref=$2 ORDER BY id LIMIT $3`, taskID, runRef, candidateverify.MaxStoredObservationsV2+1)
	if err != nil {
		return nil, errCandidateStoredObservations
	}
	defer rows.Close()
	out := make([]candidateverify.StoredObservationV2, 0)
	seenCalls, seenAttempts := map[string]bool{}, map[string]bool{}
	for rows.Next() {
		if len(out) >= candidateverify.MaxStoredObservationsV2 {
			return nil, errCandidateStoredObservations
		}
		var callRef, attemptRef, digest string
		var raw []byte
		if rows.Scan(&callRef, &attemptRef, &raw, &digest) != nil || len(raw) > 4096 {
			return nil, errCandidateStoredObservations
		}
		var observation candidateverify.StoredObservationV2
		if json.Unmarshal(raw, &observation) != nil || observation.Source.CallRef != callRef || observation.Source.Observation.AttemptRef != attemptRef ||
			observation.Source.TaskRef != strconv.FormatInt(taskID, 10) || observation.Source.RunRef != runRef || seenCalls[callRef] || seenAttempts[attemptRef] {
			return nil, errCandidateStoredObservations
		}
		canonical, _ := json.Marshal(observation)
		sum := sha256.Sum256(canonical)
		if hex.EncodeToString(sum[:]) != digest {
			return nil, errCandidateStoredObservations
		}
		seenCalls[callRef], seenAttempts[attemptRef] = true, true
		out = append(out, observation)
	}
	if rows.Err() != nil {
		return nil, errCandidateStoredObservations
	}
	return out, nil
}
