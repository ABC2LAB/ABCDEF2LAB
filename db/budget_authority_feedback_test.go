package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit/budget"
)

func TestBudgetAuthorityStartsWhileSuiteLockIsHeld(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	holder, err := d.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var held bool
	if err := holder.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(7337741002)`).Scan(&held); err != nil || !held {
		_ = holder.Close()
		t.Fatalf("acquire suite fixture lock: held=%t err=%v", held, err)
	}
	t.Cleanup(func() {
		_, _ = holder.ExecContext(context.Background(), `SELECT pg_advisory_unlock(7337741002)`)
		_ = holder.Close()
	})
	a := budgetTestAuthority(t, d, time.Now().UTC())
	if a.Epoch() == "" {
		t.Fatal("suite lock prevented authority startup")
	}
}

func TestBudgetAuthorityCrashRestartClosesPreviousReady(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	first, err := a.Issue(ctx, budgetTestRequest(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	var terminated bool
	// Do not call Close: startup must close the abandoned epoch itself.
	if err := d.QueryRowContext(ctx, `SELECT pg_terminate_backend($1)`, a.backendID).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate authority backend: terminated=%t err=%v", terminated, err)
	}
	// pg_terminate_backend acknowledges the signal, not backend exit. Wait for
	// its session (and advisory lock) to disappear before exercising restart.
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var present bool
		if err := d.QueryRowContext(waitCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1)`, a.backendID).Scan(&present); err != nil {
			t.Fatal("authority backend termination could not be confirmed")
		}
		if !present {
			break
		}
		select {
		case <-ticker.C:
		case <-waitCtx.Done():
			t.Fatal("authority backend termination timed out")
		}
	}
	next := budgetTestAuthority(t, d, now.Add(time.Second))
	if next.Epoch() == first.Epoch {
		t.Fatal("crash restart reused epoch")
	}
	var state string
	var closedEvents int
	var epochClosed bool
	if err := d.QueryRowContext(ctx, `SELECT state FROM budget_approvals WHERE request_ref=$1`, first.RequestRef).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM budget_approval_events WHERE request_ref=$1 AND state='closed'`, first.RequestRef).Scan(&closedEvents); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRowContext(ctx, `SELECT closed_at IS NOT NULL FROM budget_authority_epochs WHERE epoch=$1`, first.Epoch).Scan(&epochClosed); err != nil {
		t.Fatal(err)
	}
	if state != "closed" || closedEvents != 1 || !epochClosed {
		t.Fatalf("crash epoch cleanup: state=%s closed_events=%d epoch_closed=%t", state, closedEvents, epochClosed)
	}
	got, err := next.Resolve(ctx, first.Grant.Key, first.Grant.Binding)
	if !errors.Is(err, ErrBudgetApprovalNotReady) || got.Ledger != nil {
		t.Fatalf("old grant restored: %+v err=%v", got, err)
	}
}

func TestBudgetAuthorityIdempotencyRejectsChangedScopeAndTimes(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	request := budgetTestRequest(t, now)
	first, err := a.Issue(ctx, request, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*BudgetApprovalRequest)
	}{
		{"task", func(r *BudgetApprovalRequest) { r.TaskRef = "task.other" }},
		{"policy", func(r *BudgetApprovalRequest) { r.Binding.PolicyRef = "policy.other" }},
		{"reset", func(r *BudgetApprovalRequest) { r.Binding.ResetRef = "reset.other" }},
		{"valid_from", func(r *BudgetApprovalRequest) { r.ValidFrom = r.ValidFrom.Add(time.Second) }},
		{"valid_until", func(r *BudgetApprovalRequest) { r.ValidUntil = r.ValidUntil.Add(time.Second) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := request
			tc.change(&changed)
			got, err := a.Issue(ctx, changed, now)
			if !errors.Is(err, ErrBudgetApprovalConflict) || got != (BudgetApprovalRecord{}) {
				t.Fatalf("changed idempotent request accepted: %+v err=%v", got, err)
			}
		})
	}
	again, err := a.Issue(ctx, request, now)
	if err != nil || again.Grant != first.Grant {
		t.Fatalf("original retry changed: %+v err=%v", again, err)
	}
}

func TestBudgetAuthorityResolveRechecksDurableReady(t *testing.T) {
	for _, scenario := range []string{"closed", "binding", "read_error"} {
		t.Run(scenario, func(t *testing.T) {
			d := taskModeTestDB(t)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Microsecond)
			a := budgetTestAuthority(t, d, now)
			record, err := a.Issue(ctx, budgetTestRequest(t, now), now)
			if err != nil {
				t.Fatal(err)
			}
			binding := record.Grant.Binding
			want := ErrBudgetApprovalNotReady
			switch scenario {
			case "closed":
				if _, err := d.ExecContext(ctx, `UPDATE budget_approvals SET state='closed' WHERE request_ref=$1`, record.RequestRef); err != nil {
					t.Fatal(err)
				}
			case "binding":
				binding.PolicyRef = "policy.other"
			case "read_error":
				if _, err := d.ExecContext(ctx, `ALTER TABLE budget_approvals RENAME TO budget_approvals_feedback_hidden`); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if _, err := d.ExecContext(context.Background(), `ALTER TABLE budget_approvals_feedback_hidden RENAME TO budget_approvals`); err != nil {
						t.Error(err)
					}
				})
				want = ErrBudgetAuthorityUnavailable
			}
			got, err := a.Resolve(ctx, record.Grant.Key, binding)
			if !errors.Is(err, want) || got.Ledger != nil || got.Grant != (budget.Grant{}) {
				t.Fatalf("durable guard: registration=%+v err=%v want=%v", got, err, want)
			}
		})
	}
}

func TestBudgetAuthorityCancelledCallerDoesNotLatchAuthority(t *testing.T) {
	d := taskModeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := budgetTestAuthority(t, d, now)
	record, err := a.Issue(ctx, budgetTestRequest(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	got, err := a.Resolve(canceled, record.Grant.Key, record.Grant.Binding)
	if !errors.Is(err, ErrBudgetAuthorityUnavailable) || got.Ledger != nil || a.closed {
		t.Fatalf("caller cancellation latched authority: registration=%+v closed=%t err=%v", got, a.closed, err)
	}
	got, err = a.Resolve(ctx, record.Grant.Key, record.Grant.Binding)
	if err != nil || got.Ledger != a.ledger {
		t.Fatalf("healthy retry failed: %+v err=%v", got, err)
	}
	request := budgetTestRequest(t, now)
	if got, err := a.Issue(canceled, request, now); !errors.Is(err, ErrBudgetAuthorityUnavailable) || got != (BudgetApprovalRecord{}) || a.closed {
		t.Fatalf("canceled issue latched authority: record=%+v closed=%t err=%v", got, a.closed, err)
	}
	if _, err := a.Issue(ctx, request, now); err != nil {
		t.Fatalf("next issue after cancellation: %v", err)
	}
}
