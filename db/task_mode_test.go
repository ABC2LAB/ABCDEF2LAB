package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/taskmode"
)

// These tests never use the shared/config-file DSN. A broken explicitly
// configured isolated database fails rather than appearing as a skipped pass.
func taskModeTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("ARTEX_TASK_MODE_TEST_DSN")
	if dsn == "" {
		t.Skip("set ARTEX_TASK_MODE_TEST_DSN to a dedicated PostgreSQL test database")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatal("dedicated task-mode PostgreSQL initialization failed")
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func taskModeTestTask(t *testing.T, d *DB) int64 {
	t.Helper()
	task, err := d.CreateTask("task mode fixture", "synthetic mode classification", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return task.ID
}

func taskModeTestTx(t *testing.T, d *DB) *sql.Tx {
	t.Helper()
	tx, err := d.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func modeMarker(state taskmode.MarkerState) TaskModeMarkerReader {
	return func(_ context.Context, id int64) (taskmode.Marker, error) {
		return taskmode.Marker{TaskID: id, State: state}, nil
	}
}

func TestTaskModeRegistration(t *testing.T) {
	d := taskModeTestDB(t)
	t.Run("explicit_legacy", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		r, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeLegacy, modeMarker(taskmode.MarkerClear))
		if err != nil || r.TaskID != id || r.Mode != taskmode.ModeLegacy || r.Version != 1 {
			t.Fatalf("legacy registration: record=%+v err=%v", r, err)
		}
		tx := taskModeTestTx(t, d)
		decision, err := taskmode.Resolve(t.Context(), id, TaskModeReader(tx, modeMarker(taskmode.MarkerClear)))
		if err != nil || decision.Route() != taskmode.Legacy {
			t.Fatalf("route=%v err=%v", decision.Route(), err)
		}
	})
	t.Run("controlled_before_marker", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		r, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeControlled, nil)
		if err != nil || r.Mode != taskmode.ModeControlled || r.Version != 1 {
			t.Fatalf("controlled registration: record=%+v err=%v", r, err)
		}
		tx := taskModeTestTx(t, d)
		decision, err := taskmode.Resolve(t.Context(), id, TaskModeReader(tx, modeMarker(taskmode.MarkerClear)))
		if err != nil || decision.Route() != taskmode.Block || decision.Reason() != taskmode.ReasonControlledPending {
			t.Fatalf("route=%v reason=%s err=%v", decision.Route(), decision.Reason(), err)
		}
	})
	t.Run("upgrade_and_idempotent_retry", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		if _, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeLegacy, modeMarker(taskmode.MarkerClear)); err != nil {
			t.Fatal(err)
		}
		r, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeControlled, nil)
		if err != nil || r.Version != 2 {
			t.Fatalf("upgrade record=%+v err=%v", r, err)
		}
		again, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeControlled, nil)
		if err != nil || again != r {
			t.Fatalf("idempotent record=%+v err=%v", again, err)
		}
	})
	t.Run("downgrade_rejected", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		if _, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeControlled, nil); err != nil {
			t.Fatal(err)
		}
		r, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeLegacy, modeMarker(taskmode.MarkerClear))
		if !errors.Is(err, ErrTaskModeDowngrade) || r != (taskmode.Record{}) {
			t.Fatalf("downgrade record=%+v err=%v", r, err)
		}
	})
}

func resolveMode(t *testing.T, d *DB, id int64, marker TaskModeMarkerReader) (taskmode.Decision, error) {
	t.Helper()
	tx := taskModeTestTx(t, d)
	defer tx.Rollback()
	return taskmode.Resolve(t.Context(), id, TaskModeReader(tx, marker))
}

func modeRowCount(t *testing.T, d *DB, id int64) int {
	t.Helper()
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM task_control_modes WHERE task_id=$1`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestTaskModeLegacyRequiresVerifiedClearMarker(t *testing.T) {
	d := taskModeTestDB(t)
	cause := errors.New("PRIVATE_MARKER_DETAIL")
	cases := []struct {
		name string
		read TaskModeMarkerReader
	}{
		{"nil", nil},
		{"unknown", modeMarker(taskmode.MarkerUnknown)},
		{"present", modeMarker(taskmode.MarkerPresent)},
		{"invalid", modeMarker(255)},
		{"wrong_task", func(_ context.Context, id int64) (taskmode.Marker, error) {
			return taskmode.Marker{TaskID: id + 1, State: taskmode.MarkerClear}, nil
		}},
		{"error_with_clear_value", func(_ context.Context, id int64) (taskmode.Marker, error) {
			return taskmode.Marker{TaskID: id, State: taskmode.MarkerClear}, cause
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := taskModeTestTask(t, d)
			r, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeLegacy, tc.read)
			if err == nil || r != (taskmode.Record{}) || modeRowCount(t, d, id) != 0 {
				t.Fatalf("failed marker permitted registration: record=%+v err=%v", r, err)
			}
			if tc.name == "error_with_clear_value" {
				assertModePrivateError(t, err, cause)
			}
		})
	}
}

func assertModePrivateError(t *testing.T, err, cause error) {
	t.Helper()
	if !errors.Is(err, cause) || !errors.Is(err, ErrTaskModeStore) {
		t.Fatal("lost inspectable cause/category")
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, err), cause.Error()) {
			t.Fatal("error disclosed marker details")
		}
	}
	data, marshalErr := json.Marshal(err)
	if marshalErr != nil || strings.Contains(string(data), cause.Error()) {
		t.Fatal("error JSON disclosed marker details")
	}
}

func TestTaskModeReaderFailClosed(t *testing.T) {
	d := taskModeTestDB(t)
	t.Run("unregistered_is_not_legacy", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		got, err := resolveMode(t, d, id, modeMarker(taskmode.MarkerClear))
		if err != nil || got.Route() != taskmode.Block || got.Reason() != taskmode.ReasonRecordMissing {
			t.Fatalf("route=%v reason=%s err=%v", got.Route(), got.Reason(), err)
		}
	})
	for _, tc := range []struct {
		name string
		read TaskModeMarkerReader
	}{
		{"nil_marker_reader", nil},
		{"marker_missing", modeMarker(taskmode.MarkerClear)},
		{"marker_unknown", modeMarker(taskmode.MarkerUnknown)},
		{"marker_invalid", modeMarker(255)},
		{"wrong_marker_task", func(_ context.Context, id int64) (taskmode.Marker, error) {
			return taskmode.Marker{TaskID: id + 1, State: taskmode.MarkerPresent}, nil
		}},
		{"marker_read_error", func(_ context.Context, id int64) (taskmode.Marker, error) {
			return taskmode.Marker{TaskID: id, State: taskmode.MarkerPresent}, errors.New("PRIVATE_READER_DETAIL")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := taskModeTestTask(t, d)
			if _, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeControlled, nil); err != nil {
				t.Fatal(err)
			}
			got, err := resolveMode(t, d, id, tc.read)
			if got.Route() != taskmode.Block {
				t.Fatalf("route=%v err=%v", got.Route(), err)
			}
		})
	}
	t.Run("controlled_ready", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		if _, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeControlled, nil); err != nil {
			t.Fatal(err)
		}
		got, err := resolveMode(t, d, id, modeMarker(taskmode.MarkerPresent))
		if err != nil || got.Route() != taskmode.Controlled {
			t.Fatalf("route=%v err=%v", got.Route(), err)
		}
	})
	t.Run("missing_task", func(t *testing.T) {
		got, err := resolveMode(t, d, 1<<60, modeMarker(taskmode.MarkerClear))
		if got.Route() != taskmode.Block || !errors.Is(err, ErrTaskModeTaskAbsent) {
			t.Fatalf("route=%v err=%v", got.Route(), err)
		}
	})
	t.Run("missing_table", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		tx := taskModeTestTx(t, d)
		if _, err := tx.Exec(`ALTER TABLE task_control_modes RENAME TO task_control_modes_hidden`); err != nil {
			t.Fatal(err)
		}
		got, err := taskmode.Resolve(t.Context(), id, TaskModeReader(tx, modeMarker(taskmode.MarkerClear)))
		if got.Route() != taskmode.Block || !errors.Is(err, ErrTaskModeStore) {
			t.Fatalf("route=%v err=%v", got.Route(), err)
		}
	})
	t.Run("closed_transaction", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		tx := taskModeTestTx(t, d)
		_ = tx.Rollback()
		got, err := taskmode.Resolve(t.Context(), id, TaskModeReader(tx, modeMarker(taskmode.MarkerClear)))
		if got.Route() != taskmode.Block || !errors.Is(err, sql.ErrTxDone) {
			t.Fatalf("route=%v err=%v", got.Route(), err)
		}
	})
	t.Run("nil_transaction", func(t *testing.T) {
		got, err := taskmode.Resolve(t.Context(), 17, TaskModeReader(nil, modeMarker(taskmode.MarkerClear)))
		if got.Route() != taskmode.Block || !errors.Is(err, ErrTaskModeInput) {
			t.Fatalf("route=%v err=%v", got.Route(), err)
		}
	})
}

func TestTaskModeTransactionAndLifecycle(t *testing.T) {
	d := taskModeTestDB(t)
	t.Run("rollback_keeps_missing", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		tx := taskModeTestTx(t, d)
		if _, err := RegisterTaskModeTx(t.Context(), tx, id, taskmode.ModeControlled, nil); err != nil {
			t.Fatal(err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if modeRowCount(t, d, id) != 0 {
			t.Fatal("uncommitted fence was published")
		}
	})
	for _, level := range []sql.IsolationLevel{sql.LevelReadUncommitted, sql.LevelRepeatableRead, sql.LevelSerializable} {
		t.Run("reject_"+level.String(), func(t *testing.T) {
			id := taskModeTestTask(t, d)
			tx, err := d.BeginTx(t.Context(), &sql.TxOptions{Isolation: level})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := RegisterTaskModeTx(t.Context(), tx, id, taskmode.ModeControlled, nil); !errors.Is(err, ErrTaskModeIsolation) {
				t.Fatalf("register err=%v", err)
			}
			got, err := taskmode.Resolve(t.Context(), id, TaskModeReader(tx, modeMarker(taskmode.MarkerClear)))
			if got.Route() != taskmode.Block || !errors.Is(err, ErrTaskModeIsolation) {
				t.Fatalf("route=%v err=%v", got.Route(), err)
			}
		})
	}
	for _, column := range []string{"deleted_at", "archived_at"} {
		t.Run("reject_"+column, func(t *testing.T) {
			id := taskModeTestTask(t, d)
			// column comes from this fixed test list, never user input.
			if _, err := d.Exec(`UPDATE tasks SET `+column+`=now() WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
			if _, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeControlled, nil); !errors.Is(err, ErrTaskArchiveState) {
				t.Fatalf("err=%v", err)
			}
			got, err := resolveMode(t, d, id, modeMarker(taskmode.MarkerClear))
			if got.Route() != taskmode.Block || !errors.Is(err, ErrTaskArchiveState) {
				t.Fatalf("route=%v err=%v", got.Route(), err)
			}
		})
	}
	t.Run("schema_reapply_preserves_fence", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		r, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeControlled, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(taskModeSchemaSQL); err != nil {
			t.Fatal(err)
		}
		again, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeControlled, nil)
		if err != nil || again != r || modeRowCount(t, d, id) != 1 {
			t.Fatalf("record=%+v err=%v", again, err)
		}
	})
}

func TestTaskModeDatabaseGuards(t *testing.T) {
	d := taskModeTestDB(t)
	statements := []struct{ name, query string }{
		{"downgrade", `UPDATE task_control_modes SET mode='explicit_legacy',version=1 WHERE task_id=$1`},
		{"version_reset", `UPDATE task_control_modes SET version=2 WHERE task_id=$1`},
		{"identity_change", `UPDATE task_control_modes SET task_id=task_id+1000000 WHERE task_id=$1`},
		{"delete_fence", `DELETE FROM task_control_modes WHERE task_id=$1`},
		{"hard_delete_task", `DELETE FROM tasks WHERE id=$1`},
		{"soft_delete_task", `UPDATE tasks SET deleted_at=now() WHERE id=$1`},
		{"archive_task", `UPDATE tasks SET archived_at=now() WHERE id=$1`},
	}
	for _, tc := range statements {
		t.Run(tc.name, func(t *testing.T) {
			id := taskModeTestTask(t, d)
			if _, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeControlled, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := d.Exec(tc.query, id); err == nil {
				t.Fatal("database allowed unsafe mode/lifecycle mutation")
			}
			if modeRowCount(t, d, id) != 1 {
				t.Fatal("fence lost")
			}
		})
	}
	t.Run("truncate", func(t *testing.T) {
		if _, err := d.Exec(`TRUNCATE task_control_modes`); err == nil {
			t.Fatal("database allowed fence truncation")
		}
	})
}

func modeTestConnection(t *testing.T, d *DB) (*sql.Conn, *sql.Tx, int) {
	t.Helper()
	conn, err := d.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	var pid int
	if err := conn.QueryRowContext(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return conn, tx, pid
}

func waitModeLock(t *testing.T, d *DB, pid int) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		var blocked bool
		if err := d.QueryRow(`SELECT cardinality(pg_blocking_pids($1))>0`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatal("expected PostgreSQL row-lock wait was not observed")
		}
	}
}

type modeReadResult struct {
	decision taskmode.Decision
	err      error
}
type modeWriteResult struct {
	record taskmode.Record
	err    error
}

func TestTaskModeConcurrency(t *testing.T) {
	d := taskModeTestDB(t)
	t.Run("registration_before_reader", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		writer := taskModeTestTx(t, d)
		if _, err := RegisterTaskModeTx(t.Context(), writer, id, taskmode.ModeControlled, nil); err != nil {
			t.Fatal(err)
		}
		_, reader, pid := modeTestConnection(t, d)
		done := make(chan modeReadResult, 1)
		go func() {
			r, e := taskmode.Resolve(t.Context(), id, TaskModeReader(reader, modeMarker(taskmode.MarkerPresent)))
			done <- modeReadResult{r, e}
		}()
		waitModeLock(t, d, pid)
		if err := writer.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case r := <-done:
			if r.err != nil || r.decision.Route() != taskmode.Controlled {
				t.Fatalf("route=%v err=%v", r.decision.Route(), r.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reader did not finish")
		}
	})
	t.Run("reader_lock_until_commit", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		if _, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeLegacy, modeMarker(taskmode.MarkerClear)); err != nil {
			t.Fatal(err)
		}
		reader := taskModeTestTx(t, d)
		r, err := taskmode.Resolve(t.Context(), id, TaskModeReader(reader, modeMarker(taskmode.MarkerClear)))
		if err != nil || r.Route() != taskmode.Legacy {
			t.Fatal("legacy reader failed")
		}
		_, writer, pid := modeTestConnection(t, d)
		done := make(chan modeWriteResult, 1)
		go func() {
			r, e := RegisterTaskModeTx(t.Context(), writer, id, taskmode.ModeControlled, nil)
			done <- modeWriteResult{r, e}
		}()
		waitModeLock(t, d, pid)
		if err := reader.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case r := <-done:
			if r.err != nil || r.record.Version != 2 {
				t.Fatalf("record=%+v err=%v", r.record, r.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("registration did not finish")
		}
		if err := writer.Commit(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("different_tasks_do_not_block", func(t *testing.T) {
		first, second := taskModeTestTask(t, d), taskModeTestTask(t, d)
		reader := taskModeTestTx(t, d)
		if _, err := taskmode.Resolve(t.Context(), first, TaskModeReader(reader, modeMarker(taskmode.MarkerClear))); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { _, e := d.RegisterTaskMode(t.Context(), second, taskmode.ModeControlled, nil); done <- e }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("unrelated task blocked")
		}
	})
	t.Run("twenty_same_mode_registrations", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		const writers = 20
		start := make(chan struct{})
		results := make(chan modeWriteResult, writers)
		var wg sync.WaitGroup
		for range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				r, e := d.RegisterTaskMode(t.Context(), id, taskmode.ModeControlled, nil)
				results <- modeWriteResult{r, e}
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		for r := range results {
			if r.err != nil || r.record.TaskID != id || r.record.Mode != taskmode.ModeControlled || r.record.Version != 1 {
				t.Fatalf("record=%+v err=%v", r.record, r.err)
			}
		}
		if modeRowCount(t, d, id) != 1 {
			t.Fatal("duplicate mode records")
		}
		t.Log("writers=20 successes=20 mode_rows=1 version=1")
	})
	t.Run("cancel_reader_wait", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		holder := taskModeTestTx(t, d)
		if err := lockTaskModeTx(t.Context(), holder, id); err != nil {
			t.Fatal(err)
		}
		_, reader, pid := modeTestConnection(t, d)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan modeReadResult, 1)
		go func() {
			r, e := taskmode.Resolve(ctx, id, TaskModeReader(reader, modeMarker(taskmode.MarkerClear)))
			done <- modeReadResult{r, e}
		}()
		waitModeLock(t, d, pid)
		cancel()
		select {
		case r := <-done:
			if r.decision.Route() != taskmode.Block || !errors.Is(r.err, context.Canceled) {
				t.Fatalf("route=%v err=%v", r.decision.Route(), r.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("canceled reader did not finish")
		}
	})
	for _, first := range []taskmode.Mode{taskmode.ModeLegacy, taskmode.ModeControlled} {
		t.Run("mixed_registration_first_"+string(first), func(t *testing.T) {
			id := taskModeTestTask(t, d)
			holder := taskModeTestTx(t, d)
			if _, err := RegisterTaskModeTx(t.Context(), holder, id, first, modeMarker(taskmode.MarkerClear)); err != nil {
				t.Fatal(err)
			}
			second := taskmode.ModeControlled
			if first == taskmode.ModeControlled {
				second = taskmode.ModeLegacy
			}
			_, writer, pid := modeTestConnection(t, d)
			done := make(chan modeWriteResult, 1)
			go func() {
				r, e := RegisterTaskModeTx(t.Context(), writer, id, second, modeMarker(taskmode.MarkerClear))
				done <- modeWriteResult{r, e}
			}()
			waitModeLock(t, d, pid)
			if err := holder.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case r := <-done:
				if first == taskmode.ModeLegacy {
					if r.err != nil || r.record.Version != 2 || r.record.Mode != taskmode.ModeControlled {
						t.Fatalf("record=%+v err=%v", r.record, r.err)
					}
					if err := writer.Commit(); err != nil {
						t.Fatal(err)
					}
				} else {
					if !errors.Is(r.err, ErrTaskModeDowngrade) || r.record != (taskmode.Record{}) {
						t.Fatalf("record=%+v err=%v", r.record, r.err)
					}
					_ = writer.Rollback()
				}
			case <-time.After(5 * time.Second):
				t.Fatal("mixed registration did not finish")
			}
			got, err := resolveMode(t, d, id, modeMarker(taskmode.MarkerPresent))
			if err != nil || got.Route() != taskmode.Controlled || modeRowCount(t, d, id) != 1 {
				t.Fatalf("route=%v err=%v", got.Route(), err)
			}
		})
	}
	t.Run("marker_callback_runs_under_task_lock", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		writer := taskModeTestTx(t, d)
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		defer once.Do(func() { close(release) })
		written := make(chan modeWriteResult, 1)
		go func() {
			r, e := RegisterTaskModeTx(t.Context(), writer, id, taskmode.ModeLegacy, func(ctx context.Context, id int64) (taskmode.Marker, error) {
				close(entered)
				select {
				case <-release:
					return taskmode.Marker{TaskID: id, State: taskmode.MarkerClear}, nil
				case <-ctx.Done():
					return taskmode.Marker{}, ctx.Err()
				}
			})
			written <- modeWriteResult{r, e}
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("marker callback did not start")
		}
		_, reader, pid := modeTestConnection(t, d)
		readDone := make(chan modeReadResult, 1)
		go func() {
			r, e := taskmode.Resolve(t.Context(), id, TaskModeReader(reader, modeMarker(taskmode.MarkerClear)))
			readDone <- modeReadResult{r, e}
		}()
		waitModeLock(t, d, pid)
		once.Do(func() { close(release) })
		select {
		case r := <-written:
			if r.err != nil {
				t.Fatal(r.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("marker registration did not finish")
		}
		if err := writer.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case r := <-readDone:
			if r.err != nil || r.decision.Route() != taskmode.Legacy {
				t.Fatalf("route=%v err=%v", r.decision.Route(), r.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reader did not finish")
		}
	})
}

func TestTaskModeInvalidRequests(t *testing.T) {
	var missing *DB
	for _, tc := range []struct {
		name string
		ctx  context.Context
		id   int64
		mode taskmode.Mode
		want error
	}{
		{"nil_context", nil, 17, taskmode.ModeControlled, taskmode.ErrInvalidContext},
		{"zero_id", context.Background(), 0, taskmode.ModeControlled, ErrTaskModeInput},
		{"negative_id", context.Background(), -1, taskmode.ModeControlled, ErrTaskModeInput},
		{"unknown_mode", context.Background(), 17, taskmode.ModeUnknown, ErrTaskModeInput},
		{"invalid_mode", context.Background(), 17, "legacy", ErrTaskModeInput},
		{"missing_database", context.Background(), 17, taskmode.ModeControlled, ErrTaskModeStore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := missing.RegisterTaskMode(tc.ctx, tc.id, tc.mode, nil)
			if !errors.Is(err, tc.want) || r != (taskmode.Record{}) {
				t.Fatalf("record=%+v err=%v", r, err)
			}
		})
	}
	t.Run("nil_transaction", func(t *testing.T) {
		r, err := RegisterTaskModeTx(t.Context(), nil, 17, taskmode.ModeControlled, nil)
		if !errors.Is(err, ErrTaskModeInput) || r != (taskmode.Record{}) {
			t.Fatalf("record=%+v err=%v", r, err)
		}
	})
	t.Run("canceled_context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		r, err := missing.RegisterTaskMode(ctx, 17, taskmode.ModeControlled, nil)
		if !errors.Is(err, context.Canceled) || r != (taskmode.Record{}) {
			t.Fatalf("record=%+v err=%v", r, err)
		}
	})
}

func TestTaskModeArchiveAndCommitFailure(t *testing.T) {
	d := taskModeTestDB(t)
	for _, operation := range []string{"archive_reassignment", "archive_delete"} {
		t.Run(operation, func(t *testing.T) {
			id := taskModeTestTask(t, d)
			// A failed legacy archive may predate promotion to controlled mode.
			if _, err := d.Exec(`INSERT INTO task_archives(task_id,state) VALUES($1,$2)`, id, ArchiveFailed); err != nil {
				t.Fatal(err)
			}
			if _, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeControlled, nil); err != nil {
				t.Fatal(err)
			}
			var err error
			if operation == "archive_reassignment" {
				other := taskModeTestTask(t, d)
				_, err = d.Exec(`UPDATE task_archives SET task_id=$2 WHERE task_id=$1`, id, other)
			} else {
				_, err = d.Exec(`DELETE FROM task_archives WHERE task_id=$1`, id)
			}
			if err == nil {
				t.Fatal("controlled archive provenance could be changed or deleted")
			}
			var count int
			if err := d.QueryRow(`SELECT count(*) FROM task_archives WHERE task_id=$1`, id).Scan(&count); err != nil || count != 1 {
				t.Fatal("archive provenance changed")
			}
		})
	}
	t.Run("queued_archive_prevents_registration", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		if _, err := d.Exec(`INSERT INTO task_archives(task_id) VALUES($1)`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeControlled, nil); !errors.Is(err, ErrTaskArchiveState) {
			t.Fatalf("err=%v", err)
		}
		got, err := resolveMode(t, d, id, modeMarker(taskmode.MarkerClear))
		if got.Route() != taskmode.Block || !errors.Is(err, ErrTaskArchiveState) {
			t.Fatalf("route=%v err=%v", got.Route(), err)
		}
	})
	t.Run("controlled_archive_is_rejected", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		if _, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeControlled, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`INSERT INTO task_archives(task_id) VALUES($1)`, id); err == nil {
			t.Fatal("controlled archive accepted")
		}
		var count int
		if err := d.QueryRow(`SELECT count(*) FROM task_archives WHERE task_id=$1`, id).Scan(&count); err != nil || count != 0 {
			t.Fatal("partial archive inserted")
		}
	})
	t.Run("controlled_task_deletion_preserves_finding_source", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		if _, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeControlled, nil); err != nil {
			t.Fatal(err)
		}
		finding, err := d.AddFinding(id, 0, "synthetic_class", "", "low", "synthetic source binding", "non-sensitive fixture", "fixture", nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.DeleteTask(id); err == nil {
			t.Fatal("controlled task deletion accepted")
		}
		var taskID sql.NullInt64
		if err := d.QueryRow(`SELECT task_id FROM findings WHERE id=$1`, finding).Scan(&taskID); err != nil || !taskID.Valid || taskID.Int64 != id {
			t.Fatal("finding lost controlled source")
		}
	})
	t.Run("commit_failure_returns_no_record", func(t *testing.T) {
		id := taskModeTestTask(t, d)
		_, err := d.Exec(`CREATE FUNCTION taskmode_test_commit_failure() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'synthetic_commit_failure'; END; $$ LANGUAGE plpgsql;
CREATE CONSTRAINT TRIGGER taskmode_test_commit_failure AFTER INSERT ON task_control_modes DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION taskmode_test_commit_failure();`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := d.Exec(`DROP TRIGGER taskmode_test_commit_failure ON task_control_modes; DROP FUNCTION taskmode_test_commit_failure()`); err != nil {
				t.Error("commit-failure fixture cleanup failed")
			}
		}()
		r, err := d.RegisterTaskMode(t.Context(), id, taskmode.ModeControlled, nil)
		if !errors.Is(err, ErrTaskModeStore) || r != (taskmode.Record{}) || modeRowCount(t, d, id) != 0 {
			t.Fatalf("record=%+v err=%v", r, err)
		}
	})
}
