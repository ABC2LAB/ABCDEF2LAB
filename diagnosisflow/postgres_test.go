package diagnosisflow

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestPostgresSelectionRecoveryAndAtomicRevision(t *testing.T) {
	dsn := os.Getenv("ARTEX_FIRST_DIAGNOSIS_ROLE_TEST_DSN")
	if dsn == "" || os.Getenv("ARTEX_FIRST_DIAGNOSIS_ROLE_TEST_OWNED") != "1" {
		t.Skip("owned PostgreSQL 16 required")
	}
	pool, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = Migrate(t.Context(), pool.DB); err != nil {
		t.Fatal(err)
	}
	row, err := pool.CreateTask("Owned workflow persistence", "Synthetic crawl only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	b := Binding{JobID: "postgres-owned-" + strconv.FormatInt(row.ID, 10), TaskID: strconv.FormatInt(row.ID, 10), RunRef: "owned-run", SourceDigest: strings.Repeat("a", 64), Owner: "ARTEX", Mode: Manual, TargetURL: "https://owned.test/read", PathPrefixes: []string{"/read"}, ReadonlyPrefixes: []string{"/read"}, Methods: []string{"GET", "POST"}, ReversibleWrites: true, Limits: Limits{Format: Version, MaxPages: 2, MaxEndpoints: 32, MaxResponseBytes: 4096}, Current: func() error { return nil }}
	store := Postgres{DB: pool.DB}
	c, err := New(t.Context(), b, store)
	if err != nil {
		t.Fatal(err)
	}
	syntheticCrawl(t, c)
	s, _ := c.Snapshot()
	selected, err := c.Command(t.Context(), "ARTEX", "selection", "selection-once", s.Revision, []string{s.Endpoints[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	// A separate connection represents a new read process. It never recreates an execution.
	reopened, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	saved, err := (Postgres{DB: reopened.DB}).Read(t.Context(), "ARTEX", b.JobID)
	if err != nil || saved.Snapshot.Revision != selected.Revision || !saved.Snapshot.Endpoints[0].Selected || saved.Snapshot.Stage != "diagnosing" {
		t.Fatal("stored selection not recovered", err)
	}
	if _, err = store.Read(t.Context(), "other-owner", b.JobID); err == nil {
		t.Fatal("foreign owner read workflow")
	}
	stale := clone(saved)
	stale.Snapshot.Revision++
	if err = store.Save(context.Background(), saved.Snapshot.Revision-1, stale); !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrConflict) {
		t.Fatal("stale update accepted", err)
	}
	var revisions int
	if err = pool.QueryRow(`SELECT count(*) FROM diagnosis_workflow_events WHERE job_id=$1`, b.JobID).Scan(&revisions); err != nil || uint64(revisions) != selected.Revision {
		t.Fatal("non-atomic workflow history", err, revisions)
	}
	if _, err = pool.Exec(`INSERT INTO activity(exploration_id,worker,kind,tool,summary) VALUES($1,'work#1','tool','Bash',$2)`, row.ExplorationID, `Bash {"command":"curl --data '{\"password\":\"owned-private-value\"}'"}`); err != nil {
		t.Fatal(err)
	}
	_, activities, readErr := store.Display(t.Context(), "ARTEX", b.JobID)
	if readErr != nil || len(activities) != 1 || strings.Contains(activities[0].Summary, "owned-private-value") || strings.Contains(activities[0].Summary, "command") || !strings.Contains(activities[0].Summary, "Bash") {
		t.Fatal("raw tool activity was projected", readErr)
	}
	if _, _, err = store.Display(t.Context(), "ARTEX", b.JobID); err != nil {
		t.Fatal("actual plans/activity query failed", err)
	}
}
