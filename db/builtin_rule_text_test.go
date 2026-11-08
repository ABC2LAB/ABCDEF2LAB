package db

import (
	"strings"
	"testing"
	"unicode"
)

func dbHasHan(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0
}

// 시드 문구에는 한자가 없어야 한다. 규칙 패턴은 대상 입력과 맞추는 값이라 검사하지 않는다.
func TestBuiltinSeedTextHasNoHan(t *testing.T) {
	for _, a := range builtinAgents {
		if dbHasHan(a.name) || dbHasHan(a.desc) {
			t.Errorf("agent %s text contains Han", a.key)
		}
		for _, v := range a.vars {
			if dbHasHan(v.desc) || dbHasHan(v.example) {
				t.Errorf("agent %s var %s contains Han", a.key, v.name)
			}
		}
	}
	for _, r := range defaultInterceptRules {
		if dbHasHan(r.name) || dbHasHan(r.message) {
			t.Errorf("rule %q contains Han", r.name)
		}
	}
	for _, r := range defaultInterceptRulesV2 {
		if dbHasHan(r.name) || dbHasHan(r.message) {
			t.Errorf("rule %q contains Han", r.name)
		}
	}
	for _, r := range defaultAssetInterceptRules {
		if dbHasHan(r.note) {
			t.Errorf("asset rule %q contains Han", r.note)
		}
	}
	if dbHasHan(deleteEndpointRuleName) || dbHasHan(deleteEndpointRuleMessage) {
		t.Error("delete endpoint rule contains Han")
	}
}

// 한자가 남은 내장 규칙만 현재 문구로 바뀌고, 운영자가 고친 글과 활성 여부·우선순위는 그대로여야 한다. 한 번만 돈다.
func TestRefreshBuiltinRuleText(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	// 복원 Cleanup 뒤에 닫히도록 먼저 등록한다.
	t.Cleanup(func() { d.Close() })
	oldRule, editedRule, oldAsset := defaultInterceptRules[0], defaultInterceptRules[1], defaultAssetInterceptRules[0]
	if _, err := d.Exec(`UPDATE intercept_rules SET name=$1, message=$2, enabled=false, priority=7 WHERE pattern=$3`, legacyRuleFixtures[0].Name, legacyRuleFixtures[0].Message, oldRule.pattern); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE intercept_rules SET name=$1, message=$2 WHERE pattern=$3`, "운영자 규칙", "운영자 설명", editedRule.pattern); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE asset_intercept_rules SET note=$1 WHERE kind=$2 AND pattern=$3`, legacyAssetNoteFixtures[0], oldAsset.kind, oldAsset.pattern); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := d.Exec(`UPDATE intercept_rules SET name=$1, message=$2, enabled=true, priority=$3 WHERE pattern=$4`, oldRule.name, oldRule.message, oldRule.priority, oldRule.pattern); err != nil {
			t.Error(err)
		}
		if _, err := d.Exec(`UPDATE intercept_rules SET name=$1, message=$2 WHERE pattern=$3`, editedRule.name, editedRule.message, editedRule.pattern); err != nil {
			t.Error(err)
		}
		if _, err := d.Exec(`UPDATE asset_intercept_rules SET note=$1 WHERE kind=$2 AND pattern=$3`, oldAsset.note, oldAsset.kind, oldAsset.pattern); err != nil {
			t.Error(err)
		}
	})
	if err := d.SetSetting("builtin_rule_text_korean_v1", "false"); err != nil {
		t.Fatal(err)
	}

	if err := d.refreshBuiltinRuleText(); err != nil {
		t.Fatal(err)
	}

	var name, message string
	var enabled bool
	var priority int
	if err := d.QueryRow(`SELECT name, message, enabled, priority FROM intercept_rules WHERE pattern=$1`, oldRule.pattern).Scan(&name, &message, &enabled, &priority); err != nil {
		t.Fatal(err)
	}
	if name != oldRule.name || message != oldRule.message || enabled || priority != 7 {
		t.Fatalf("old rule: name=%q message=%q enabled=%v priority=%d", name, message, enabled, priority)
	}
	if err := d.QueryRow(`SELECT name, message FROM intercept_rules WHERE pattern=$1`, editedRule.pattern).Scan(&name, &message); err != nil {
		t.Fatal(err)
	}
	if name != "운영자 규칙" || message != "운영자 설명" {
		t.Fatalf("operator edit overwritten: name=%q message=%q", name, message)
	}
	var note string
	if err := d.QueryRow(`SELECT note FROM asset_intercept_rules WHERE kind=$1 AND pattern=$2`, oldAsset.kind, oldAsset.pattern).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if note != oldAsset.note {
		t.Fatalf("old asset note: %q", note)
	}

	if _, err := d.Exec(`UPDATE intercept_rules SET message=$1 WHERE pattern=$2`, "禁止旧操作", oldRule.pattern); err != nil {
		t.Fatal(err)
	}
	if err := d.refreshBuiltinRuleText(); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT message FROM intercept_rules WHERE pattern=$1`, oldRule.pattern).Scan(&message); err != nil {
		t.Fatal(err)
	}
	if message != "禁止旧操作" {
		t.Fatalf("refresh must run once: %q", message)
	}
}

// 운영자가 한쪽만 고쳐도 소유권을 확정할 수 없으므로 행 전체를 보존한다.
func TestRefreshBuiltinRuleTextPerField(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	nameEdited, messageEdited := defaultInterceptRules[2], defaultInterceptRules[3]
	set := func(name, message, pattern string) {
		t.Helper()
		if _, err := d.Exec(`UPDATE intercept_rules SET name=$1, message=$2 WHERE pattern=$3`, name, message, pattern); err != nil {
			t.Fatal(err)
		}
	}
	set("운영자 이름", "禁止旧操作", nameEdited.pattern)
	set("[内置] 旧规则", "운영자 설명", messageEdited.pattern)
	t.Cleanup(func() {
		if _, err := d.Exec(`UPDATE intercept_rules SET name=$1, message=$2 WHERE pattern=$3`, nameEdited.name, nameEdited.message, nameEdited.pattern); err != nil {
			t.Error(err)
		}
		if _, err := d.Exec(`UPDATE intercept_rules SET name=$1, message=$2 WHERE pattern=$3`, messageEdited.name, messageEdited.message, messageEdited.pattern); err != nil {
			t.Error(err)
		}
	})
	if err := d.SetSetting("builtin_rule_text_korean_v1", "false"); err != nil {
		t.Fatal(err)
	}

	if err := d.refreshBuiltinRuleText(); err != nil {
		t.Fatal(err)
	}

	var name, message string
	if err := d.QueryRow(`SELECT name, message FROM intercept_rules WHERE pattern=$1`, nameEdited.pattern).Scan(&name, &message); err != nil {
		t.Fatal(err)
	}
	if name != "운영자 이름" || message != "禁止旧操作" {
		t.Fatalf("name edit: name=%q message=%q", name, message)
	}
	if err := d.QueryRow(`SELECT name, message FROM intercept_rules WHERE pattern=$1`, messageEdited.pattern).Scan(&name, &message); err != nil {
		t.Fatal(err)
	}
	if name != "[内置] 旧规则" || message != "운영자 설명" {
		t.Fatalf("message edit: name=%q message=%q", name, message)
	}
}

// 기동 시드가 갱신을 불러야 기존 설치에 실제로 적용된다.
func TestBuiltinRuleTextRefreshRunsOnOpen(t *testing.T) {
	dsn := testDSN(t)
	d, err := Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	rule := defaultInterceptRules[4]
	if _, err := d.Exec(`UPDATE intercept_rules SET message=$1 WHERE pattern=$2`, legacyRuleFixtures[4].Message, rule.pattern); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := d.Exec(`UPDATE intercept_rules SET message=$1 WHERE pattern=$2`, rule.message, rule.pattern); err != nil {
			t.Error(err)
		}
	})
	if err := d.SetSetting("builtin_rule_text_korean_v1", "false"); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })

	var message string
	if err := reopened.QueryRow(`SELECT message FROM intercept_rules WHERE pattern=$1`, rule.pattern).Scan(&message); err != nil {
		t.Fatal(err)
	}
	if message != rule.message {
		t.Fatalf("seed did not refresh rule text: %q", message)
	}
}
