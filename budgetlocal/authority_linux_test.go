//go:build linux

package budgetlocal

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/transmit/budget"
	"github.com/jackc/pgx/v5"
)

// Require two explicitly supplied, distinct test databases. Never fall back to
// the product configuration or create an external approval service.
func isolatedAuthority(t *testing.T) (*db.DB, *db.BudgetAuthority) {
	t.Helper()
	general, dedicated := os.Getenv("ARTEX_PG_DSN"), os.Getenv("ARTEX_TASK_MODE_TEST_DSN")
	if dedicated == "" {
		t.Skip("set a dedicated ARTEX_TASK_MODE_TEST_DSN")
	}
	a, errA := pgx.ParseConfig(general)
	b, errB := pgx.ParseConfig(dedicated)
	if general == "" || errA != nil || errB != nil || a.Database == b.Database {
		t.Fatal("explicit distinct general and task test databases required")
	}
	d, err := db.Open(dedicated)
	if err != nil {
		t.Fatal("isolated PostgreSQL initialization failed")
	}
	t.Cleanup(func() { _ = d.Close() })
	authority, err := db.StartBudgetAuthority(context.Background(), d, budget.Limits{MaxApprovals: 10, MaxRequestsPerApproval: 10}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = authority.Close(ctx, time.Now())
	})
	return d, authority
}

func TestCommittedApprovalWithLostReplyRetriesSameReference(t *testing.T) {
	path := socketPath(t)
	d, authority := isolatedAuthority(t)
	var first atomic.Bool
	first.Store(true)
	callbackDone := make(chan struct{})
	callback := func(ctx context.Context, _ uint32, request db.BudgetApprovalRequest, now time.Time) (db.BudgetApprovalRecord, error) {
		record, err := authority.Issue(ctx, request, now)
		if err == nil && first.CompareAndSwap(true, false) {
			<-ctx.Done()
			close(callbackDone)
			return record, ctx.Err()
		}
		return record, err
	}
	s, err := listenWithTimeout(context.Background(), path, uint32(os.Geteuid()), uint32(os.Geteuid()+1), callback, true, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	request := testRequest()
	request.RequestRef = "committed.lost.reply"
	response, err := submit(context.Background(), path, uint32(os.Geteuid()), request, true)
	if !errors.Is(err, ErrUnknown) || response != (Response{}) {
		t.Fatalf("ambiguous committed result: %+v %v", response, err)
	}
	select {
	case <-callbackDone:
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not observe deadline")
	}
	var state, budgetRef, epoch string
	if err := d.QueryRow(`SELECT state,budget_ref,epoch FROM budget_approvals WHERE request_ref=$1`, request.RequestRef).Scan(&state, &budgetRef, &epoch); err != nil || state != "ready" {
		t.Fatalf("issuance not durably ready: %s %v", state, err)
	}
	retry, err := submit(context.Background(), path, uint32(os.Geteuid()), request, true)
	if err != nil || retry.BudgetRef != budgetRef || retry.Epoch != epoch {
		t.Fatalf("same reference changed grant: %+v %v", retry, err)
	}
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM budget_approvals WHERE request_ref=$1`, request.RequestRef).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate issuance: %d %v", count, err)
	}
}

func TestApprovalTimeoutDoesNotLatchAuthorityClosed(t *testing.T) {
	path := socketPath(t)
	d, authority := isolatedAuthority(t)
	priorRequest := testRequest()
	priorRequest.RequestRef = "prior.ready"
	prior, err := authority.Issue(context.Background(), db.BudgetApprovalRequest{
		RequestRef: priorRequest.RequestRef, IssuerRef: "operator.prior", TaskRef: priorRequest.TaskRef,
		Binding: budget.Binding{PolicyRef: priorRequest.PolicyRef, ResetRef: priorRequest.ResetRef}, MaxRequests: priorRequest.MaxRequests,
		ValidFrom: priorRequest.ValidFrom, ValidUntil: priorRequest.ValidUntil,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	// Ensure the blocker is released before server/authority cleanup on failure.
	if _, err := tx.Exec(`LOCK TABLE budget_authority_current IN ACCESS EXCLUSIVE MODE`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	entered := make(chan context.Context, 1)
	done := make(chan struct{}, 1)
	callback := func(ctx context.Context, _ uint32, request db.BudgetApprovalRequest, now time.Time) (db.BudgetApprovalRecord, error) {
		entered <- ctx
		record, err := authority.Issue(ctx, request, now)
		done <- struct{}{}
		return record, err
	}
	s, err := listenWithTimeout(context.Background(), path, uint32(os.Geteuid()), uint32(os.Geteuid()+1), callback, true, time.Second)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	defer tx.Rollback()
	request := testRequest()
	request.RequestRef = "probe.timeout"
	result := make(chan error, 1)
	go func() {
		response, err := submit(context.Background(), path, uint32(os.Geteuid()), request, true)
		if response != (Response{}) {
			result <- errors.New("unexpected metadata")
			return
		}
		result <- err
	}()
	var callbackCtx context.Context
	select {
	case callbackCtx = <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("callback not entered")
	}
	// Observe the actual blocked authority probe, rather than relying on a sleep.
	probeCtx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err = d.QueryRowContext(probeCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT epoch,closed FROM budget_authority_current%')`).Scan(&waiting)
		if err != nil {
			t.Fatal("authority probe did not reach table blocker")
		}
		if waiting {
			break
		}
		select {
		case <-ticker.C:
		case <-probeCtx.Done():
			t.Fatal("authority probe not blocked")
		}
	}
	select {
	case <-callbackCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("callback deadline not enforced")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("authority did not finish timed out probe")
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrUnknown) {
			t.Fatalf("timeout reported as rejection: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client remained blocked")
	}
	registration, err := authority.Resolve(context.Background(), prior.Grant.Key, prior.Grant.Binding)
	if err != nil || registration.Ledger == nil {
		t.Fatalf("timeout latched authority closed: %v", err)
	}
	request.RequestRef = "after.probe.timeout"
	response, err := submit(context.Background(), path, uint32(os.Geteuid()), request, true)
	if err != nil || response.State != "ready" {
		t.Fatalf("next approval failed: %+v %v", response, err)
	}
}
