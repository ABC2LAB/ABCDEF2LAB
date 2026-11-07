package server

import (
	"context"
	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"os"
	"testing"
)

// 전용 DB에서 기존 버전 저장과 서버 hook이 E②의 별도 키를 읽는지 확인한다.
func TestE2WorkerPromptSeedAndRuntimeOverride(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("set ARTEX_PG_DSN to a dedicated synthetic PostgreSQL database")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("explicit test database unavailable: %v", err)
	}
	t.Cleanup(func() { pg.Close() })
	s := &Server{ctx: context.Background(), m: &Manager{pg: pg}}
	seedPrompts(pg)
	a, err := pg.GetAgentByKey("e2_worker")
	if err != nil || a == nil || a.Role != "worker" {
		t.Fatalf("E2 prompt agent missing: %v %v", a, err)
	}
	original, err := pg.CurrentPrompt(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pg.SavePrompt(a.ID, original, "test", "restore"); err != nil {
			t.Error(err)
		}
	})
	if original != agent.BuiltinPromptSeeds()["e2_worker"] {
		t.Fatal("E2 initial body not seeded")
	}
	if _, err := pg.SavePrompt(a.ID, "synthetic-diagnostic-v2", "test", "test"); err != nil {
		t.Fatal(err)
	}
	seedPrompts(pg)
	got, ok := s.buildRuntimeHooks().PromptOverride("e2_worker")
	if !ok || got != "synthetic-diagnostic-v2" {
		t.Fatal("runtime did not read saved E2 body or reseed overwrote it")
	}
	generic, ok := s.buildRuntimeHooks().PromptOverride("worker")
	if !ok || generic == got {
		t.Fatal("E2 and generic worker prompt identities mixed")
	}
}
