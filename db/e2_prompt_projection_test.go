package db

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e2prompt"
)

func promptDetail(source, template, system string) []byte {
	b, _ := json.Marshal(map[string]string{"version": e2prompt.Version, "key": "e2_worker", "source": source, "template_digest": strings.Repeat(template, 64), "guidance_digest": strings.Repeat("b", 64), "control_digest": strings.Repeat("c", 64), "tools_digest": strings.Repeat("d", 64), "system_digest": strings.Repeat(system, 64)})
	return b
}
func promptPG(t *testing.T) (*DB, *Task, *ExplorationStore, int64, string) {
	t.Helper()
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("synthetic PostgreSQL not configured")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatal("configured PostgreSQL unavailable")
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("synthetic prompt", "synthetic", nil, 0, 300)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.DeleteTask(task.ID); err != nil {
			t.Error("fixture cleanup failed")
		}
	})
	store := d.Exploration(task.ExplorationID)
	intent, err := store.AddNode(KindIntent, map[string]any{"summary": "synthetic"}, 0, "open", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	run := fmt.Sprintf("prompt-%d", task.ID)
	if err := store.RegisterE2PromptRun(t.Context(), task.ID, run); err != nil {
		t.Fatal(err)
	}
	return d, task, store, intent, run
}

func TestE2PromptProjectionPostgres(t *testing.T) {
	for _, scenario := range []string{"one", "many", "conflict", "system-only", "missing", "collecting", "failed-close", "invalid-detail", "missing-sequence", "foreign-activity", "duplicate", "store-failure", "empty", "untracked", "reuse-run"} {
		t.Run(scenario, func(t *testing.T) {
			d, task, s, intent, run := promptPG(t)
			ctx := t.Context()
			n := 1
			if scenario == "empty" || scenario == "untracked" {
				n = 0
			}
			if scenario == "many" {
				n = 205
			}
			if scenario == "conflict" || scenario == "system-only" || scenario == "missing" {
				n = 2
			}
			var seq int64
			for i := 0; i < n; i++ {
				var err error
				seq, err = s.BeginE2PromptWork(ctx, task.ID, run, intent)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "missing" && i == 1 {
					continue
				}
				detail := promptDetail("builtin", "a", "e")
				if scenario == "conflict" && i == 1 {
					detail = promptDetail("override", "f", "e")
				}
				if scenario == "system-only" && i == 1 {
					detail = promptDetail("builtin", "a", "f")
				}
				if scenario == "store-failure" {
					name := fmt.Sprintf("prompt_fail_%d", task.ID)
					_, err = d.Exec(`CREATE FUNCTION ` + name + `() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'synthetic'; END$$`)
					if err != nil {
						t.Fatal(err)
					}
					_, err = d.Exec(`CREATE TRIGGER ` + name + ` BEFORE INSERT ON activity FOR EACH ROW WHEN (NEW.exploration_id=` + fmt.Sprint(task.ExplorationID) + `) EXECUTE FUNCTION ` + name + `() `)
					if err != nil {
						t.Fatal(err)
					}
					err = s.SaveE2PromptActivity(ctx, task.ID, run, seq, intent, "work#1", detail)
					d.Exec(`DROP TRIGGER ` + name + ` ON activity`)
					d.Exec(`DROP FUNCTION ` + name + `() `)
					if err == nil {
						t.Fatal("storage failure accepted")
					}
					continue
				}
				if err = s.SaveE2PromptActivity(ctx, task.ID, run, seq, intent, "work#1", detail); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "duplicate" {
				if s.SaveE2PromptActivity(ctx, task.ID, run, seq, intent, "work#1", promptDetail("builtin", "a", "e")) == nil {
					t.Fatal("duplicate accepted")
				}
			}
			if scenario == "invalid-detail" {
				_, err := d.Exec(`UPDATE activity SET detail='{"secret":"synthetic-private-input"}' WHERE id=(SELECT activity_id FROM e2_prompt_observations WHERE run_ref=$1 AND sequence=1)`, run)
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "missing-sequence" {
				if _, err := d.Exec(`DELETE FROM e2_prompt_observations WHERE run_ref=$1`, run); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "foreign-activity" {
				if _, err := d.Exec(`UPDATE activity SET node_id=NULL WHERE id=(SELECT activity_id FROM e2_prompt_observations WHERE run_ref=$1 AND sequence=1)`, run); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "collecting" {
				if err := s.SealE2PromptRun(ctx, task.ID, run, scenario != "failed-close"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "untracked" {
				if _, err := s.AppendActivity(Activity{NodeID: &intent, Worker: "work#1", Kind: "e2_prompt", Tool: "E2HTTP", Detail: string(promptDetail("builtin", "a", "e"))}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "reuse-run" {
				if s.RegisterE2PromptRun(ctx, task.ID, run) == nil {
					t.Fatal("run reference reused")
				}
			}
			p, err := s.E2PromptProjection(ctx, task.ID, run)
			if err != nil {
				t.Fatal(err)
			}
			expected := "complete"
			switch scenario {
			case "empty", "untracked":
				expected = "empty"
			case "conflict":
				expected = "conflict"
			case "missing", "failed-close", "store-failure", "reuse-run":
				expected = "incomplete"
			case "collecting":
				expected = "collecting"
			case "invalid-detail", "missing-sequence", "foreign-activity":
				expected = "invalid"
			}
			if p.State != expected || p.Complete != (expected == "complete") {
				t.Fatalf("state=%s expected=%s", p.State, expected)
			}
			if scenario == "many" && (len(p.Observations) != 205 || p.ExpectedCount != 205) {
				t.Fatal("pagination-sized observations lost")
			}
			if scenario == "conflict" && len(p.Observations) != 2 {
				t.Fatal("conflicting observations discarded")
			}
			wire, _ := json.Marshal(p)
			for _, private := range []string{"system_digest", "synthetic-private-input", "detail", "payload"} {
				if strings.Contains(string(wire), private) {
					t.Fatal("private field leaked")
				}
			}
			if _, err = s.E2PromptProjection(ctx, task.ID+1, run); err == nil {
				t.Fatal("foreign task accepted")
			}
			if _, err = s.BeginE2PromptWork(ctx, task.ID, run, intent+100000); err == nil {
				t.Fatal("foreign intent accepted")
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err = s.E2PromptProjection(canceled, task.ID, run); err == nil {
				t.Fatal("query failure became empty projection")
			}
			if scenario != "collecting" {
				if _, err = s.BeginE2PromptWork(ctx, task.ID, run, intent); err == nil {
					t.Fatal("sealed run reopened")
				}
			}
		})
	}
}
