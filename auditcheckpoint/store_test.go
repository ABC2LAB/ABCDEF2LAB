package auditcheckpoint

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Test-only authority; it does not authenticate any real service, identity,
// lifecycle witness, restore anchor, or key registry.
type fakeHost struct {
	epoch              Epoch
	key                []byte
	deny               bool
	keyHook            func()
	leaseCtx           context.Context
	acquired, released atomic.Int64
}
type fakeLease struct {
	host  *fakeHost
	epoch Epoch
}

func (h *fakeHost) Acquire(ctx context.Context, _ Access) (Lease, error) {
	if h.deny {
		return nil, errors.New("synthetic private auth failure")
	}
	h.acquired.Add(1)
	return &fakeLease{host: h, epoch: h.epoch}, nil
}
func (l *fakeLease) Context() context.Context {
	if l.host.leaseCtx != nil {
		return l.host.leaseCtx
	}
	return context.Background()
}
func (l *fakeLease) Epoch() Epoch { return l.epoch }
func (l *fakeLease) ResolveKey(context.Context, Binding) ([]byte, error) {
	if l.host.keyHook != nil {
		l.host.keyHook()
	}
	return l.host.key, nil
}
func (l *fakeLease) Release() { l.host.released.Add(1) }

type fixture struct {
	db    *sql.DB
	store *Store
	host  *fakeHost
	cp    []auditlog.Checkpoint
	snap  [][]byte
}

func newFixture(t *testing.T, provision bool) *fixture {
	t.Helper()
	dsn := os.Getenv("ARTEX_AUDIT_TEST_DSN")
	if dsn == "" {
		t.Skip("dedicated audit test PostgreSQL not supplied")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid isolated PostgreSQL configuration")
	}
	admin := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = admin.Close() })
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal("fixture randomness")
	}
	schemaName := "audit_cp_" + hex.EncodeToString(random[:])
	if _, err := admin.Exec(`CREATE SCHEMA ` + schemaName); err != nil {
		t.Fatal("isolated fixture schema unavailable")
	}
	config.RuntimeParams["search_path"] = schemaName
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(8)
	t.Cleanup(func() {
		_ = db.Close()
		if _, err := admin.Exec(`DROP SCHEMA ` + schemaName + ` CASCADE`); err != nil {
			t.Error("fixture cleanup failed")
		}
	})
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	f := &fixture{db: db, host: &fakeHost{epoch: Epoch{StoreRef: "synthetic-store", EpochRef: "synthetic-epoch", Generation: 1, AnchorDigest: sha256.Sum256([]byte("synthetic external anchor"))}, key: bytes.Repeat([]byte{0x47}, 32)}}
	if provision {
		e := f.host.epoch
		// Explicit fixture provisioning is deliberately outside the core API.
		if _, err := db.Exec(`INSERT INTO audit_checkpoint_epochs VALUES($1,$2,$3,$4)`, e.StoreRef, e.EpochRef, int64(e.Generation), e.AnchorDigest[:]); err != nil {
			t.Fatal("synthetic epoch fixture")
		}
	}
	f.store, err = New(db, f.host, auditlog.ExtensionLimits{MaxBytes: 1 << 20, MaxEvents: 8, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	j, err := auditlog.Create(dir, "synthetic.jsonl", auditlog.Config{Scope: transmit.Scope{RunRef: "run:synthetic", TaskRef: "task:synthetic", CaseRef: "case:synthetic", ResetRef: "reset:synthetic", PolicyRef: "policy:synthetic"}, KeyRef: "key:synthetic", Key: f.host.key})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	for i := 0; i < 3; i++ {
		if i > 0 {
			e := transmit.Event{Stage: "result", Receipt: transmit.Receipt{At: time.Now().UTC(), Scope: j.Checkpoint().Scope, Attempt: uint64(i), AccountRef: "account:synthetic", SessionRef: "session:synthetic", ResourceRef: "resource:synthetic", ApprovalRef: "approval:synthetic", Method: "GET", Reason: transmit.Stopped}}
			if err := j.Record(context.Background(), e); err != nil {
				t.Fatal(err)
			}
		}
		f.cp = append(f.cp, j.Checkpoint())
		data, err := os.ReadFile(filepath.Join(dir, "synthetic.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		f.snap = append(f.snap, data)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if f.host.acquired.Load() != f.host.released.Load() {
			t.Error("authority lease was not released")
		}
	})
	return f
}

func (f *fixture) req(kind Kind, op string, prev, cur int, rev uint64) Request {
	r := Request{Epoch: f.host.epoch, OperationRef: op, PublisherRef: "publisher:synthetic", Kind: kind, ExpectedRevision: rev, Candidate: f.cp[cur]}
	if kind != Register {
		r.Previous = f.cp[prev]
	}
	return r
}
func (f *fixture) query(op string) Query {
	return Query{Epoch: f.host.epoch, Binding: binding("publisher:synthetic", f.cp[0]), OperationRef: op}
}
func publish(t *testing.T, f *fixture, r Request, index int) Receipt {
	t.Helper()
	var source io.Reader
	if index >= 0 {
		source = bytes.NewReader(f.snap[index])
	}
	got, err := f.store.Publish(context.Background(), r, source)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func counts(t *testing.T, f *fixture, wantHistory, wantOperations int) {
	t.Helper()
	for table, want := range map[string]int{"audit_checkpoint_history": wantHistory, "audit_checkpoint_operations": wantOperations, "audit_checkpoint_heads": 1} {
		if wantHistory == 0 && table == "audit_checkpoint_heads" {
			want = 0
		}
		var got int
		if err := f.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s count=%d expected=%d", table, got, want)
		}
	}
}

func TestStoreProgressRetryAndHistoricalACK(t *testing.T) {
	f := newFixture(t, true)
	r0 := f.req(Register, "op:register", 0, 0, 0)
	first := publish(t, f, r0, 0)
	if first.Head.Revision != 1 || first.Head.Phase != Open || first.Head.Checkpoint != f.cp[0] {
		t.Fatal("initial binding")
	}
	if got := publish(t, f, r0, 0); got != first {
		t.Fatal("retry changed durable receipt")
	}
	duplicate := r0
	duplicate.OperationRef = "op:register-copy"
	if got := publish(t, f, duplicate, 0); got.Head.Revision != 1 {
		t.Fatal("duplicate registration advanced head")
	}
	r1 := f.req(Append, "op:append", 0, 1, 1)
	second := publish(t, f, r1, 1)
	if second.Head.Revision != 2 {
		t.Fatal("append revision")
	}
	noop := f.req(Append, "op:noop", 1, 1, 2)
	if got := publish(t, f, noop, 1); got.Head.Revision != 2 {
		t.Fatal("no-op advanced history")
	}
	seal := f.req(Seal, "op:seal", 1, 1, 2)
	last := publish(t, f, seal, 1)
	if last.Head.Revision != 3 || last.Head.Phase != Sealed {
		t.Fatal("seal did not issue distinct revision")
	}
	if got := publish(t, f, r0, 0); got != first {
		t.Fatal("terminal state lost historical registration ACK")
	}
	if got := publish(t, f, seal, 1); got != last {
		t.Fatal("terminal exact retry")
	}
	h, err := f.store.Head(context.Background(), f.query(""))
	if err != nil || h != last.Head {
		t.Fatal("head must be separately current")
	}
	old, err := f.store.Operation(context.Background(), f.query(r0.OperationRef))
	if err != nil || old != first || old.Head.Revision == h.Revision {
		t.Fatal("past ACK became current head")
	}
	counts(t, f, 3, 5)
}

func TestStoreConflictingPayloadAndBinding(t *testing.T) {
	for _, name := range []string{"same-op-other-candidate", "same-op-other-bytes", "publisher", "scope", "journal", "key-ref", "stale-revision", "old-prefix", "lower-sequence"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, true)
			reg := f.req(Register, "op:register", 0, 0, 0)
			publish(t, f, reg, 0)
			app := f.req(Append, "op:append", 0, 1, 1)
			publish(t, f, app, 1)
			r := f.req(Append, "op:next", 1, 2, 2)
			data := f.snap[2]
			switch name {
			case "same-op-other-candidate":
				r.OperationRef = app.OperationRef
			case "same-op-other-bytes":
				r = app
				data = append(bytes.Clone(f.snap[1]), '\n')
			case "publisher":
				r.PublisherRef = "publisher:other"
			case "scope":
				r.Candidate.Scope.TaskRef = "task:other"
				r.Previous.Scope.TaskRef = "task:other"
			case "journal":
				r.Candidate.JournalID = strings.Repeat("e", 32)
				r.Previous.JournalID = r.Candidate.JournalID
			case "key-ref":
				r.Candidate.KeyRef = "key:other"
				r.Previous.KeyRef = "key:other"
			case "stale-revision":
				r.ExpectedRevision = 1
			case "old-prefix":
				r.Previous.MAC = strings.Repeat("e", 64)
			case "lower-sequence":
				r.Candidate = f.cp[0]
				data = f.snap[0]
			}
			if got, err := f.store.Publish(context.Background(), r, bytes.NewReader(data)); err == nil || got != (Receipt{}) {
				t.Fatal("conflicting publication accepted")
			}
			counts(t, f, 2, 2)
		})
	}
}

func TestStoreTerminalRejectsNewOperation(t *testing.T) {
	for _, kind := range []Kind{Seal, MarkIncomplete, Quarantine} {
		t.Run(string(kind), func(t *testing.T) {
			f := newFixture(t, true)
			publish(t, f, f.req(Register, "op:r", 0, 0, 0), 0)
			terminal := f.req(kind, "op:t", 0, 0, 1)
			index := 0
			if kind != Seal {
				index = -1
			}
			r := publish(t, f, terminal, index)
			if r.Head.Phase == Open || r.Head.Revision != 2 {
				t.Fatal("terminal transition")
			}
			if got := publish(t, f, terminal, index); got != r {
				t.Fatal("terminal retry")
			}
			next := f.req(Append, "op:resume", 0, 1, 2)
			if got, err := f.store.Publish(context.Background(), next, bytes.NewReader(f.snap[1])); !errors.Is(err, ErrTerminal) || got != (Receipt{}) {
				t.Fatal("terminal resume accepted")
			}
			counts(t, f, 2, 2)
		})
	}
}

func TestStoreConcurrentCASAndExactOperation(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(fmt.Sprint(same), func(t *testing.T) {
			f := newFixture(t, true)
			publish(t, f, f.req(Register, "op:r", 0, 0, 0), 0)
			entered := make(chan struct{}, 2)
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			f.host.keyHook = func() { entered <- struct{}{}; <-release }
			r1 := f.req(Append, "op:a", 0, 1, 1)
			r2 := f.req(Append, "op:b", 0, 2, 1)
			index := 2
			if same {
				r2 = r1
				index = 1
			}
			type outcome struct {
				r Receipt
				e error
			}
			results := make(chan outcome, 2)
			go func() {
				r, e := f.store.Publish(context.Background(), r1, bytes.NewReader(f.snap[1]))
				results <- outcome{r, e}
			}()
			go func() {
				r, e := f.store.Publish(context.Background(), r2, bytes.NewReader(f.snap[index]))
				results <- outcome{r, e}
			}()
			for i := 0; i < 2; i++ {
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("both verifiers did not reach barrier")
				}
			}
			// Both key callbacks run without an epoch/head row lock.
			tx, err := f.db.Begin()
			if err != nil {
				t.Fatal("probe transaction")
			}
			if _, err := tx.Exec(`SELECT 1 FROM audit_checkpoint_epochs FOR UPDATE NOWAIT`); err != nil {
				t.Fatal("key callback held epoch row lock")
			}
			if _, err := tx.Exec(`SELECT 1 FROM audit_checkpoint_heads FOR UPDATE NOWAIT`); err != nil {
				t.Fatal("key callback held head row lock")
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal("probe rollback")
			}
			unblock()
			a, b := <-results, <-results
			if same {
				if a.e != nil || b.e != nil || a.r != b.r {
					t.Fatal("exact concurrent retries diverged")
				}
			} else {
				if (a.e == nil) == (b.e == nil) || a.e != nil && !errors.Is(a.e, ErrConflict) || b.e != nil && !errors.Is(b.e, ErrConflict) {
					t.Fatal("CAS did not select exactly one writer")
				}
			}
			counts(t, f, 2, 2)
		})
	}
}

type beforeHeadQueryer struct {
	db   *sql.DB
	once sync.Once
	run  func()
}

func (q *beforeHeadQueryer) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if strings.HasPrefix(query, "SELECT h.publisher_ref") {
		q.once.Do(q.run)
	}
	return q.db.QueryRowContext(ctx, query, args...)
}

func TestStoreExactOperationCommitsBetweenReceiptMissAndHeadRead(t *testing.T) {
	for _, kind := range []Kind{Append, Seal} {
		t.Run(string(kind), func(t *testing.T) {
			f := newFixture(t, true)
			publish(t, f, f.req(Register, "op:r", 0, 0, 0), 0)
			r := f.req(kind, "op:interleaved", 0, 1, 1)
			racing, err := New(f.db, f.host, f.store.limits)
			if err != nil {
				t.Fatal(err)
			}
			var first Receipt
			racing.read = &beforeHeadQueryer{db: f.db, run: func() { first = publish(t, f, r, 1) }}
			got, err := racing.Publish(context.Background(), r, bytes.NewReader(f.snap[1]))
			if err != nil || got != first {
				t.Fatal("same operation's atomic receipt lost to a head transition")
			}
			counts(t, f, 2, 2)
		})
	}
}

func TestStoreForcesSynchronousCommitForPublication(t *testing.T) {
	f := newFixture(t, true)
	f.store.commit = func(tx *sql.Tx) error {
		var value string
		if err := tx.QueryRow(`SHOW synchronous_commit`).Scan(&value); err != nil || value != "on" {
			t.Error("publication did not require local durable WAL commit")
		}
		return tx.Commit()
	}
	// The supplied pool may inherit an unsafe session default; each publication
	// must explicitly override it within its transaction.
	f.db.SetMaxOpenConns(1)
	if _, err := f.db.Exec(`SET synchronous_commit=off`); err != nil {
		t.Fatal("fixture session setting")
	}
	publish(t, f, f.req(Register, "op:r", 0, 0, 0), 0)
	counts(t, f, 1, 1)
}

func TestStoreRollbackAndCommitUnknown(t *testing.T) {
	for _, mode := range []string{"insert-failure", "deferred-commit-failure", "lost-ack"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, true)
			if mode != "lost-ack" {
				if _, err := f.db.Exec(`CREATE FUNCTION fail_publication() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$`); err != nil {
					t.Fatal("fault fixture")
				}
				ddl := `CREATE TRIGGER fail_publication BEFORE INSERT ON audit_checkpoint_operations FOR EACH ROW EXECUTE FUNCTION fail_publication()`
				if mode == "deferred-commit-failure" {
					ddl = `CREATE CONSTRAINT TRIGGER fail_publication AFTER INSERT ON audit_checkpoint_operations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_publication()`
				}
				if _, err := f.db.Exec(ddl); err != nil {
					t.Fatal("fault trigger")
				}
			} else {
				f.store.commit = func(tx *sql.Tx) error {
					if err := tx.Commit(); err != nil {
						return err
					}
					return errors.New("synthetic lost commit acknowledgment")
				}
			}
			r := f.req(Register, "op:r", 0, 0, 0)
			got, err := f.store.Publish(context.Background(), r, bytes.NewReader(f.snap[0]))
			want := ErrCommitUnknown
			if mode == "insert-failure" {
				want = ErrStorage
			}
			if !errors.Is(err, want) || got != (Receipt{}) {
				t.Fatal("failed/unknown commit produced ACK")
			}
			if mode == "lost-ack" {
				counts(t, f, 1, 1)
				saved, err := f.store.Operation(context.Background(), f.query(r.OperationRef))
				if err != nil {
					t.Fatal(err)
				}
				if retry := publish(t, f, r, 0); retry != saved {
					t.Fatal("unknown commit lost exact outcome")
				}
			} else {
				counts(t, f, 0, 0)
				if _, err := f.store.Operation(context.Background(), f.query(r.OperationRef)); !errors.Is(err, ErrNotFound) {
					t.Fatal("rollback left an operation")
				}
				if _, err := f.db.Exec(`DROP TRIGGER fail_publication ON audit_checkpoint_operations`); err != nil {
					t.Fatal("remove fixture failure")
				}
				publish(t, f, r, 0)
				counts(t, f, 1, 1)
			}
		})
	}
}

func TestStoreEpochAndAuthorityFailClosed(t *testing.T) {
	for _, mode := range []string{"missing-epoch", "wrong-host-epoch", "epoch-race", "epoch-new-anchor", "auth-denied", "read-denied", "lease-cancelled", "wrong-read-scope", "nil-key"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, mode != "missing-epoch")
			r := f.req(Register, "op:r", 0, 0, 0)
			if mode == "read-denied" || mode == "wrong-read-scope" || mode == "epoch-new-anchor" {
				publish(t, f, r, 0)
			}
			switch mode {
			case "wrong-host-epoch":
				r.Epoch.EpochRef = "caller:forged"
			case "epoch-race":
				f.host.keyHook = func() {
					if _, err := f.db.Exec(`UPDATE audit_checkpoint_epochs SET generation=generation+1`); err != nil {
						t.Error("epoch fault fixture")
					}
				}
			case "epoch-new-anchor":
				f.host.epoch.Generation++
				f.host.epoch.AnchorDigest = sha256.Sum256([]byte("synthetic new anchor"))
				e := f.host.epoch
				if _, err := f.db.Exec(`UPDATE audit_checkpoint_epochs SET generation=$1,anchor_digest=$2`, int64(e.Generation), e.AnchorDigest[:]); err != nil {
					t.Fatal("epoch fixture")
				}
			case "auth-denied", "read-denied":
				f.host.deny = true
			case "lease-cancelled":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				f.host.leaseCtx = ctx
			case "nil-key":
				f.host.key = nil
			}
			if mode == "read-denied" || mode == "wrong-read-scope" || mode == "epoch-new-anchor" {
				q := f.query("")
				if mode == "wrong-read-scope" {
					q.Binding.Scope.TaskRef = "task:other"
				}
				if got, err := f.store.Head(context.Background(), q); err == nil || got != (Head{}) {
					t.Fatal("unauthorized/stale head read")
				}
				q.OperationRef = r.OperationRef
				if got, err := f.store.Operation(context.Background(), q); err == nil || got != (Receipt{}) {
					t.Fatal("unauthorized/stale operation read")
				}
				counts(t, f, 1, 1)
			} else {
				if got, err := f.store.Publish(context.Background(), r, bytes.NewReader(f.snap[0])); err == nil || got != (Receipt{}) {
					t.Fatal("missing authority accepted")
				}
				counts(t, f, 0, 0)
			}
		})
	}
}

func TestStoreValidMACForkRejected(t *testing.T) {
	f := newFixture(t, true)
	publish(t, f, f.req(Register, "op:r", 0, 0, 0), 0)
	publish(t, f, f.req(Append, "op:a", 0, 1, 1), 1)
	lines := bytes.Split(f.snap[2][:len(f.snap[2])-1], []byte{'\n'})
	var header struct {
		Header json.RawMessage `json:"header"`
		MAC    string          `json:"mac"`
	}
	if err := json.Unmarshal(lines[0], &header); err != nil {
		t.Fatal("header fixture")
	}
	previous := header.MAC
	for i := 1; i < len(lines); i++ {
		type payload struct {
			Sequence uint64         `json:"sequence"`
			Previous string         `json:"previous"`
			Event    transmit.Event `json:"event"`
		}
		var entry struct {
			Record payload `json:"record"`
			MAC    string  `json:"mac"`
		}
		if err := json.Unmarshal(lines[i], &entry); err != nil {
			t.Fatal("event fixture")
		}
		entry.Record.Previous = previous
		if i == 1 {
			entry.Record.Event.ResourceRef = "resource:fork"
		}
		body, err := json.Marshal(entry.Record)
		if err != nil {
			t.Fatal("payload fixture")
		}
		mac := hmac.New(sha256.New, f.host.key)
		_, _ = mac.Write([]byte("ABC2LAB.auditlog.event.v1\x00"))
		_, _ = mac.Write(body)
		entry.MAC = hex.EncodeToString(mac.Sum(nil))
		previous = entry.MAC
		lines[i], err = json.Marshal(entry)
		if err != nil {
			t.Fatal("fork fixture")
		}
	}
	fork := append(bytes.Join(lines, []byte{'\n'}), '\n')
	candidate := f.cp[2]
	candidate.MAC = previous
	if _, err := auditlog.Verify(context.Background(), bytes.NewReader(fork), f.host.key, candidate); err != nil {
		t.Fatal("fork must independently have valid MAC")
	}
	r := f.req(Append, "op:fork", 1, 2, 2)
	r.Candidate = candidate
	if got, err := f.store.Publish(context.Background(), r, bytes.NewReader(fork)); !errors.Is(err, ErrVerification) || got != (Receipt{}) {
		t.Fatal("MAC-valid fork advanced head")
	}
	counts(t, f, 2, 2)
}

func TestStoreSnapshotBoundariesAndCancellation(t *testing.T) {
	for _, mode := range []string{"bytes", "events", "tail", "cancel", "no-progress", "source-error", "mark-with-source"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, true)
			r := f.req(Register, "op:r", 0, 0, 0)
			ctx := context.Background()
			var source io.Reader = bytes.NewReader(f.snap[0])
			switch mode {
			case "bytes":
				f.store.limits.MaxBytes = int64(len(f.snap[0]) - 1)
			case "events":
				publish(t, f, r, 0)
				f.store.limits.MaxEvents = 1
				r = f.req(Append, "op:a", 0, 2, 1)
				source = bytes.NewReader(f.snap[2])
			case "tail":
				source = bytes.NewReader(append(bytes.Clone(f.snap[0]), []byte("{}\n")...))
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "no-progress":
				source = emptyReader{}
			case "source-error":
				source = errorReader{}
			case "mark-with-source":
				publish(t, f, r, 0)
				r = f.req(MarkIncomplete, "op:bad", 0, 0, 1)
			}
			if got, err := f.store.Publish(ctx, r, source); err == nil || got != (Receipt{}) {
				t.Fatal("invalid source produced ACK")
			}
			n := 0
			if mode == "events" || mode == "mark-with-source" {
				n = 1
			}
			counts(t, f, n, n)
		})
	}
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, nil }

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("private source error must not escape")
}

func TestStoreDDLImmutableAndMigrationIdempotent(t *testing.T) {
	f := newFixture(t, true)
	publish(t, f, f.req(Register, "op:r", 0, 0, 0), 0)
	if err := Migrate(context.Background(), f.db); err != nil {
		t.Fatal("explicit repeated migration")
	}
	for _, table := range []string{"audit_checkpoint_history", "audit_checkpoint_operations"} {
		if _, err := f.db.Exec(`DELETE FROM ` + table); err == nil {
			t.Fatal("immutable history/operation deletion")
		}
	}
	counts(t, f, 1, 1)
}

func TestStoreConfigRejectsMissingHostAndLimits(t *testing.T) {
	var host *fakeHost
	if _, err := New(&sql.DB{}, host, auditlog.ExtensionLimits{MaxBytes: 1, MaxEvents: 1, Timeout: time.Second}); !errors.Is(err, ErrConfig) {
		t.Fatal("typed nil authority")
	}
	if _, err := New(&sql.DB{}, &fakeHost{}, auditlog.ExtensionLimits{}); !errors.Is(err, ErrConfig) {
		t.Fatal("implicit limits")
	}
}
