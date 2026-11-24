package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
)

const diagnosisVerificationPolicyOrigin = "diagnosis_verification_policy_v2"
const diagnosisVerificationV2 = "passive/v2"

type diagnosisVerificationPolicy struct {
	Version string `json:"version"`
	TaskID  int64  `json:"task_id"`
	RunRef  string `json:"run_ref"`
}

// The durable marker is written before any model-stage candidate can be saved.
// Task row locking serializes duplicate starts without changing old reports.
func saveDiagnosisVerificationPolicy(ctx context.Context, pool *sql.DB, taskID int64, runRef string) error {
	if ctx == nil || pool == nil || taskID <= 0 || runRef == "" || len(runRef) > 256 {
		return ErrFirstDiagnosisOutcome
	}
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exp int64
	if err := tx.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, taskID).Scan(&exp); err != nil {
		return err
	}
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT payload FROM exploration_nodes WHERE exploration_id=$1 AND kind='fact' AND state='confirmed' AND origin=$2 AND payload->>'task_id'=$3 ORDER BY id DESC LIMIT 1`, exp, diagnosisVerificationPolicyOrigin, strconv.FormatInt(taskID, 10)).Scan(&raw)
	if err == nil {
		var old diagnosisVerificationPolicy
		if json.Unmarshal(raw, &old) != nil || old.Version != diagnosisVerificationV2 || old.TaskID != taskID || old.RunRef != runRef {
			return ErrFirstDiagnosisOutcome
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	payload, _ := json.Marshal(diagnosisVerificationPolicy{Version: diagnosisVerificationV2, TaskID: taskID, RunRef: runRef})
	if _, err := tx.ExecContext(ctx, `INSERT INTO exploration_nodes(exploration_id,kind,state,origin,payload) VALUES($1,'fact','confirmed',$2,$3::jsonb)`, exp, diagnosisVerificationPolicyOrigin, string(payload)); err != nil {
		return err
	}
	return tx.Commit()
}

func readDiagnosisVerificationPolicy(ctx context.Context, tx *sql.Tx, taskID int64) (*diagnosisVerificationPolicy, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT n.payload FROM tasks t JOIN exploration_nodes n ON n.exploration_id=t.exploration_id WHERE t.id=$1 AND t.deleted_at IS NULL AND n.kind='fact' AND n.state='confirmed' AND n.origin=$2 AND n.payload->>'task_id'=$3 ORDER BY n.id DESC LIMIT 1`, taskID, diagnosisVerificationPolicyOrigin, strconv.FormatInt(taskID, 10)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var policy diagnosisVerificationPolicy
	if json.Unmarshal(raw, &policy) != nil || policy.Version != diagnosisVerificationV2 || policy.TaskID != taskID || policy.RunRef == "" {
		return nil, ErrFirstDiagnosisOutcome
	}
	return &policy, nil
}
