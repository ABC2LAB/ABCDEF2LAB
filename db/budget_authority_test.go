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

func budgetTestRequest(t *testing.T, now time.Time) BudgetApprovalRequest {
	t.Helper()
	ref, err := newBudgetRef()
	if err != nil {
		t.Fatal(err)
	}
	return BudgetApprovalRequest{
		RequestRef:  "req." + ref,
		IssuerRef:   "operator.1",
		TaskRef:     "task.1",
		Binding:     budget.Binding{PolicyRef: "policy.1", ResetRef: "reset.1"},
		MaxRequests: 2,
		ValidFrom:   now.Add(-time.Minute),
		ValidUntil:  now.Add(time.Hour),
	}
}

func budgetTestAuthority(t *testing.T, d *DB, now time.Time) *BudgetAuthority {
	t.Helper()
	a, err := StartBudgetAuthority(context.Background(), d, budget.Limits{
		MaxApprovals: 10, MaxRequestsPerApproval: 10,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background(), time.Now()) })
	return a
}

func TestBudgetAuthorityConcurrentIdempotenceAndReservations(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	request := budgetTestRequest(t, now)

	const callers = 16
	records := make([]BudgetApprovalRecord, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range records {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			records[i], errs[i] = a.Issue(ctx, request, now)
		}(i)
	}
	wg.Wait()
	first := records[0]
	for i, err := range errs {
		if err != nil || records[i].Grant != first.Grant || records[i].Epoch != first.Epoch || records[i].State != "ready" {
			t.Fatalf("caller %d: record=%+v err=%v", i, records[i], err)
		}
	}
	var approvalCount, eventCount int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM budget_approvals WHERE request_ref=$1`, request.RequestRef).Scan(&approvalCount); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM budget_approval_events WHERE request_ref=$1`, request.RequestRef).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if approvalCount != 1 || eventCount != 2 {
		t.Fatalf("duplicate issuance: approvals=%d events=%d", approvalCount, eventCount)
	}
	registration, err := a.Resolve(ctx, first.Grant.Key, first.Grant.Binding)
	if err != nil || registration.Ledger == nil || registration.Epoch != first.Epoch {
		t.Fatalf("resolve: %+v, %v", registration, err)
	}
	attempt := budget.Attempt{RunRef: "run.1", Sequence: 1}
	if err := registration.Ledger.Reserve(first.Grant.Key, first.Grant.Binding, attempt, now); err != nil {
		t.Fatal(err)
	}
	again, err := a.Issue(ctx, request, now)
	if err != nil || again.Grant.Key != first.Grant.Key {
		t.Fatalf("retry changed approval: %+v, %v", again, err)
	}
	if err := registration.Ledger.Reserve(first.Grant.Key, first.Grant.Binding, attempt, now); !errors.Is(err, budget.ErrDuplicateAttempt) {
		t.Fatalf("retry reset reservation: %v", err)
	}
	remaining, err := registration.Ledger.Remaining(first.Grant.Key, first.Grant.Binding, now)
	if err != nil || remaining != 1 {
		t.Fatalf("retry changed remaining: %d, %v", remaining, err)
	}
	request.MaxRequests++
	if _, err := a.Issue(ctx, request, now); !errors.Is(err, ErrBudgetApprovalConflict) {
		t.Fatalf("changed request accepted: %v", err)
	}
	request.MaxRequests--
	request.IssuerRef = "operator.2"
	if _, err := a.Issue(ctx, request, now); !errors.Is(err, ErrBudgetApprovalConflict) {
		t.Fatalf("changed issuer accepted: %v", err)
	}
}

func TestBudgetAuthorityRestartAndExclusiveEpoch(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	request := budgetTestRequest(t, now)
	first, err := a.Issue(ctx, request, now)
	if err != nil {
		t.Fatal(err)
	}
	limits := budget.Limits{MaxApprovals: 10, MaxRequestsPerApproval: 10}
	if other, err := StartBudgetAuthority(ctx, d, limits, now); !errors.Is(err, ErrBudgetAuthorityUnavailable) || other != nil {
		t.Fatalf("overlapping authority started: %v", err)
	}
	if err := a.Close(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	next, err := StartBudgetAuthority(ctx, d, limits, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close(ctx, time.Now())
	if next.Epoch() == first.Epoch {
		t.Fatal("restart reused epoch")
	}
	if _, err := a.Resolve(ctx, first.Grant.Key, first.Grant.Binding); !errors.Is(err, ErrBudgetAuthorityUnavailable) {
		t.Fatalf("old authority still resolves: %v", err)
	}
	if _, err := next.Resolve(ctx, first.Grant.Key, first.Grant.Binding); !errors.Is(err, ErrBudgetApprovalNotReady) {
		t.Fatalf("restart restored old grant: %v", err)
	}
	var oldState string
	if err := d.QueryRowContext(ctx, `SELECT state FROM budget_approvals WHERE request_ref=$1`, request.RequestRef).Scan(&oldState); err != nil || oldState != "closed" {
		t.Fatalf("restart did not close old ready grant: state=%s err=%v", oldState, err)
	}
	if _, err := next.Issue(ctx, request, now.Add(2*time.Second)); !errors.Is(err, ErrBudgetApprovalStale) {
		t.Fatalf("old request ref reissued: %v", err)
	}
	explicit := budgetTestRequest(t, now.Add(2*time.Second))
	second, err := next.Issue(ctx, explicit, now.Add(2*time.Second))
	if err != nil || second.Grant.Key.BudgetRef == first.Grant.Key.BudgetRef {
		t.Fatalf("explicit restart approval: %+v, %v", second, err)
	}
}

func TestBudgetAuthorityLostLockAndCancelledClose(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	request := budgetTestRequest(t, now)
	record, err := a.Issue(ctx, request, now)
	if err != nil {
		t.Fatal(err)
	}
	var unlocked bool
	if err := a.conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock($1)`, budgetAuthorityLockKey).Scan(&unlocked); err != nil || !unlocked {
		t.Fatalf("test lock release: %v, %v", unlocked, err)
	}
	if _, err := a.Resolve(ctx, record.Grant.Key, record.Grant.Binding); !errors.Is(err, ErrBudgetAuthorityUnavailable) {
		t.Fatalf("lost advisory lock still resolves: %v", err)
	}
	if err := a.Close(ctx, now.Add(time.Second)); !errors.Is(err, ErrBudgetAuthorityUnavailable) {
		t.Fatalf("lost lock close outcome: %v", err)
	}
	next := budgetTestAuthority(t, d, now.Add(2*time.Second))
	if next.Epoch() == record.Epoch {
		t.Fatal("lost lock reused old epoch")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := next.Close(canceled, now.Add(3*time.Second)); !errors.Is(err, ErrBudgetAuthorityUnavailable) {
		t.Fatalf("cancelled close outcome: %v", err)
	}
	last := budgetTestAuthority(t, d, now.Add(4*time.Second))
	if last.Epoch() == next.Epoch() {
		t.Fatal("cancelled close retained authority lock")
	}
}

func TestBudgetAuthorityStartupStoreFailureReleasesLock(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err := d.ExecContext(ctx, `CREATE OR REPLACE FUNCTION block_budget_epoch_test() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'injected_epoch_failure';
END; $$ LANGUAGE plpgsql;
CREATE TRIGGER trg_block_budget_epoch_test BEFORE INSERT ON budget_authority_epochs
FOR EACH ROW EXECUTE FUNCTION block_budget_epoch_test();`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS trg_block_budget_epoch_test ON budget_authority_epochs;
DROP FUNCTION IF EXISTS block_budget_epoch_test();`)
	})
	limits := budget.Limits{MaxApprovals: 10, MaxRequestsPerApproval: 10}
	if failed, err := StartBudgetAuthority(ctx, d, limits, now); !errors.Is(err, ErrBudgetAuthorityUnavailable) || failed != nil {
		t.Fatalf("startup store failure escaped: %v", err)
	}
	if _, err := d.ExecContext(ctx, `DROP TRIGGER trg_block_budget_epoch_test ON budget_authority_epochs`); err != nil {
		t.Fatal(err)
	}
	authority := budgetTestAuthority(t, d, now.Add(time.Second))
	if authority.Epoch() == "" {
		t.Fatal("startup retry did not acquire authority")
	}
}

func TestBudgetAuthorityRejectsURLRefsBeforePrepare(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	base := budgetTestRequest(t, now)
	for _, mutate := range []func(*BudgetApprovalRequest){
		func(r *BudgetApprovalRequest) { r.RequestRef = "http://example" },
		func(r *BudgetApprovalRequest) { r.IssuerRef = "http://example" },
		func(r *BudgetApprovalRequest) { r.TaskRef = "http://example" },
		func(r *BudgetApprovalRequest) { r.Binding.PolicyRef = "http://example" },
	} {
		request := base
		mutate(&request)
		if _, err := a.Issue(ctx, request, now); !errors.Is(err, ErrBudgetApprovalInvalid) {
			t.Fatalf("URL ref accepted: %+v, %v", request, err)
		}
	}
	var count int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM budget_approvals WHERE request_ref=$1`, base.RequestRef).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid ref prepared: count=%d err=%v", count, err)
	}
}

func TestBudgetAuthorityReadyStoreFailureStaysUnpublished(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	request := budgetTestRequest(t, now)
	_, err := d.ExecContext(ctx, `CREATE OR REPLACE FUNCTION block_budget_ready_test() RETURNS trigger AS $$
BEGIN
    IF NEW.state = 'ready' THEN
        RAISE EXCEPTION 'injected_ready_failure';
    END IF;
    RETURN NEW;
END; $$ LANGUAGE plpgsql;
CREATE TRIGGER trg_block_budget_ready_test BEFORE UPDATE OF state ON budget_approvals
FOR EACH ROW EXECUTE FUNCTION block_budget_ready_test();`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS trg_block_budget_ready_test ON budget_approvals;
DROP FUNCTION IF EXISTS block_budget_ready_test();`)
	})
	if _, err := a.Issue(ctx, request, now); !errors.Is(err, ErrBudgetAuthorityUnavailable) {
		t.Fatalf("ready store failure escaped: %v", err)
	}
	var budgetRef, state string
	if err := d.QueryRowContext(ctx, `SELECT budget_ref,state FROM budget_approvals WHERE request_ref=$1`, request.RequestRef).Scan(&budgetRef, &state); err != nil {
		t.Fatal(err)
	}
	if state != "prepared" {
		t.Fatalf("failed ready transition published state %s", state)
	}
	if _, err := a.Resolve(ctx, budget.Key{TaskRef: request.TaskRef, BudgetRef: budgetRef}, request.Binding); !errors.Is(err, ErrBudgetApprovalNotReady) {
		t.Fatalf("prepared grant resolved: %v", err)
	}
	if _, err := d.ExecContext(ctx, `DROP TRIGGER trg_block_budget_ready_test ON budget_approvals`); err != nil {
		t.Fatal(err)
	}
	record, err := a.Issue(ctx, request, now)
	if err != nil || record.Grant.Key.BudgetRef != budgetRef || record.State != "ready" {
		t.Fatalf("same-epoch retry: %+v, %v", record, err)
	}
	var events int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM budget_approval_events WHERE request_ref=$1`, request.RequestRef).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("retry duplicated state history: %d", events)
	}
}

func TestBudgetAuthorityPrepareStoreFailureHasNoGrant(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	request := budgetTestRequest(t, now)
	_, err := d.ExecContext(ctx, `CREATE OR REPLACE FUNCTION block_budget_prepare_test() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'injected_prepare_failure';
END; $$ LANGUAGE plpgsql;
CREATE TRIGGER trg_block_budget_prepare_test BEFORE INSERT ON budget_approvals
FOR EACH ROW EXECUTE FUNCTION block_budget_prepare_test();`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS trg_block_budget_prepare_test ON budget_approvals;
DROP FUNCTION IF EXISTS block_budget_prepare_test();`)
	})
	if _, err := a.Issue(ctx, request, now); !errors.Is(err, ErrBudgetAuthorityUnavailable) {
		t.Fatalf("prepare store failure escaped: %v", err)
	}
	var count int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM budget_approvals WHERE request_ref=$1`, request.RequestRef).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed preparation stored a grant")
	}
	if _, err := d.ExecContext(ctx, `DROP TRIGGER trg_block_budget_prepare_test ON budget_approvals`); err != nil {
		t.Fatal(err)
	}
	if record, err := a.Issue(ctx, request, now); err != nil || record.State != "ready" {
		t.Fatalf("same request retry after failed preparation: %+v, %v", record, err)
	}
}

func TestBudgetApprovalImmutableGrantAndHistory(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	request := budgetTestRequest(t, now)
	if _, err := a.Issue(ctx, request, now); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE budget_approvals SET max_requests=max_requests+1 WHERE request_ref=$1`,
		`UPDATE budget_approvals SET ready_at=ready_at+interval '1 second' WHERE request_ref=$1`,
		`DELETE FROM budget_approvals WHERE request_ref=$1`,
		`DELETE FROM budget_approval_events WHERE request_ref=$1`,
	} {
		if _, err := d.ExecContext(ctx, statement, request.RequestRef); err == nil {
			t.Fatal(fmt.Sprintf("mutable authority record: %s", statement))
		}
	}
	for _, statement := range []string{
		`TRUNCATE budget_approval_events`,
		`TRUNCATE budget_approvals CASCADE`,
	} {
		if _, err := d.ExecContext(ctx, statement); err == nil {
			t.Fatal(fmt.Sprintf("truncated authority record: %s", statement))
		}
	}
}
