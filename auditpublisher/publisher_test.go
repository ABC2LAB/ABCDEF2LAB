//go:build linux

package auditpublisher

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Only these tests provision an epoch and implement a synthetic Host. They do
// not prove production authority, process isolation or a trusted restore anchor.
type syntheticHost struct {
	epoch        auditcheckpoint.Epoch
	key          []byte
	scope        transmit.Scope
	publisher    string
	denyRegister atomic.Bool
	denyAppend   atomic.Bool
	allowSeal    atomic.Bool
	witness      atomic.Pointer[CloseWitness]
	acquired     atomic.Int64
	released     atomic.Int64
}
type syntheticLease struct{ host *syntheticHost }

type syntheticRootScope struct {
	ctx      context.Context
	cancel   context.CancelFunc
	anchor   *os.File
	dir      string
	released atomic.Int64
}

func (s *syntheticRootScope) Context() context.Context { return s.ctx }
func (s *syntheticRootScope) Anchor() *os.File         { return s.anchor }
func (s *syntheticRootScope) Directory() string        { return s.dir }
func (s *syntheticRootScope) Release()                 { s.released.Add(1) }

func (h *syntheticHost) Acquire(_ context.Context, a auditcheckpoint.Access) (auditcheckpoint.Lease, error) {
	if a.Epoch != h.epoch || a.Binding.PublisherRef != h.publisher || a.Binding.Scope != h.scope || a.Binding.KeyRef != "key:synthetic" ||
		a.Purpose == auditcheckpoint.PublishAccess && (a.Publication.Kind == auditcheckpoint.Register && h.denyRegister.Load() ||
			a.Publication.Kind == auditcheckpoint.Append && h.denyAppend.Load() ||
			a.Publication.Kind == auditcheckpoint.Seal && (!h.allowSeal.Load() || !h.witness.Load().Check(a))) {
		return nil, errors.New("synthetic authority refusal")
	}
	h.acquired.Add(1)
	return syntheticLease{host: h}, nil
}
func (l syntheticLease) Context() context.Context     { return context.Background() }
func (l syntheticLease) Epoch() auditcheckpoint.Epoch { return l.host.epoch }
func (l syntheticLease) ResolveKey(context.Context, auditcheckpoint.Binding) ([]byte, error) {
	return l.host.key, nil
}
func (l syntheticLease) Release() { l.host.released.Add(1) }

type publisherFixture struct {
	db     *sql.DB
	store  *auditcheckpoint.Store
	host   *syntheticHost
	root   *syntheticRootScope
	config Config
	path   string
}

func TestConfigJSONOmitsKeysAndCapabilities(t *testing.T) {
	key := []byte("synthetic-private-key-never-in-json")
	config := Config{Store: &auditcheckpoint.Store{}, RootScope: &syntheticRootScope{dir: "private-root"},
		Scope: transmit.Scope{RunRef: "private-run"}, Key: key,
		BindCloseWitness: func(*CloseWitness) error { return nil }, PublisherRef: "metadata-only"}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Config JSON refused redacted capabilities: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Store", "RootScope", "Scope", "Key", "BindCloseWitness"} {
		if _, present := fields[name]; present {
			t.Fatalf("Config JSON emitted private field %s", name)
		}
	}
	if bytes.Contains(encoded, []byte(base64.StdEncoding.EncodeToString(key))) ||
		bytes.Contains(encoded, []byte("private-root")) || bytes.Contains(encoded, []byte("private-run")) {
		t.Fatal("Config JSON exposed private key or scope contents")
	}
	if _, present := fields["PublisherRef"]; !present {
		t.Fatal("Config JSON omitted harmless metadata unexpectedly")
	}
}

func fixture(t *testing.T) *publisherFixture {
	t.Helper()
	dsn := os.Getenv("ARTEX_AUDIT_TEST_DSN")
	if dsn == "" {
		t.Skip("dedicated audit test PostgreSQL not supplied")
	}
	pg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid isolated PostgreSQL configuration")
	}
	admin := stdlib.OpenDB(*pg)
	t.Cleanup(func() { _ = admin.Close() })
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal("fixture randomness unavailable")
	}
	schema := "audit_pub_" + hex.EncodeToString(random[:])
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal("isolated fixture schema unavailable")
	}
	pg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*pg)
	db.SetMaxOpenConns(8)
	t.Cleanup(func() {
		_ = db.Close()
		if _, err := admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Error("isolated fixture cleanup failed")
		}
	})
	if err := auditcheckpoint.Migrate(context.Background(), db); err != nil {
		t.Fatal("checkpoint fixture migration failed")
	}
	epoch := auditcheckpoint.Epoch{StoreRef: "store:synthetic", EpochRef: "epoch:synthetic", Generation: 1,
		AnchorDigest: sha256.Sum256([]byte("test-only independent restore anchor"))}
	if _, err := db.Exec(`INSERT INTO audit_checkpoint_epochs VALUES($1,$2,$3,$4)`,
		epoch.StoreRef, epoch.EpochRef, int64(epoch.Generation), epoch.AnchorDigest[:]); err != nil {
		t.Fatal("synthetic epoch provisioning failed")
	}
	scope := transmit.Scope{RunRef: "run:synthetic", TaskRef: "task:synthetic", CaseRef: "case:synthetic",
		ResetRef: "reset:synthetic", PolicyRef: "policy:synthetic"}
	host := &syntheticHost{epoch: epoch, key: bytes.Repeat([]byte{0x47}, auditlog.KeyBytes), scope: scope, publisher: "publisher:synthetic"}
	store, err := auditcheckpoint.New(db, host, auditlog.ExtensionLimits{MaxBytes: 1 << 20, MaxEvents: 8, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal("checkpoint fixture construction failed")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	anchor, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = anchor.Close() })
	rootCtx, cancel := context.WithCancel(context.Background())
	root := &syntheticRootScope{ctx: rootCtx, cancel: cancel, anchor: anchor, dir: dir}
	t.Cleanup(cancel)
	f := &publisherFixture{db: db, store: store, host: host, root: root, path: filepath.Join(dir, "synthetic.jsonl")}
	f.config = Config{Store: store, Epoch: epoch, PublisherRef: host.publisher, RootScope: root,
		Filename: "synthetic.jsonl", Scope: scope, KeyRef: "key:synthetic", Key: host.key, MaxSnapshotBytes: 1 << 20,
		BindCloseWitness: func(w *CloseWitness) error {
			if !host.witness.CompareAndSwap(nil, w) {
				return errors.New("synthetic witness already bound")
			}
			return nil
		}}
	t.Cleanup(func() {
		if host.acquired.Load() != host.released.Load() {
			t.Error("synthetic Host lease leaked")
		}
	})
	return f
}

func (f *publisherFixture) open(t *testing.T) *Publisher {
	t.Helper()
	p, err := Open(context.Background(), f.config)
	if err != nil {
		t.Fatalf("publisher Open failed: %v", err)
	}
	t.Cleanup(func() {
		if !p.closed {
			_ = p.Abort()
		}
	})
	return p
}

func (f *publisherFixture) head(t *testing.T, p *Publisher) auditcheckpoint.Head {
	t.Helper()
	query := auditcheckpoint.Query{Epoch: f.host.epoch, Binding: auditcheckpoint.Binding{
		PublisherRef: f.host.publisher, Scope: f.host.scope, JournalID: p.head.Checkpoint.JournalID, KeyRef: "key:synthetic"}}
	h, err := f.store.Head(context.Background(), query)
	if err != nil {
		t.Fatalf("current head unavailable: %v", err)
	}
	return h
}

func (f *publisherFixture) counts(t *testing.T, history int) {
	t.Helper()
	var got int
	if err := f.db.QueryRow(`SELECT count(*) FROM audit_checkpoint_history`).Scan(&got); err != nil || got != history {
		t.Fatalf("history rows = %d, want %d", got, history)
	}
}

func intent(scope transmit.Scope, attempt uint64) transmit.Event {
	return transmit.Event{Stage: "intent", Receipt: transmit.Receipt{Scope: scope, Attempt: attempt,
		AccountRef: "owner", SessionRef: "session", ResourceRef: "resource", ApprovalRef: "approval",
		Method: "GET", At: time.Now().UTC()}}
}
func stopped(scope transmit.Scope, attempt uint64) transmit.Event {
	return transmit.Event{Stage: "result", Receipt: transmit.Receipt{Scope: scope, Attempt: attempt,
		AccountRef: "owner", SessionRef: "session", ResourceRef: "resource", ApprovalRef: "approval",
		Method: "GET", Reason: transmit.Stopped, At: time.Now().UTC()}}
}

func sealAccess(f *publisherFixture, p *Publisher) auditcheckpoint.Access {
	cp := p.head.Checkpoint
	return auditcheckpoint.Access{Purpose: auditcheckpoint.PublishAccess, Epoch: f.host.epoch,
		Binding: auditcheckpoint.Binding{PublisherRef: f.host.publisher, Scope: f.host.scope,
			JournalID: cp.JournalID, KeyRef: cp.KeyRef},
		Publication: auditcheckpoint.Request{Epoch: f.host.epoch, PublisherRef: f.host.publisher,
			Kind: auditcheckpoint.Seal, Previous: cp, Candidate: cp}}
}

func TestCloseWitnessExactBindingAndRealClose(t *testing.T) {
	f := fixture(t)
	p := f.open(t)
	access := sealAccess(f, p)
	w := f.host.witness.Load()
	if w == nil || w.Check(access) || (&CloseWitness{}).Check(access) || (*CloseWitness)(nil).Check(access) {
		t.Fatal("unclosed or zero witness authorized Seal")
	}
	f.host.allowSeal.Store(true) // synthetic drain assertion; this checks only local Close evidence
	if lease, err := f.host.Acquire(context.Background(), access); err == nil {
		lease.Release()
		t.Fatal("Host accepted Seal before Journal.Close")
	}
	if _, err := p.Seal(context.Background()); err != nil {
		t.Fatalf("Host could not inspect witness during Seal: %v", err)
	}
	if !w.Check(access) {
		t.Fatal("successful local Close was not evidenced")
	}
	wrong := access
	wrong.Binding.JournalID += ":other"
	if w.Check(wrong) {
		t.Fatal("witness authorized another journal")
	}
	wrong = access
	wrong.Publication.Candidate.Sequence++
	if w.Check(wrong) {
		t.Fatal("witness authorized another checkpoint")
	}
	wrong = access
	wrong.Epoch.Generation++
	wrong.Publication.Epoch = wrong.Epoch
	if w.Check(wrong) {
		t.Fatal("witness authorized another epoch")
	}
}

func TestCloseWitnessInvalidatedOnAbortAndRecordFailure(t *testing.T) {
	t.Run("abort", func(t *testing.T) {
		f := fixture(t)
		p := f.open(t)
		access := sealAccess(f, p)
		if err := p.Abort(); err != nil {
			t.Fatal(err)
		}
		if f.host.witness.Load().Check(access) {
			t.Fatal("aborted journal had Close evidence")
		}
	})
	t.Run("record-failure", func(t *testing.T) {
		f := fixture(t)
		p := f.open(t)
		f.host.denyAppend.Store(true)
		if err := p.Record(context.Background(), intent(f.host.scope, 1)); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("expected failed Record, got %v", err)
		}
		if f.host.witness.Load().Check(sealAccess(f, p)) {
			t.Fatal("failed journal had Close evidence")
		}
	})
}

func TestPublisherDurableHeaderAppendSeal(t *testing.T) {
	f := fixture(t)
	p := f.open(t)
	if h := f.head(t, p); h.Revision != 1 || h.Phase != auditcheckpoint.Open || h.Checkpoint.Sequence != 0 {
		t.Fatal("Open returned before a durable header head")
	}
	if ack, err := p.AcknowledgedHead(); err != nil || !sameHead(ack, f.head(t, p)) {
		t.Fatal("header ACK metadata unavailable")
	}
	if err := p.Record(context.Background(), intent(f.host.scope, 1)); err != nil {
		t.Fatal(err)
	}
	if h := f.head(t, p); h.Revision != 2 || h.Checkpoint.Sequence != 1 {
		t.Fatal("intent not durably acknowledged")
	}
	if ack, err := p.AcknowledgedHead(); err != nil || !sameHead(ack, f.head(t, p)) {
		t.Fatal("intent ACK metadata unavailable")
	}
	if err := p.Record(context.Background(), stopped(f.host.scope, 1)); err != nil {
		t.Fatal(err)
	}
	f.host.allowSeal.Store(true) // test-only Host authorization; no production drain proof
	final, err := p.Seal(context.Background())
	if err != nil || final.Phase != auditcheckpoint.Sealed || final.Revision != 4 || final.Checkpoint.Sequence != 2 {
		t.Fatalf("sealed head unavailable: %v", err)
	}
	if h := f.head(t, p); !sameHead(h, final) {
		t.Fatal("returned seal is not current")
	}
	if ack, err := p.AcknowledgedHead(); err != nil || !sameHead(ack, final) {
		t.Fatal("sealed ACK metadata unavailable")
	}
	if f.root.released.Load() != 1 {
		t.Fatal("sealed publisher retained root scope")
	}
	journal, err := os.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if _, err := auditlog.Verify(context.Background(), journal, f.host.key, final.Checkpoint); err != nil {
		t.Fatalf("preserved journal does not match sealed head: %v", err)
	}
	f.counts(t, 4)
	if err := p.Record(context.Background(), intent(f.host.scope, 2)); !errors.Is(err, ErrClosed) {
		t.Fatalf("record after seal = %v", err)
	}
}

func TestPublisherHeaderRefusalLeavesFileAndNoRecorder(t *testing.T) {
	f := fixture(t)
	f.host.denyRegister.Store(true)
	if p, err := Open(context.Background(), f.config); p != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("untrusted header returned recorder: %v", err)
	}
	if info, err := os.Stat(f.path); err != nil || info.Size() == 0 {
		t.Fatal("failed registration erased the synced header")
	}
	if f.root.released.Load() != 1 {
		t.Fatal("failed registration retained root scope")
	}
	if w := f.host.witness.Load(); w == nil || w.Check(auditcheckpoint.Access{}) || !w.failed {
		t.Fatal("failed Open did not invalidate its bound Close witness")
	}
	f.counts(t, 0)
}

func TestCloseWitnessBinderFailureInvalidatesAndPreservesHeader(t *testing.T) {
	f := fixture(t)
	f.config.BindCloseWitness = func(w *CloseWitness) error {
		f.host.witness.Store(w)
		return errors.New("synthetic binder failed after retaining witness")
	}
	if p, err := Open(context.Background(), f.config); p != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failed binder returned usable publisher: %v", err)
	}
	if w := f.host.witness.Load(); w == nil || !w.failed || w.ready {
		t.Fatal("failed binder retained usable Close evidence")
	}
	if f.root.released.Load() != 1 {
		t.Fatal("failed binder retained root scope")
	}
	if info, err := os.Stat(f.path); err != nil || info.Size() == 0 {
		t.Fatal("failed binder erased original synced header")
	}
	f.counts(t, 0)
}

func TestPublisherAbortAfterSetupFailurePreservesOpenHead(t *testing.T) {
	f := fixture(t)
	p := f.open(t)
	if err := p.Abort(); err != nil {
		t.Fatal(err)
	}
	if f.root.released.Load() != 1 || !p.closed {
		t.Fatal("Abort did not release local publisher resources")
	}
	if h := f.head(t, p); h.Phase != auditcheckpoint.Open || h.Checkpoint.Sequence != 0 {
		t.Fatal("Abort invented an external sealed phase")
	}
	if _, err := p.AcknowledgedHead(); !errors.Is(err, ErrFailed) {
		t.Fatal("aborted publisher exposed cached ACK as current authority")
	}
	if err := p.Record(context.Background(), intent(f.host.scope, 1)); !errors.Is(err, ErrClosed) {
		t.Fatalf("aborted publisher accepted a record: %v", err)
	}
	if info, err := os.Stat(f.path); err != nil || info.Size() == 0 {
		t.Fatal("Abort removed the original journal")
	}
}

func TestPublisherIntentACKRefusalBlocksTargetAndSticks(t *testing.T) {
	f := fixture(t)
	p := f.open(t)
	f.host.denyAppend.Store(true)
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	u, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	account := idor.AccountRef{Label: "owner", Handle: "handle:owner", Status: oracle.SubjectVerified}
	cfg := transmit.Config{Scope: f.host.scope, Origin: target.URL, ConnectIP: netip.MustParseAddr(u.Hostname()),
		ExpiresAt: time.Now().Add(time.Minute), Sessions: []transmit.Session{{Account: account, ExpiresAt: time.Now().Add(time.Minute)}},
		Resources:   []transmit.Resource{{Ref: "resource", URL: target.URL + "/item", ApprovalRef: "approval", ReadOnly: true, Accounts: []string{"owner"}}},
		MaxRequests: 2, RequestTimeout: time.Second, MaxBodyBytes: 1024, Recorder: p}
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	request := idor.AccessRequest{As: account, ResourceRef: "resource", Method: http.MethodGet}
	for i := 0; i < 2; i++ {
		_, receipt, ok := tx.SendDetailed(request)
		if ok || receipt.Admitted || receipt.Reason != transmit.AuditUnavailable {
			t.Fatalf("send %d bypassed denied intent ACK: %v", i, receipt.Reason)
		}
	}
	tx.StopAndWait()
	if hits.Load() != 0 || !p.failed {
		t.Fatal("denied intent ACK reached target or was not sticky")
	}
	if _, err := p.AcknowledgedHead(); !errors.Is(err, ErrFailed) {
		t.Fatal("failed recorder exposed cached ACK as current authority")
	}
	if h := f.head(t, p); h.Revision != 1 || h.Checkpoint.Sequence != 0 {
		t.Fatal("failed intent was represented as a durable head")
	}
	if info, err := os.Stat(f.path); err != nil || info.Size() == 0 {
		t.Fatal("failed intent erased its original journal")
	}
}

func TestPublisherLoopbackDrainThenSeal(t *testing.T) {
	f := fixture(t)
	p := f.open(t)
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	u, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	account := idor.AccountRef{Label: "owner", Handle: "handle:owner", Status: oracle.SubjectVerified}
	cfg := transmit.Config{Scope: f.host.scope, Origin: target.URL, ConnectIP: netip.MustParseAddr(u.Hostname()),
		ExpiresAt: time.Now().Add(time.Minute), Sessions: []transmit.Session{{Account: account, ExpiresAt: time.Now().Add(time.Minute)}},
		Resources:   []transmit.Resource{{Ref: "resource", URL: target.URL + "/item", ApprovalRef: "approval", ReadOnly: true, Accounts: []string{"owner"}}},
		MaxRequests: 2, RequestTimeout: time.Second, MaxBodyBytes: 1024, Recorder: p}
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, receipt, ok := tx.SendDetailed(idor.AccessRequest{As: account, ResourceRef: "resource", Method: http.MethodGet})
	if !ok || receipt.Reason != transmit.Completed || hits.Load() != 1 {
		t.Fatalf("loopback send not durably recorded: %v", receipt.Reason)
	}
	if h := f.head(t, p); h.Revision != 3 || h.Phase != auditcheckpoint.Open || h.Checkpoint.Sequence != 2 {
		t.Fatal("intent and result did not reach current head")
	}
	tx.StopAndWait()
	f.host.allowSeal.Store(true) // test-only authorization after the real transmitter drain
	final, err := p.Seal(context.Background())
	if err != nil || final.Phase != auditcheckpoint.Sealed || final.Revision != 4 || hits.Load() != 1 {
		t.Fatalf("drained run not sealed exactly once: %v", err)
	}
}

func TestPublisherResultAndSealRefusalsNeverSucceed(t *testing.T) {
	t.Run("result", func(t *testing.T) {
		f := fixture(t)
		p := f.open(t)
		if err := p.Record(context.Background(), intent(f.host.scope, 1)); err != nil {
			t.Fatal(err)
		}
		f.host.denyAppend.Store(true)
		if err := p.Record(context.Background(), stopped(f.host.scope, 1)); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("result ACK failure = %v", err)
		}
		if _, err := p.Seal(context.Background()); !errors.Is(err, ErrFailed) {
			t.Fatalf("failed result sealed: %v", err)
		}
		if h := f.head(t, p); h.Phase != auditcheckpoint.Open || h.Checkpoint.Sequence != 1 {
			t.Fatal("failed result promoted to sealed or newest head")
		}
		if data, err := os.ReadFile(f.path); err != nil || bytes.Count(data, []byte{'\n'}) != 3 {
			t.Fatal("failed result truncated original file")
		}
	})
	t.Run("seal", func(t *testing.T) {
		f := fixture(t)
		p := f.open(t)
		if err := p.Record(context.Background(), intent(f.host.scope, 1)); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Seal(context.Background()); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("unauthenticated seal succeeded: %v", err)
		}
		if f.host.witness.Load().Check(sealAccess(f, p)) {
			t.Fatal("failed Seal retained Close authorization")
		}
		if h := f.head(t, p); h.Phase != auditcheckpoint.Open || h.Checkpoint.Sequence != 1 {
			t.Fatal("seal failure changed current phase")
		}
	})
}

type uncertainCommitStore struct {
	checkpointStore
	seen    bool
	request auditcheckpoint.Request
	source  []byte
	calls   int
}

func (s *uncertainCommitStore) Publish(ctx context.Context, req auditcheckpoint.Request, source *bytes.Reader) (auditcheckpoint.Receipt, error) {
	if req.Kind != auditcheckpoint.Append {
		return s.checkpointStore.Publish(ctx, req, source)
	}
	s.calls++
	data, err := io.ReadAll(source)
	if err != nil {
		return auditcheckpoint.Receipt{}, err
	}
	if !s.seen {
		s.seen, s.request, s.source = true, req, bytes.Clone(data)
		if _, err := s.checkpointStore.Publish(ctx, req, bytes.NewReader(data)); err != nil {
			return auditcheckpoint.Receipt{}, err
		}
		return auditcheckpoint.Receipt{}, auditcheckpoint.ErrCommitUnknown
	}
	if req != s.request || !bytes.Equal(data, s.source) {
		return auditcheckpoint.Receipt{}, errors.New("retry changed operation or snapshot")
	}
	return s.checkpointStore.Publish(ctx, req, bytes.NewReader(data))
}

func TestPublisherReconcilesExactUnknownCommit(t *testing.T) {
	f := fixture(t)
	p := f.open(t)
	uncertain := &uncertainCommitStore{checkpointStore: p.store}
	p.store = uncertain
	if err := p.Record(context.Background(), intent(f.host.scope, 1)); err != nil {
		t.Fatalf("exact unknown-commit retry failed: %v", err)
	}
	if uncertain.calls != 2 || f.head(t, p).Revision != 2 {
		t.Fatal("unknown commit was duplicated or not reconciled against current head")
	}
	f.counts(t, 2)
}

func TestPublisherCancellationConcurrencyAndHardlink(t *testing.T) {
	t.Run("canceled", func(t *testing.T) {
		f := fixture(t)
		p := f.open(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := p.Record(ctx, intent(f.host.scope, 1)); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("canceled record = %v", err)
		}
		if err := p.Record(context.Background(), intent(f.host.scope, 2)); !errors.Is(err, ErrFailed) {
			t.Fatalf("canceled recorder was reusable: %v", err)
		}
		if f.head(t, p).Checkpoint.Sequence != 0 {
			t.Fatal("canceled record advanced head")
		}
	})
	t.Run("concurrent", func(t *testing.T) {
		f := fixture(t)
		p := f.open(t)
		var wg sync.WaitGroup
		for i := uint64(1); i <= 4; i++ {
			wg.Add(1)
			go func(attempt uint64) {
				defer wg.Done()
				if err := p.Record(context.Background(), stopped(f.host.scope, attempt)); err != nil {
					t.Errorf("concurrent record %d: %v", attempt, err)
				}
			}(i)
		}
		wg.Wait()
		if h := f.head(t, p); h.Revision != 5 || h.Checkpoint.Sequence != 4 {
			t.Fatal("concurrent records did not serialize at durable head")
		}
	})
	t.Run("hardlink", func(t *testing.T) {
		f := fixture(t)
		p := f.open(t)
		if err := os.Link(f.path, f.path+".link"); err != nil {
			t.Fatal(err)
		}
		if err := p.Record(context.Background(), intent(f.host.scope, 1)); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("hardlinked journal accepted: %v", err)
		}
		if f.head(t, p).Checkpoint.Sequence != 0 {
			t.Fatal("hardlink violation advanced durable head")
		}
	})
	t.Run("root-revoked", func(t *testing.T) {
		f := fixture(t)
		p := f.open(t)
		f.root.cancel()
		if err := p.Record(context.Background(), intent(f.host.scope, 1)); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("revoked root accepted record: %v", err)
		}
		if f.head(t, p).Checkpoint.Sequence != 0 {
			t.Fatal("revoked root advanced durable head")
		}
	})
}
