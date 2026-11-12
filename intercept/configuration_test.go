package intercept

import (
	"context"
	"database/sql"
	"fmt"
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

// 깨진 정규식(검사 전에 저장됐거나 DB를 직접 고친 행)은 허용으로 새지 않는다(#86). 막기·확인 규칙이 깨지면 모든
// 호출을 규칙 ID와 rule_invalid로 막고, 허용 규칙은 그 규칙만 건너뛴다(P3 결정안 C12).
func TestInvalidStoredRegexCannotFallThroughToAllow(t *testing.T) {
	d := configurationTestDB(t)
	const privatePattern = "(?P<synthetic-private-regex"
	brokenRule := func(t *testing.T, name, action string, priority int) db.InterceptRule {
		t.Helper()
		rule, err := d.CreateInterceptRule(name, "tool_name", "regex", "^Bash$", action, "", priority, true, false, 0, "deny")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, rule.ID) })
		if _, err := d.Exec(`UPDATE intercept_rules SET pattern=$2 WHERE id=$1`, rule.ID, privatePattern); err != nil {
			t.Fatal(err)
		}
		return rule
	}
	for _, action := range []string{"deny", "ask", "allow"} {
		t.Run(action, func(t *testing.T) {
			rule := brokenRule(t, "invalid-regex-"+action, action, 100000)
			ic := New(d)
			if !ic.IsToolEnabled("Bash") {
				t.Fatal("invalid configuration bypassed the intercept path")
			}
			decision, matched := ic.Match("Bash", []byte(`{}`))
			if strings.Contains(decision.Message, privatePattern) || strings.Contains(decision.ModelMessage, privatePattern) {
				t.Fatal("stored regex leaked into the decision")
			}
			if ic.cached == nil {
				t.Fatal("one invalid rule disabled the whole configuration")
			}
			if action == "allow" {
				if decision.ReasonCode == "rule_invalid" || decision.ReasonCode == "configuration_unavailable" || matched && decision.RuleID == rule.ID {
					t.Fatalf("broken allow rule was not skipped: %+v matched=%v", decision, matched)
				}
			} else if !matched || decision.Action != "deny" || decision.ReasonCode != "rule_invalid" || decision.RuleID != rule.ID || decision.RuleName != rule.Name || !strings.Contains(decision.Message, fmt.Sprintf("#%d", rule.ID)) {
				t.Fatalf("invalid %s rule did not deny with its attribution: %+v matched=%v", action, decision, matched)
			}
			if err := d.ToggleInterceptRule(rule.ID, false); err != nil {
				t.Fatal(err)
			}
			ic.Invalidate()
			if decision, _ := ic.Match("Bash", []byte(`{}`)); decision.ReasonCode == "rule_invalid" {
				t.Fatal("disabled invalid regex prevented configuration repair")
			}
		})
	}
	// 깨진 막기 규칙은 우선순위가 더 높은 유효한 허용 규칙이 있어도 모든 호출을 막는다(#86과 같은 엄격함).
	t.Run("broken-blocking-rule-blocks-all-calls", func(t *testing.T) {
		broken := brokenRule(t, "invalid-regex-low", "deny", 99000)
		allow, err := d.CreateInterceptRule("valid-allow-high", "tool_name", "regex", "^Bash$", "allow", "", 99500, true, false, 0, "deny")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, allow.ID) })
		ic := New(d)
		for _, tool := range []string{"Bash", "Read"} {
			if decision, matched := ic.Match(tool, []byte(`{}`)); !matched || decision.Action != "deny" || decision.ReasonCode != "rule_invalid" || decision.RuleID != broken.ID {
				t.Fatalf("%s was not denied by the broken rule: %+v", tool, decision)
			}
		}
	})
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
