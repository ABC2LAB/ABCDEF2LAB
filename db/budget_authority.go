package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"encoding/hex"
	"errors"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/transmit/budget"
)

//go:embed budget_authority_schema.sql
var budgetAuthoritySchemaSQL string

const budgetAuthorityLockKey int64 = 7337741002

var (
	ErrBudgetAuthorityUnavailable = errors.New("budget_authority_unavailable")
	ErrBudgetApprovalInvalid      = errors.New("budget_approval_invalid")
	ErrBudgetApprovalConflict     = errors.New("budget_approval_conflict")
	ErrBudgetApprovalStale        = errors.New("budget_approval_stale_epoch")
	ErrBudgetApprovalNotReady     = errors.New("budget_approval_not_ready")
)

var budgetApprovalRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$`)

// BudgetApprovalRequest is an already authenticated and authorized host decision.
// The caller must verify issuer permission, task ownership and current policy/reset
// before calling Issue. This store does not authenticate a model or an HTTP caller.
// RequestRef is the idempotency key; a retry cannot alter any other field.
type BudgetApprovalRequest struct {
	RequestRef  string
	IssuerRef   string
	TaskRef     string
	Binding     budget.Binding
	MaxRequests uint64
	ValidFrom   time.Time
	ValidUntil  time.Time
}

type BudgetApprovalRecord struct {
	RequestRef string
	IssuerRef  string
	Epoch      string
	IssuedAt   time.Time
	ReadyAt    time.Time
	State      string
	Grant      budget.Grant
}

// BudgetRegistration may be used to construct a controlled transmitter only
// while the host keeps this authority alive and its execution gate open.
// A future server adapter must stop existing executions if authority is lost.
type BudgetRegistration struct {
	Grant  budget.Grant
	Epoch  string
	Ledger *budget.Ledger
}

// BudgetAuthority owns one ledger and one PostgreSQL advisory lock session.
// A new process always starts a new epoch and never restores old reservations.
// Do not copy or expose the ledger outside Resolve.
type BudgetAuthority struct {
	mu        sync.Mutex
	db        *DB
	conn      *sql.Conn
	backendID int
	epoch     string
	ledger    *budget.Ledger
	limits    budget.Limits
	ready     map[budget.Key]BudgetApprovalRecord
	closed    bool
}

// StartBudgetAuthority fails if another process owns the same database's budget
// authority. The lock connection stays checked out until Close or process death.
func StartBudgetAuthority(ctx context.Context, d *DB, limits budget.Limits, now time.Time) (_ *BudgetAuthority, err error) {
	if ctx == nil || d == nil || d.DB == nil || !validBudgetTime(now) {
		return nil, ErrBudgetApprovalInvalid
	}
	ledger, err := budget.New(limits)
	if err != nil {
		return nil, err
	}
	conn, err := d.Conn(ctx)
	if err != nil {
		return nil, ErrBudgetAuthorityUnavailable
	}
	lockAttempted := false
	defer func() {
		if err != nil {
			if lockAttempted {
				_ = releaseBudgetAuthorityConn(conn)
			} else {
				_ = conn.Close()
			}
		}
	}()
	var acquired bool
	lockAttempted = true
	if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, budgetAuthorityLockKey).Scan(&acquired); err != nil {
		return nil, ErrBudgetAuthorityUnavailable
	}
	if !acquired {
		return nil, ErrBudgetAuthorityUnavailable
	}
	var backendID int
	if err = conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&backendID); err != nil {
		return nil, ErrBudgetAuthorityUnavailable
	}
	epoch, err := newBudgetRef()
	if err != nil {
		return nil, err
	}
	tx, err := d.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, ErrBudgetAuthorityUnavailable
	}
	defer tx.Rollback()
	now = budgetTime(now)
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_authority_epochs(epoch,started_at) VALUES($1,$2)`, epoch, now); err != nil {
		return nil, ErrBudgetAuthorityUnavailable
	}
	var previous string
	scanErr := tx.QueryRowContext(ctx, `SELECT epoch FROM budget_authority_current WHERE singleton=1 FOR UPDATE`).Scan(&previous)
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return nil, ErrBudgetAuthorityUnavailable
	}
	if scanErr == nil {
		if err = closeBudgetEpochTx(ctx, tx, previous, now); err != nil {
			return nil, ErrBudgetAuthorityUnavailable
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_authority_current(singleton,epoch,closed)
VALUES(1,$1,FALSE) ON CONFLICT(singleton) DO UPDATE SET epoch=EXCLUDED.epoch,closed=FALSE`, epoch); err != nil {
		return nil, ErrBudgetAuthorityUnavailable
	}
	if err = tx.Commit(); err != nil {
		return nil, ErrBudgetAuthorityUnavailable
	}
	return &BudgetAuthority{db: d, conn: conn, backendID: backendID, epoch: epoch,
		ledger: ledger, limits: limits, ready: make(map[budget.Key]BudgetApprovalRecord)}, nil
}

// Epoch is diagnostic metadata, never authority to issue or use a grant.
func (a *BudgetAuthority) Epoch() string {
	if a == nil {
		return ""
	}
	return a.epoch
}

// Issue persists a prepared grant before registering it in the shared ledger.
// Only after the ready state commits is it added to the in-memory registry.
// An ambiguous store error must be retried with the same RequestRef.
func (a *BudgetAuthority) Issue(ctx context.Context, request BudgetApprovalRequest, now time.Time) (BudgetApprovalRecord, error) {
	request.ValidFrom, request.ValidUntil = budgetTime(request.ValidFrom), budgetTime(request.ValidUntil)
	now = budgetTime(now)
	if a == nil || ctx == nil || !validBudgetRequest(request, now, a.limits) {
		return BudgetApprovalRecord{}, ErrBudgetApprovalInvalid
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.checkActive(ctx); err != nil {
		return BudgetApprovalRecord{}, err
	}
	record, err := a.prepare(ctx, request, now)
	if err != nil {
		return BudgetApprovalRecord{}, err
	}
	if record.Epoch != a.epoch {
		return BudgetApprovalRecord{}, ErrBudgetApprovalStale
	}
	if record.State == "closed" || !sameBudgetRequest(record, request) {
		return BudgetApprovalRecord{}, ErrBudgetApprovalConflict
	}
	if _, published := a.ready[record.Grant.Key]; published {
		return record, nil
	}
	if err := a.ledger.Register(record.Grant, now); err != nil {
		return BudgetApprovalRecord{}, err
	}
	if record.State == "prepared" {
		record, err = a.markReady(ctx, record.RequestRef, now)
		if err != nil {
			return BudgetApprovalRecord{}, err
		}
	} else if record.State != "ready" {
		return BudgetApprovalRecord{}, ErrBudgetApprovalNotReady
	}
	a.ready[record.Grant.Key] = record
	return record, nil
}

// Resolve verifies the live epoch and durable ready state before handing the
// same ledger pointer to a new controlled execution. No missing-budget fallback.
func (a *BudgetAuthority) Resolve(ctx context.Context, key budget.Key, binding budget.Binding) (BudgetRegistration, error) {
	if a == nil || ctx == nil {
		return BudgetRegistration{}, ErrBudgetApprovalInvalid
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.checkActive(ctx); err != nil {
		return BudgetRegistration{}, err
	}
	record, exists := a.ready[key]
	if !exists || record.Grant.Binding != binding {
		return BudgetRegistration{}, ErrBudgetApprovalNotReady
	}
	var epoch, state string
	if err := a.db.QueryRowContext(ctx, `SELECT epoch,state FROM budget_approvals WHERE request_ref=$1`, record.RequestRef).Scan(&epoch, &state); err != nil {
		return BudgetRegistration{}, ErrBudgetAuthorityUnavailable
	}
	if epoch != a.epoch || state != "ready" {
		return BudgetRegistration{}, ErrBudgetApprovalNotReady
	}
	return BudgetRegistration{Grant: record.Grant, Epoch: a.epoch, Ledger: a.ledger}, nil
}

// Close shuts the local gate first, records the closed epoch, then releases the
// dedicated lock connection. Callers must stop/drain executions before Close.
func (a *BudgetAuthority) Close(ctx context.Context, now time.Time) error {
	if a == nil || ctx == nil || !validBudgetTime(now) {
		return ErrBudgetApprovalInvalid
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.conn == nil {
		return nil
	}
	a.closed = true
	now = budgetTime(now)
	storeErr := a.closeEpoch(ctx, now)
	releaseErr := releaseBudgetAuthorityConn(a.conn)
	a.conn = nil
	if storeErr != nil || releaseErr != nil {
		return ErrBudgetAuthorityUnavailable
	}
	return nil
}

func releaseBudgetAuthorityConn(conn *sql.Conn) error {
	unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var unlocked bool
	unlockErr := conn.QueryRowContext(unlockCtx, `SELECT pg_advisory_unlock($1)`, budgetAuthorityLockKey).Scan(&unlocked)
	if unlockErr != nil {
		// Never return a session with an unknown lock state to the pool.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	closeErr := conn.Close()
	if unlockErr != nil || !unlocked || closeErr != nil {
		return ErrBudgetAuthorityUnavailable
	}
	return nil
}

func (a *BudgetAuthority) closeEpoch(ctx context.Context, now time.Time) error {
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	if err := tx.QueryRowContext(ctx, `SELECT epoch FROM budget_authority_current WHERE singleton=1 FOR UPDATE`).Scan(&current); err != nil {
		return err
	}
	if current == a.epoch {
		if err := closeBudgetEpochTx(ctx, tx, a.epoch, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE budget_authority_current SET closed=TRUE WHERE singleton=1 AND epoch=$1`, a.epoch); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func closeBudgetEpochTx(ctx context.Context, tx *sql.Tx, epoch string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `WITH moved AS (
UPDATE budget_approvals SET state='closed' WHERE epoch=$1 AND state='ready'
RETURNING request_ref)
INSERT INTO budget_approval_events(request_ref,epoch,state,at)
SELECT request_ref,$1,'closed',$2 FROM moved`, epoch, now); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE budget_authority_epochs SET closed_at=COALESCE(closed_at,$2) WHERE epoch=$1`, epoch, now)
	return err
}

func (a *BudgetAuthority) checkActive(ctx context.Context) error {
	if a.closed {
		return ErrBudgetAuthorityUnavailable
	}
	var backendID int
	var held bool
	if err := a.conn.QueryRowContext(ctx, `SELECT pg_backend_pid(), EXISTS (
SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND granted
AND classid=($1::bigint >> 32)::oid
AND objid=($1::bigint & 4294967295)::oid AND objsubid=1)`, budgetAuthorityLockKey).Scan(&backendID, &held); err != nil || backendID != a.backendID || !held {
		a.closed = true
		return ErrBudgetAuthorityUnavailable
	}
	var epoch string
	var closed bool
	if err := a.db.QueryRowContext(ctx, `SELECT epoch,closed FROM budget_authority_current WHERE singleton=1`).Scan(&epoch, &closed); err != nil || epoch != a.epoch || closed {
		a.closed = true
		return ErrBudgetAuthorityUnavailable
	}
	return nil
}

func (a *BudgetAuthority) prepare(ctx context.Context, request BudgetApprovalRequest, now time.Time) (BudgetApprovalRecord, error) {
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return BudgetApprovalRecord{}, ErrBudgetAuthorityUnavailable
	}
	defer tx.Rollback()
	var currentEpoch string
	var closed bool
	if err := tx.QueryRowContext(ctx, `SELECT epoch,closed FROM budget_authority_current WHERE singleton=1 FOR UPDATE`).Scan(&currentEpoch, &closed); err != nil || closed || currentEpoch != a.epoch {
		return BudgetApprovalRecord{}, ErrBudgetAuthorityUnavailable
	}
	budgetRef, err := newBudgetRef()
	if err != nil {
		return BudgetApprovalRecord{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO budget_approvals
(request_ref,issuer_ref,task_ref,budget_ref,policy_ref,reset_ref,max_requests,valid_from,valid_until,epoch,state,issued_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'prepared',$11) ON CONFLICT(request_ref) DO NOTHING`,
		request.RequestRef, request.IssuerRef, request.TaskRef, budgetRef,
		request.Binding.PolicyRef, request.Binding.ResetRef, int64(request.MaxRequests),
		request.ValidFrom, request.ValidUntil, a.epoch, now)
	if err != nil {
		return BudgetApprovalRecord{}, ErrBudgetAuthorityUnavailable
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return BudgetApprovalRecord{}, ErrBudgetAuthorityUnavailable
	}
	if inserted == 1 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO budget_approval_events(request_ref,epoch,state,at) VALUES($1,$2,'prepared',$3)`, request.RequestRef, a.epoch, now); err != nil {
			return BudgetApprovalRecord{}, ErrBudgetAuthorityUnavailable
		}
	}
	record, err := scanBudgetApproval(tx.QueryRowContext(ctx, `SELECT request_ref,issuer_ref,task_ref,budget_ref,policy_ref,reset_ref,max_requests,valid_from,valid_until,epoch,state,issued_at,ready_at
FROM budget_approvals WHERE request_ref=$1`, request.RequestRef))
	if err != nil {
		return BudgetApprovalRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return BudgetApprovalRecord{}, ErrBudgetAuthorityUnavailable
	}
	return record, nil
}

func (a *BudgetAuthority) markReady(ctx context.Context, requestRef string, now time.Time) (BudgetApprovalRecord, error) {
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return BudgetApprovalRecord{}, ErrBudgetAuthorityUnavailable
	}
	defer tx.Rollback()
	var epoch string
	var closed bool
	if err := tx.QueryRowContext(ctx, `SELECT epoch,closed FROM budget_authority_current WHERE singleton=1 FOR UPDATE`).Scan(&epoch, &closed); err != nil || closed || epoch != a.epoch {
		return BudgetApprovalRecord{}, ErrBudgetAuthorityUnavailable
	}
	record, err := scanBudgetApproval(tx.QueryRowContext(ctx, `SELECT request_ref,issuer_ref,task_ref,budget_ref,policy_ref,reset_ref,max_requests,valid_from,valid_until,epoch,state,issued_at,ready_at
FROM budget_approvals WHERE request_ref=$1 FOR UPDATE`, requestRef))
	if err != nil {
		return BudgetApprovalRecord{}, err
	}
	if record.Epoch != a.epoch || record.State == "closed" {
		return BudgetApprovalRecord{}, ErrBudgetApprovalStale
	}
	if record.State == "prepared" {
		if _, err := tx.ExecContext(ctx, `UPDATE budget_approvals SET state='ready',ready_at=$2 WHERE request_ref=$1 AND state='prepared'`, requestRef, now); err != nil {
			return BudgetApprovalRecord{}, ErrBudgetAuthorityUnavailable
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO budget_approval_events(request_ref,epoch,state,at) VALUES($1,$2,'ready',$3)`, requestRef, a.epoch, now); err != nil {
			return BudgetApprovalRecord{}, ErrBudgetAuthorityUnavailable
		}
		record.State, record.ReadyAt = "ready", now
	}
	if err := tx.Commit(); err != nil {
		return BudgetApprovalRecord{}, ErrBudgetAuthorityUnavailable
	}
	return record, nil
}

func scanBudgetApproval(row *sql.Row) (BudgetApprovalRecord, error) {
	var record BudgetApprovalRecord
	var maxRequests int64
	var readyAt sql.NullTime
	err := row.Scan(&record.RequestRef, &record.IssuerRef, &record.Grant.Key.TaskRef,
		&record.Grant.Key.BudgetRef, &record.Grant.Binding.PolicyRef, &record.Grant.Binding.ResetRef,
		&maxRequests, &record.Grant.ValidFrom, &record.Grant.ValidUntil,
		&record.Epoch, &record.State, &record.IssuedAt, &readyAt)
	if err != nil || maxRequests <= 0 {
		return BudgetApprovalRecord{}, ErrBudgetAuthorityUnavailable
	}
	record.Grant.MaxRequests = uint64(maxRequests)
	if readyAt.Valid {
		record.ReadyAt = readyAt.Time
	}
	return record, nil
}

func sameBudgetRequest(record BudgetApprovalRecord, request BudgetApprovalRequest) bool {
	return record.IssuerRef == request.IssuerRef &&
		record.Grant.Key.TaskRef == request.TaskRef &&
		record.Grant.Binding == request.Binding &&
		record.Grant.MaxRequests == request.MaxRequests &&
		record.Grant.ValidFrom.Equal(request.ValidFrom) &&
		record.Grant.ValidUntil.Equal(request.ValidUntil)
}

func validBudgetRequest(request BudgetApprovalRequest, now time.Time, limits budget.Limits) bool {
	return validBudgetRef(request.RequestRef) && validBudgetRef(request.IssuerRef) &&
		validBudgetRef(request.TaskRef) && validBudgetRef(request.Binding.PolicyRef) &&
		validBudgetRef(request.Binding.ResetRef) && validBudgetTime(now) &&
		validBudgetTime(request.ValidFrom) && validBudgetTime(request.ValidUntil) &&
		request.ValidFrom.Before(request.ValidUntil) && now.Before(request.ValidUntil) &&
		request.MaxRequests > 0 && request.MaxRequests <= limits.MaxRequestsPerApproval &&
		request.MaxRequests <= uint64(math.MaxInt64)
}

func validBudgetRef(ref string) bool {
	return budgetApprovalRefPattern.MatchString(ref) && !strings.Contains(ref, "://")
}
func validBudgetTime(at time.Time) bool {
	return !at.IsZero() && at.Year() >= 1 && at.Year() <= 9999
}
func budgetTime(at time.Time) time.Time { return at.Round(0).UTC().Truncate(time.Microsecond) }

func newBudgetRef() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", ErrBudgetAuthorityUnavailable
	}
	return hex.EncodeToString(bytes[:]), nil
}
