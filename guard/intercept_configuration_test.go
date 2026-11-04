package guard

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

func TestInterceptLoadFailureBlocksWithoutJudge(t *testing.T) {
	connection, err := sql.Open("pgx", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	ic := intercept.New(&db.DB{DB: connection})
	called := false
	ic.SetReviewer(func(context.Context, int64, string, intercept.ReviewInput) (intercept.Decision, error) {
		called = true
		return intercept.Decision{Action: "allow"}, nil
	})
	g := NewWithInterceptor(ic)
	blocked, _, _ := g.Hooks().PreToolUse(t.Context(), "Bash", []byte(`{"command":"synthetic"}`))
	if !blocked || called {
		t.Fatalf("configuration failure bypassed guard: blocked=%v reviewer_called=%v", blocked, called)
	}
	entries := g.Audit()
	if len(entries) == 0 || entries[len(entries)-1].Action != "block" || !strings.Contains(entries[len(entries)-1].Reason, "설정") {
		t.Fatal("configuration failure did not leave a block reason")
	}
}

func TestStoredInvalidRegexBlocksBeforeAllowOrJudge(t *testing.T) {
	dsn := os.Getenv("ARTEX_INTERCEPT_TEST_DSN")
	if dsn == "" {
		t.Skip("set ARTEX_INTERCEPT_TEST_DSN to a dedicated PostgreSQL test database")
	}
	// Use the same pinned suite lock as db/server tests before schema startup.
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
		t.Fatal("dedicated intercept test database unavailable")
	}
	t.Cleanup(func() { _ = d.Close() })
	create := func(name, matchType, pattern, action string, priority int) db.InterceptRule {
		t.Helper()
		r, err := d.CreateInterceptRule(name, "tool_name", matchType, pattern, action, "", priority, true, false, 0, "deny")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, r.ID) })
		return r
	}
	create("regex-fixture-high-allow", "string", "Bash", "allow", 10000)
	low := create("regex-fixture-low-deny", "regex", "^Bash$", "deny", -10000)
	ic := intercept.New(d)
	for _, key := range []string{"intercept_enabled_tools", "llm_judge_enabled"} {
		previous, existed, err := d.GetSetting(key)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if existed {
				_ = d.SetSetting(key, previous)
			} else {
				_, _ = d.Exec(`DELETE FROM settings WHERE key=$1`, key)
			}
		})
	}
	if err := ic.SetEnabledTools([]string{"Bash"}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetBool("llm_judge_enabled", true); err != nil {
		t.Fatal(err)
	}
	called := false
	ic.SetReviewer(func(context.Context, int64, string, intercept.ReviewInput) (intercept.Decision, error) {
		called = true
		return intercept.Decision{Action: "allow"}, nil
	})
	g := NewWithInterceptor(ic)
	input := []byte(`{"command":"synthetic"}`)
	ctx := intercept.WithReviewContext(t.Context(), "/tmp/intercept-fixture", intercept.ReviewBackground{Source: intercept.BackgroundUserMessage, Text: "synthetic fixture check"})
	if decision, judged := ic.Judge(ctx, "Bash", input); !judged || decision.Action != "allow" || !called {
		t.Fatal("positive control did not reach the fallback reviewer")
	}
	called = false
	if blocked, _, _ := g.Hooks().PreToolUse(ctx, "Bash", input); blocked {
		t.Fatal("valid higher-priority allow was not preserved")
	}
	const privatePattern = "(?P<synthetic-private-regex"
	if _, err := d.Exec(`UPDATE intercept_rules SET pattern=$2 WHERE id=$1`, low.ID, privatePattern); err != nil {
		t.Fatal(err)
	}
	ic.Invalidate()
	if blocked, _, _ := g.Hooks().PreToolUse(ctx, "Bash", input); !blocked || called {
		t.Fatal("stored invalid regex fell through to allow or judge")
	}
	for _, entry := range g.Audit() {
		if strings.Contains(entry.Reason, privatePattern) {
			t.Fatal("configuration denial exposed the invalid pattern")
		}
	}
	if err := d.ToggleInterceptRule(low.ID, false); err != nil {
		t.Fatal(err)
	}
	ic.Invalidate()
	if blocked, _, _ := g.Hooks().PreToolUse(ctx, "Bash", input); blocked {
		t.Fatal("disabling the invalid row did not recover valid rule evaluation")
	}
	if blocked, _, _ := g.Hooks().PreToolUse(ctx, "UnconfiguredFixtureTool", input); blocked || called {
		t.Fatal("valid configuration changed the enabled-tools boundary")
	}
}
