//go:build linux && auditpublisherresource

package auditpublisher

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Explicit -benchtime=1x and -count provide one independently checked sequence
// per repetition. No production authority or request is issued by this fixture.
func BenchmarkPublisherSequenceResource(b *testing.B) {
	b.StopTimer()
	if b.N != 1 {
		b.Fatal("manual benchmark requires -benchtime=1x")
	}
	n, err := strconv.Atoi(os.Getenv("ARTEX_AUDIT_SEQUENCE_N"))
	if err != nil || (n != 1 && n != 8 && n != 32 && n != 128 && n != 512) {
		b.Fatal("set ARTEX_AUDIT_SEQUENCE_N to 1, 8, 32, 128 or 512 pairs")
	}
	s := sequenceSample{Pairs: n, Stage: "setup"}
	var f *sequenceFixture
	b.Cleanup(func() {
		if f != nil {
			if err := f.close(); err != nil {
				s.Failures++
				s.Completed = false
				s.Stage = "cleanup"
				b.Error("sequence fixture cleanup failed")
			}
		}
		data, err := json.Marshal(s)
		if err != nil {
			b.Error("sequence summary encoding failed")
			return
		}
		b.Logf("sequence_sample=%s", data)
	})
	f, err = newSequenceFixture(b)
	if err != nil {
		s.Failures++
		b.Fatal("explicit disposable sequence database/fixture unavailable")
	}
	fail := func(stage string) {
		b.StopTimer()
		s.Stage = stage
		s.Failures++
		b.Fatalf("sequence failed at %s; incomplete sample", stage)
	}
	events := make([]transmit.Event, 0, 2*n)
	for i := 0; i < n; i++ {
		// Six-digit attempts and a fixed timestamp keep payload widths stable
		// across every supported N; intent/result shapes repeat unchanged.
		r := transmit.Receipt{Scope: f.host.scope, Attempt: uint64(100000 + i), AccountRef: "owner", SessionRef: "session",
			ResourceRef: "resource", ApprovalRef: "approval", Method: "GET", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
		events = append(events, transmit.Event{Stage: "intent", Receipt: r})
		r.Reason = transmit.Stopped
		events = append(events, transmit.Event{Stage: "result", Receipt: r})
	}
	var widths [2]int
	for i, event := range events {
		encoded, encodeErr := json.Marshal(event)
		if encodeErr != nil {
			fail("event-preparation")
		}
		if i < 2 {
			widths[i] = len(encoded)
		}
		if len(encoded) != widths[i%2] {
			fail("event-width")
		}
	}
	b.Logf("sequence_input intent_bytes=%d result_bytes=%d", widths[0], widths[1])
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	f.host.ctx = ctx
	f.root.ctx = ctx
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	b.ReportAllocs()
	b.ResetTimer()
	b.StartTimer()
	start := time.Now()
	f.publisher, err = Open(ctx, f.config)
	s.OpenMeasured = true
	s.OpenMS = float64(time.Since(start).Nanoseconds()) / 1e6
	if err != nil {
		s.TotalMS = float64(time.Since(start).Nanoseconds()) / 1e6
		fail("register-open")
	}
	s.RegisterOK = 1
	recordStart := time.Now()
	for _, event := range events {
		if err = f.publisher.Record(ctx, event); err != nil {
			s.RecordsMeasured = true
			s.RecordMS = float64(time.Since(recordStart).Nanoseconds()) / 1e6
			s.TotalMS = float64(time.Since(start).Nanoseconds()) / 1e6
			fail("record-append")
		}
		s.RecordOK++
	}
	s.RecordMS = float64(time.Since(recordStart).Nanoseconds()) / 1e6
	s.RecordsMeasured = true
	f.host.allowSeal = true // synthetic assertion only; there are no transmitters
	sealStart := time.Now()
	final, sealErr := f.publisher.Seal(ctx)
	s.SealMeasured = true
	s.SealMS = float64(time.Since(sealStart).Nanoseconds()) / 1e6
	s.TotalMS = float64(time.Since(start).Nanoseconds()) / 1e6
	b.StopTimer()
	runtime.ReadMemStats(&after)
	s.AllocBytes, s.Mallocs = after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs
	s.AllocationMeasured = true
	if sealErr != nil {
		fail("seal")
	}
	s.SealOK = 1
	s.Sequence, s.Revision, s.Phase = final.Checkpoint.Sequence, final.Revision, string(final.Phase)
	// Validation, migration, event preparation, MemStats and cleanup are outside
	// the sequence timer. No internal verification duration is inferred.
	check, checkCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer checkCancel()
	query := auditcheckpoint.Query{Epoch: f.host.epoch, Binding: auditcheckpoint.Binding{PublisherRef: "publisher:synthetic",
		Scope: f.host.scope, JournalID: final.Checkpoint.JournalID, KeyRef: "key:synthetic"}}
	head, err := f.store.Head(check, query)
	if err != nil || head.Checkpoint != final.Checkpoint || head.Revision != final.Revision || head.Phase != final.Phase || head.Epoch != final.Epoch {
		fail("head-readback")
	}
	if err := f.db.QueryRowContext(check, `SELECT
		(SELECT count(*) FROM audit_checkpoint_heads),
		(SELECT count(*) FROM audit_checkpoint_history),
		(SELECT count(*) FROM audit_checkpoint_operations),
		(SELECT count(DISTINCT receipt_ref) FROM audit_checkpoint_operations)`).Scan(&s.Heads, &s.History, &s.Receipts, &s.DistinctReceipts); err != nil {
		fail("sql-counts")
	}
	if s.RegisterOK != 1 || s.RecordOK != 2*n || s.SealOK != 1 || s.Sequence != uint64(2*n) ||
		s.Revision != uint64(2*n+2) || final.Phase != auditcheckpoint.Sealed || s.Heads != 1 ||
		s.History != int64(2*n+2) || s.Receipts != s.History || s.DistinctReceipts != s.Receipts {
		fail("accounting")
	}
	file, err := os.Open(filepath.Join(f.root.dir, "sequence.jsonl"))
	if err != nil {
		fail("journal-open")
	}
	info, statErr := file.Stat()
	_, verifyErr := auditlog.Verify(check, file, f.host.key, final.Checkpoint)
	closeErr := file.Close()
	if statErr != nil || verifyErr != nil || closeErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		fail("journal-verify")
	}
	s.Bytes = info.Size()
	if f.root.released.Load() != 1 || f.host.acquired.Load() != f.host.released.Load() {
		fail("lease-accounting")
	}
	s.Stage, s.Completed = "verified-sealed", true
	s.AccountingVerified = true
	b.ReportMetric(s.OpenMS, "open_ms/sequence")
	b.ReportMetric(s.RecordMS, "records_ms/sequence")
	b.ReportMetric(s.SealMS, "seal_ms/sequence")
	b.ReportMetric(s.TotalMS, "total_ms/sequence")
	b.ReportMetric(s.RecordMS*1000/float64(s.RecordOK), "mean_us/record")
	b.ReportMetric(float64(s.AllocBytes), "cumulative_alloc_bytes/sequence")
	b.ReportMetric(float64(s.Bytes), "journal_bytes")
}

type sequenceSample struct {
	OpenMeasured       bool    `json:"open_measured"`
	RecordsMeasured    bool    `json:"records_measured"`
	SealMeasured       bool    `json:"seal_measured"`
	AllocationMeasured bool    `json:"allocation_measured"`
	AccountingVerified bool    `json:"accounting_verified"`
	Pairs              int     `json:"n_pairs"`
	RecordOK           int     `json:"record_successes"`
	RegisterOK         int     `json:"register_successes"`
	SealOK             int     `json:"seal_successes"`
	Failures           int     `json:"failures"`
	Completed          bool    `json:"completed_repetition"`
	Stage              string  `json:"stage"`
	Bytes              int64   `json:"journal_bytes"`
	Sequence           uint64  `json:"final_sequence"`
	Revision           uint64  `json:"final_revision"`
	Phase              string  `json:"final_phase"`
	Heads              int64   `json:"head_rows"`
	History            int64   `json:"history_rows"`
	Receipts           int64   `json:"receipt_rows"`
	DistinctReceipts   int64   `json:"distinct_receipts"`
	OpenMS             float64 `json:"open_ms"`
	RecordMS           float64 `json:"records_ms"`
	SealMS             float64 `json:"seal_ms"`
	TotalMS            float64 `json:"total_ms"`
	AllocBytes         uint64  `json:"cumulative_alloc_bytes"`
	Mallocs            uint64  `json:"cumulative_mallocs"`
}

type sequenceRoot struct {
	ctx      context.Context
	anchor   *os.File
	dir      string
	released atomic.Int64
}

func (r *sequenceRoot) Context() context.Context { return r.ctx }
func (r *sequenceRoot) Anchor() *os.File         { return r.anchor }
func (r *sequenceRoot) Directory() string        { return r.dir }
func (r *sequenceRoot) Release()                 { r.released.Add(1) }

type sequenceHost struct {
	ctx                context.Context
	epoch              auditcheckpoint.Epoch
	scope              transmit.Scope
	key                []byte
	witness            *CloseWitness
	allowSeal          bool
	acquired, released atomic.Int64
}

func (h *sequenceHost) Acquire(ctx context.Context, a auditcheckpoint.Access) (auditcheckpoint.Lease, error) {
	if ctx.Err() != nil || a.Epoch != h.epoch || a.Binding.Scope != h.scope || a.Binding.PublisherRef != "publisher:synthetic" || a.Binding.KeyRef != "key:synthetic" ||
		(a.Purpose == auditcheckpoint.PublishAccess && a.Publication.Kind == auditcheckpoint.Seal && (!h.allowSeal || !h.witness.Check(a))) {
		return nil, errors.New("synthetic fixture authority refused")
	}
	h.acquired.Add(1)
	return sequenceLease{h}, nil
}

type sequenceLease struct{ host *sequenceHost }

func (l sequenceLease) Context() context.Context     { return l.host.ctx }
func (l sequenceLease) Epoch() auditcheckpoint.Epoch { return l.host.epoch }
func (l sequenceLease) ResolveKey(context.Context, auditcheckpoint.Binding) ([]byte, error) {
	return l.host.key, nil
}
func (l sequenceLease) Release() { l.host.released.Add(1) }

type sequenceFixture struct {
	admin, db     *sql.DB
	schema        string
	schemaCreated bool
	store         *auditcheckpoint.Store
	host          *sequenceHost
	root          *sequenceRoot
	config        Config
	publisher     *Publisher
}

func newSequenceFixture(b *testing.B) (_ *sequenceFixture, err error) {
	dsn := os.Getenv("ARTEX_AUDIT_SEQUENCE_DSN")
	u, err := url.Parse(dsn)
	if err != nil || os.Getenv("ARTEX_AUDIT_SEQUENCE_DISPOSABLE") != "yes" || u.Scheme != "postgres" ||
		u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "/audit_sequence_bench" {
		return nil, errors.New("explicit disposable database required")
	}
	pg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	pg.ConnectTimeout = 3 * time.Second
	pg.RuntimeParams["synchronous_commit"] = "on"
	pg.RuntimeParams["statement_timeout"] = "10000"
	pg.RuntimeParams["lock_timeout"] = "1000"
	f := &sequenceFixture{admin: stdlib.OpenDB(*pg)}
	defer func() {
		if err != nil {
			if closeErr := f.close(); closeErr != nil {
				b.Error("failed fixture cleanup")
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var suffix [8]byte
	if _, err = rand.Read(suffix[:]); err != nil {
		return nil, err
	}
	f.schema = "audit_sequence_" + hex.EncodeToString(suffix[:])
	if _, err = f.admin.ExecContext(ctx, `CREATE SCHEMA `+f.schema); err != nil {
		return nil, err
	}
	f.schemaCreated = true
	pg.RuntimeParams["search_path"] = f.schema
	f.db = stdlib.OpenDB(*pg)
	f.db.SetMaxOpenConns(2)
	f.db.SetMaxIdleConns(2)
	var sync, fsync, version string
	if err = f.db.QueryRowContext(ctx, `SELECT current_setting('synchronous_commit'), current_setting('fsync'), current_setting('server_version')`).Scan(&sync, &fsync, &version); err != nil || sync != "on" || fsync != "on" {
		return nil, errors.New("durability settings unavailable")
	}
	b.Logf("sequence_fixture pg=%s synchronous_commit=on fsync=on client_pool_max_open=2", version)
	if err = auditcheckpoint.Migrate(ctx, f.db); err != nil {
		return nil, err
	}
	var initialHeads, initialHistory, initialReceipts int64
	if err = f.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM audit_checkpoint_heads),
		(SELECT count(*) FROM audit_checkpoint_history), (SELECT count(*) FROM audit_checkpoint_operations)`).Scan(&initialHeads, &initialHistory, &initialReceipts); err != nil {
		return nil, err
	}
	if initialHeads != 0 || initialHistory != 0 || initialReceipts != 0 {
		return nil, errors.New("fresh fixture tables not empty")
	}
	b.Log("sequence_fixture initial_heads=0 initial_history=0 initial_receipts=0")
	scope := transmit.Scope{RunRef: "run:synthetic", TaskRef: "task:synthetic", CaseRef: "case:synthetic", ResetRef: "reset:synthetic", PolicyRef: "policy:synthetic"}
	f.host = &sequenceHost{ctx: context.Background(), scope: scope, key: make([]byte, auditlog.KeyBytes), epoch: auditcheckpoint.Epoch{StoreRef: "store:synthetic", EpochRef: "epoch:synthetic", Generation: 1, AnchorDigest: sha256.Sum256([]byte("synthetic benchmark anchor"))}}
	if _, err = rand.Read(f.host.key); err != nil {
		return nil, err
	}
	e := f.host.epoch
	if _, err = f.db.ExecContext(ctx, `INSERT INTO audit_checkpoint_epochs VALUES($1,$2,$3,$4)`, e.StoreRef, e.EpochRef, int64(e.Generation), e.AnchorDigest[:]); err != nil {
		return nil, err
	}
	f.store, err = auditcheckpoint.New(f.db, f.host, auditlog.ExtensionLimits{MaxBytes: 8 << 20, MaxEvents: 1024, Timeout: 10 * time.Second})
	if err != nil {
		return nil, err
	}
	dir := b.TempDir()
	if err = os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	anchor, openErr := os.Open(dir)
	if openErr != nil {
		return nil, openErr
	}
	f.root = &sequenceRoot{ctx: context.Background(), anchor: anchor, dir: dir}
	f.config = Config{Store: f.store, Epoch: e, PublisherRef: "publisher:synthetic", RootScope: f.root, Filename: "sequence.jsonl", Scope: scope, KeyRef: "key:synthetic", Key: f.host.key, MaxSnapshotBytes: 8 << 20,
		BindCloseWitness: func(w *CloseWitness) error {
			if f.host.witness != nil {
				return errors.New("duplicate fixture witness")
			}
			f.host.witness = w
			return nil
		}}
	return f, nil
}

func (f *sequenceFixture) close() error {
	var result error
	if f.publisher != nil {
		if err := f.publisher.Abort(); err != nil && !errors.Is(err, ErrClosed) {
			result = errors.Join(result, err)
		}
	}
	if f.root != nil {
		result = errors.Join(result, f.root.anchor.Close())
	}
	if f.host != nil {
		clear(f.host.key)
	}
	if f.db != nil {
		result = errors.Join(result, f.db.Close())
	}
	if f.admin != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if f.schemaCreated {
			_, err := f.admin.ExecContext(ctx, fmt.Sprintf(`DROP SCHEMA %s CASCADE`, f.schema))
			result = errors.Join(result, err)
		}
		result = errors.Join(result, f.admin.Close())
	}
	return result
}
