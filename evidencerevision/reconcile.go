package evidencerevision

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strconv"

	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/transmit"
)

const DescriptorVersion = 1

// Descriptor is non-sensitive reconciliation data, not authority or a collected
// observation. Persist it durably in protected host metadata BEFORE Issue. A
// lost descriptor cannot be reconstructed from a journal as proof of an exact
// operation. It can never be used to construct Collected or issue a revision.
type Descriptor struct {
	Version       int               `json:"version"`
	OperationRef  string            `json:"operation_ref"`
	TaskID        int64             `json:"task_id,string"`
	ExplorationID int64             `json:"exploration_id,string"`
	IntentID      int64             `json:"intent_id,string"`
	Scope         transmit.Scope    `json:"scope"`
	ModeVersion   uint64            `json:"mode_version,string"`
	RequestDigest [sha256.Size]byte `json:"request_digest"`
}

// Describe consumes no database state and grants no permission. It requires a
// valid opaque Collected; actual private-collector provenance remains a host
// precondition, not a fact authenticated by this function.
func Describe(r Request) (Descriptor, error) {
	a, digest, err := requestInfo(r, ReadReceipt)
	if err != nil {
		return Descriptor{}, err
	}
	return Descriptor{Version: DescriptorVersion, OperationRef: r.OperationRef, TaskID: a.TaskID, ExplorationID: r.ExpectedExplorationID, IntentID: a.IntentID, Scope: a.Scope, ModeVersion: r.ExpectedModeVersion, RequestDigest: digest}, nil
}

func descriptorAccess(d Descriptor) (Access, error) {
	if d.Version != DescriptorVersion || !validRef(d.OperationRef) || d.TaskID <= 0 || d.ExplorationID <= 0 || d.IntentID <= 0 || d.ModeVersion == 0 || d.ModeVersion > math.MaxInt64 || d.RequestDigest == ([32]byte{}) || strconv.FormatInt(d.TaskID, 10) != d.Scope.TaskRef {
		return Access{}, ErrInput
	}
	for _, ref := range []string{d.Scope.RunRef, d.Scope.TaskRef, d.Scope.CaseRef, d.Scope.ResetRef, d.Scope.PolicyRef} {
		if !validRef(ref) {
			return Access{}, ErrInput
		}
	}
	return Access{Purpose: ReadReceipt, TaskID: d.TaskID, IntentID: d.IntentID, Scope: d.Scope, OperationRef: d.OperationRef, RequestDigest: d.RequestDigest}, nil
}

type reconciliationRow struct {
	Receipt
	execution, purpose                                    string
	binding, operationDigest, inputDigest, evidenceDigest []byte
	mode, revision                                        int64
}

type reconciliationQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readReconciliationRow(ctx context.Context, q reconciliationQueryer, operation string) (reconciliationRow, error) {
	var r reconciliationRow
	err := q.QueryRowContext(ctx, `SELECT o.operation_ref,o.request_digest,o.recorded_at,i.revision,i.execution_ref,i.purpose,i.task_id,i.exploration_id,i.intent_id,i.mode_version,i.binding,i.input_digest,i.evidence_digest,i.issued_at FROM evidence_revision_operations o JOIN evidence_revision_initials i USING(revision) WHERE o.operation_ref=$1`, operation).Scan(&r.OperationRef, &r.operationDigest, &r.RecordedAt, &r.revision, &r.execution, &r.purpose, &r.TaskID, &r.ExplorationID, &r.IntentID, &r.mode, &r.binding, &r.inputDigest, &r.evidenceDigest, &r.IssuedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return reconciliationRow{}, ErrNotFound
	}
	if err != nil {
		return reconciliationRow{}, ErrStore
	}
	return r, nil
}

type reconciliationBinding struct {
	Version        int
	Expected       evidencebind.Context
	EvidenceDigest [32]byte
	Owner, Cross   evidencebind.EvidenceRef
	CanaryFields   []string
}

func verifyReconciliationRow(r reconciliationRow, d Descriptor) (Receipt, error) {
	if len(r.binding) == 0 || len(r.binding) > 64<<10 || len(r.operationDigest) != 32 || len(r.inputDigest) != 32 || len(r.evidenceDigest) != 32 || r.revision <= 0 || r.mode <= 0 || r.purpose != "initial" {
		return Receipt{}, ErrStore
	}
	var b reconciliationBinding
	decoder := json.NewDecoder(bytes.NewReader(r.binding))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&b); err != nil {
		return Receipt{}, ErrStore
	}
	canonical, err := json.Marshal(b)
	// Canonical equality also rejects duplicate keys, trailing content and a
	// changed binding format. This is not reconstruction of raw observations.
	if err != nil || !bytes.Equal(canonical, r.binding) || b.Version != 1 {
		return Receipt{}, ErrStore
	}
	stored := b.Expected
	if r.OperationRef != d.OperationRef || r.TaskID != d.TaskID || r.ExplorationID != d.ExplorationID || r.IntentID != d.IntentID || uint64(r.mode) != d.ModeVersion || stored.IntentID != r.IntentID || stored.Scope != d.Scope || stored.Scope.RunRef != r.execution || strconv.FormatInt(r.TaskID, 10) != stored.Scope.TaskRef {
		return Receipt{}, ErrConflict
	}
	if b.Owner.IntentID != stored.IntentID || b.Cross.IntentID != stored.IntentID || b.Owner.Scope != stored.Scope || b.Cross.Scope != stored.Scope || b.Owner.Role != evidencebind.OwnerRead || b.Cross.Role != evidencebind.CrossRead || !bytes.Equal(b.EvidenceDigest[:], r.evidenceDigest) {
		return Receipt{}, ErrStore
	}
	encoded, err := json.Marshal(struct {
		Version       int
		ExplorationID int64
		ModeVersion   uint64
		Binding       json.RawMessage
	}{1, r.ExplorationID, uint64(r.mode), r.binding})
	if err != nil {
		return Receipt{}, ErrStore
	}
	digest := sha256.Sum256(append([]byte("artex.initial.evidence.revision.v1\x00"), encoded...))
	if digest != d.RequestDigest || !bytes.Equal(digest[:], r.operationDigest) || !bytes.Equal(digest[:], r.inputDigest) {
		return Receipt{}, ErrConflict
	}
	out := r.Receipt
	// Return the stored execution association, never caller-supplied Scope.
	out.Scope = stored.Scope
	out.Revision = uint64(r.revision)
	out.ModeVersion = uint64(r.mode)
	out.EvidenceDigest = b.EvidenceDigest
	out.BindingDigest = digest
	return out, nil
}

// Reconcile reads a past operation after the original Request memory is lost.
// The actual ReadReceipt scope is still mandatory. No current mode permission,
// Collected reconstruction, SQL write, sequence allocation or Issue call occurs.
func (s *Store) Reconcile(ctx context.Context, d Descriptor) (Receipt, error) {
	if s == nil || s.db == nil || nilCapability(s.provider) || ctx == nil || ctx.Err() != nil {
		return Receipt{}, ErrScope
	}
	a, err := descriptorAccess(d)
	if err != nil {
		return Receipt{}, err
	}
	lease, err := s.provider.Acquire(ctx, a)
	if err != nil || nilCapability(lease) {
		return Receipt{}, ErrScope
	}
	defer lease.Release()
	leaseCtx := lease.Context()
	if leaseCtx == nil || leaseCtx.Err() != nil {
		return Receipt{}, ErrScope
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(leaseCtx, cancel)
	defer func() { stop(); cancel() }()
	if leaseCtx.Err() != nil {
		return Receipt{}, ErrScope
	}
	// Immutable metadata is copied and parsed/hashed before any SQL row lock.
	row, err := readReconciliationRow(ctx, s.db, d.OperationRef)
	if err != nil {
		return Receipt{}, err
	}
	out, err := verifyReconciliationRow(row, d)
	if err != nil {
		return Receipt{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Receipt{}, ErrStore
	}
	defer tx.Rollback()
	var exploration int64
	err = tx.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 FOR SHARE`, row.TaskID).Scan(&exploration)
	if errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, ErrBinding
	}
	if err != nil {
		return Receipt{}, ErrStore
	}
	if exploration != row.ExplorationID {
		return Receipt{}, ErrBinding
	}
	var kind string
	err = tx.QueryRowContext(ctx, `SELECT kind FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 FOR SHARE`, row.IntentID, exploration).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, ErrBinding
	}
	if err != nil {
		return Receipt{}, ErrStore
	}
	if kind != "intent" {
		return Receipt{}, ErrBinding
	}
	// Recheck the exact immutable row under the relationship locks. No parsing,
	// hashing, host/marker callbacks or source reads occur inside this region.
	after, err := readReconciliationRow(ctx, tx, d.OperationRef)
	if err != nil {
		return Receipt{}, err
	}
	if !sameReconciliationRow(row, after) {
		return Receipt{}, ErrConflict
	}
	if ctx.Err() != nil {
		return Receipt{}, ErrScope
	}
	return out, nil
}

// SQL TIMESTAMPTZ identifies an instant; pool sessions can scan the same value
// with different locations. Normalize only the timestamp representations while
// keeping every other receipt field and immutable stored byte exact.
func sameReconciliationRow(left, right reconciliationRow) bool {
	a, b := left.Receipt, right.Receipt
	a.IssuedAt, a.RecordedAt = a.IssuedAt.UTC(), a.RecordedAt.UTC()
	b.IssuedAt, b.RecordedAt = b.IssuedAt.UTC(), b.RecordedAt.UTC()
	return a == b && left.revision == right.revision && left.mode == right.mode &&
		left.execution == right.execution && left.purpose == right.purpose &&
		bytes.Equal(left.binding, right.binding) && bytes.Equal(left.operationDigest, right.operationDigest) &&
		bytes.Equal(left.inputDigest, right.inputDigest) && bytes.Equal(left.evidenceDigest, right.evidenceDigest)
}
