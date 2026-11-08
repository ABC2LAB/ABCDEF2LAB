package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Autumn-27/artex/llmpolicy"
	"strconv"
	"time"
)

const diagnosisModelUsageOrigin = "first_diagnosis_model_preparation_v1"

type diagnosisModelPreparation struct {
	Version   int                       `json:"version"`
	TaskID    int64                     `json:"task_id"`
	RecordID  string                    `json:"record_id"`
	SavedAt   time.Time                 `json:"saved_at"`
	Succeeded bool                      `json:"succeeded"`
	Usage     llmpolicy.OpenRouterUsage `json:"usage"`
}

// 유료 준비 호출도 실패 여부와 무관하게 저장한다. 원인 원문은 저장하지 않는다.
func (s *FirstDiagnosisSupervisor) PersistPreparationModelUsage(ctx context.Context, taskID string, operationErr error) error {
	if s == nil || ctx == nil || ctx.Err() != nil {
		return ErrFirstDiagnosisOutcome
	}
	usage := s.ModelUsage()
	if usage == nil || usage.Requests == 0 {
		return nil
	}
	task, err := strconv.ParseInt(taskID, 10, 64)
	if err != nil || task <= 0 || strconv.FormatInt(task, 10) != taskID || s.m == nil || s.m.pg == nil {
		return ErrFirstDiagnosisOutcome
	}
	return saveDiagnosisPreparationModelUsage(ctx, s.m.pg.DB, task, *usage, operationErr == nil)
}
func saveDiagnosisPreparationModelUsage(ctx context.Context, pool *sql.DB, task int64, usage llmpolicy.OpenRouterUsage, succeeded bool) error {
	if ctx == nil || ctx.Err() != nil || pool == nil || task <= 0 || usage.Requests <= 0 {
		return ErrFirstDiagnosisOutcome
	}
	body, err := json.Marshal(struct {
		Succeeded bool
		Usage     llmpolicy.OpenRouterUsage
	}{succeeded, usage})
	if err != nil {
		return ErrFirstDiagnosisOutcome
	}
	digest := sha256.Sum256(body)
	ref := hex.EncodeToString(digest[:])
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		return ErrFirstDiagnosisOutcome
	}
	defer tx.Rollback()
	var exploration int64
	if tx.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, task).Scan(&exploration) != nil {
		return ErrFirstDiagnosisOutcome
	}
	var existing []byte
	err = tx.QueryRowContext(ctx, `SELECT payload FROM exploration_nodes WHERE exploration_id=$1 AND origin=$2 AND payload->>'task_id'=$3 AND payload->>'record_id'=$4 LIMIT 1`, exploration, diagnosisModelUsageOrigin, strconv.FormatInt(task, 10), ref).Scan(&existing)
	if err == nil {
		var old diagnosisModelPreparation
		if json.Unmarshal(existing, &old) != nil || old.Version != 1 || old.TaskID != task || old.RecordID != ref {
			return ErrFirstDiagnosisOutcome
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ErrFirstDiagnosisOutcome
	}
	payload, err := json.Marshal(diagnosisModelPreparation{Version: 1, TaskID: task, RecordID: ref, SavedAt: time.Now().UTC(), Succeeded: succeeded, Usage: usage})
	if err != nil {
		return ErrFirstDiagnosisOutcome
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO exploration_nodes(exploration_id,kind,state,origin,payload) VALUES($1,'fact','confirmed',$2,$3::jsonb)`, exploration, diagnosisModelUsageOrigin, string(payload)); err != nil {
		return ErrFirstDiagnosisOutcome
	}
	if tx.Commit() != nil {
		return ErrFirstDiagnosisOutcome
	}
	return nil
}
func readDiagnosisPreparationUsage(ctx context.Context, pool *sql.DB, task int64) ([]diagnosisModelPreparation, error) {
	rows, err := pool.QueryContext(ctx, `SELECT n.payload FROM tasks t JOIN exploration_nodes n ON n.exploration_id=t.exploration_id WHERE t.id=$1 AND t.deleted_at IS NULL AND n.kind='fact' AND n.state='confirmed' AND n.origin=$2 AND n.payload->>'task_id'=$3 ORDER BY n.id LIMIT 129`, task, diagnosisModelUsageOrigin, strconv.FormatInt(task, 10))
	if err != nil {
		return nil, ErrFirstDiagnosisOutcome
	}
	defer rows.Close()
	var result []diagnosisModelPreparation
	for rows.Next() {
		var raw []byte
		var entry diagnosisModelPreparation
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &entry) != nil || entry.Version != 1 || entry.TaskID != task || entry.SavedAt.IsZero() || len(entry.RecordID) != 64 || entry.Usage.Requests <= 0 {
			return nil, ErrFirstDiagnosisOutcome
		}
		result = append(result, entry)
	}
	if rows.Err() != nil || rows.Close() != nil || len(result) > 128 {
		return nil, ErrFirstDiagnosisOutcome
	}
	return result, nil
}
