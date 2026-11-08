package db

import (
	"strings"
	"testing"
	"unicode"
)

func TestBuiltinRuleModelMessagesRequireExactDefaults(t *testing.T) {
	for _, seed := range builtinRuleTextSeeds() {
		r := InterceptRule{Name: seed.name, Message: seed.message, Pattern: seed.pattern, MatchTarget: seed.target, MatchType: seed.typ, Action: seed.action}
		message, code := BuiltinRuleModelMessage(r)
		if code == "" || !strings.Contains(message, code) || strings.IndexFunc(message, func(r rune) bool { return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hangul, r) }) >= 0 {
			t.Fatalf("model message is not English: %q", message)
		}
		old := legacyRuleTextHashes[seed.pattern]
		if old.Name == "" || old.Message == "" {
			t.Fatal("legacy rule identity missing")
		}
		r.Name = "用户自己的规则"
		if message, code := BuiltinRuleModelMessage(r); message != "" || code != "" {
			t.Fatal("same-pattern custom rule was classified as built-in")
		}
		r.Name = seed.name
		r.Message = "用户说明"
		if message, _ := BuiltinRuleModelMessage(r); message != "" {
			t.Fatal("custom message was replaced")
		}
	}
}

func TestRefreshBuiltinRuleTextPreservesSamePatternCustomRowsAndNotes(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("전용 PostgreSQL 필요: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	seed := defaultInterceptRules[0]
	const name, message, note = "用户自定义规则", "保留用户说明", "自定义资产备注"
	rule, err := d.CreateInterceptRule(name, seed.target, seed.typ, seed.pattern, seed.action, message, 77, true, false, 60, "deny")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, rule.ID) })
	asset := defaultAssetInterceptRules[0]
	if _, err := d.Exec(`UPDATE asset_intercept_rules SET note=$1 WHERE kind=$2 AND pattern=$3 AND builtin`, note, asset.kind, asset.pattern); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`UPDATE asset_intercept_rules SET note=$1 WHERE kind=$2 AND pattern=$3 AND builtin`, asset.note, asset.kind, asset.pattern)
	})
	if err := d.SetSetting("builtin_rule_text_korean_v1", "false"); err != nil {
		t.Fatal(err)
	}
	if err := d.refreshBuiltinRuleText(); err != nil {
		t.Fatal(err)
	}
	var gotName, gotMessage, gotNote string
	if err := d.QueryRow(`SELECT name,message FROM intercept_rules WHERE id=$1`, rule.ID).Scan(&gotName, &gotMessage); err != nil {
		t.Fatal(err)
	}
	if gotName != name || gotMessage != message {
		t.Fatalf("custom row changed: %q %q", gotName, gotMessage)
	}
	if err := d.QueryRow(`SELECT note FROM asset_intercept_rules WHERE kind=$1 AND pattern=$2 AND builtin`, asset.kind, asset.pattern).Scan(&gotNote); err != nil {
		t.Fatal(err)
	}
	if gotNote != note {
		t.Fatalf("custom note changed: %q", gotNote)
	}
}
