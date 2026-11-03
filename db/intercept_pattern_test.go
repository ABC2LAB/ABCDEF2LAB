package db

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestInterceptPatternValidation(t *testing.T) {
	const privatePattern = "(?P<synthetic-private-pattern"
	if err := ValidateInterceptPattern("regex", privatePattern); !errors.Is(err, ErrInvalidInterceptPattern) || strings.Contains(err.Error(), privatePattern) {
		t.Fatal("invalid regex was accepted or exposed its source")
	}
	for _, tc := range []struct{ kind, pattern string }{{"regex", "^Bash$"}, {"string", privatePattern}} {
		if err := ValidateInterceptPattern(tc.kind, tc.pattern); err != nil {
			t.Fatal("valid regex or literal string was rejected")
		}
	}
	var unavailable *DB
	if _, err := unavailable.CreateInterceptRule("fixture", "tool_name", "regex", privatePattern, "deny", "", 0, true, false, 0, "deny"); !errors.Is(err, ErrInvalidInterceptPattern) {
		t.Fatal("create reached storage before rejecting the invalid pattern")
	}
	if _, err := unavailable.UpdateInterceptRule(1, "fixture", "tool_name", "regex", privatePattern, "deny", "", 0, false, false, 0, "deny"); !errors.Is(err, ErrInvalidInterceptPattern) {
		t.Fatal("update reached storage before rejecting the invalid pattern")
	}
}

func TestInterceptPatternWritesRejectInvalidRegex(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	const invalid = "(?P<synthetic-private-pattern"
	for _, enabled := range []bool{false, true} {
		if _, err := d.CreateInterceptRule(t.Name(), "tool_name", "regex", invalid, "deny", "", 10, enabled, false, 0, "deny"); !errors.Is(err, ErrInvalidInterceptPattern) {
			t.Fatal("invalid pattern was stored")
		}
	}
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM intercept_rules WHERE name=$1`, t.Name()).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected create changed stored rows")
	}
	rule, err := d.CreateInterceptRule(t.Name(), "tool_name", "regex", "^Bash$", "deny", "", 10, false, false, 0, "deny")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, rule.ID) })
	if _, err := d.UpdateInterceptRule(rule.ID, "changed", "tool_name", "regex", invalid, "allow", "", 99, true, false, 0, "deny"); !errors.Is(err, ErrInvalidInterceptPattern) {
		t.Fatal("invalid update was accepted")
	}
	var name, pattern string
	var enabled bool
	if err := d.QueryRow(`SELECT name, pattern, enabled FROM intercept_rules WHERE id=$1`, rule.ID).Scan(&name, &pattern, &enabled); err != nil || name != rule.Name || pattern != rule.Pattern || enabled {
		t.Fatal("rejected update changed stored fields")
	}
	if _, err := d.Exec(`UPDATE intercept_rules SET pattern=$2 WHERE id=$1`, rule.ID, invalid); err != nil {
		t.Fatal(err)
	}
	if err := d.ToggleInterceptRule(rule.ID, true); !errors.Is(err, ErrInvalidInterceptPattern) {
		t.Fatal("legacy invalid pattern was enabled")
	}
	if err := d.QueryRow(`SELECT enabled FROM intercept_rules WHERE id=$1`, rule.ID).Scan(&enabled); err != nil || enabled {
		t.Fatal("rejected toggle enabled the row")
	}
	if err := d.ToggleInterceptRule(rule.ID, false); err != nil {
		t.Fatal("invalid legacy row could not be disabled")
	}
	if _, err := d.UpdateInterceptRule(rule.ID, rule.Name, "tool_name", "regex", "^Bash$", "deny", "", 10, false, false, 0, "deny"); err != nil {
		t.Fatal(err)
	}
	if err := d.ToggleInterceptRule(rule.ID, true); err != nil {
		t.Fatal("repaired regex could not be enabled")
	}
	if err := d.ToggleInterceptRule(-1, true); err != nil {
		t.Fatal("absent rule changed its no-op behavior")
	}
}

func TestInterceptEnableValidatesLockedPattern(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	rule, err := d.CreateInterceptRule(t.Name(), "tool_name", "regex", "^Bash$", "deny", "", 10, false, false, 0, "deny")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, rule.ID) })
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE intercept_rules SET pattern='(' WHERE id=$1`, rule.ID); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- d.ToggleInterceptRule(rule.ID, true) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked bool
		err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND state='active' AND wait_event_type='Lock' AND query LIKE '%intercept_rules%')`).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("enable did not wait on the concurrent pattern update")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, ErrInvalidInterceptPattern) {
			t.Fatal("enable did not validate the committed pattern under the row lock")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("enable did not finish after releasing the row lock")
	}
}
