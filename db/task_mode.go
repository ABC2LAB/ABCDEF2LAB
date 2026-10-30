package db

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"

	"github.com/Autumn-27/artex/taskmode"
)

//go:embed task_mode_schema.sql
var taskModeSchemaSQL string

var (
	ErrTaskModeStore      = errors.New("task_mode_store_unavailable")
	ErrTaskModeInput      = errors.New("task_mode_input_invalid")
	ErrTaskModeDowngrade  = errors.New("task_mode_downgrade_rejected")
	ErrTaskModeMarker     = errors.New("task_mode_marker_unverified")
	ErrTaskModeIsolation  = errors.New("task_mode_requires_read_committed")
	ErrTaskModeTaskAbsent = errors.New("task_mode_task_unavailable")
)

// TaskModeMarkerReader must inspect server-owned markers under the same task
// registration synchronization as this transaction. Errors are never clear.
// It runs with the task row locked: do not acquire evidence/global locks, start
// another transaction, or wait for registration from inside this callback.
type TaskModeMarkerReader func(context.Context, int64) (taskmode.Marker, error)

// RegisterTaskMode commits a monotonic mode fence before the host creates any
// execution marker/capability. Only explicit legacy registration needs a clear
// marker. A returned fence is not permission to transmit or confirm a finding.
func (d *DB) RegisterTaskMode(ctx context.Context, taskID int64, mode taskmode.Mode, markers TaskModeMarkerReader) (taskmode.Record, error) {
	if err := validateTaskModeRequest(ctx, taskID, mode); err != nil {
		return taskmode.Record{}, err
	}
	if d == nil || d.DB == nil {
		return taskmode.Record{}, ErrTaskModeStore
	}
	tx, err := d.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return taskmode.Record{}, taskModeStoreError{cause: err}
	}
	defer tx.Rollback()
	record, err := RegisterTaskModeTx(ctx, tx, taskID, mode, markers)
	if err != nil {
		return taskmode.Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return taskmode.Record{}, taskModeStoreError{cause: err}
	}
	return record, nil
}

// RegisterTaskModeTx retains the task lock until the caller commits/rolls back.
// Its successful Record is uncommitted; never publish it as a committed fence.
func RegisterTaskModeTx(ctx context.Context, tx *sql.Tx, taskID int64, mode taskmode.Mode, markers TaskModeMarkerReader) (taskmode.Record, error) {
	if err := validateTaskModeRequest(ctx, taskID, mode); err != nil {
		return taskmode.Record{}, err
	}
	if err := lockTaskModeTx(ctx, tx, taskID); err != nil {
		return taskmode.Record{}, err
	}
	old, err := readTaskModeRecordTx(ctx, tx, taskID)
	if err != nil {
		return taskmode.Record{}, err
	}
	if old.Mode == taskmode.ModeControlled && mode == taskmode.ModeLegacy {
		return taskmode.Record{}, ErrTaskModeDowngrade
	}
	if mode == taskmode.ModeLegacy {
		if markers == nil {
			return taskmode.Record{}, ErrTaskModeMarker
		}
		marker, err := markers(ctx, taskID)
		if err != nil {
			return taskmode.Record{}, taskModeStoreError{cause: err}
		}
		check := taskmode.Decide(taskID, taskmode.Snapshot{
			Record: taskmode.Record{TaskID: taskID, Mode: mode, Version: 1}, Marker: marker,
		})
		if check.Route() != taskmode.Legacy {
			return taskmode.Record{}, ErrTaskModeMarker
		}
	}
	if err := ctx.Err(); err != nil {
		return taskmode.Record{}, err
	}
	if old.Mode == mode {
		return old, nil
	}
	var row *sql.Row
	if old == (taskmode.Record{}) {
		row = tx.QueryRowContext(ctx, `INSERT INTO task_control_modes(task_id,mode,version)
VALUES($1,$2,1) RETURNING task_id,mode,version`, taskID, string(mode))
	} else {
		row = tx.QueryRowContext(ctx, `UPDATE task_control_modes SET mode=$2,version=version+1
WHERE task_id=$1 AND mode='explicit_legacy' AND version=$3 RETURNING task_id,mode,version`, taskID, string(mode), int64(old.Version))
	}
	return scanTaskModeRecord(row)
}

// TaskModeReader binds Resolve to the caller's live READ COMMITTED transaction.
// The caller keeps it open through admission/write. For finding writes, acquire
// the existing evidence advisory lock before this reader takes the task lock.
func TaskModeReader(tx *sql.Tx, markers TaskModeMarkerReader) taskmode.Reader {
	return func(ctx context.Context, taskID int64) (taskmode.Snapshot, error) {
		if err := lockTaskModeTx(ctx, tx, taskID); err != nil {
			return taskmode.Snapshot{}, err
		}
		record, err := readTaskModeRecordTx(ctx, tx, taskID)
		if err != nil || record == (taskmode.Record{}) {
			return taskmode.Snapshot{}, err
		}
		if markers == nil {
			return taskmode.Snapshot{}, ErrTaskModeMarker
		}
		marker, err := markers(ctx, taskID)
		if err != nil {
			return taskmode.Snapshot{}, taskModeStoreError{cause: err}
		}
		if err := ctx.Err(); err != nil {
			return taskmode.Snapshot{}, err
		}
		return taskmode.Snapshot{Record: record, Marker: marker}, nil
	}
}

func validateTaskModeRequest(ctx context.Context, taskID int64, mode taskmode.Mode) error {
	if ctx == nil {
		return taskmode.ErrInvalidContext
	}
	if taskID <= 0 || mode != taskmode.ModeLegacy && mode != taskmode.ModeControlled {
		return ErrTaskModeInput
	}
	return ctx.Err()
}

func lockTaskModeTx(ctx context.Context, tx *sql.Tx, taskID int64) error {
	if ctx == nil {
		return taskmode.ErrInvalidContext
	}
	if tx == nil || taskID <= 0 {
		return ErrTaskModeInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// A separate mode query after the lock must see the latest commit, not a
	// REPEATABLE READ snapshot taken before another registration completed.
	var isolation string
	if err := tx.QueryRowContext(ctx, `SHOW transaction_isolation`).Scan(&isolation); err != nil {
		return taskModeStoreError{cause: err}
	}
	if isolation != "read committed" {
		return ErrTaskModeIsolation
	}
	var deleted, archived sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT deleted_at,archived_at FROM tasks WHERE id=$1 FOR UPDATE`, taskID).Scan(&deleted, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTaskModeTaskAbsent
	}
	if err != nil {
		return taskModeStoreError{cause: err}
	}
	if deleted.Valid || archived.Valid {
		return ErrTaskArchiveState
	}
	var archiving bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_archives WHERE task_id=$1 AND state<>$2)`, taskID, ArchiveFailed).Scan(&archiving); err != nil {
		return taskModeStoreError{cause: err}
	}
	if archiving {
		return ErrTaskArchiveState
	}
	return nil
}

func readTaskModeRecordTx(ctx context.Context, tx *sql.Tx, taskID int64) (taskmode.Record, error) {
	record, err := scanTaskModeRecord(tx.QueryRowContext(ctx, `SELECT task_id,mode,version FROM task_control_modes WHERE task_id=$1`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return taskmode.Record{}, nil
	}
	return record, err
}

func scanTaskModeRecord(row *sql.Row) (taskmode.Record, error) {
	var record taskmode.Record
	var version int64
	err := row.Scan(&record.TaskID, &record.Mode, &version)
	if err != nil {
		return taskmode.Record{}, taskModeStoreError{cause: err}
	}
	if record.TaskID <= 0 || version <= 0 || record.Mode != taskmode.ModeLegacy && record.Mode != taskmode.ModeControlled {
		return taskmode.Record{}, ErrTaskModeInput
	}
	record.Version = uint64(version)
	return record, nil
}

// Keep database/marker diagnostics inspectable without printing query or path
// details in the ordinary host/model error channel.
type taskModeStoreError struct{ cause error }

func (e taskModeStoreError) Error() string        { return ErrTaskModeStore.Error() }
func (e taskModeStoreError) Unwrap() error        { return e.cause }
func (e taskModeStoreError) Is(target error) bool { return target == ErrTaskModeStore }
func (e taskModeStoreError) GoString() string     { return "db.taskModeStoreError{cause:[REDACTED]}" }
