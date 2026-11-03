package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit/budget"
)

func TestBudgetRunBindingDeferredCommitFailureDoesNotPublish(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	record, err := a.Issue(ctx, budgetTestRequest(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `CREATE FUNCTION reject_budget_run_commit_feedback() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'injected_run_commit_failure';
END; $$ LANGUAGE plpgsql;
CREATE CONSTRAINT TRIGGER trg_reject_budget_run_commit_feedback
AFTER INSERT ON budget_run_bindings DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION reject_budget_run_commit_feedback();`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS trg_reject_budget_run_commit_feedback ON budget_run_bindings;
DROP FUNCTION IF EXISTS reject_budget_run_commit_feedback();`)
	})
	got, err := bindBudgetTestRun(ctx, a, record, "case.commit", now.Add(time.Minute), now)
	if !errors.Is(err, ErrBudgetAuthorityUnavailable) || got != (BudgetRunBinding{}) {
		t.Fatalf("failed commit exposed execution input: %+v err=%v", got, err)
	}
	var rows int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM budget_run_bindings WHERE approval_request_ref=$1`, record.RequestRef).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("failed commit left binding rows=%d err=%v", rows, err)
	}
	remaining, err := a.ledger.Remaining(record.Grant.Key, record.Grant.Binding, now)
	if err != nil || remaining != record.Grant.MaxRequests {
		t.Fatalf("failed commit reserved budget: remaining=%d err=%v", remaining, err)
	}
	if _, err := d.ExecContext(ctx, `DROP TRIGGER trg_reject_budget_run_commit_feedback ON budget_run_bindings`); err != nil {
		t.Fatal(err)
	}
	if got, err := bindBudgetTestRun(ctx, a, record, "case.commit", now.Add(time.Minute), now); err != nil || got.Ledger == nil || got.Scope.RunRef == "" {
		t.Fatalf("commit recovery failed: %+v err=%v", got, err)
	}
}

func TestBudgetRunBindingRejectsDurablyClosedApproval(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	record, err := a.Issue(ctx, budgetTestRequest(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	// The in-memory ready snapshot stays unchanged; BindRun must re-read DB state.
	if _, err := d.ExecContext(ctx, `UPDATE budget_approvals SET state='closed' WHERE request_ref=$1`, record.RequestRef); err != nil {
		t.Fatal(err)
	}
	got, err := bindBudgetTestRun(ctx, a, record, "case.closed", now.Add(time.Minute), now)
	if !errors.Is(err, ErrBudgetApprovalNotReady) || got != (BudgetRunBinding{}) {
		t.Fatalf("closed approval bound: %+v err=%v", got, err)
	}
	var rows int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM budget_run_bindings WHERE approval_request_ref=$1`, record.RequestRef).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("closed approval stored rows=%d err=%v", rows, err)
	}
}

func TestBudgetRunBindingRejectsBindingDifferentFromApproval(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	record, err := a.Issue(ctx, budgetTestRequest(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"policy", "reset"} {
		t.Run(field, func(t *testing.T) {
			wrong := record.Grant.Binding
			if field == "policy" {
				wrong.PolicyRef = "policy.other"
			} else {
				wrong.ResetRef = "reset.other"
			}
			// The scope matches the supplied binding, isolating comparison with
			// the durable approval rather than the argument-consistency checks.
			got, err := a.BindRun(ctx, record.Grant.Key, wrong, record.Grant.Key.TaskRef,
				"case.binding", wrong.PolicyRef, wrong.ResetRef, now.Add(time.Minute), now)
			if !errors.Is(err, ErrBudgetApprovalNotReady) || got != (BudgetRunBinding{}) {
				t.Fatalf("different approval binding accepted: %+v err=%v", got, err)
			}
		})
	}
	var rows int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM budget_run_bindings WHERE approval_request_ref=$1`, record.RequestRef).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("different binding stored rows=%d err=%v", rows, err)
	}
}

func TestBudgetRunBindingKeepsEarlierExecutionExpiry(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	record, err := a.Issue(ctx, budgetTestRequest(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	expires := now.Add(time.Minute)
	got, err := bindBudgetTestRun(ctx, a, record, "case.expiry", expires, now)
	if err != nil {
		t.Fatal(err)
	}
	var storedExpires, storedBound time.Time
	if err := d.QueryRowContext(ctx, `SELECT expires_at,bound_at FROM budget_run_bindings WHERE run_ref=$1`, got.Scope.RunRef).Scan(&storedExpires, &storedBound); err != nil {
		t.Fatal(err)
	}
	if !got.ExpiresAt.Equal(expires) || !storedExpires.Equal(expires) || !storedBound.Equal(now) {
		t.Fatalf("earlier execution expiry changed: returned=%v stored=%v bound=%v", got.ExpiresAt, storedExpires, storedBound)
	}
	late := record.Grant.ValidUntil
	if got, err := bindBudgetTestRun(ctx, a, record, "case.expired", late.Add(time.Minute), late); !errors.Is(err, budget.ErrExpired) || got != (BudgetRunBinding{}) {
		t.Fatalf("expired grant bound: %+v err=%v", got, err)
	}
}
