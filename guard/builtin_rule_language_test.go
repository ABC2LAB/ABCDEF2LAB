package guard

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

// 실제 명령은 실행하지 않고 내장 규칙의 일치 판정부터 훅 결과까지 확인한다.
func TestBuiltinRuleMatchToGuardSeparatesModelAndAudit(t *testing.T) {
	dsn := os.Getenv("ARTEX_INTERCEPT_TEST_DSN")
	if dsn == "" {
		t.Skip("ARTEX_INTERCEPT_TEST_DSN 전용 PostgreSQL 필요")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
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
