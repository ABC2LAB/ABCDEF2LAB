package db

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"time"

	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
)

//go:embed budget_run_binding_schema.sql
var budgetRunBindingSchemaSQL string

// BudgetRunBinding is the durable link between a server-issued execution and
// one approved budget. Its ledger is usable only while the host keeps this
// authority and execution gate alive. It is not permission to perform HTTP.
type BudgetRunBinding struct {
	Scope              transmit.Scope
	Key                budget.Key
	Binding            budget.Binding
	ApprovalRequestRef string
	IssuerRef          string
	Epoch              string
	ExpiresAt          time.Time
	BoundAt            time.Time
	Ledger             *budget.Ledger
}

// BindRun allocates a fresh RunRef and persists its full immutable provenance
// before returning the ledger pointer. A failed/ambiguous commit returns no
// execution input; the unknown RunRef is never replayed or reconstructed.
// Caller-supplied task/case/policy/reset and expiry are trusted host values.
// The server adapter must verify task, policy, reset, resource, and account
// authority before attaching the result to a controlled transmitter.
func (a *BudgetAuthority) BindRun(
	ctx context.Context, key budget.Key, binding budget.Binding,
	taskRef, caseRef, policyRef, resetRef string,
	expiresAt, now time.Time,
) (BudgetRunBinding, error) {
	now, expiresAt = budgetTime(now), budgetTime(expiresAt)
	if a == nil || ctx == nil || !validBudgetTime(now) || !validBudgetTime(expiresAt) ||
		!now.Before(expiresAt) || !validBudgetRef(taskRef) || !validBudgetRef(caseRef) ||
		!validBudgetRef(policyRef) || !validBudgetRef(resetRef) {
		return BudgetRunBinding{}, ErrBudgetApprovalInvalid
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.checkActive(ctx); err != nil {
		return BudgetRunBinding{}, err
	}
	record, exists := a.ready[key]
	if !exists || record.State != "ready" || record.Grant.Binding != binding ||
		key.TaskRef != taskRef || binding.PolicyRef != policyRef || binding.ResetRef != resetRef ||
		record.Epoch != a.epoch {
		return BudgetRunBinding{}, ErrBudgetApprovalNotReady
	}
	if !now.Before(record.Grant.ValidUntil) {
		return BudgetRunBinding{}, budget.ErrExpired
	}
	if expiresAt.After(record.Grant.ValidUntil) {
		expiresAt = record.Grant.ValidUntil
	}
	runID, err := newBudgetRef()
	if err != nil {
		return BudgetRunBinding{}, err
	}
	out := BudgetRunBinding{
		Scope: transmit.Scope{
			RunRef: "e2-" + runID, TaskRef: taskRef, CaseRef: caseRef,
			PolicyRef: policyRef, ResetRef: resetRef,
		},
		Key: key, Binding: binding, ApprovalRequestRef: record.RequestRef,
		IssuerRef: record.IssuerRef, Epoch: a.epoch, ExpiresAt: expiresAt,
		BoundAt: now,
	}
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return BudgetRunBinding{}, ErrBudgetAuthorityUnavailable
	}
	defer tx.Rollback()
	var currentEpoch, approvalEpoch, state string
	var closed bool
	if err := tx.QueryRowContext(ctx, `SELECT epoch,closed FROM budget_authority_current
WHERE singleton=1 FOR UPDATE`).Scan(&currentEpoch, &closed); err != nil || closed || currentEpoch != a.epoch {
		return BudgetRunBinding{}, ErrBudgetAuthorityUnavailable
	}
	if err := tx.QueryRowContext(ctx, `SELECT epoch,state FROM budget_approvals
WHERE request_ref=$1 FOR SHARE`, record.RequestRef).Scan(&approvalEpoch, &state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BudgetRunBinding{}, ErrBudgetApprovalNotReady
		}
		return BudgetRunBinding{}, ErrBudgetAuthorityUnavailable
	}
	if approvalEpoch != a.epoch || state != "ready" {
		return BudgetRunBinding{}, ErrBudgetApprovalNotReady
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO budget_run_bindings
(run_ref,task_ref,case_ref,policy_ref,reset_ref,budget_ref,approval_request_ref,issuer_ref,epoch,expires_at,bound_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		out.Scope.RunRef, taskRef, caseRef, policyRef, resetRef, key.BudgetRef,
		record.RequestRef, record.IssuerRef, a.epoch, expiresAt, now); err != nil {
		return BudgetRunBinding{}, ErrBudgetAuthorityUnavailable
	}
	if err := tx.Commit(); err != nil {
		return BudgetRunBinding{}, ErrBudgetAuthorityUnavailable
	}
	// The lock or epoch may have changed while the commit was in flight.
	if err := a.checkActive(ctx); err != nil {
		return BudgetRunBinding{}, err
	}
	out.Ledger = a.ledger
	return out, nil
}
