package auditcheckpoint

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"

	"github.com/Autumn-27/artex/transmit/auditlog"
)

//go:embed schema.sql
var schema string

// Migrate is explicit and transactional. It neither connects global db.Open nor
// provisions authority/epoch rows, roles, transport, or operational permissions.
func Migrate(ctx context.Context, db *sql.DB) error {
	if ctx == nil || db == nil {
		return ErrConfig
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ErrStorage
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SET LOCAL synchronous_commit=on`); err != nil {
		return ErrStorage
	}
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return ErrStorage
	}
	if err := tx.Commit(); err != nil {
		return ErrCommitUnknown
	}
	return nil
}

type Store struct {
	db     *sql.DB
	read   queryer
	host   Host
	limits auditlog.ExtensionLimits
	commit func(*sql.Tx) error
}

func New(db *sql.DB, host Host, limits auditlog.ExtensionLimits) (*Store, error) {
	if db == nil || isNil(host) || limits.MaxBytes <= 0 || limits.MaxBytes > auditlog.MaxExtensionBytes ||
		limits.MaxEvents == 0 || limits.MaxEvents > auditlog.MaxExtensionEvents || limits.Timeout <= 0 || limits.Timeout > auditlog.MaxExtensionTimeout {
		return nil, ErrConfig
	}
	return &Store{db: db, read: db, host: host, limits: limits, commit: (*sql.Tx).Commit}, nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Func, reflect.Chan, reflect.Slice, reflect.Interface:
		return v.IsNil()
	}
	return false
}

func (s *Store) acquire(ctx context.Context, access Access) (context.Context, Lease, func(), error) {
	if s == nil || s.db == nil || isNil(s.host) || ctx == nil || ctx.Err() != nil || !validEpoch(access.Epoch) {
		return nil, nil, nil, ErrAuthority
	}
	lease, err := s.host.Acquire(ctx, access)
	if err != nil || isNil(lease) {
		return nil, nil, nil, ErrAuthority
	}
	if lease.Context() == nil || lease.Context().Err() != nil || lease.Epoch() != access.Epoch {
		lease.Release()
		return nil, nil, nil, ErrAuthority
	}
	bounded, cancel := context.WithTimeout(ctx, s.limits.Timeout)
	stop := context.AfterFunc(lease.Context(), cancel)
	cleanup := func() { stop(); cancel(); lease.Release() }
	if bounded.Err() != nil {
		cleanup()
		return nil, nil, nil, ErrAuthority
	}
	return bounded, lease, cleanup, nil
}

func validCheckpoint(cp auditlog.Checkpoint) bool {
	if cp.Version != auditlog.Version || !validRef(cp.KeyRef) || len(cp.JournalID) != 32 || len(cp.MAC) != 64 {
		return false
	}
	if _, err := hex.DecodeString(cp.JournalID); err != nil {
		return false
	}
	if _, err := hex.DecodeString(cp.MAC); err != nil {
		return false
	}
	for _, r := range []string{cp.Scope.RunRef, cp.Scope.TaskRef, cp.Scope.CaseRef, cp.Scope.ResetRef, cp.Scope.PolicyRef} {
		if !validRef(r) {
			return false
		}
	}
	return true
}

func validRequest(r Request) bool {
	if !validEpoch(r.Epoch) || !validRef(r.OperationRef) || !validRef(r.PublisherRef) || !validCheckpoint(r.Candidate) || r.ExpectedRevision >= math.MaxInt64 {
		return false
	}
	switch r.Kind {
	case Register:
		return r.ExpectedRevision == 0 && r.Previous == (auditlog.Checkpoint{}) && r.Candidate.Sequence == 0
	case Append, Seal, MarkIncomplete, Quarantine:
		return r.ExpectedRevision > 0 && validCheckpoint(r.Previous) && binding(r.PublisherRef, r.Previous) == binding(r.PublisherRef, r.Candidate)
	default:
		return false
	}
}

// Publish consumes an owned bounded snapshot and verifies it before taking any
// SQL row/advisory lock. For incomplete/quarantine the candidate must be the
// already accepted prefix and source must be nil; partial journals are never
// truncated or reclassified as verified complete input.
func (s *Store) Publish(ctx context.Context, request Request, source io.Reader) (Receipt, error) {
	if !validRequest(request) {
		return Receipt{}, ErrRequest
	}
	access := Access{Purpose: PublishAccess, Epoch: request.Epoch, Binding: binding(request.PublisherRef, request.Candidate), OperationRef: request.OperationRef, Publication: request}
	ctx, lease, release, err := s.acquire(ctx, access)
	if err != nil {
		return Receipt{}, err
	}
	defer release()
	if err := checkEpoch(ctx, s.read, request.Epoch, false); err != nil {
		return Receipt{}, err
	}
	var snapshot []byte
	if request.Kind == MarkIncomplete || request.Kind == Quarantine {
		if source != nil || request.Candidate != request.Previous {
			return Receipt{}, ErrRequest
		}
	} else {
		if source == nil {
			return Receipt{}, ErrRequest
		}
		snapshot, err = io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, source: source}, s.limits.MaxBytes+1))
		defer clear(snapshot)
		if err != nil || ctx.Err() != nil || int64(len(snapshot)) > s.limits.MaxBytes {
			return Receipt{}, ErrVerification
		}
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return Receipt{}, ErrRequest
	}
	snapshotDigest := sha256.Sum256(snapshot)
	digest := sha256.Sum256(append(payload, snapshotDigest[:]...))
	if receipt, found, err := operation(ctx, s.read, access, &digest); err != nil || found {
		return receipt, err
	}

	var previous auditlog.Checkpoint
	if request.Kind == Register {
		previous = request.Candidate
	} else {
		head, err := head(ctx, s.read, access)
		if err == nil && head.Phase != Open {
			err = ErrTerminal
		}
		if err == nil && (head.Revision != request.ExpectedRevision || head.Checkpoint != request.Previous) {
			err = ErrConflict
		}
		if err != nil {
			// An identical operation can commit after the first receipt miss but
			// before this head read. Its atomic receipt is now visible too.
			if receipt, found, retryErr := operation(ctx, s.read, access, &digest); retryErr != nil || found {
				return receipt, retryErr
			}
			return Receipt{}, err
		}
		previous = head.Checkpoint
	}
	if request.Kind == Register || request.Kind == Append || request.Kind == Seal {
		key, err := lease.ResolveKey(ctx, access.Binding)
		if err != nil || len(key) != auditlog.KeyBytes || ctx.Err() != nil {
			return Receipt{}, ErrAuthority
		}
		ownedKey := bytes.Clone(key)
		_, err = auditlog.VerifyExtension(ctx, bytes.NewReader(snapshot), ownedKey, previous, request.Candidate, s.limits)
		clear(ownedKey)
		if err != nil || ctx.Err() != nil {
			return Receipt{}, ErrVerification
		}
	}
	return s.publishVerified(ctx, request, digest)
}

// Head and Operation are separately authorized. A receipt replay never causes
// the historical operation's revision to be presented as the current head.
func (s *Store) Head(ctx context.Context, query Query) (Head, error) {
	access, err := queryAccess(query, HeadAccess)
	if err != nil {
		return Head{}, err
	}
	ctx, _, release, err := s.acquire(ctx, access)
	if err != nil {
		return Head{}, err
	}
	defer release()
	if err := checkEpoch(ctx, s.read, query.Epoch, false); err != nil {
		return Head{}, err
	}
	return head(ctx, s.read, access)
}

func (s *Store) Operation(ctx context.Context, query Query) (Receipt, error) {
	access, err := queryAccess(query, OperationAccess)
	if err != nil {
		return Receipt{}, err
	}
	ctx, _, release, err := s.acquire(ctx, access)
	if err != nil {
		return Receipt{}, err
	}
	defer release()
	if err := checkEpoch(ctx, s.read, query.Epoch, false); err != nil {
		return Receipt{}, err
	}
	r, found, err := operation(ctx, s.read, access, nil)
	if err == nil && !found {
		err = ErrNotFound
	}
	return r, err
}

func queryAccess(q Query, purpose Purpose) (Access, error) {
	cp := auditlog.Checkpoint{Version: auditlog.Version, Scope: q.Binding.Scope, JournalID: q.Binding.JournalID, KeyRef: q.Binding.KeyRef, MAC: string(bytes.Repeat([]byte{'0'}, 64))}
	if !validEpoch(q.Epoch) || !validRef(q.Binding.PublisherRef) || !validCheckpoint(cp) || purpose == OperationAccess && !validRef(q.OperationRef) || purpose == HeadAccess && q.OperationRef != "" {
		return Access{}, ErrRequest
	}
	return Access{Purpose: purpose, Epoch: q.Epoch, Binding: q.Binding, OperationRef: q.OperationRef}, nil
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func checkEpoch(ctx context.Context, q queryer, e Epoch, locked bool) error {
	query := `SELECT 1 FROM audit_checkpoint_epochs WHERE store_ref=$1 AND epoch_ref=$2 AND generation=$3 AND anchor_digest=$4`
	if locked {
		query += ` FOR SHARE`
	}
	var one int
	err := q.QueryRowContext(ctx, query, e.StoreRef, e.EpochRef, int64(e.Generation), e.AnchorDigest[:]).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrEpoch
	}
	if err != nil {
		return ErrStorage
	}
	return nil
}

const headColumns = `h.publisher_ref,h.revision,h.phase,h.checkpoint,h.recorded_at`
const currentEpochJoin = ` JOIN audit_checkpoint_epochs AS e ON e.store_ref=h.store_ref AND e.epoch_ref=h.epoch_ref AND e.generation=h.generation AND e.anchor_digest=h.anchor_digest AND e.generation=$3 AND e.anchor_digest=$4 `

func scanHead(row *sql.Row, epoch Epoch) (Head, error) {
	h := Head{Epoch: epoch}
	var revision int64
	var raw []byte
	err := row.Scan(&h.PublisherRef, &revision, &h.Phase, &raw, &h.RecordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Head{}, ErrNotFound
	}
	if err != nil || revision <= 0 || json.Unmarshal(raw, &h.Checkpoint) != nil {
		return Head{}, ErrStorage
	}
	h.Revision = uint64(revision)
	return h, nil
}

func head(ctx context.Context, q queryer, a Access) (Head, error) {
	query := `SELECT ` + headColumns + ` FROM audit_checkpoint_heads AS p JOIN audit_checkpoint_history AS h USING(store_ref,run_ref,revision)` + currentEpochJoin + ` WHERE p.store_ref=$1 AND h.epoch_ref=$2 AND p.run_ref=$5`
	h, err := scanHead(q.QueryRowContext(ctx, query, a.Epoch.StoreRef, a.Epoch.EpochRef, int64(a.Epoch.Generation), a.Epoch.AnchorDigest[:], a.Binding.Scope.RunRef), a.Epoch)
	if err == nil && !matches(a.Binding, h) {
		return Head{}, ErrConflict
	}
	return h, err
}

func operation(ctx context.Context, q queryer, a Access, digest *[32]byte) (Receipt, bool, error) {
	query := `SELECT o.receipt_ref,o.request_digest,o.committed_at,` + headColumns + ` FROM audit_checkpoint_operations AS o JOIN audit_checkpoint_history AS h USING(store_ref,run_ref,revision)` + currentEpochJoin + ` WHERE o.store_ref=$1 AND o.epoch_ref=$2 AND o.operation_ref=$5`
	r := Receipt{OperationRef: a.OperationRef, Head: Head{Epoch: a.Epoch}}
	var raw, storedDigest []byte
	var revision int64
	err := q.QueryRowContext(ctx, query, a.Epoch.StoreRef, a.Epoch.EpochRef, int64(a.Epoch.Generation), a.Epoch.AnchorDigest[:], a.OperationRef).Scan(&r.ReceiptRef, &storedDigest, &r.CommittedAt, &r.Head.PublisherRef, &revision, &r.Head.Phase, &raw, &r.Head.RecordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, false, nil
	}
	if err != nil || revision <= 0 || len(storedDigest) != 32 || json.Unmarshal(raw, &r.Head.Checkpoint) != nil {
		return Receipt{}, false, ErrStorage
	}
	r.Head.Revision = uint64(revision)
	if !matches(a.Binding, r.Head) || digest != nil && !bytes.Equal(storedDigest, digest[:]) {
		return Receipt{}, false, ErrConflict
	}
	return r, true, nil
}

func (s *Store) publishVerified(ctx context.Context, req Request, digest [32]byte) (Receipt, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Receipt{}, ErrStorage
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SET LOCAL synchronous_commit=on`); err != nil {
		return Receipt{}, ErrStorage
	}
	// Only database work follows these locks. A host changing authority must
	// revoke/wait for leases before taking this row; no inverse drain dependency.
	if err := checkEpoch(ctx, tx, req.Epoch, true); err != nil {
		return Receipt{}, err
	}
	lockRef, err := json.Marshal([]string{req.Epoch.StoreRef, req.Epoch.EpochRef, req.OperationRef})
	if err != nil {
		return Receipt{}, ErrRequest
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(lockRef)); err != nil {
		return Receipt{}, ErrStorage
	}
	a := Access{Epoch: req.Epoch, Binding: binding(req.PublisherRef, req.Candidate), OperationRef: req.OperationRef}
	if receipt, found, err := operation(ctx, tx, a, &digest); err != nil || found {
		return receipt, err
	}
	h, headErr := head(ctx, tx, a)
	if headErr != nil && !errors.Is(headErr, ErrNotFound) {
		return Receipt{}, headErr
	}
	phase, revision := Open, req.ExpectedRevision+1
	noOp := false
	if req.Kind == Register {
		revision = 1
		if headErr == nil {
			if h.Revision != 1 || h.Phase != Open || h.Checkpoint != req.Candidate {
				return Receipt{}, ErrConflict
			}
			noOp = true
		}
	} else {
		if headErr != nil {
			return Receipt{}, ErrConflict
		}
		if h.Phase != Open {
			return Receipt{}, ErrTerminal
		}
		if h.Revision != req.ExpectedRevision || h.Checkpoint != req.Previous {
			return Receipt{}, ErrConflict
		}
		switch req.Kind {
		case Seal:
			phase = Sealed
		case MarkIncomplete:
			phase = Incomplete
		case Quarantine:
			phase = Quarantined
		}
		noOp = req.Kind == Append && req.Candidate == req.Previous
		if noOp {
			revision = h.Revision
		}
	}
	if !noOp {
		raw, err := json.Marshal(req.Candidate)
		if err != nil {
			return Receipt{}, ErrRequest
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO audit_checkpoint_history(store_ref,run_ref,revision,epoch_ref,generation,anchor_digest,publisher_ref,phase,checkpoint) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`, req.Epoch.StoreRef, req.Candidate.Scope.RunRef, int64(revision), req.Epoch.EpochRef, int64(req.Epoch.Generation), req.Epoch.AnchorDigest[:], req.PublisherRef, phase, raw)
		if err != nil {
			return Receipt{}, ErrStorage
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return Receipt{}, ErrConflict
		}
	}
	var result sql.Result
	if req.Kind == Register && !noOp {
		result, err = tx.ExecContext(ctx, `INSERT INTO audit_checkpoint_heads(store_ref,run_ref,epoch_ref,journal_id,revision) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, req.Epoch.StoreRef, req.Candidate.Scope.RunRef, req.Epoch.EpochRef, req.Candidate.JournalID, int64(revision))
	} else {
		expected := req.ExpectedRevision
		if req.Kind == Register {
			expected = 1
		}
		result, err = tx.ExecContext(ctx, `UPDATE audit_checkpoint_heads SET revision=$1 WHERE store_ref=$2 AND run_ref=$3 AND epoch_ref=$4 AND revision=$5 AND journal_id=$6`, int64(revision), req.Epoch.StoreRef, req.Candidate.Scope.RunRef, req.Epoch.EpochRef, int64(expected), req.Candidate.JournalID)
	}
	if err != nil {
		return Receipt{}, ErrStorage
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return Receipt{}, ErrConflict
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Receipt{}, ErrStorage
	}
	receiptRef := "cp-receipt:" + hex.EncodeToString(nonce[:])
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_checkpoint_operations(store_ref,epoch_ref,operation_ref,request_digest,receipt_ref,run_ref,revision) VALUES($1,$2,$3,$4,$5,$6,$7)`, req.Epoch.StoreRef, req.Epoch.EpochRef, req.OperationRef, digest[:], receiptRef, req.Candidate.Scope.RunRef, int64(revision)); err != nil {
		return Receipt{}, ErrStorage
	}
	r, found, err := operation(ctx, tx, a, &digest)
	if err != nil || !found {
		return Receipt{}, ErrStorage
	}
	if ctx.Err() != nil {
		return Receipt{}, ErrAuthority
	}
	if err := s.commit(tx); err != nil {
		return Receipt{}, ErrCommitUnknown
	}
	return r, nil
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
	empty  int
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.source.Read(p)
	if r.ctx.Err() != nil {
		return n, r.ctx.Err()
	}
	if n == 0 && err == nil {
		r.empty++
		if r.empty >= 100 {
			return 0, io.ErrNoProgress
		}
	} else {
		r.empty = 0
	}
	return n, err
}
