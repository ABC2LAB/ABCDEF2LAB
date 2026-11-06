package server

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/taskmode"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

// Requires an explicitly owned disposable database. Never resolves the normal
// application DSN. Authority is synthetic; only save/commit/rows are real PG.
func TestFinalizationScopeOwnedPostgres(t *testing.T) {
	dsn := os.Getenv("ARTEX_FINALIZATION_SCOPE_TEST_DSN")
	if dsn == "" {
		t.Skip("set ARTEX_FINALIZATION_SCOPE_TEST_DSN to an owned disposable PostgreSQL")
	}
	if os.Getenv("ARTEX_FINALIZATION_SCOPE_TEST_OWNED") != "1" {
		t.Fatal("explicit disposable database ownership required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal("owned PostgreSQL initialization failed")
	}
	defer d.Close()
	task, err := d.CreateTask("synthetic scope lifetime", "synthetic only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := d.Exploration(task.ExplorationID).AddIntent(map[string]any{"description": "synthetic scope"}, 5, nil, "scope-test")
	if err != nil {
		t.Fatal(err)
	}
	markers := func(_ context.Context, id int64) (taskmode.Marker, error) {
		return taskmode.Marker{TaskID: id, State: taskmode.MarkerPresent}, nil
	}
	mode, err := d.RegisterTaskMode(ctx, task.ID, taskmode.ModeControlled, markers)
	if err != nil {
		t.Fatal(err)
	}
	in, source := finalizationRecordFixture()
	expected := in.Expected
	expected.IntentID, expected.Scope.TaskRef = intent, strconv.FormatInt(task.ID, 10)
	in.Expected, in.Evidence.Policy.Context = expected, expected
	for _, observation := range []*evidencebind.Observation{&in.Evidence.Owner, &in.Evidence.Cross} {
		observation.Context, observation.Receipt.Scope = expected, expected.Scope
		observation.Evidence.IntentID, observation.Evidence.Scope = intent, expected.Scope
	}
	in.Evidence.Result.Context = expected
	in.Evidence.Result.OwnerEvidence, in.Evidence.Result.CrossEvidence = in.Evidence.Owner.Evidence, in.Evidence.Cross.Evidence
	source.Expected, source.ExplorationID, source.ModeVersion = expected, task.ExplorationID, mode.Version
	source.FinalCheckpointScope = expected.Scope
	source.EvidenceDigest, err = e3record.EvidenceDigest(in.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := auditlog.Checkpoint{Version: auditlog.Version, JournalID: "synthetic", Scope: expected.Scope, Sequence: 4, MAC: hex.EncodeToString(make([]byte, 32))}
	source.FinalCheckpointDigest, err = ControlledReadCheckpointDigest(checkpoint)
	if err != nil {
		t.Fatal(err)
	}

	// Block the real writer inside its INSERT, after it has entered SQL. This
	// deliberately holds only test locks; the scope provider never waits on SQL.
	const lockID = 911344321
	conn, err := d.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		t.Fatal(err)
	}
	unlocked := false
	unlock := func() {
		if unlocked {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cleanupCancel()
		if _, err := conn.ExecContext(cleanupCtx, "SELECT pg_advisory_unlock($1)", lockID); err != nil {
			t.Error("test barrier unlock failed")
			return
		}
		unlocked = true
	}
	defer unlock()
	function := fmt.Sprintf("finalization_scope_test_%d", task.ID)
	ddl := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(%d); RETURN NEW; END $$;
CREATE TRIGGER %s BEFORE INSERT ON e3_evidence_manifests FOR EACH ROW EXECUTE FUNCTION %s()`, function, lockID, function, function)
	if _, err := d.ExecContext(ctx, ddl); err != nil {
		t.Fatal("test barrier setup failed")
	}
	defer func() {
		unlock()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cleanupCancel()
		if _, err := d.ExecContext(cleanupCtx, "DROP FUNCTION "+function+"() CASCADE"); err != nil {
			t.Error("test barrier cleanup failed")
		}
	}()
	lease, revoke := context.WithCancel(ctx)
	defer revoke()
	released := make(chan struct{})
	scope := &finalizationPGScope{ctx: lease, released: released}
	c := &controlledFindingCollector{collected: true, input: in, digest: source.EvidenceDigest, createdAt: source.RecordCreatedAt, markers: markers, save: d.RecordControlledFinding,
		request: ControlledReadVerificationRequest{Scope: expected.Scope}, plan: ControlledReadFindingPlan{Expected: expected, RequireFinalizationScope: true}}
	c.plan.FinalizationScope = &finalizationTestProvider{acquire: func(context.Context, ControlledReadVerificationRequest, evidencebind.Context) (ControlledReadFinalizationScope, error) {
		return scope, nil
	}}
	c.plan.Source = func(context.Context, evidencebind.Context) (e3record.Source, error) { return source, nil }
	done := make(chan error, 1)
	go func() {
		done <- c.finalize(ctx, ControlledReadVerificationEvidence{Request: c.request, Completed: true, Finalized: true, FinalCheckpoint: checkpoint})
	}()
	// Ensure goroutine completion before closing DB, even on an assertion failure.
	finished := false
	defer func() {
		if !finished {
			cancel()
			unlock()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("save did not stop after cancellation")
			}
		}
	}()
	for {
		var waiting bool
		if err := d.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND objid=$1 AND NOT granted)", lockID).Scan(&waiting); err != nil {
			t.Fatal("barrier observation failed")
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			finished = true
			t.Fatalf("save ended before SQL barrier: %v", err)
		case <-ctx.Done():
			t.Fatal("SQL barrier timeout")
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case <-released:
		t.Fatal("scope released while real SQL save blocked")
	default:
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", lockID); err != nil {
		t.Fatal(err)
	}
	unlocked = true
	select {
	case err := <-done:
		finished = true
		if err != nil {
			t.Fatalf("real save failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("save completion timeout")
	}
	select {
	case <-released:
	default:
		t.Fatal("scope not released after commit")
	}
	for _, table := range []string{"e3_evidence_manifests", "e3_verdict_issuances", "e3_verdict_links", "findings"} {
		var count int
		if err := d.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE task_id=$1", task.ID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s rows=%d queryFailed=%v", table, count, err != nil)
		}
	}
}

type finalizationPGScope struct {
	ctx      context.Context
	released chan struct{}
}

func (s *finalizationPGScope) Context() context.Context        { return s.ctx }
func (s *finalizationPGScope) Check(ctx context.Context) error { return ctx.Err() }
func (s *finalizationPGScope) Release()                        { close(s.released) }
