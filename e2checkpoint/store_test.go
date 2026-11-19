package e2checkpoint

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
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/transmit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

type testHost struct {
	epoch       Epoch
	binding     Binding
	allowNew    bool
	key         []byte
	denyHead    atomic.Bool
	denyPublish atomic.Bool
	acquired    atomic.Int64
	released    atomic.Int64
}
type testLease struct{ host *testHost }

func (h *testHost) Acquire(_ context.Context, a Access) (Lease, error) {
	if h.allowNew && h.binding == (Binding{}) && a.Purpose == PublishAccess && a.Publication.Kind == Register {
		h.binding = a.Binding // 합성 fixture에서만 처음 저널 ID를 결속한다.
	}
	if a.Epoch != h.epoch || a.Binding != h.binding ||
		a.Purpose != PublishAccess && a.Purpose != HeadAccess && a.Purpose != VerifyAccess ||
		a.Purpose == HeadAccess && h.denyHead.Load() ||
		a.Purpose == PublishAccess && h.denyPublish.Load() {
		return nil, errors.New("synthetic authority denied")
	}
	h.acquired.Add(1)
	return testLease{h}, nil
}
func (l testLease) Context() context.Context { return context.Background() }
func (l testLease) Epoch() Epoch             { return l.host.epoch }
func (l testLease) ResolveKey(_ context.Context, b Binding) ([]byte, error) {
	if b != l.host.binding {
		return nil, errors.New("synthetic key denied")
	}
	return l.host.key, nil
}
func (l testLease) Release() { l.host.released.Add(1) }

type nilLeaseHost struct{}
type nilLease struct{}

func (nilLeaseHost) Acquire(context.Context, Access) (Lease, error) {
	var lease *nilLease
	return lease, nil
}
func (*nilLease) Context() context.Context                            { panic("typed nil lease used") }
func (*nilLease) Epoch() Epoch                                        { panic("typed nil lease used") }
func (*nilLease) ResolveKey(context.Context, Binding) ([]byte, error) { panic("typed nil lease used") }
func (*nilLease) Release()                                            { panic("typed nil lease used") }

func TestValidationRejectsUntrustedInputs(t *testing.T) {
	if _, err := New(nil, &testHost{}, 1024, time.Second); !errors.Is(err, ErrConfig) {
		t.Fatal(err)
	}
	e := Epoch{StoreRef: "store-test", EpochRef: "epoch-test", Generation: 1, AnchorDigest: sha256.Sum256([]byte("anchor"))}
	b := Binding{PublisherRef: "publisher-test", KeyRef: "key-test", JournalID: "0123456789abcdef0123456789abcdef",
		Scope: transmit.Scope{RunRef: "run-test", TaskRef: "task-test", CaseRef: "case-test", ResetRef: "reset-test", PolicyRef: "policy-test"}}
	cp := e2proxy.JournalCheckpoint{Version: e2proxy.JournalVersionV3, JournalID: b.JournalID, Scope: b.Scope,
		MAC: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	db, err := sql.Open("pgx", "postgres://localhost/unused")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var absent *testHost
	if _, err := New(db, absent, 1024, time.Second); !errors.Is(err, ErrConfig) {
		t.Fatal("typed nil Host accepted", err)
	}
	store, err := New(db, nilLeaseHost{}, 1024, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Head(t.Context(), Query{Epoch: e, Binding: b}); !errors.Is(err, ErrAuthority) {
		t.Fatal("typed nil Lease accepted", err)
	}
	if !validEpoch(e) || !validBinding(b) || !validRequest(Request{Epoch: e, Binding: b, Kind: Register, Candidate: cp}) {
		t.Fatal("valid synthetic binding rejected")
	}
	for _, bad := range []Request{
		{Epoch: e, Binding: b, Kind: Append, Candidate: cp, Previous: cp, ExpectedRevision: 1},
		{Epoch: e, Binding: b, Kind: Seal, Candidate: cp},
		{Epoch: e, Binding: b, Kind: Register, Candidate: e2proxy.JournalCheckpoint{}},
		{Epoch: e, Binding: b, Kind: "unknown", Candidate: cp},
	} {
		if validRequest(bad) {
			t.Fatal("invalid publication accepted")
		}
	}
}

func testFixture(t *testing.T) (*Store, *testHost, *e2proxy.Journal, string) {
	t.Helper()
	dsn := os.Getenv("ARTEX_AUDIT_TEST_DSN")
	if dsn == "" {
		t.Skip("dedicated PostgreSQL 16 not supplied")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test PostgreSQL configuration")
	}
	admin := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = admin.Close() })
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	schemaName := "e2_cp_" + hex.EncodeToString(random[:])
	if _, err := admin.Exec("CREATE SCHEMA " + schemaName); err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schemaName
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(4)
	t.Cleanup(func() {
		_ = db.Close()
		if _, err := admin.Exec("DROP SCHEMA " + schemaName + " CASCADE"); err != nil {
			t.Error("fixture cleanup failed")
		}
	})
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	host := &testHost{epoch: Epoch{StoreRef: "store-test", EpochRef: "epoch-test", Generation: 1,
		AnchorDigest: sha256.Sum256([]byte("separately owned test anchor"))}, key: bytes.Repeat([]byte{0x58}, 32)}
	e := host.epoch
	if _, err := db.Exec("INSERT INTO e2_http_audit_epochs VALUES($1,$2,$3,$4)",
		e.StoreRef, e.EpochRef, int64(e.Generation), e.AnchorDigest[:]); err != nil {
		t.Fatal(err)
	}
	store, err := New(db, host, 1<<20, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	scope := transmit.Scope{RunRef: "run-test", TaskRef: "task-test", CaseRef: "case-test", ResetRef: "reset-test", PolicyRef: "policy-test"}
	journal, err := e2proxy.CreateJournalV3(dir, "run.journal", host.key, scope, "key-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	host.binding = Binding{PublisherRef: "publisher-test", KeyRef: "key-test", Scope: scope, JournalID: journal.Checkpoint().JournalID}
	t.Cleanup(func() {
		if host.acquired.Load() != host.released.Load() {
			t.Error("authority lease leaked")
		}
	})
	return store, host, journal, filepath.Join(dir, "run.journal")
}
func testEvent(scope transmit.Scope, index uint64) e2proxy.Event {
	return e2proxy.Event{Index: index, Stage: "result", Receipt: e2proxy.Receipt{
		Version: e2proxy.JournalVersionV3, IntentID: 1, Scope: scope, PolicyRevision: "revision-test",
		CallRef: "call-test", AttemptID: "0123456789abcdef0123456789abcdef", Sequence: index, Method: "GET",
		Decision: "denied", Reason: e2proxy.PolicyDenied, NotSent: true, AuditComplete: true,
		At: time.Unix(1_700_000_000, 0).UTC(),
	}}
}
func snapshot(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func request(h *testHost, kind Kind, revision uint64, previous, candidate e2proxy.JournalCheckpoint) Request {
	return Request{Epoch: h.epoch, Binding: h.binding, Kind: kind, ExpectedRevision: revision,
		Previous: previous, Candidate: candidate}
}

func TestCurrentHeadPublicationAndFailures(t *testing.T) {
	store, host, journal, path := testFixture(t)
	initial := journal.Checkpoint()
	first := snapshot(t, path)
	q := Query{Epoch: host.epoch, Binding: host.binding}
	if _, err := store.Head(t.Context(), q); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing head", err)
	}
	r0 := request(host, Register, 0, e2proxy.JournalCheckpoint{}, initial)
	head, err := store.Publish(t.Context(), r0, bytes.NewReader(first))
	if err != nil || head.Revision != 1 || head.Checkpoint != initial || head.Phase != Open {
		t.Fatal(head, err)
	}
	if _, err := store.Publish(t.Context(), r0, bytes.NewReader(first)); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate register", err)
	}
	if err := journal.Record(t.Context(), testEvent(host.binding.Scope, 1)); err != nil {
		t.Fatal(err)
	}
	second := snapshot(t, path)
	next := journal.Checkpoint()
	r1 := request(host, Append, 1, initial, next)
	if _, err := store.Publish(t.Context(), r1, bytes.NewReader(first)); !errors.Is(err, ErrIntegrity) {
		t.Fatal("missing tail accepted", err)
	}
	head, err = store.Publish(t.Context(), r1, bytes.NewReader(second))
	if err != nil || head.Revision != 2 || head.Checkpoint != next {
		t.Fatal(head, err)
	}
	if _, err := store.Publish(t.Context(), r1, bytes.NewReader(second)); !errors.Is(err, ErrConflict) {
		t.Fatal("old revision accepted", err)
	}
	if err := journal.Record(t.Context(), testEvent(host.binding.Scope, 2)); err != nil {
		t.Fatal(err)
	}
	third := snapshot(t, path)
	last := journal.Checkpoint()
	host.denyHead.Store(true)
	if _, err := store.Publish(t.Context(), request(host, Append, 2, next, last), bytes.NewReader(third)); !errors.Is(err, ErrACKUnknown) {
		t.Fatal("missing readback ACK accepted", err)
	}
	host.denyHead.Store(false)
	head, err = store.Head(t.Context(), q)
	if err != nil || head.Revision != 3 || head.Checkpoint != last {
		t.Fatal(head, err)
	}
	if _, err := store.Publish(t.Context(), request(host, Append, 2, next, last), bytes.NewReader(third)); !errors.Is(err, ErrConflict) {
		t.Fatal("uncertain append retried", err)
	}
	head, err = store.Publish(t.Context(), request(host, Seal, 3, last, last), bytes.NewReader(third))
	if err != nil || head.Phase != Sealed || head.Revision != 4 {
		t.Fatal(head, err)
	}
	summary, verified, err := store.VerifyCurrent(t.Context(), q, bytes.NewReader(third))
	if err != nil || verified != head || summary.Checkpoint != last || summary.Events != 2 {
		t.Fatal("current head validation", err)
	}
	if _, _, err := store.VerifyCurrent(t.Context(), q, bytes.NewReader(second)); !errors.Is(err, ErrIntegrity) {
		t.Fatal("stale authentic snapshot accepted", err)
	}
	mutated := bytes.Clone(third)
	mutated[len(mutated)-10] ^= 1
	if _, _, err := store.VerifyCurrent(t.Context(), q, bytes.NewReader(mutated)); !errors.Is(err, ErrIntegrity) {
		t.Fatal("changed journal accepted", err)
	}
	if _, err := store.Publish(t.Context(), request(host, Append, 4, last, last), bytes.NewReader(third)); !errors.Is(err, ErrRequest) {
		t.Fatal("invalid append accepted", err)
	}
	// Epoch rotation invalidates the old head even while its row remains.
	if _, err := store.db.Exec("UPDATE e2_http_audit_epochs SET epoch_ref='epoch-next' WHERE store_ref=$1",
		host.epoch.StoreRef); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Head(t.Context(), q); !errors.Is(err, ErrEpoch) {
		t.Fatal("stale epoch read", err)
	}
	if _, _, err := store.VerifyCurrent(t.Context(), q, bytes.NewReader(third)); !errors.Is(err, ErrEpoch) {
		t.Fatal("stale epoch verified", err)
	}
}

type changingReader struct {
	reader  *bytes.Reader
	before  func() error
	changed bool
}

func (r *changingReader) Read(p []byte) (int, error) {
	if !r.changed {
		r.changed = true
		if err := r.before(); err != nil {
			return 0, err
		}
	}
	return r.reader.Read(p)
}
func TestVerifyCurrentRejectsHeadChangeDuringSnapshot(t *testing.T) {
	store, host, journal, path := testFixture(t)
	initial, data := journal.Checkpoint(), snapshot(t, path)
	if _, err := store.Publish(t.Context(), request(host, Register, 0, e2proxy.JournalCheckpoint{}, initial),
		bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	source := &changingReader{reader: bytes.NewReader(data), before: func() error {
		_, err := store.db.Exec("UPDATE e2_http_audit_heads SET revision=revision+1 WHERE store_ref=$1 AND run_ref=$2",
			host.epoch.StoreRef, host.binding.Scope.RunRef)
		return err
	}}
	if _, _, err := store.VerifyCurrent(t.Context(), Query{Epoch: host.epoch, Binding: host.binding}, source); !errors.Is(err, ErrIntegrity) {
		t.Fatal("head changed during read", err)
	}
}

// reSignEarlierEvent는 같은 저널 ID와 감사 키로 이전 사건을 바꾼 다른 유효 체인을 만든다.
// 새 후보의 MAC 검증만 통과해도 저장된 접두부의 바이트 digest가 달라야 한다.
func reSignEarlierEvent(t *testing.T, data, key []byte, candidate e2proxy.JournalCheckpoint) ([]byte, e2proxy.JournalCheckpoint) {
	t.Helper()
	type record struct {
		Index    uint64
		Previous string
		Event    e2proxy.Event
	}
	type line struct {
		Kind   string
		Record *record `json:",omitempty"`
		MAC    string
	}
	parts := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(parts) != 3 {
		t.Fatal("fixture needs header and two events")
	}
	var header struct{ MAC string }
	if err := json.Unmarshal(parts[0], &header); err != nil {
		t.Fatal(err)
	}
	previous := header.MAC
	altered := append(bytes.Clone(parts[0]), '\n')
	for index, part := range parts[1:] {
		var row line
		if err := json.Unmarshal(part, &row); err != nil || row.Record == nil {
			t.Fatal("invalid fixture row", err)
		}
		if index == 0 {
			row.Record.Event.PolicyRevision = "revision-replaced"
		}
		row.Record.Previous = previous
		encoded, err := json.Marshal(row.Record)
		if err != nil {
			t.Fatal(err)
		}
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(e2proxy.JournalVersionV3 + "\x00event\x00"))
		mac.Write(encoded)
		row.MAC = hex.EncodeToString(mac.Sum(nil))
		previous = row.MAC
		encoded, err = json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		altered = append(append(altered, encoded...), '\n')
	}
	candidate.MAC = previous
	return altered, candidate
}

func TestAppendRejectsReSignedEarlierHistory(t *testing.T) {
	store, host, journal, path := testFixture(t)
	initial := journal.Checkpoint()
	if _, err := store.Publish(t.Context(), request(host, Register, 0, e2proxy.JournalCheckpoint{}, initial),
		bytes.NewReader(snapshot(t, path))); err != nil {
		t.Fatal(err)
	}
	if err := journal.Record(t.Context(), testEvent(host.binding.Scope, 1)); err != nil {
		t.Fatal(err)
	}
	first := journal.Checkpoint()
	if _, err := store.Publish(t.Context(), request(host, Append, 1, initial, first),
		bytes.NewReader(snapshot(t, path))); err != nil {
		t.Fatal(err)
	}
	if err := journal.Record(t.Context(), testEvent(host.binding.Scope, 2)); err != nil {
		t.Fatal(err)
	}
	original, final := snapshot(t, path), journal.Checkpoint()
	altered, alteredFinal := reSignEarlierEvent(t, original, host.key, final)
	if _, err := e2proxy.VerifyJournal(t.Context(), bytes.NewReader(altered), host.key, alteredFinal); err != nil {
		t.Fatal("독립적으로 유효한 다른 체인이 아님", err)
	}
	if _, err := store.Publish(t.Context(), request(host, Append, 2, first, alteredFinal), bytes.NewReader(altered)); !errors.Is(err, ErrConflict) {
		t.Fatal("게시된 접두부의 다른 체인을 허용", err)
	}
	head, err := store.Publish(t.Context(), request(host, Append, 2, first, final), bytes.NewReader(original))
	if err != nil || head.Revision != 3 || head.Checkpoint != final {
		t.Fatal("정상 접두부 잇기 실패", err)
	}
}

func TestPublishAfterEpochRotationDoesNotCommit(t *testing.T) {
	store, host, journal, path := testFixture(t)
	initial := journal.Checkpoint()
	if _, err := store.Publish(t.Context(), request(host, Register, 0, e2proxy.JournalCheckpoint{}, initial),
		bytes.NewReader(snapshot(t, path))); err != nil {
		t.Fatal(err)
	}
	if err := journal.Record(t.Context(), testEvent(host.binding.Scope, 1)); err != nil {
		t.Fatal(err)
	}
	next := journal.Checkpoint()
	if _, err := store.db.Exec("UPDATE e2_http_audit_epochs SET generation=2 WHERE store_ref=$1", host.epoch.StoreRef); err != nil {
		t.Fatal(err)
	}
	_, err := store.Publish(t.Context(), request(host, Append, 1, initial, next), bytes.NewReader(snapshot(t, path)))
	var revision int64
	if qerr := store.db.QueryRow("SELECT revision FROM e2_http_audit_heads WHERE store_ref=$1", host.epoch.StoreRef).Scan(&revision); qerr != nil {
		t.Fatal(qerr)
	}
	if !errors.Is(err, ErrEpoch) || revision != 1 {
		t.Fatalf("stale epoch publish: err=%v revision=%d", err, revision)
	}
}

func TestVerifyCurrentRejectsEpochChangeDuringSnapshot(t *testing.T) {
	store, host, journal, path := testFixture(t)
	initial, data := journal.Checkpoint(), snapshot(t, path)
	if _, err := store.Publish(t.Context(), request(host, Register, 0, e2proxy.JournalCheckpoint{}, initial),
		bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	source := &changingReader{reader: bytes.NewReader(data), before: func() error {
		_, err := store.db.Exec("UPDATE e2_http_audit_epochs SET generation=2 WHERE store_ref=$1", host.epoch.StoreRef)
		return err
	}}
	if _, _, err := store.VerifyCurrent(t.Context(), Query{Epoch: host.epoch, Binding: host.binding}, source); !errors.Is(err, ErrEpoch) {
		t.Fatal("검증 중 epoch 교체를 허용", err)
	}
}
