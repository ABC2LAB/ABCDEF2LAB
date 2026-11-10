package diagnosisflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
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
	stale.Snapshot.Revision = saved.Snapshot.Revision
	if err = store.Save(context.Background(), saved.Snapshot.Revision-1, stale); !errors.Is(err, ErrConflict) {
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

func TestPostgresAdversarialInputsAndProjectionFaultRemainRoundLocal(t *testing.T) {
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
	template, _ := flowFixture(t, Auto)
	makeController := func(database Postgres) *Controller {
		row, err := pool.CreateTask("Owned adversarial workflow", "Synthetic HTTP only", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		b := template.binding
		b.TaskID = strconv.FormatInt(row.ID, 10)
		b.JobID = "postgres-adversarial-" + b.TaskID
		b.Limits.MaxResponseBytes = 1 << 20
		c, err := New(t.Context(), b, database)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c := makeController(Postgres{DB: pool.DB})
	_, err = c.Crawl(t.Context(), func(_ context.Context, target string) (e2runner.Result, error) {
		body := `<a href="/read/%00x">NUL</a><form method="dialog" action="/read/dialog"></form><form method="` + strings.Repeat("x", 140<<10) + `" action="/read/long"></form>`
		return closedPage(c, target, body, 200), nil
	})
	if err != nil {
		t.Fatal("target input rejected jsonb write", err)
	}
	for i, v := range []e2proxy.RequestView{{Method: "GET", URL: "https://owned.test/read/%00x"}, {Method: strings.Repeat("x", 140<<10), URL: "https://owned.test/read"}, {Method: "GET", URL: "https://owned.test/read"}} {
		if err = c.After(t.Context(), v, e2proxy.Receipt{AttemptID: fmt.Sprintf("denied-%d", i), Decision: "denied", StatusCode: 999, ForwardAttempted: i == 2, ResponseReceived: i == 2}); err != nil {
			t.Fatal("denied request poisoned projection", err)
		}
	}
	stored, err := (Postgres{DB: pool.DB}).Read(t.Context(), "ARTEX", c.binding.JobID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(stored)
	if len(raw) > 64<<10 || strings.Contains(string(raw), `\u0000`) || strings.Contains(string(raw), `"status":999`) {
		t.Fatal("unbounded or invalid durable projection")
	}
	// A real closed pool simulates an optional UI-store outage. It must stop this
	// controller, preserve its failure fact, and not become a wire-unknown error.
	faultPool, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	broken := makeController(Postgres{DB: faultPool.DB})
	faultPool.Close()
	if err = broken.After(t.Context(), e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read"}, e2proxy.Receipt{AttemptID: "fault"}); !errors.Is(err, ErrProjection) {
		t.Fatal("wrong storage fault class", err)
	}
	if err = broken.Close(t.Context(), "completed"); !errors.Is(err, ErrProjection) {
		t.Fatal("poisoned round completed", err)
	}
	state, _ := broken.Snapshot()
	if !broken.Failed() || state.Stage != "failed" || state.Failure != "workflow_projection_failed" {
		t.Fatal("projection failure lost")
	}
	next := makeController(Postgres{DB: pool.DB})
	if next.Failed() {
		t.Fatal("healthy later round inherited projection fault")
	}
}
