package db

import (
	"context"
	"database/sql"
	"errors"
	"sort"
)

// This compatibility guard rejects a known controlled fence. Missing fences
// retain legacy behavior here; strict unknown->deny belongs to the dedicated
// path and server admission. It is not a complete global mode gate.
func rejectControlledTaskTx(ctx context.Context, tx *sql.Tx, taskID, explorationID, nodeID int64) error {
	if ctx == nil || ctx.Err() != nil || tx == nil {
		return ErrControlledFindingInput
	}
	var isolation string
	if err := tx.QueryRowContext(ctx, "SHOW transaction_isolation").Scan(&isolation); err != nil {
		return controlledFindingStoreError{err}
	}
	if isolation != "read committed" {
		return controlledFindingStoreError{ErrTaskModeIsolation}
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, findingEvidenceLockKey); err != nil {
		return controlledFindingStoreError{err}
	}
	ids := map[int64]bool{}
	if taskID > 0 {
		ids[taskID] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM tasks WHERE exploration_id=$1 OR exploration_id=(SELECT exploration_id FROM exploration_nodes WHERE id=$2)`, explorationID, nodeID)
	if err != nil {
		return controlledFindingStoreError{err}
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return controlledFindingStoreError{err}
		}
		ids[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return controlledFindingStoreError{err}
	}
	ordered := make([]int64, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for _, id := range ordered {
		var locked int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE id=$1 FOR UPDATE`, id).Scan(&locked); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return controlledFindingStoreError{err}
		}
		var mode string
		err := tx.QueryRowContext(ctx, `SELECT mode FROM task_control_modes WHERE task_id=$1`, id).Scan(&mode)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return controlledFindingStoreError{err}
		}
		if mode == "controlled" {
			return ErrControlledFindingGeneric
		}
	}
	return nil
}

func rejectControlledFindingTx(ctx context.Context, tx *sql.Tx, findingID int64) error {
	var taskID, nodeID sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT task_id,node_id FROM findings WHERE id=$1`, findingID).Scan(&taskID, &nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return controlledFindingStoreError{err}
	}
	return rejectControlledTaskTx(ctx, tx, taskID.Int64, 0, nodeID.Int64)
}

func rejectControlledRetestTx(ctx context.Context, tx *sql.Tx, retestID, conversationID int64) error {
	var findingID int64
	err := tx.QueryRowContext(ctx, `SELECT finding_id FROM finding_retests WHERE id=$1 OR conversation_id=$2`, retestID, conversationID).Scan(&findingID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return controlledFindingStoreError{err}
	}
	return rejectControlledFindingTx(ctx, tx, findingID)
}
