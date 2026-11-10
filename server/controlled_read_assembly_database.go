// 승인된 기존 공급자의 수명과 실제 검증 경계만 조립하며 권한을 발급하지 않는다.
package server

import (
	"context"
	"database/sql"
)

func CheckControlledReadAssemblyDatabase(ctx context.Context, database *sql.DB) error {
	if ctx == nil || ctx.Err() != nil || database == nil {
		return ErrControlledReadHostConfiguration
	}
	tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ErrControlledReadHostConfiguration
	}
	defer tx.Rollback()
	required := []struct {
		table   string
		columns []string
	}{
		{"task_control_modes", []string{"task_id", "mode", "version"}},
		{"e3_evidence_manifests", []string{"id", "task_id", "execution_ref", "evidence_revision", "canonical", "digest", "frozen_at"}},
		{"e3_oracle_verdicts", []string{"id", "record_version", "payload_canonical", "payload", "payload_digest", "oracle_value"}},
		{"e3_verdict_issuances", []string{"id", "verdict_id", "manifest_id", "task_id", "exploration_id", "intent_id", "mode_version", "execution_ref", "eval_run_id", "operation_key", "input_digest"}},
		{"e3_verdict_links", []string{"issuance_id", "task_id", "finding_id", "purpose", "evidence_version"}},
		{"evidence_revision_initials", []string{"revision", "execution_ref", "task_id", "exploration_id", "intent_id", "mode_version", "binding", "input_digest", "evidence_digest"}},
		{"evidence_revision_operations", []string{"operation_ref", "revision", "request_digest"}},
		{"audit_checkpoint_epochs", []string{"store_ref", "epoch_ref", "generation", "anchor_digest"}},
		{"audit_checkpoint_history", []string{"store_ref", "run_ref", "revision", "epoch_ref", "generation", "publisher_ref", "phase", "checkpoint"}},
		{"audit_checkpoint_heads", []string{"store_ref", "run_ref", "epoch_ref", "journal_id", "revision"}},
		{"audit_checkpoint_operations", []string{"store_ref", "epoch_ref", "operation_ref", "request_digest", "receipt_ref", "run_ref", "revision"}},
	}
	for _, relation := range required {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pg_attribute WHERE attrelid=to_regclass($1) AND attname=ANY($2) AND attnum>0 AND NOT attisdropped`, relation.table, relation.columns).Scan(&count); err != nil || count != len(relation.columns) {
			return ErrControlledReadHostConfiguration
		}
	}
	if tx.Commit() != nil {
		return ErrControlledReadHostConfiguration
	}
	return nil
}
