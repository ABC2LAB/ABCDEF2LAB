package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit/budget"
)

func bindBudgetTestRun(
	ctx context.Context, a *BudgetAuthority, record BudgetApprovalRecord,
	caseRef string, expiresAt, now time.Time,
) (BudgetRunBinding, error) {
	return a.BindRun(ctx, record.Grant.Key, record.Grant.Binding,
		record.Grant.Key.TaskRef, caseRef,
		record.Grant.Binding.PolicyRef, record.Grant.Binding.ResetRef,
		expiresAt, now)
}

func TestBudgetRunBindingConcurrentImmutableProvenance(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	request := budgetTestRequest(t, now)
	record, err := a.Issue(ctx, request, now)
	if err != nil {
		t.Fatal(err)
	}
	const runs = 16
	bindings := make([]BudgetRunBinding, runs)
	errs := make([]error, runs)
	var wg sync.WaitGroup
	for i := range bindings {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bindings[i], errs[i] = bindBudgetTestRun(ctx, a, record,
				fmt.Sprintf("case.%d", i), now.Add(2*time.Hour), now)
		}(i)
	}
	wg.Wait()
	seen := make(map[string]bool, runs)
	for i, binding := range bindings {
		if errs[i] != nil || binding.Scope.RunRef == "" || seen[binding.Scope.RunRef] ||
			binding.Scope.CaseRef != fmt.Sprintf("case.%d", i) ||
			binding.Scope.TaskRef != request.TaskRef ||
			binding.Key != record.Grant.Key || binding.Binding != record.Grant.Binding ||
			binding.ApprovalRequestRef != request.RequestRef ||
			binding.IssuerRef != request.IssuerRef ||
			binding.Epoch != a.Epoch() || binding.Ledger == nil ||
			!binding.ExpiresAt.Equal(record.Grant.ValidUntil) {
			t.Fatalf("run %d: %+v, %v", i, binding, errs[i])
		}
		seen[binding.Scope.RunRef] = true
		var storedTask, storedCase, storedPolicy, storedReset, storedBudget, storedRequest, storedIssuer, storedEpoch string
		if err := d.QueryRowContext(ctx, `SELECT task_ref,case_ref,policy_ref,reset_ref,budget_ref,
approval_request_ref,issuer_ref,epoch FROM budget_run_bindings WHERE run_ref=$1`,
			binding.Scope.RunRef).Scan(&storedTask, &storedCase, &storedPolicy, &storedReset,
			&storedBudget, &storedRequest, &storedIssuer, &storedEpoch); err != nil ||
			storedTask != binding.Scope.TaskRef || storedCase != binding.Scope.CaseRef ||
			storedPolicy != binding.Scope.PolicyRef || storedReset != binding.Scope.ResetRef ||
			storedBudget != binding.Key.BudgetRef || storedRequest != request.RequestRef ||
			storedIssuer != request.IssuerRef || storedEpoch != binding.Epoch {
			t.Fatalf("run %d provenance differs from DB: %v", i, err)
		}
	}
	remaining, err := bindings[0].Ledger.Remaining(record.Grant.Key, record.Grant.Binding, now)
	if err != nil || remaining != request.MaxRequests {
		t.Fatalf("binding reserved budget: remaining=%d err=%v", remaining, err)
	}
	for i := 0; i < int(request.MaxRequests); i++ {
		if err := bindings[i].Ledger.Reserve(record.Grant.Key, record.Grant.Binding,
			budget.Attempt{RunRef: bindings[i].Scope.RunRef, Sequence: 1}, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := bindings[2].Ledger.Reserve(record.Grant.Key, record.Grant.Binding,
		budget.Attempt{RunRef: bindings[2].Scope.RunRef, Sequence: 1}, now); !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("run split escaped cumulative budget: %v", err)
	}
}

func TestBudgetRunBindingRejectsMismatchedScopeAndExpiry(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	request := budgetTestRequest(t, now)
	record, err := a.Issue(ctx, request, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                         string
		task, caseRef, policy, reset string
		expiresAt                    time.Time
		want                         error
	}{
		{"wrong task", "task.2", "case.1", "policy.1", "reset.1", now.Add(time.Minute), ErrBudgetApprovalNotReady},
		{"wrong policy", "task.1", "case.1", "policy.2", "reset.1", now.Add(time.Minute), ErrBudgetApprovalNotReady},
		{"wrong reset", "task.1", "case.1", "policy.1", "reset.2", now.Add(time.Minute), ErrBudgetApprovalNotReady},
		{"URL case", "task.1", "http://case", "policy.1", "reset.1", now.Add(time.Minute), ErrBudgetApprovalInvalid},
		{"expired execution", "task.1", "case.1", "policy.1", "reset.1", now, ErrBudgetApprovalInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := a.BindRun(ctx, record.Grant.Key, record.Grant.Binding,
				tc.task, tc.caseRef, tc.policy, tc.reset, tc.expiresAt, now); !errors.Is(err, tc.want) {
				t.Fatalf("scope or expiry accepted: %v", err)
			}
		})
	}
	var count int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM budget_run_bindings WHERE approval_request_ref=$1`, request.RequestRef).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected run bound: count=%d err=%v", count, err)
	}
}

func TestBudgetRunBindingStoreFailureDoesNotPublish(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	request := budgetTestRequest(t, now)
	record, err := a.Issue(ctx, request, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.ExecContext(ctx, `CREATE OR REPLACE FUNCTION block_budget_run_test() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'injected_run_bind_failure';
END; $$ LANGUAGE plpgsql;
CREATE TRIGGER trg_block_budget_run_test BEFORE INSERT ON budget_run_bindings
FOR EACH ROW EXECUTE FUNCTION block_budget_run_test();`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS trg_block_budget_run_test ON budget_run_bindings;
DROP FUNCTION IF EXISTS block_budget_run_test();`)
	})
	if bound, err := bindBudgetTestRun(ctx, a, record, "case.1", now.Add(time.Minute), now); !errors.Is(err, ErrBudgetAuthorityUnavailable) || bound.Ledger != nil || bound.Scope.RunRef != "" {
		t.Fatalf("failed store exposed execution: %+v, %v", bound, err)
	}
	var count int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM budget_run_bindings WHERE approval_request_ref=$1`, request.RequestRef).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed binding stored: count=%d err=%v", count, err)
	}
	remaining, err := a.ledger.Remaining(record.Grant.Key, record.Grant.Binding, now)
	if err != nil || remaining != request.MaxRequests {
		t.Fatalf("failed binding reserved budget: %d, %v", remaining, err)
	}
	if _, err := d.ExecContext(ctx, `DROP TRIGGER trg_block_budget_run_test ON budget_run_bindings`); err != nil {
		t.Fatal(err)
	}
	if bound, err := bindBudgetTestRun(ctx, a, record, "case.1", now.Add(time.Minute), now); err != nil || bound.Ledger == nil {
		t.Fatalf("binding after store recovery: %+v, %v", bound, err)
	}
}

func TestBudgetRunBindingLockLossAtCommitStaysUnpublished(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	request := budgetTestRequest(t, now)
	record, err := a.Issue(ctx, request, now)
	if err != nil {
		t.Fatal(err)
	}
	ddl := fmt.Sprintf(`CREATE OR REPLACE FUNCTION drop_budget_lock_after_run_test() RETURNS trigger AS $$
BEGIN
    PERFORM pg_terminate_backend(%d);
    RETURN NEW;
END; $$ LANGUAGE plpgsql;
CREATE TRIGGER trg_drop_budget_lock_after_run_test AFTER INSERT ON budget_run_bindings
FOR EACH ROW EXECUTE FUNCTION drop_budget_lock_after_run_test();`, a.backendID)
	if _, err := d.ExecContext(ctx, ddl); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS trg_drop_budget_lock_after_run_test ON budget_run_bindings;
DROP FUNCTION IF EXISTS drop_budget_lock_after_run_test();`)
	})
	bound, err := bindBudgetTestRun(ctx, a, record, "case.1", now.Add(time.Minute), now)
	if !errors.Is(err, ErrBudgetAuthorityUnavailable) || bound.Ledger != nil || bound.Scope.RunRef != "" {
		t.Fatalf("lost lock published a run: %+v, %v", bound, err)
	}
	var stored int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM budget_run_bindings WHERE approval_request_ref=$1`, request.RequestRef).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("ambiguous committed row not retained: count=%d err=%v", stored, err)
	}
	next := budgetTestAuthority(t, d, now.Add(time.Second))
	if _, err := bindBudgetTestRun(ctx, next, record, "case.2", now.Add(time.Minute), now.Add(time.Second)); !errors.Is(err, ErrBudgetApprovalNotReady) {
		t.Fatalf("old grant reused after lock loss: %v", err)
	}
}

func TestBudgetRunBindingRestartAndImmutableRows(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	request := budgetTestRequest(t, now)
	record, err := a.Issue(ctx, request, now)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := bindBudgetTestRun(ctx, a, record, "case.1", now.Add(time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE budget_run_bindings SET budget_ref='other' WHERE run_ref=$1`,
		`DELETE FROM budget_run_bindings WHERE run_ref=$1`,
	} {
		if _, err := d.ExecContext(ctx, statement, bound.Scope.RunRef); err == nil {
			t.Fatalf("run binding mutated: %s", statement)
		}
	}
	if _, err := d.ExecContext(ctx, `TRUNCATE budget_run_bindings`); err == nil {
		t.Fatal("run bindings truncated")
	}
	if err := a.Close(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	next := budgetTestAuthority(t, d, now.Add(2*time.Second))
	if _, err := bindBudgetTestRun(ctx, next, record, "case.2", now.Add(time.Minute), now.Add(2*time.Second)); !errors.Is(err, ErrBudgetApprovalNotReady) {
		t.Fatalf("old approval bound after restart: %v", err)
	}
	var storedEpoch string
	if err := d.QueryRowContext(ctx, `SELECT epoch FROM budget_run_bindings WHERE run_ref=$1`, bound.Scope.RunRef).Scan(&storedEpoch); err != nil || storedEpoch != record.Epoch {
		t.Fatalf("run provenance lost on restart: epoch=%s err=%v", storedEpoch, err)
	}
}
