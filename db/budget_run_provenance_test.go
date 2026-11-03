package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// An attacker or faulty writer must not splice a real approval's identity
// fields into another approval's otherwise valid run row.
func TestBudgetRunBindingProvenanceForeignKey(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	authority := budgetTestAuthority(t, d, now)
	request := budgetTestRequest(t, now)
	first, err := authority.Issue(ctx, request, now)
	if err != nil {
		t.Fatal(err)
	}
	otherRequest := budgetTestRequest(t, now)
	otherRequest.TaskRef = "task.2"
	otherRequest.Binding.PolicyRef = "policy.2"
	otherRequest.Binding.ResetRef = "reset.2"
	otherRequest.IssuerRef = "operator.2"
	second, err := authority.Issue(ctx, otherRequest, now)
	if err != nil {
		t.Fatal(err)
	}
	otherEpoch, err := newBudgetRef()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO budget_authority_epochs(epoch,started_at) VALUES($1,$2)`, otherEpoch, now); err != nil {
		t.Fatal(err)
	}

	bound, err := bindBudgetTestRun(ctx, authority, first, "case.good", now.Add(time.Minute), now)
	if err != nil || bound.Ledger == nil {
		t.Fatalf("valid bind failed: %+v, %v", bound, err)
	}
	// Each alternate value comes from another approval or a valid epoch, so the
	// composite relationship must be what rejects a spliced row.
	cases := []struct {
		name, task, budget, policy, reset, issuer, epoch string
	}{
		{"task", second.Grant.Key.TaskRef, first.Grant.Key.BudgetRef, first.Grant.Binding.PolicyRef, first.Grant.Binding.ResetRef, first.IssuerRef, first.Epoch},
		{"budget", first.Grant.Key.TaskRef, second.Grant.Key.BudgetRef, first.Grant.Binding.PolicyRef, first.Grant.Binding.ResetRef, first.IssuerRef, first.Epoch},
		{"policy", first.Grant.Key.TaskRef, first.Grant.Key.BudgetRef, second.Grant.Binding.PolicyRef, first.Grant.Binding.ResetRef, first.IssuerRef, first.Epoch},
		{"reset", first.Grant.Key.TaskRef, first.Grant.Key.BudgetRef, first.Grant.Binding.PolicyRef, second.Grant.Binding.ResetRef, first.IssuerRef, first.Epoch},
		{"issuer", first.Grant.Key.TaskRef, first.Grant.Key.BudgetRef, first.Grant.Binding.PolicyRef, first.Grant.Binding.ResetRef, second.IssuerRef, first.Epoch},
		{"epoch", first.Grant.Key.TaskRef, first.Grant.Key.BudgetRef, first.Grant.Binding.PolicyRef, first.Grant.Binding.ResetRef, first.IssuerRef, otherEpoch},
	}
	insert := func(runRef, task, budget, policy, reset, issuer, epoch string) error {
		_, err := d.ExecContext(ctx, `INSERT INTO budget_run_bindings
(run_ref,task_ref,case_ref,policy_ref,reset_ref,budget_ref,approval_request_ref,issuer_ref,epoch,expires_at,bound_at)
VALUES($1,$2,'case.forged',$3,$4,$5,$6,$7,$8,$9,$10)`,
			runRef, task, policy, reset, budget, first.RequestRef, issuer, epoch,
			now.Add(time.Minute), now)
		return err
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := insert("e2-forged-"+tc.name, tc.task, tc.budget, tc.policy, tc.reset, tc.issuer, tc.epoch)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
				t.Fatalf("expected provenance FK refusal, got %v", err)
			}
		})
	}

	// Reapply to an already-created schema, including a valid stored run.
	// Startup must restore the constraint and retain the existing evidence.
	if _, err := d.ExecContext(ctx, `ALTER TABLE budget_run_bindings DROP CONSTRAINT budget_run_approval_provenance_fk`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, budgetRunBindingSchemaSQL); err != nil {
		t.Fatal(err)
	}
	if err := insert("e2-forged-upgrade", first.Grant.Key.TaskRef, second.Grant.Key.BudgetRef,
		first.Grant.Binding.PolicyRef, first.Grant.Binding.ResetRef, first.IssuerRef, first.Epoch); err == nil {
		t.Fatal("schema upgrade did not restore provenance constraint")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
			t.Fatalf("expected restored provenance FK refusal, got %v", err)
		}
	}
	var count int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM budget_run_bindings WHERE approval_request_ref=$1`, first.RequestRef).Scan(&count); err != nil || count != 1 {
		t.Fatalf("valid run was lost or forged row persisted: count=%d err=%v", count, err)
	}
}

// NOT VALID would admit new rows safely while silently retaining an existing
// forged provenance row. Startup must validate old rows too and fail closed.
func TestBudgetRunBindingProvenanceUpgradeRejectsExistingMismatch(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	authority := budgetTestAuthority(t, d, now)
	first, err := authority.Issue(ctx, budgetTestRequest(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := authority.Issue(ctx, budgetTestRequest(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	good, err := bindBudgetTestRun(ctx, authority, first, "case.upgrade.good", now.Add(time.Minute), now)
	if err != nil || good.Ledger == nil {
		t.Fatalf("valid binding fixture failed: %v", err)
	}
	forgedRef, err := newBudgetRef()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `ALTER TABLE budget_run_bindings DROP CONSTRAINT budget_run_approval_provenance_fk`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO budget_run_bindings
 (run_ref,task_ref,case_ref,policy_ref,reset_ref,budget_ref,approval_request_ref,issuer_ref,epoch,expires_at,bound_at)
 VALUES($1,$2,'case.upgrade.forged',$3,$4,$5,$6,$7,$8,$9,$10)`, forgedRef, first.Grant.Key.TaskRef,
		first.Grant.Binding.PolicyRef, first.Grant.Binding.ResetRef, second.Grant.Key.BudgetRef, first.RequestRef,
		first.IssuerRef, first.Epoch, now.Add(time.Minute), now); err != nil {
		t.Fatalf("existing mismatch fixture was not created: %v", err)
	}
	_, err = tx.ExecContext(ctx, budgetRunBindingSchemaSQL)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("upgrade accepted an existing provenance mismatch: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var validated bool
	if err := d.QueryRowContext(ctx, `SELECT convalidated FROM pg_constraint WHERE conrelid='budget_run_bindings'::regclass AND conname='budget_run_approval_provenance_fk'`).Scan(&validated); err != nil || !validated {
		t.Fatalf("rollback did not restore validated constraint: %v", err)
	}
	var forgedCount, goodCount int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM budget_run_bindings WHERE run_ref=$1`, forgedRef).Scan(&forgedCount); err != nil || forgedCount != 0 {
		t.Fatalf("forged fixture escaped transaction: %d %v", forgedCount, err)
	}
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM budget_run_bindings WHERE approval_request_ref=$1`, first.RequestRef).Scan(&goodCount); err != nil || goodCount != 1 {
		t.Fatalf("valid evidence changed: %d %v", goodCount, err)
	}
}
