package diagnosisflow

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// 별도 연결로 재조회해도 보호 근거·종료 사유가 남고 다른 회차에서 재사용되지 않는지 검사한다.
func TestPostgresExplorationInventoryRecoveryIsRunLocal(t *testing.T) {
	dsn := os.Getenv("ARTEX_FIRST_DIAGNOSIS_ROLE_TEST_DSN")
	if dsn == "" || os.Getenv("ARTEX_FIRST_DIAGNOSIS_ROLE_TEST_OWNED") != "1" {
		t.Skip("owned PostgreSQL 16 required")
	}
	pool, err := db.Open(dsn)
	if err != nil {
		t.Fatal("owned database open failed")
	}
	defer pool.Close()
	if err := Migrate(t.Context(), pool.DB); err != nil {
		t.Fatal("workflow migration failed")
	}
	store := Postgres{DB: pool.DB}
	create := func(target, run string) *Controller {
		t.Helper()
		row, err := pool.CreateTask("Owned API Recon persistence", "Synthetic private metadata only", nil, 0, 0)
		if err != nil {
			t.Fatal("owned task create failed")
		}
		template, _ := flowFixture(t, Manual)
		binding := template.binding
		binding.TaskID = strconv.FormatInt(row.ID, 10)
		binding.JobID = "owned-recon-" + binding.TaskID
		binding.TargetRef, binding.TargetRevision, binding.RunRef = target, 7, run
		c, err := New(t.Context(), binding, store)
		if err != nil {
			t.Fatal("workflow create failed")
		}
		return c
	}
	c := create("target-one", "run-one")
	b := ExplorationBinding{SessionRef: "session-owned", RunRef: c.binding.RunRef, JobID: c.binding.JobID, TaskID: c.binding.TaskID, TargetRef: c.binding.TargetRef, TargetRevision: c.binding.TargetRevision, SourceDigest: c.binding.SourceDigest}
	reader := &privateDiscoveryReader{binding: b, rows: map[uint64]ExplorationObservation{}}
	if _, err := c.BindExploration(t.Context(), reader); err != nil {
		t.Fatal("private reader bind failed")
	}
	if _, err := c.ReportStatic(t.Context(), []Discovery{{Method: "GET", Path: "/read/api", QueryKeys: []string{"inferred_name"}}}); err != nil {
		t.Fatal("static persistence failed")
	}
	o := observationFor(b, 1, "/read/api")
	o.QueryKeys = []string{"actual_name"}
	reader.rows[1] = o
	if _, err := c.ReportObserved(t.Context(), b, 1); err != nil {
		t.Fatal("observed persistence failed")
	}
	if err := c.FinishExploration(t.Context(), b, "cap_exhausted"); err != nil {
		t.Fatal("partial finalization failed")
	}
	reopened, err := db.Open(dsn)
	if err != nil {
		t.Fatal("owned database reopen failed")
	}
	defer reopened.Close()
	saved, err := (Postgres{DB: reopened.DB}).Read(t.Context(), c.binding.Owner, c.binding.JobID)
	if err != nil || saved.Exploration == nil || saved.Exploration.Binding != b || saved.Exploration.LastSequence != 1 || saved.Exploration.ClosedReason != "cap_exhausted" || saved.Snapshot.Stage != "awaiting_selection" || !saved.Snapshot.ExplorationPartial {
		t.Fatal("private exploration state not recovered")
	}
	if len(saved.DiscoveryInventory) != 2 || saved.DiscoveryInventory[0].Confidence != "inferred" || saved.DiscoveryInventory[1].Confidence != "observed" || strings.Join(saved.DiscoveryInventory[1].QueryKeys, ",") != "actual_name" {
		t.Fatal("private source metadata not recovered")
	}
	public, _ := json.Marshal(Project(saved.Snapshot))
	if strings.Contains(string(public), "discovery_inventory") || strings.Contains(string(public), "inferred_name") {
		t.Fatal("private source metadata leaked into observed public shape")
	}
	other := create("target-two", "run-two")
	if _, err := other.BindExploration(t.Context(), reader); !errors.Is(err, ErrExplorationBinding) {
		t.Fatal("foreign run reader rebound")
	}
	foreign, err := store.Read(t.Context(), other.binding.Owner, other.binding.JobID)
	if err != nil || foreign.Exploration != nil || len(foreign.DiscoveryInventory) != 0 {
		t.Fatal("private source metadata mixed between targets")
	}
}
