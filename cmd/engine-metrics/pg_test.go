package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const fixtureSecret = "synthetic-account-cookie-token-summary-never-output"

type queryTrace struct {
	on, readonly, repeatable, unexpected atomic.Bool
	rollbacks, commits                   atomic.Int64
}

func (q *queryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !q.on.Load() {
		return ctx
	}
	s := strings.ToLower(strings.TrimSpace(data.SQL))
	switch {
	case strings.HasPrefix(s, "begin"):
		q.readonly.Store(strings.Contains(s, "read only"))
		q.repeatable.Store(strings.Contains(s, "repeatable read"))
	case strings.HasPrefix(s, "select"), strings.HasPrefix(s, "with"):
	case strings.HasPrefix(s, "rollback"):
		q.rollbacks.Add(1)
	case strings.HasPrefix(s, "commit"):
		q.commits.Add(1)
	default:
		q.unexpected.Store(true)
	}
	return ctx
}
func (*queryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type pgFixture struct {
	db    *sql.DB
	dsn   string
	trace *queryTrace
}

func newPGFixture(t *testing.T) *pgFixture {
	t.Helper()
	dsn := os.Getenv("ENGINE_METRICS_TEST_DSN")
	if dsn == "" {
		t.Skip("owned PostgreSQL DSN absent; DB validation not run")
	}
	if !regexp.MustCompile(`^codex-owned-pg-v1:[a-f0-9]{64}$`).MatchString(os.Getenv("ENGINE_METRICS_OWNED_TEST_PG")) {
		t.Fatal("owned test cluster guard required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("fixture configuration")
	}
	admin := stdlib.OpenDB(*cfg)
	name := "engine_metrics_fixture_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.ExecContext(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close()
		t.Fatal("create owned fixture DB")
	}
	copyCfg := cfg.Copy()
	copyCfg.Database = name
	f := &pgFixture{trace: &queryTrace{}}
	copyCfg.Tracer = f.trace
	copyCfg.RuntimeParams["search_path"] = "pg_catalog"
	f.db = stdlib.OpenDB(*copyCfg)
	f.db.SetMaxOpenConns(2)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("fixture URL")
	}
	u.Path = "/" + name
	f.dsn = u.String()
	t.Cleanup(func() {
		if f.db.Close() != nil {
			t.Error("fixture pool close")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error("owned fixture DB cleanup")
		}
		if admin.Close() != nil {
			t.Error("fixture admin close")
		}
	})
	// Minimal projection of dev db/schema.sql, with nullable legacy/corrupt
	// timestamps/states to exercise reporting boundaries beyond current constraints.
	f.exec(t, `CREATE TABLE public.explorations(id BIGINT PRIMARY KEY,goal TEXT);
CREATE TABLE public.tasks(id BIGINT PRIMARY KEY,exploration_id BIGINT UNIQUE,name TEXT);
CREATE TABLE public.exploration_nodes(id BIGINT PRIMARY KEY,exploration_id BIGINT,kind TEXT,state TEXT,created_at TIMESTAMPTZ,completed_at TIMESTAMPTZ,payload JSONB);
CREATE TABLE public.activity(exploration_id BIGINT,worker TEXT,kind TEXT,input_tokens INTEGER,output_tokens INTEGER,cache_read_tokens INTEGER,cache_write_tokens INTEGER,summary TEXT,detail TEXT,metadata JSONB);
INSERT INTO public.explorations VALUES(10,'`+fixtureSecret+`'),(20,'other'),(30,'empty');
INSERT INTO public.tasks VALUES(1,10,'`+fixtureSecret+`'),(2,20,'other');`)
	return f
}

func (f *pgFixture) exec(t *testing.T, q string) {
	t.Helper()
	if _, err := f.db.ExecContext(t.Context(), q); err != nil {
		t.Fatal("owned fixture SQL")
	}
}
func (f *pgFixture) run(t *testing.T, args ...string) report {
	t.Helper()
	var out, errOut bytes.Buffer
	if run(t.Context(), args, func(string) string { return f.dsn }, &out, &errOut) != 0 || errOut.Len() != 0 {
		t.Fatal("CLI fixture query failed")
	}
	if strings.Contains(out.String(), fixtureSecret) || strings.Contains(out.String(), f.dsn) {
		t.Fatal("private source escaped")
	}
	var result report
	if json.Unmarshal(out.Bytes(), &result) != nil {
		t.Fatal("report JSON")
	}
	return result
}

func TestPGKnownCountsScopeAndPrivacy(t *testing.T) {
	f := newPGFixture(t)
	f.exec(t, `INSERT INTO public.exploration_nodes VALUES
(1,10,'intent','done','2026-01-01','2026-01-01 00:00:10','{"secret":"`+fixtureSecret+`"}'),
(2,10,'intent','blocked','2026-01-01','2026-01-01 00:00:30','{}'),
(3,10,'intent','running','2026-01-01',NULL,'{}'),
(4,10,'intent','stopped','2026-01-01',NULL,'{}'),
(5,10,'intent','paused',NULL,NULL,'{}'),
(6,10,'intent','exhausted','2026-01-01','2025-12-31 23:59:55','{}'),
(7,10,'intent','open','2026-01-01',NULL,'{}'),
(8,10,'intent','deleted','2026-01-01',NULL,'{}'),
(9,10,'intent','done','2026-01-01',NULL,'{}'),
(10,10,'intent','`+fixtureSecret+`','2026-01-01','2026-01-01 00:00:20','{}'),
(11,10,'intent',NULL,'2026-01-01',NULL,'{}'),
(12,10,'intent','done','infinity','infinity','{}'),
(13,10,'fact','done','2026-01-01','2026-01-02','{}'),
(14,20,'intent','done','2026-01-01','2026-01-02','{}');
INSERT INTO public.activity(exploration_id,worker,kind,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES
(10,'planner','round',NULL,NULL,NULL,NULL),
(10,'worker','result',100,10,NULL,0),
(10,'planner','result',NULL,20,4,NULL),
(10,'`+fixtureSecret+`','text',5,NULL,NULL,NULL),
(10,'worker','result',-2,0,0,0),
(10,'worker','round',NULL,NULL,NULL,NULL),
(20,'planner','round',99999,99999,99999,99999);
UPDATE public.activity SET summary='`+fixtureSecret+`',detail='`+fixtureSecret+`',metadata='{"secret":"`+fixtureSecret+`"}';`)
	r := f.run(t, "--task-id", "1")
	i := r.Intents
	if r.ExplorationID != "10" || r.TaskID == nil || *r.TaskID != "1" || i.Recorded != 12 || i.States.Done != 3 || i.States.Running != 1 || i.States.Paused != 1 || i.States.Blocked != 1 || i.States.Stopped != 1 || i.States.Unknown != 1 || i.States.Null != 1 {
		t.Fatal("scope/state counts")
	}
	if i.MissingCreated != 1 || i.MissingCompleted != 7 || i.TerminalMissingCompleted != 2 || i.NegativeElapsed != 1 || i.NonFiniteTime != 1 || i.Elapsed.RecordedPairs != 3 {
		t.Fatal("timestamp boundaries")
	}
	if i.Elapsed.MinimumSeconds == nil || *i.Elapsed.MinimumSeconds != 10 || *i.Elapsed.MaximumSeconds != 30 || *i.Elapsed.MeanSeconds != 20 || *i.Elapsed.SumSeconds != 60 {
		t.Fatal("elapsed including wait values")
	}
	a, b := r.AllActivity, r.ResultActivity
	if a.Recorded != 6 || a.PlannerRounds != 1 || b.Recorded != 3 || b.PlannerRounds != 0 || a.Input.RecordedSum == nil || *a.Input.RecordedSum != 103 || a.Input.Missing != 3 || a.Input.Negative != 1 || *b.Input.RecordedSum != 98 || b.Input.Missing != 1 {
		t.Fatal("observed activities/input tokens")
	}
	if *b.Output.RecordedSum != 30 || b.Output.Missing != 0 || *b.CacheRead.RecordedSum != 4 || b.CacheRead.Missing != 1 || *b.CacheWrite.RecordedSum != 0 || b.CacheWrite.Missing != 1 {
		t.Fatal("output/cache token NULL versus zero")
	}
	other := f.run(t, "--exploration-id", "20")
	if other.Intents.Recorded != 1 || other.AllActivity.Recorded != 1 || *other.AllActivity.Input.RecordedSum != 99999 {
		t.Fatal("other exploration isolation")
	}
}

func TestPGEmptyAndMissingScope(t *testing.T) {
	f := newPGFixture(t)
	r := f.run(t, "--exploration-id", "30")
	if r.TaskID != nil || r.Intents.Recorded != 0 || r.Intents.Elapsed.RecordedPairs != 0 || r.Intents.Elapsed.MeanSeconds != nil || r.AllActivity.Recorded != 0 || r.AllActivity.Input.RecordedSum != nil || r.AllActivity.Input.Missing != 0 || r.ResultActivity.Output.RecordedSum != nil {
		t.Fatal("empty scope became invented zeros")
	}
	for _, flag := range []string{"--task-id", "--exploration-id"} {
		var out, errOut bytes.Buffer
		if run(t.Context(), []string{flag, "999"}, func(string) string { return f.dsn }, &out, &errOut) == 0 || out.Len() != 0 || !strings.Contains(errOut.String(), "scope_not_found") {
			t.Fatal("missing scope became empty success")
		}
	}
}

func TestPGCancellationAndQueryDeadline(t *testing.T) {
	f := newPGFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out, errOut bytes.Buffer
	if run(ctx, []string{"--task-id", "1"}, func(string) string { return f.dsn }, &out, &errOut) == 0 || out.Len() != 0 || !strings.Contains(errOut.String(), "cancelled") {
		t.Fatal("cancelled query succeeded")
	}
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal("fixture lock tx")
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(t.Context(), "LOCK TABLE public.exploration_nodes IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal("fixture lock")
	}
	var lockerPID int
	if tx.QueryRowContext(t.Context(), "SELECT pg_catalog.pg_backend_pid()").Scan(&lockerPID) != nil {
		t.Fatal("fixture locker identity")
	}
	// A new CLI connection could time out before reaching the locked SELECT.
	// Warm a victim connection and observe the exact query waiting for a Lock
	// in this owned database before accepting the deadline failure.
	f.db.SetMaxOpenConns(3)
	var one int
	if f.db.QueryRowContext(t.Context(), "SELECT 1").Scan(&one) != nil {
		t.Fatal("warm victim connection")
	}
	victimCtx, victimCancel := context.WithTimeout(t.Context(), time.Second)
	defer victimCancel()
	type outcome struct {
		result report
		err    error
	}
	done := make(chan outcome, 1)
	go func() { r, e := collect(victimCtx, f.db, selection{task: 1}); done <- outcome{r, e} }()
	observed := false
	probeCtx, probeCancel := context.WithTimeout(t.Context(), 800*time.Millisecond)
	defer probeCancel()
	for !observed && probeCtx.Err() == nil {
		// pg_stat_activity normally truncates query text at 1024 bytes. Combine
		// a fixed prefix with the actual locker's PID instead of requiring the
		// entire statement, and leave the server setting unchanged.
		probeErr := f.db.QueryRowContext(probeCtx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_stat_activity
WHERE datname=pg_catalog.current_database() AND wait_event_type='Lock'
AND pg_catalog.left(query,256)=pg_catalog.left($1,256)
AND $2::integer=ANY(pg_catalog.pg_blocking_pids(pid)))`, intentsSQL, lockerPID).Scan(&observed)
		if probeErr != nil {
			break
		}
		if !observed {
			select {
			case <-probeCtx.Done():
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	got := <-done
	if !observed || got.err == nil || got.err.Error() != "deadline_exceeded" || got.result.ExplorationID != "" {
		t.Fatalf("locked intent SELECT boundary: observed=%v error=%v", observed, got.err)
	}
}

func TestPGRejectsUnexpectedEffectiveSearchPath(t *testing.T) {
	f := newPGFixture(t)
	f.db.SetMaxOpenConns(1)
	f.exec(t, "SET search_path=public")
	r, err := collect(t.Context(), f.db, selection{task: 1})
	if err == nil || err.Error() != "connection_configuration_failed" || r.ExplorationID != "" {
		t.Fatal("unexpected effective lookup path accepted")
	}
}

func TestPGReadOnlyAndNoWrites(t *testing.T) {
	f := newPGFixture(t)
	f.trace.on.Store(true)
	if _, err := collect(t.Context(), f.db, selection{task: 1}); err != nil {
		t.Fatal("actual read-only collect")
	}
	f.trace.on.Store(false)
	if !f.trace.readonly.Load() || !f.trace.repeatable.Load() || f.trace.rollbacks.Load() != 1 || f.trace.commits.Load() != 0 || f.trace.unexpected.Load() {
		t.Fatal("driver transaction/write boundary")
	}
	tx, err := f.db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal("readonly probe")
	}
	_, err = tx.ExecContext(t.Context(), "INSERT INTO public.explorations VALUES(40,'probe')")
	if err == nil {
		_ = tx.Rollback()
		t.Fatal("server permitted write in readonly transaction")
	}
	_ = tx.Rollback()
	var count int
	if f.db.QueryRowContext(t.Context(), "SELECT count(*) FROM public.explorations").Scan(&count) != nil || count != 3 {
		t.Fatal("fixture data modified")
	}
}

func TestPGQueryFailureDoesNotLeak(t *testing.T) {
	f := newPGFixture(t)
	f.exec(t, `INSERT INTO public.activity(exploration_id,input_tokens) VALUES(10,1);
ALTER TABLE public.activity RENAME TO private_activity;
CREATE FUNCTION public.private_failure() RETURNS INTEGER LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION '`+fixtureSecret+`'; END $$;
CREATE VIEW public.activity AS SELECT exploration_id,worker,kind,public.private_failure() AS input_tokens,output_tokens,cache_read_tokens,cache_write_tokens FROM public.private_activity;`)
	var out, errOut bytes.Buffer
	if run(t.Context(), []string{"--task-id", "1"}, func(string) string { return f.dsn }, &out, &errOut) == 0 || out.Len() != 0 || errOut.String() != "{\"error\":\"query_failed\"}\n" || strings.Contains(errOut.String(), fixtureSecret) {
		t.Fatal("private driver exception escaped or partial success")
	}
}
