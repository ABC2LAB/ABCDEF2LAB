// Package e2checkpoint는 일반 E② v3 저널의 현재 head를 별도 DB에 보관한다.
// Host 권위, epoch 발급, DB 역할 분리, 실제 송신 연결은 이 패키지 밖의 책임이다.
package e2checkpoint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/transmit"
)

//go:embed schema.sql
var schema string

var (
	ErrConfig     = errors.New("e2checkpoint: invalid configuration")
	ErrRequest    = errors.New("e2checkpoint: invalid request")
	ErrAuthority  = errors.New("e2checkpoint: authority unavailable")
	ErrEpoch      = errors.New("e2checkpoint: current epoch unavailable")
	ErrNotFound   = errors.New("e2checkpoint: head unavailable")
	ErrConflict   = errors.New("e2checkpoint: publication conflict")
	ErrIntegrity  = errors.New("e2checkpoint: snapshot integrity failure")
	ErrStorage    = errors.New("e2checkpoint: storage unavailable")
	ErrACKUnknown = errors.New("e2checkpoint: publication acknowledgement unknown")
)

type Epoch struct {
	StoreRef     string
	EpochRef     string
	Generation   uint64
	AnchorDigest [sha256.Size]byte
}
type Binding struct {
	PublisherRef string
	KeyRef       string
	Scope        transmit.Scope
	JournalID    string
}
type Phase string

const (
	Open        Phase = "open"
	Sealed      Phase = "sealed"
	Interrupted Phase = "interrupted"
)

type Kind string

const (
	Register  Kind = "register"
	Append    Kind = "append"
	Seal      Kind = "seal"
	Interrupt Kind = "interrupt"
)

type Request struct {
	Epoch            Epoch
	Binding          Binding
	Kind             Kind
	ExpectedRevision uint64
	Previous         e2proxy.JournalCheckpoint
	Candidate        e2proxy.JournalCheckpoint
}
type Query struct {
	Epoch   Epoch
	Binding Binding
}
type Head struct {
	Epoch      Epoch
	Binding    Binding
	Revision   uint64
	Phase      Phase
	Checkpoint e2proxy.JournalCheckpoint
}
type Purpose string

const (
	PublishAccess Purpose = "publish"
	HeadAccess    Purpose = "head"
	VerifyAccess  Purpose = "verify"
)

type Access struct {
	Purpose     Purpose
	Epoch       Epoch
	Binding     Binding
	Publication Request
}

// Host는 호출자가 아닌 독립 bootstrap에서 실행·저장소 권한을 확인한다.
// Seal은 실제 실행 종료 근거도 Host가 확인해야 한다.
type Host interface {
	Acquire(context.Context, Access) (Lease, error)
}
type Lease interface {
	Context() context.Context
	Epoch() Epoch
	ResolveKey(context.Context, Binding) ([]byte, error)
	Release()
}
type Store struct {
	db       *sql.DB
	host     Host
	maxBytes int64
	timeout  time.Duration
}

const maxJournalBytes int64 = 256 << 20

var refPattern = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$")

func validRef(s string) bool { return refPattern.MatchString(s) && !strings.Contains(s, "://") }
func validEpoch(e Epoch) bool {
	return validRef(e.StoreRef) && validRef(e.EpochRef) && e.Generation > 0 &&
		e.Generation <= math.MaxInt64 && e.AnchorDigest != ([sha256.Size]byte{})
}
func validBinding(b Binding) bool {
	if !validRef(b.PublisherRef) || !validRef(b.KeyRef) || len(b.JournalID) != 32 {
		return false
	}
	if _, err := hex.DecodeString(b.JournalID); err != nil {
		return false
	}
	for _, ref := range []string{b.Scope.RunRef, b.Scope.TaskRef, b.Scope.CaseRef, b.Scope.ResetRef, b.Scope.PolicyRef} {
		if !validRef(ref) {
			return false
		}
	}
	return true
}
func validCheckpoint(cp e2proxy.JournalCheckpoint, b Binding) bool {
	if cp.Version != e2proxy.JournalVersionV3 || cp.JournalID != b.JournalID ||
		cp.Scope != b.Scope || len(cp.MAC) != 64 {
		return false
	}
	_, err := hex.DecodeString(cp.MAC)
	return err == nil
}
func validRequest(r Request) bool {
	if !validEpoch(r.Epoch) || !validBinding(r.Binding) || !validCheckpoint(r.Candidate, r.Binding) ||
		r.ExpectedRevision >= math.MaxInt64 {
		return false
	}
	switch r.Kind {
	case Register:
		return r.ExpectedRevision == 0 && r.Previous == (e2proxy.JournalCheckpoint{}) && r.Candidate.Sequence == 0
	case Append:
		return r.ExpectedRevision > 0 && validCheckpoint(r.Previous, r.Binding) &&
			r.Candidate.Sequence > r.Previous.Sequence
	case Seal, Interrupt:
		return r.ExpectedRevision > 0 && validCheckpoint(r.Previous, r.Binding) && r.Candidate == r.Previous
	default:
		return false
	}
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
func New(db *sql.DB, host Host, maxBytes int64, timeout time.Duration) (*Store, error) {
	if db == nil || isNil(host) || maxBytes <= 0 || maxBytes > maxJournalBytes ||
		timeout <= 0 || timeout > time.Minute {
		return nil, ErrConfig
	}
	return &Store{db: db, host: host, maxBytes: maxBytes, timeout: timeout}, nil
}

// Migrate는 구조만 만든다. epoch와 DB 역할은 설치 관리자가 따로 준비한다.
func Migrate(ctx context.Context, db *sql.DB) error {
	if ctx == nil || db == nil {
		return ErrConfig
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ErrStorage
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL synchronous_commit=on"); err != nil {
		return ErrStorage
	}
	if _, err = tx.ExecContext(ctx, schema); err != nil {
		return ErrStorage
	}
	if err = tx.Commit(); err != nil {
		return ErrACKUnknown
	}
	return nil
}
func (s *Store) acquire(ctx context.Context, a Access) (context.Context, Lease, func(), error) {
	if s == nil || s.db == nil || isNil(s.host) || ctx == nil || ctx.Err() != nil ||
		!validEpoch(a.Epoch) || !validBinding(a.Binding) {
		return nil, nil, nil, ErrAuthority
	}
	lease, err := s.host.Acquire(ctx, a)
	if err != nil || isNil(lease) {
		return nil, nil, nil, ErrAuthority
	}
	if lease.Context() == nil || lease.Context().Err() != nil || lease.Epoch() != a.Epoch {
		lease.Release()
		return nil, nil, nil, ErrAuthority
	}
	bounded, cancel := context.WithTimeout(ctx, s.timeout)
	stop := context.AfterFunc(lease.Context(), cancel)
	release := func() { stop(); cancel(); lease.Release() }
	if bounded.Err() != nil {
		release()
		return nil, nil, nil, ErrAuthority
	}
	return bounded, lease, release, nil
}

type rowQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func checkEpoch(ctx context.Context, q rowQuery, e Epoch, lock bool) error {
	statement := "SELECT epoch_ref,generation,anchor_digest FROM e2_http_audit_epochs WHERE store_ref=$1"
	if lock {
		statement += " FOR SHARE"
	}
	var ref string
	var generation int64
	var anchor []byte
	if err := q.QueryRowContext(ctx, statement, e.StoreRef).Scan(&ref, &generation, &anchor); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrEpoch
		}
		return ErrStorage
	}
	if ref != e.EpochRef || generation != int64(e.Generation) || !bytes.Equal(anchor, e.AnchorDigest[:]) {
		return ErrEpoch
	}
	return nil
}

type storedHead struct {
	Head
	snapshotBytes  int64
	snapshotDigest [sha256.Size]byte
}

func readHead(ctx context.Context, q rowQuery, e Epoch, b Binding, lock bool) (storedHead, error) {
	statement := "SELECT epoch_ref,generation,anchor_digest,publisher_ref,key_ref,journal_id,revision,phase,checkpoint,snapshot_bytes,snapshot_digest " +
		"FROM e2_http_audit_heads WHERE store_ref=$1 AND run_ref=$2"
	if lock {
		statement += " FOR UPDATE"
	}
	var h storedHead
	var epochRef, publisherRef, keyRef, journalID, phase string
	var generation, revision, n int64
	var anchor, encoded, digest []byte
	err := q.QueryRowContext(ctx, statement, e.StoreRef, b.Scope.RunRef).
		Scan(&epochRef, &generation, &anchor, &publisherRef, &keyRef, &journalID, &revision, &phase, &encoded, &n, &digest)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storedHead{}, ErrNotFound
		}
		return storedHead{}, ErrStorage
	}
	if epochRef != e.EpochRef || generation != int64(e.Generation) || !bytes.Equal(anchor, e.AnchorDigest[:]) ||
		publisherRef != b.PublisherRef || keyRef != b.KeyRef || journalID != b.JournalID ||
		revision <= 0 || n <= 0 || len(digest) != sha256.Size ||
		(phase != string(Open) && phase != string(Sealed) && phase != string(Interrupted)) {
		return storedHead{}, ErrIntegrity
	}
	if err := json.Unmarshal(encoded, &h.Checkpoint); err != nil || !validCheckpoint(h.Checkpoint, b) {
		return storedHead{}, ErrIntegrity
	}
	h.Epoch, h.Binding, h.Revision, h.Phase = e, b, uint64(revision), Phase(phase)
	h.snapshotBytes = n
	copy(h.snapshotDigest[:], digest)
	return h, nil
}

// Head는 모든 호출에서 외부 등록 epoch와 인증된 실행 결속을 다시 확인한다.
func (s *Store) Head(ctx context.Context, query Query) (Head, error) {
	if !validEpoch(query.Epoch) || !validBinding(query.Binding) {
		return Head{}, ErrRequest
	}
	ctx, _, release, err := s.acquire(ctx, Access{Purpose: HeadAccess, Epoch: query.Epoch, Binding: query.Binding})
	if err != nil {
		return Head{}, err
	}
	defer release()
	if err := checkEpoch(ctx, s.db, query.Epoch, false); err != nil {
		return Head{}, err
	}
	row, err := readHead(ctx, s.db, query.Epoch, query.Binding, false)
	if err != nil {
		return Head{}, err
	}
	if ctx.Err() != nil {
		return Head{}, ErrAuthority
	}
	return row.Head, nil
}
func headerKeyRef(snapshot []byte) (string, error) {
	n := bytes.IndexByte(snapshot, '\n')
	if n < 0 {
		return "", ErrIntegrity
	}
	var line struct{ Header struct{ KeyRef string } }
	if err := json.Unmarshal(snapshot[:n], &line); err != nil {
		return "", ErrIntegrity
	}
	return line.Header.KeyRef, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Publish는 현재 head를 확정한 뒤 별도 인증된 Head 읽기까지 성공해야 ACK한다.
// 실패 후 동일 저널 재시도·송신 허용 여부는 상위 recorder가 fail-closed로 정한다.
func (s *Store) Publish(ctx context.Context, request Request, source io.Reader) (Head, error) {
	if !validRequest(request) || source == nil {
		return Head{}, ErrRequest
	}
	if err := s.publish(ctx, request, source); err != nil {
		return Head{}, err
	}
	head, err := s.Head(ctx, Query{Epoch: request.Epoch, Binding: request.Binding})
	phase := Open
	if request.Kind == Seal {
		phase = Sealed
	} else if request.Kind == Interrupt {
		phase = Interrupted
	}
	if err != nil || head.Checkpoint != request.Candidate || head.Revision != request.ExpectedRevision+1 ||
		head.Phase != phase {
		return Head{}, ErrACKUnknown
	}
	return head, nil
}
func (s *Store) publish(ctx context.Context, r Request, source io.Reader) error {
	ctx, lease, release, err := s.acquire(ctx, Access{Purpose: PublishAccess, Epoch: r.Epoch, Binding: r.Binding, Publication: r})
	if err != nil {
		return err
	}
	defer release()
	snapshot, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: source}, s.maxBytes+1))
	if err != nil || ctx.Err() != nil || int64(len(snapshot)) > s.maxBytes {
		return ErrIntegrity
	}
	defer clear(snapshot)
	key, err := lease.ResolveKey(ctx, r.Binding)
	if err != nil || len(key) < 32 || len(key) > 4096 || ctx.Err() != nil {
		return ErrAuthority
	}
	owned := bytes.Clone(key)
	_, err = e2proxy.VerifyJournal(ctx, bytes.NewReader(snapshot), owned, r.Candidate)
	clear(owned)
	if err != nil {
		return ErrIntegrity
	}
	keyRef, err := headerKeyRef(snapshot)
	if err != nil || keyRef != r.Binding.KeyRef {
		return ErrIntegrity
	}
	digest := sha256.Sum256(snapshot)
	encoded, err := json.Marshal(r.Candidate)
	if err != nil {
		return ErrRequest
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrStorage
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL synchronous_commit=on"); err != nil {
		return ErrStorage
	}
	if err := checkEpoch(ctx, tx, r.Epoch, true); err != nil {
		return err
	}
	current, err := readHead(ctx, tx, r.Epoch, r.Binding, true)
	if r.Kind == Register {
		if err == nil {
			return ErrConflict
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO e2_http_audit_heads "+
			"(store_ref,run_ref,epoch_ref,generation,anchor_digest,publisher_ref,key_ref,journal_id,revision,phase,checkpoint,snapshot_bytes,snapshot_digest) "+
			"VALUES($1,$2,$3,$4,$5,$6,$7,$8,1,'open',$9,$10,$11)",
			r.Epoch.StoreRef, r.Binding.Scope.RunRef, r.Epoch.EpochRef, int64(r.Epoch.Generation),
			r.Epoch.AnchorDigest[:], r.Binding.PublisherRef, r.Binding.KeyRef, r.Binding.JournalID, encoded, len(snapshot), digest[:])
	} else {
		if err != nil {
			return err
		}
		if current.Phase != Open || current.Revision != r.ExpectedRevision ||
			current.Checkpoint != r.Previous || int64(len(snapshot)) < current.snapshotBytes ||
			sha256.Sum256(snapshot[:current.snapshotBytes]) != current.snapshotDigest {
			return ErrConflict
		}
		phase := Open
		if r.Kind == Seal || r.Kind == Interrupt {
			if int64(len(snapshot)) != current.snapshotBytes {
				return ErrConflict
			}
			if r.Kind == Seal {
				phase = Sealed
			} else {
				phase = Interrupted
			}
		}
		var result sql.Result
		result, err = tx.ExecContext(ctx, "UPDATE e2_http_audit_heads SET revision=$3,phase=$4,checkpoint=$5,"+
			"snapshot_bytes=$6,snapshot_digest=$7 WHERE store_ref=$1 AND run_ref=$2 AND revision=$8",
			r.Epoch.StoreRef, r.Binding.Scope.RunRef, int64(current.Revision+1), phase, encoded, len(snapshot), digest[:], int64(current.Revision))
		if err == nil {
			affected, affErr := result.RowsAffected()
			if affErr != nil || affected != 1 {
				return ErrConflict
			}
		}
	}
	if err != nil {
		return ErrStorage
	}
	if ctx.Err() != nil || lease.Context().Err() != nil {
		return ErrAuthority
	}
	if err := tx.Commit(); err != nil {
		return ErrACKUnknown
	}
	return nil
}
