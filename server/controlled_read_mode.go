package server

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/taskmode"
)

// ControlledTaskModeHost comes only from trusted Go bootstrap. CheckRoot must
// quickly check the already-established deployment/root/writer generation; a
// pathname or a newly created directory is not proof of that trust.
type ControlledTaskModeHost struct {
	RootPath   string
	RootAnchor *os.File
	CheckRoot  func(context.Context) error
	// CheckTaskQuiescent quickly validates independently established task/child
	// drain evidence. It must not initiate or wait for drain under these calls.
	CheckTaskQuiescent func(context.Context, int64) error
}

type controlledTaskModeConfig struct {
	host      ControlledTaskModeHost
	lifecycle context.Context
}

type controlledReadTransition struct{ key controlledWorkKey }

func (s *Server) ConfigureControlledTaskMode(host ControlledTaskModeHost) error {
	if s == nil || s.ctx == nil || s.ctx.Err() != nil || s.engine == nil || s.m == nil || s.m.pg == nil ||
		host.RootAnchor == nil || host.CheckRoot == nil || host.CheckTaskQuiescent == nil || !filepath.IsAbs(host.RootPath) || filepath.Clean(host.RootPath) != host.RootPath {
		return errControlledReadUnavailable
	}
	r := s.engine.controlledReads
	if r == nil || r.store == nil || filepath.Clean(r.store.stateDir) != host.RootPath || host.CheckRoot(s.ctx) != nil {
		return errControlledReadUnavailable
	}
	info, err := host.RootAnchor.Stat()
	current, pathErr := os.Lstat(host.RootPath)
	if err != nil || pathErr != nil || info.Mode() != os.ModeDir|0o700 ||
		current.Mode() != os.ModeDir|0o700 || !os.SameFile(info, current) {
		return errControlledReadUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.mode != nil || len(r.pending) != 0 || len(r.active) != 0 {
		return errControlledReadUnavailable
	}
	r.mode = &controlledTaskModeConfig{host: host, lifecycle: s.ctx}
	return nil
}

func (r *controlledReadRuntime) taskModeMarkers(taskID int64) (db.TaskModeMarkerReader, error) {
	if r == nil || r.mode == nil || taskID <= 0 {
		return nil, errControlledReadUnavailable
	}
	mode := r.mode
	return newControlledTaskMarkerReader(controlledTaskMarkerReaderOptions{
		RootPath: mode.host.RootPath, RootAnchor: mode.host.RootAnchor, MaxEntries: maxTaskMarkerEntries,
		CheckScope: func(ctx context.Context, actual int64) error {
			if ctx == nil || ctx.Err() != nil || actual != taskID || mode.lifecycle.Err() != nil || mode.host.CheckRoot(ctx) != nil {
				return errControlledReadUnavailable
			}
			return nil
		},
	})
}

// ControlledTaskModeMarkers is a trusted storage-host callback, not an API
// authority. The storage host must invoke it under its live task-row transaction
// through db.TaskModeReader; the callback itself does not acquire SQL locks.
func (s *Server) ControlledTaskModeMarkers() (db.TaskModeMarkerReader, error) {
	if s == nil || s.engine == nil || s.engine.controlledReads == nil || s.engine.controlledReads.mode == nil {
		return nil, errControlledReadUnavailable
	}
	r := s.engine.controlledReads
	return func(ctx context.Context, taskID int64) (taskmode.Marker, error) {
		reader, err := r.taskModeMarkers(taskID)
		if err != nil {
			return taskmode.Marker{}, err
		}
		return reader(ctx, taskID)
	}, nil
}

func (e *Engine) resolveControlledTaskMode(ctx context.Context, task string) (taskmode.Decision, error) {
	if e == nil || e.m == nil || e.m.pg == nil || e.controlledReads == nil || e.controlledReads.mode == nil ||
		ctx == nil || ctx.Err() != nil {
		return taskmode.Decision{}, errControlledReadUnavailable
	}
	id, err := strconv.ParseInt(task, 10, 64)
	if err != nil || id <= 0 {
		return taskmode.Decision{}, errControlledReadUnavailable
	}
	markers, err := e.controlledReads.taskModeMarkers(id)
	if err != nil {
		return taskmode.Decision{}, errControlledReadUnavailable
	}
	tx, err := e.m.pg.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return taskmode.Decision{}, errControlledReadUnavailable
	}
	defer tx.Rollback()
	decision, err := taskmode.Resolve(ctx, id, db.TaskModeReader(tx, markers))
	if err != nil || decision.Route() == taskmode.Block || tx.Commit() != nil {
		return taskmode.Decision{}, errControlledReadUnavailable
	}
	return decision, nil
}

func (e *Engine) controlledTransitionBlocks(task string) bool {
	r := e.controlledReads
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed || r.quiescing[task]
}

// Registration may retry a previously blocked transition, while every ordinary
// task operation continues to respect that task's closed admission gate.
func (e *Engine) beginControlledReadRegistration(task string) bool {
	e.deleteMu.RLock()
	defer e.deleteMu.RUnlock()
	if e.IsDeleting(task) {
		return false
	}
	e.incControlledRegistration(task)
	return true
}

func (e *Engine) registerControlledReadMode(ctx context.Context, task *Task, intentID int64, snapshotRef, expectedState string) (err error) {
	r := e.controlledReads
	if ctx == nil || task == nil || r == nil || r.mode == nil || r.store == nil || e.m == nil || e.m.pg == nil {
		return errControlledReadUnavailable
	}
	taskID, parseErr := strconv.ParseInt(task.ID, 10, 64)
	if parseErr != nil || taskID <= 0 {
		return errControlledReadUnavailable
	}
	key := controlledWorkKey{task.ID, intentID}
	// deleteMu is also the admission-registration barrier. No DB/file I/O is
	// performed while it or the short runtime registry lock is held.
	e.deleteMu.Lock()
	r.mu.Lock()
	lifecycle := task.lifecycleSnapshot()
	busy := r.closed || ctx.Err() != nil || !e.IsPaused(task.ID) || !lifecycle.Paused ||
		isTerminalStatus(lifecycle.Status) || e.IsDeleting(task.ID) || e.inflightCount(task.ID) != 1
	for active := range r.active {
		busy = busy || active.task == task.ID
	}
	if busy {
		r.mu.Unlock()
		e.deleteMu.Unlock()
		return errControlledReadNotPaused
	}
	token := &controlledReadTransition{key: key}
	r.tokens[key] = token
	r.quiescing[task.ID] = true
	if previous, exists := r.pending[key]; exists {
		clear(previous.Key)
		delete(r.pending, key)
	}
	r.mu.Unlock()
	e.deleteMu.Unlock()
	var loaded controlledReadSpec
	published := false
	defer func() {
		if !published {
			clear(loaded.Key)
			r.mu.Lock()
			if r.tokens[key] == token {
				delete(r.tokens, key)
			}
			// A failed transition stays blocked; only a new checked registration
			// may reopen it. Never roll the durable fence/marker back to legacy.
			r.mu.Unlock()
		}
	}()
	ctx = firstDiagnosisRegistrationContext(ctx, task.ID)
	if r.mode.host.CheckRoot(ctx) != nil || r.mode.host.CheckTaskQuiescent(ctx, taskID) != nil {
		return errControlledReadUnavailable
	}
	// Transaction A commits the monotonic fence before a marker or capability.
	if _, err := e.m.pg.RegisterTaskMode(ctx, taskID, taskmode.ModeControlled, nil); err != nil {
		return errControlledReadUnavailable
	}
	var mapRegistration string
	if r.mapStore != nil {
		mapRegistration, err = r.mapStore.invalidate(ctx, task.ID)
		if err != nil {
			return errControlledReadUnavailable
		}
	}
	loaded, err = r.store.load(task.ID, intentID, snapshotRef)
	if err != nil || validateControlledReadSpec(loaded) != nil {
		return errControlledReadUnavailable
	}
	loaded.mapRegistration = mapRegistration
	markers, err := r.taskModeMarkers(taskID)
	if err != nil {
		return errControlledReadUnavailable
	}
	// Transaction B owns task -> marker -> intent CAS, without a registry lock.
	tx, err := e.m.pg.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return errControlledReadUnavailable
	}
	defer tx.Rollback()
	fence, err := db.RegisterTaskModeTx(ctx, tx, taskID, taskmode.ModeControlled, nil)
	if err != nil || r.mode.host.CheckRoot(ctx) != nil || r.store.mark(task.ID, intentID) != nil {
		return errControlledReadUnavailable
	}
	decision, err := taskmode.Resolve(ctx, taskID, db.TaskModeReader(tx, markers))
	if err != nil || decision.Route() != taskmode.Controlled || fence.Version == 0 {
		return errControlledReadUnavailable
	}
	var changed int64
	err = tx.QueryRowContext(ctx, `UPDATE exploration_nodes AS n SET state='open'
FROM tasks AS t WHERE t.id=$1 AND n.exploration_id=t.exploration_id
AND n.id=$2 AND n.kind='intent' AND n.state=$3 RETURNING n.id`, taskID, intentID, expectedState).Scan(&changed)
	if err != nil || changed != intentID || tx.Commit() != nil {
		return errControlledReadUnavailable
	}
	if r.mode.host.CheckRoot(ctx) != nil || r.mode.host.CheckTaskQuiescent(ctx, taskID) != nil {
		return errControlledReadUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	lifecycle = task.lifecycleSnapshot()
	if r.closed || ctx.Err() != nil || r.tokens[key] != token || !e.IsPaused(task.ID) || !lifecycle.Paused ||
		isTerminalStatus(lifecycle.Status) || e.IsDeleting(task.ID) || e.inflightCount(task.ID) != 1 {
		return errControlledReadUnavailable
	}
	if r.mapStore != nil && r.mapStore.register(ctx, loaded) != nil {
		return errControlledReadUnavailable
	}
	r.pending[key] = loaded
	delete(r.tokens, key)
	delete(r.quiescing, task.ID)
	published = true
	return nil
}
