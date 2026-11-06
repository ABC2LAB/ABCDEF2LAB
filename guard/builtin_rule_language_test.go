package guard

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

func guardLanguageTestDB(t *testing.T) *db.DB {
	t.Helper()
	dsn := os.Getenv("ARTEX_INTERCEPT_TEST_DSN")
	if dsn == "" {
		t.Skip("ARTEX_INTERCEPT_TEST_DSN 전용 PostgreSQL 필요")
	}
	// 다른 패키지의 시드·설정 변경과 겹치지 않도록 같은 전용 DB 잠금을 쓴다.
	locker, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locker.Close() })
	lease, err := locker.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	if _, err := lease.ExecContext(t.Context(), `SELECT pg_advisory_lock(7337741002)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = lease.ExecContext(context.Background(), `SELECT pg_advisory_unlock(7337741002)`) })
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// 실제 명령은 실행하지 않고 내장 규칙의 일치 판정부터 훅 결과까지 확인한다.
func TestBuiltinRuleMatchToGuardSeparatesModelAndAudit(t *testing.T) {
	d := guardLanguageTestDB(t)
	ic := intercept.New(d)
	input := json.RawMessage(`{"command":"rm -rf /synthetic-fixture"}`)
	dec, matched := ic.Match("Bash", input)
	if !matched || dec.Action != "deny" || dec.ModelMessage == "" || dec.ReasonCode == "" {
		t.Fatalf("built-in match=%v %+v", matched, dec)
	}
	g := NewWithInterceptor(ic)
	blocked, message, _ := g.Hooks().PreToolUse(context.Background(), "Bash", input)
	if !blocked || !strings.Contains(message, dec.ReasonCode) || strings.Contains(message, dec.Message) {
		t.Fatalf("model result=%q", message)
	}
	if got := g.Audit(); len(got) != 1 || got[0].Reason != dec.Message {
		t.Fatal("human audit reason changed")
	}
}

// 빈 메시지의 프로그램 기본값과 운영자가 직접 쓴 메시지를 실제 훅에서 구분한다.
func TestCustomRuleMatchToGuardKeepsMessageOwnership(t *testing.T) {
	d := guardLanguageTestDB(t)
	for _, custom := range []string{"", "운영자 지정 설명"} {
		t.Run(custom, func(t *testing.T) {
			rule, err := d.CreateInterceptRule("표시용 사용자 규칙", "tool_input", "string", "guard-language-fixture", "deny", custom, 100000, true, false, 0, "deny")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, rule.ID) })
			ic := intercept.New(d)
			g := NewWithInterceptor(ic)
			blocked, message, _ := g.Hooks().PreToolUse(t.Context(), "Bash", json.RawMessage(`{"command":"guard-language-fixture"}`))
			if !blocked || len(g.Audit()) != 1 {
				t.Fatalf("rule did not block exactly once: %v %q", blocked, message)
			}
			if custom != "" {
				if !strings.Contains(message, custom) || g.Audit()[0].Reason != custom {
					t.Fatal("operator message changed")
				}
			} else if !strings.Contains(message, "reason_code=rule_deny") || strings.Contains(message, rule.Name) || !strings.Contains(g.Audit()[0].Reason, rule.Name) {
				t.Fatalf("program default crossed model/display boundary: %q", message)
			}
		})
	}
}
