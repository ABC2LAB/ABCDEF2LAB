package guard

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

func TestAuditHasOneFinalDecisionOnConfigurationFailure(t *testing.T) {
	connection, err := sql.Open("pgx", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	g := NewWithInterceptor(intercept.New(&db.DB{DB: connection}))
	blocked, _, _ := g.Hooks().PreToolUse(t.Context(), "Bash", []byte(`{"command":"synthetic"}`))
	entries := g.Audit()
	if !blocked || len(entries) != 1 || entries[0].Action != "block" {
		t.Fatalf("configuration failure: blocked=%v audit=%+v", blocked, entries)
	}
}

func TestAuditRecordsAllowedCallOnce(t *testing.T) {
	for _, tool := range []string{"Bash", "shell_open", "shell_send", "WebFetch"} {
		t.Run(tool, func(t *testing.T) {
			g := New()
			blocked, _, _ := g.Hooks().PreToolUse(t.Context(), tool, []byte(`{"command":"synthetic","text":"synthetic"}`))
			entries := g.Audit()
			if blocked || len(entries) != 1 || entries[0].Action != "allow" || entries[0].Tool != tool {
				t.Fatalf("allowed call: blocked=%v audit=%+v", blocked, entries)
			}
		})
	}
}

const auditFixtureTool = "GuardAuditFixture"

func auditDecisionDatabase(t *testing.T) *db.DB {
	t.Helper()
	dsn := os.Getenv("ARTEX_INTERCEPT_TEST_DSN")
	if dsn == "" {
		t.Skip("set ARTEX_INTERCEPT_TEST_DSN to a dedicated PostgreSQL test database")
	}
	locker, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("dedicated test lock unavailable")
	}
	t.Cleanup(func() { _ = locker.Close() })
	lease, err := locker.Conn(t.Context())
	if err != nil {
		t.Fatal("dedicated test lock connection unavailable")
	}
	t.Cleanup(func() { _ = lease.Close() })
	if _, err := lease.ExecContext(t.Context(), `SELECT pg_advisory_lock(7337741002)`); err != nil {
		t.Fatal("dedicated test lock unavailable")
	}
	t.Cleanup(func() { _, _ = lease.ExecContext(context.Background(), `SELECT pg_advisory_unlock(7337741002)`) })
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal("dedicated guard audit database unavailable")
	}
	t.Cleanup(func() { _ = d.Close() })
	previous, existed, err := d.GetSetting("intercept_enabled_tools")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = d.SetSetting("intercept_enabled_tools", previous)
		} else {
			_, _ = d.Exec(`DELETE FROM settings WHERE key='intercept_enabled_tools'`)
		}
	})
	if err := intercept.New(d).SetEnabledTools([]string{auditFixtureTool}); err != nil {
		t.Fatal(err)
	}
	return d
}

func auditRule(t *testing.T, d *db.DB, action string) {
	t.Helper()
	rule, err := d.CreateInterceptRule("guard-audit-fixture", "tool_name", "string", auditFixtureTool, action, "synthetic decision", 10000, true, false, 0, "deny")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM intercept_pending WHERE rule_id=$1`, rule.ID)
		_, _ = d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, rule.ID)
	})
}

func TestAuditRecordsRuleOutcomeOnce(t *testing.T) {
	d := auditDecisionDatabase(t)
	for _, action := range []string{"allow", "deny"} {
		t.Run(action, func(t *testing.T) {
			auditRule(t, d, action)
			g := NewWithInterceptor(intercept.New(d))
			blocked, _, _ := g.Hooks().PreToolUse(t.Context(), auditFixtureTool, []byte(`{}`))
			wantAction := "allow"
			if action == "deny" {
				wantAction = "block"
			}
			entries := g.Audit()
			if blocked != (action == "deny") || len(entries) != 1 || entries[0].Action != wantAction {
				t.Fatalf("rule %s: blocked=%v audit=%+v", action, blocked, entries)
			}
		})
	}
}

func TestAuditWaitsForHumanDecision(t *testing.T) {
	d := auditDecisionDatabase(t)
	for _, outcome := range []string{"allow", "deny", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			auditRule(t, d, "ask")
			ic := intercept.New(d)
			g := NewWithInterceptor(ic)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			pending := make(chan int64, 1)
			ctx = intercept.WithTaskContext(ctx, "", "guard-fixture", func(activity db.Activity) {
				var detail struct {
					PendingID int64 `json:"pending_id"`
				}
				if err := json.Unmarshal([]byte(activity.Detail), &detail); err == nil {
					pending <- detail.PendingID
				}
			})
			done := make(chan bool, 1)
			go func() {
				blocked, _, _ := g.Hooks().PreToolUse(ctx, auditFixtureTool, []byte(`{}`))
				done <- blocked
			}()
			var pendingID int64
			select {
			case pendingID = <-pending:
			case <-ctx.Done():
				t.Fatal("approval did not become pending")
			}
			// Observing the approval request must not expose a premature allow.
			if entries := g.Audit(); len(entries) != 0 {
				t.Errorf("approval still pending but audit already has a decision: %+v", entries)
			}
			if outcome == "cancel" {
				cancel()
			} else if err := ic.Decide(pendingID, outcome == "allow"); err != nil {
				t.Fatal(err)
			}
			select {
			case blocked := <-done:
				wantAction := "block"
				if outcome == "allow" {
					wantAction = "allow"
				}
				entries := g.Audit()
				if blocked != (outcome != "allow") || len(entries) != 1 || entries[0].Action != wantAction {
					t.Fatalf("human %s: blocked=%v audit=%+v", outcome, blocked, entries)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("approval hook did not finish")
			}
		})
	}
}
