//go:build linux

package main

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/Autumn-27/artex/diagnosisprep"
)

func (b *urlPreparationRuntime) CandidateBudgetUsage(ctx context.Context, delegation string) (diagnosisprep.Budget, int64, error) {
	var used diagnosisprep.Budget
	pool, err := b.candidateDatabase(ctx)
	if err != nil {
		return used, 0, errPreparation
	}
	defer pool.Close()
	var count int64
	err = pool.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(requests),0),COALESCE(sum(model_requests),0),COALESCE(sum(input_tokens),0),
COALESCE(sum(output_tokens),0),COALESCE(sum(cost_usd_micros),0),COALESCE(sum(duration_seconds),0)
FROM diagnostic_candidate_budget_reservations WHERE delegation_ref=$1`, delegation).
		Scan(&count, &used.Requests, &used.ModelRequests, &used.InputTokens, &used.OutputTokens, &used.CostUSDMicros, &used.DurationSeconds)
	if err != nil {
		return diagnosisprep.Budget{}, 0, errPreparation
	}
	return used, count, nil
}

func (b *urlPreparationRuntime) ReadCandidateBudget(ctx context.Context, requestID int64) (*diagnosisprep.CandidateBudgetRecord, error) {
	pool, err := b.candidateDatabase(ctx)
	if err != nil {
		return nil, errPreparation
	}
	defer pool.Close()
	var record diagnosisprep.CandidateBudgetRecord
	var taskID int64
	err = pool.QueryRowContext(ctx, `SELECT task_id,evidence_digest,delegation_ref,installation_digest,requests,model_requests,input_tokens,output_tokens,cost_usd_micros,duration_seconds
FROM diagnostic_candidate_budget_reservations WHERE request_id=$1`, requestID).
		Scan(&taskID, &record.EvidenceDigest, &record.DelegationRef, &record.InstallationDigest,
			&record.Budget.Requests, &record.Budget.ModelRequests, &record.Budget.InputTokens, &record.Budget.OutputTokens, &record.Budget.CostUSDMicros, &record.Budget.DurationSeconds)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, errPreparation
	}
	record.RequestID, record.TaskID = requestID, strconv.FormatInt(taskID, 10)
	return &record, nil
}

func (b *urlPreparationRuntime) InsertCandidateBudget(ctx context.Context, record diagnosisprep.CandidateBudgetRecord) error {
	pool, err := b.candidateDatabase(ctx)
	if err != nil {
		return errPreparation
	}
	defer pool.Close()
	taskID, err := strconv.ParseInt(record.TaskID, 10, 64)
	if err != nil || taskID <= 0 || strconv.FormatInt(taskID, 10) != record.TaskID {
		return errPreparation
	}
	_, err = pool.ExecContext(ctx, `INSERT INTO diagnostic_candidate_budget_reservations
(request_id,task_id,evidence_digest,delegation_ref,installation_digest,requests,model_requests,input_tokens,output_tokens,cost_usd_micros,duration_seconds)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, record.RequestID, taskID, record.EvidenceDigest, record.DelegationRef,
		record.InstallationDigest, record.Budget.Requests, record.Budget.ModelRequests, record.Budget.InputTokens,
		record.Budget.OutputTokens, record.Budget.CostUSDMicros, record.Budget.DurationSeconds)
	if err != nil {
		return errPreparation
	}
	return nil
}
