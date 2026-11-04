package intercept

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func configurationTestDB(t *testing.T) *db.DB {
	t.Helper()
	dsn := os.Getenv("ARTEX_INTERCEPT_TEST_DSN")
	if dsn == "" {
		t.Skip("set ARTEX_INTERCEPT_TEST_DSN to a dedicated PostgreSQL test database")
	}
	// Coordinate with db/server TestMain before Open runs schema migrations.
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
	return d
}

func TestInvalidStoredRegexCannotFallThroughToAllow(t *testing.T) {
	d := configurationTestDB(t)
	for _, action := range []string{"deny", "ask", "allow"} {
		t.Run(action, func(t *testing.T) {
			rule, err := d.CreateInterceptRule("invalid-regex-"+action, "tool_name", "regex", "^Bash$", action, "", -100, true, false, 0, "deny")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, rule.ID) })
			const privatePattern = "(?P<synthetic-private-regex"
			if _, err := d.Exec(`UPDATE intercept_rules SET pattern=$2 WHERE id=$1`, rule.ID, privatePattern); err != nil {
				t.Fatal(err)
			}
			ic := New(d)
			if !ic.IsToolEnabled("Bash") {
				t.Fatal("invalid configuration bypassed the intercept path")
			}
			decision, matched := ic.Match("Bash", []byte(`{}`))
			if !matched || decision.Action != "deny" || decision.RuleID != 0 || decision.RuleName != "" || decision.ConfigDigest != "" || strings.Contains(decision.Message, privatePattern) {
				t.Fatalf("invalid regex did not produce an unattributed configuration denial: %+v matched=%v", decision, matched)
			}
			if ic.cached != nil || ic.enabledTools != nil {
				t.Fatal("failed configuration load published a partial cache")
			}
			if err := d.ToggleInterceptRule(rule.ID, false); err != nil {
				t.Fatal(err)
			}
			ic.Invalidate()
			if _, err := ic.rules(); err != nil {
				t.Fatal("disabled invalid regex prevented configuration repair")
			}
		})
	}
}

func TestMatchLoadFailureReturnsDenial(t *testing.T) {
	connection, err := sql.Open("pgx", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	ic := New(&db.DB{DB: connection})
	decision, matched := ic.Match("Bash", []byte(`{}`))
	if !matched || decision.Action != "deny" || decision.RuleID != 0 || decision.Message == "" {
		t.Fatal("database load failure was treated as no matching rule")
	}
}

func TestMissingDatabaseCannotPublishConfiguration(t *testing.T) {
	for _, d := range []*db.DB{nil, {}} {
		ic := New(d)
		if _, err := ic.rules(); err != ErrConfigurationUnavailable {
			t.Fatalf("missing database load = %v", err)
		}
		if enabled, err := ic.CheckToolEnabled("Read"); enabled || err != ErrConfigurationUnavailable {
			t.Fatal("missing database looked like a valid tool configuration")
		}
		if decision, matched := ic.Match("Read", []byte(`{}`)); !matched || decision.Action != "deny" || decision.RuleID != 0 {
			t.Fatal("missing database did not deny without attributing a rule")
		}
		if ic.cached != nil || ic.enabledTools != nil {
			t.Fatal("missing database published configuration")
		}
	}
}

func TestInvalidEnabledToolsCannotDisableInterception(t *testing.T) {
	d := configurationTestDB(t)
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
	for _, value := range []string{"{", "null"} {
		if err := d.SetSetting("intercept_enabled_tools", value); err != nil {
			t.Fatal(err)
		}
		ic := New(d)
		if tools, err := ic.GetEnabledTools(); err != ErrConfigurationUnavailable || tools != nil {
			t.Fatal("malformed settings were presented as an empty valid configuration")
		}
		if !ic.IsToolEnabled("Bash") {
			t.Fatal("malformed configuration disabled interception")
		}
		if decision, matched := ic.Match("Bash", []byte(`{}`)); !matched || decision.Action != "deny" {
			t.Fatal("malformed enabled-tools configuration fell through")
		}
		if ic.cached != nil || ic.enabledTools != nil {
			t.Fatal("malformed configuration published a partial cache")
		}
	}
}

func TestEmptyEnabledToolsRemainsAnExplicitOptOut(t *testing.T) {
	d := configurationTestDB(t)
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
	ic := New(d)
	if err := ic.SetEnabledTools(nil); err != nil {
		t.Fatal(err)
	}
	if tools, err := ic.GetEnabledTools(); err != nil || tools == nil || len(tools) != 0 {
		t.Fatal("explicit empty setting did not round trip as an array")
	}
	if enabled, err := ic.CheckToolEnabled("Bash"); err != nil || enabled {
		t.Fatal("explicit opt-out was treated as configuration failure")
	}
}

func TestUnavailableSettingsCannotPublishPartialCache(t *testing.T) {
	d := configurationTestDB(t)
	if _, err := d.Exec(`ALTER TABLE settings RENAME TO pr67_unavailable_settings`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := d.Exec(`ALTER TABLE pr67_unavailable_settings RENAME TO settings`); err != nil {
			t.Fatal("failed to restore dedicated test settings table")
		}
	}()
	ic := New(d)
	if decision, matched := ic.Match("Bash", []byte(`{}`)); !matched || decision.Action != "deny" {
		t.Fatal("settings read failure became an unmatched rule")
	}
	if ic.cached != nil || ic.enabledTools != nil {
		t.Fatal("settings read failure published a partial cache")
	}
}
