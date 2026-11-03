package evidencerevision

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"reflect"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/taskmode"
)

//go:embed schema.sql
var schema string

// Migrate is explicit. Existing application task/mode/intent tables must exist;
// it never connects global db.Open or provisions an actual trust authority.
func Migrate(ctx context.Context, d *sql.DB) error {
	if ctx == nil || d == nil {
		return ErrInput
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return ErrStore
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SET LOCAL synchronous_commit=on`); err != nil {
		return ErrStore
	}
	if _, err = tx.ExecContext(ctx, schema); err != nil {
		return ErrStore
	}
	if err = tx.Commit(); err != nil {
		return ErrOutcomeUnknown
	}
	return nil
}
func nilCapability(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Func, reflect.Chan, reflect.Interface, reflect.Slice:
		return r.IsNil()
	}
	return false
}
func New(d *sql.DB, p ScopeProvider) (*Store, error) {
	if d == nil || nilCapability(p) {
		return nil, ErrInput
	}
	return &Store{db: d, provider: p, commit: (*sql.Tx).Commit}, nil
}

func (s *Store) Issue(ctx context.Context, r Request) (Receipt, error) {
	return s.apply(ctx, r, IssueInitial)
}
func (s *Store) Lookup(ctx context.Context, r Request) (Receipt, error) {
	return s.apply(ctx, r, ReadReceipt)
}

func (s *Store) apply(ctx context.Context, r Request, purpose Purpose) (Receipt, error) {
	if s == nil || s.db == nil || nilCapability(s.provider) || ctx == nil || ctx.Err() != nil {
		return Receipt{}, ErrScope
	}
	access, digest, err := requestInfo(r, purpose)
	if err != nil {
		return Receipt{}, err
	}
	lease, err := s.provider.Acquire(ctx, access)
	if err != nil || nilCapability(lease) {
		return Receipt{}, ErrScope
	}
	defer lease.Release()
	leaseCtx := lease.Context()
	if leaseCtx == nil || leaseCtx.Err() != nil {
		return Receipt{}, ErrScope
	}
	// Snapshot/host/file callbacks finish before taking database row locks.
	marker := lease.MarkerSnapshot()
	if purpose == IssueInitial && (marker.TaskID != access.TaskID || marker.State != taskmode.MarkerPresent) {
		return Receipt{}, ErrMode
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(leaseCtx, cancel)
	defer func() { stop(); cancel() }()
	if leaseCtx.Err() != nil {
		return Receipt{}, ErrScope
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Receipt{}, ErrStore
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SET LOCAL synchronous_commit=on`); err != nil {
		return Receipt{}, ErrStore
	}
	if err := checkFacts(ctx, tx, r, access, marker, purpose == IssueInitial); err != nil {
		return Receipt{}, err
	}
	if purpose == ReadReceipt {
		out, found, err := readOperation(ctx, tx, r, access, digest)
		if err != nil {
			return Receipt{}, err
		}
		if !found {
			return Receipt{}, ErrNotFound
		}
		if ctx.Err() != nil {
			return Receipt{}, ErrScope
		}
		return out, nil
	}
	// Serialize identical operation retries even if the requested RunRef differs.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "evidence-operation:"+r.OperationRef); err != nil {
		return Receipt{}, ErrStore
	}
	if out, found, err := readOperation(ctx, tx, r, access, digest); err != nil || found {
		return out, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "evidence-initial:"+access.Scope.RunRef); err != nil {
		return Receipt{}, ErrStore
	}
	var revision int64
	var previous []byte
	err = tx.QueryRowContext(ctx, `SELECT revision,input_digest FROM evidence_revision_initials WHERE execution_ref=$1`, access.Scope.RunRef).Scan(&revision, &previous)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `INSERT INTO evidence_revision_initials(execution_ref,task_id,exploration_id,intent_id,mode_version,binding,input_digest,evidence_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING revision`, access.Scope.RunRef, access.TaskID, r.ExpectedExplorationID, access.IntentID, int64(r.ExpectedModeVersion), r.Collected.binding, digest[:], r.Collected.digest[:]).Scan(&revision)
		if err != nil {
			return Receipt{}, ErrStore
		}
	} else if err != nil {
		return Receipt{}, ErrStore
	} else if !bytes.Equal(previous, digest[:]) {
		return Receipt{}, ErrConflict
	}
	if revision <= 0 {
		return Receipt{}, ErrStore
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO evidence_revision_operations(operation_ref,revision,request_digest) VALUES($1,$2,$3)`, r.OperationRef, revision, digest[:]); err != nil {
		return Receipt{}, ErrStore
	}
	out, found, err := readOperation(ctx, tx, r, access, digest)
	if err != nil || !found {
		return Receipt{}, ErrStore
	}
	if ctx.Err() != nil {
		return Receipt{}, ErrScope
	}
	if err := s.commit(tx); err != nil {
		return Receipt{}, ErrOutcomeUnknown
	}
	return out, nil
}

func checkFacts(ctx context.Context, tx *sql.Tx, r Request, a Access, marker taskmode.Marker, issuing bool) error {
	var exploration int64
	var deleted sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT exploration_id,deleted_at FROM tasks WHERE id=$1 FOR UPDATE`, a.TaskID).Scan(&exploration, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBinding
	}
	if err != nil {
		return ErrStore
	}
	if exploration != r.ExpectedExplorationID {
		return ErrBinding
	}
	if issuing {
		if deleted.Valid {
			return ErrBinding
		}
		var archive string
		err := tx.QueryRowContext(ctx, `SELECT state FROM task_archives WHERE task_id=$1`, a.TaskID).Scan(&archive)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return ErrStore
		}
		if err == nil && archive != db.ArchiveFailed {
			return ErrBinding
		}
		var mode string
		var version int64
		err = tx.QueryRowContext(ctx, `SELECT mode,version FROM task_control_modes WHERE task_id=$1 FOR UPDATE`, a.TaskID).Scan(&mode, &version)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMode
		}
		if err != nil {
			return ErrStore
		}
		decision := taskmode.Decide(a.TaskID, taskmode.Snapshot{Record: taskmode.Record{TaskID: a.TaskID, Mode: taskmode.Mode(mode), Version: uint64(version)}, Marker: marker})
		if version <= 0 || uint64(version) != r.ExpectedModeVersion || decision.Route() != taskmode.Controlled {
			return ErrMode
		}
	}
	var kind string
	err = tx.QueryRowContext(ctx, `SELECT kind FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 FOR UPDATE`, a.IntentID, exploration).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBinding
	}
	if err != nil {
		return ErrStore
	}
	if kind != "intent" {
		return ErrBinding
	}
	return nil
}

func readOperation(ctx context.Context, tx *sql.Tx, r Request, a Access, digest [32]byte) (Receipt, bool, error) {
	var out Receipt
	var revision, modeVersion int64
	var requestDigest, inputDigest, evidenceDigest []byte
	err := tx.QueryRowContext(ctx, `SELECT o.operation_ref,o.request_digest,o.recorded_at,i.revision,i.task_id,i.exploration_id,i.intent_id,i.mode_version,i.input_digest,i.evidence_digest,i.issued_at FROM evidence_revision_operations o JOIN evidence_revision_initials i USING(revision) WHERE o.operation_ref=$1`, r.OperationRef).Scan(&out.OperationRef, &requestDigest, &out.RecordedAt, &revision, &out.TaskID, &out.ExplorationID, &out.IntentID, &modeVersion, &inputDigest, &evidenceDigest, &out.IssuedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, false, nil
	}
	if err != nil {
		return Receipt{}, false, ErrStore
	}
	if !bytes.Equal(requestDigest, digest[:]) || !bytes.Equal(inputDigest, digest[:]) || !bytes.Equal(evidenceDigest, r.Collected.digest[:]) || out.TaskID != a.TaskID || out.ExplorationID != r.ExpectedExplorationID || out.IntentID != a.IntentID || modeVersion != int64(r.ExpectedModeVersion) {
		return Receipt{}, false, ErrConflict
	}
	if revision <= 0 {
		return Receipt{}, false, ErrStore
	}
	out.Revision, out.ModeVersion = uint64(revision), uint64(modeVersion)
	out.Scope = a.Scope
	out.EvidenceDigest = r.Collected.digest
	out.BindingDigest = digest
	return out, true, nil
}
