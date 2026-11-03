// Package auditpublisher joins a local HMAC journal to the inactive, host-authorized
// checkpoint store. It does not create publisher authority, store epochs, or keys.
package auditpublisher

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"sync"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

var (
	ErrConfig      = errors.New("auditpublisher: invalid configuration")
	ErrUnavailable = errors.New("auditpublisher: checkpoint publication unavailable")
	ErrFailed      = errors.New("auditpublisher: recorder failed")
	ErrClosed      = errors.New("auditpublisher: recorder closed")
)

// RootScope is an already-authorized deployment lease, not a path validator.
// The host must bind its long-held Anchor, directory, exclusive writer scope and
// revocation Context to this execution. Methods and Release must not reenter the
// Publisher, start SQL work, or drain a transmitter while Publisher.mu is held.
// Open takes ownership of a valid lease and releases it on failure/Seal/Abort.
type RootScope interface {
	Context() context.Context
	Anchor() *os.File
	Directory() string
	Release()
}

// Config contains host-owned inputs. RootScope cannot be created by checking a
// path, inode or 0700 mode. The checkpoint Store must use a real Host/Lease that validates
// publisher scope, current epoch, key mapping and Seal lifecycle evidence.
// Nothing in this package constructs that Host or activates an HTTP execution.
type Config struct {
	Store            *auditcheckpoint.Store
	Epoch            auditcheckpoint.Epoch
	PublisherRef     string
	RootScope        RootScope
	Filename         string
	Scope            transmit.Scope
	KeyRef           string
	Key              []byte
	MaxSnapshotBytes int64
	// BindCloseWitness gives the trusted Host this instance's Close evidence
	// before Register. It must not reenter Publisher or start SQL work.
	BindCloseWitness func(*CloseWitness) error
}

// Publisher is a transmit.Recorder. Its nil-free Record result means the local
// event is synced and the exact checkpoint is the current durable store head.
// One Publisher owns one new journal; it never resumes or repairs a file.
type Publisher struct {
	mu        sync.Mutex
	store     checkpointStore
	epoch     auditcheckpoint.Epoch
	publisher string
	rootScope RootScope
	rootCtx   context.Context
	journal   *auditlog.Journal
	snapshot  *journalSnapshot
	head      auditcheckpoint.Head
	witness   *CloseWitness
	failed    bool
	closed    bool
}

type checkpointStore interface {
	Publish(context.Context, auditcheckpoint.Request, *bytes.Reader) (auditcheckpoint.Receipt, error)
	Head(context.Context, auditcheckpoint.Query) (auditcheckpoint.Head, error)
}

// storeAdapter keeps the public constructor tied to the concrete, Host-backed
// CPD Store. The internal interface is only for focused failure injection.
type storeAdapter struct{ *auditcheckpoint.Store }

func (s storeAdapter) Publish(ctx context.Context, request auditcheckpoint.Request, source *bytes.Reader) (auditcheckpoint.Receipt, error) {
	return s.Store.Publish(ctx, request, source)
}

var _ transmit.Recorder = (*Publisher)(nil)

// Open returns no usable recorder until both the synced header and its CPD
// Register/current-Head acknowledgement have been verified. A failed Open leaves
// any created journal on disk for diagnosis and never substitutes a new RunRef.
func Open(ctx context.Context, config Config) (*Publisher, error) {
	if ctx == nil || ctx.Err() != nil || config.Store == nil || nilRootScope(config.RootScope) ||
		config.MaxSnapshotBytes <= 0 || config.MaxSnapshotBytes > auditlog.MaxExtensionBytes || config.BindCloseWitness == nil {
		return nil, ErrConfig
	}
	rootCtx, anchor := config.RootScope.Context(), config.RootScope.Anchor()
	if rootCtx == nil || rootCtx.Err() != nil || anchor == nil {
		return nil, ErrConfig
	}
	keepScope := false
	defer func() {
		if !keepScope {
			config.RootScope.Release()
		}
	}()
	ctx, cancel := bindRootContext(ctx, rootCtx)
	defer cancel()
	operation, err := newOperationRef()
	if err != nil {
		return nil, ErrUnavailable
	}
	directory := config.RootScope.Directory()
	root, err := openJournalRoot(directory, anchor)
	if err != nil {
		return nil, ErrConfig
	}
	journal, err := auditlog.Create(directory, config.Filename, auditlog.Config{
		Scope: config.Scope, KeyRef: config.KeyRef, Key: config.Key,
	})
	if err != nil {
		_ = root.close()
		return nil, ErrUnavailable
	}
	var witness *CloseWitness
	closeFailedOpen := func(snapshot *journalSnapshot) {
		witness.invalidate()
		if snapshot != nil {
			_ = snapshot.close()
		}
		_ = journal.Close()
		_ = root.close()
	}
	snapshot, err := root.attach(config.Filename, config.MaxSnapshotBytes)
	if err != nil {
		closeFailedOpen(nil)
		return nil, ErrUnavailable
	}
	p := &Publisher{store: storeAdapter{config.Store}, epoch: config.Epoch, rootScope: config.RootScope, rootCtx: rootCtx,
		publisher: config.PublisherRef, journal: journal, snapshot: snapshot}
	cp := journal.Checkpoint()
	witness = newCloseWitness(config.Epoch, auditcheckpoint.Binding{PublisherRef: config.PublisherRef,
		Scope: cp.Scope, JournalID: cp.JournalID, KeyRef: cp.KeyRef}, rootCtx)
	p.witness = witness
	if err := config.BindCloseWitness(witness); err != nil {
		closeFailedOpen(snapshot)
		return nil, ErrUnavailable
	}
	owned, err := snapshot.read(ctx)
	if err != nil {
		closeFailedOpen(snapshot)
		return nil, ErrUnavailable
	}
	defer clear(owned)
	request := auditcheckpoint.Request{Epoch: config.Epoch, OperationRef: operation,
		PublisherRef: config.PublisherRef, Kind: auditcheckpoint.Register, Candidate: cp}
	head, err := p.publish(ctx, request, owned, auditcheckpoint.Open)
	if err != nil {
		closeFailedOpen(snapshot)
		return nil, ErrUnavailable
	}
	p.head = head
	keepScope = true
	return p, nil
}

// Record serializes Journal.Write+Sync, a bounded owned snapshot, CPD CAS and
// current-head readback. Any failure is sticky, even when the local file or SQL
// commit may have progressed; the original file is never truncated or deleted.
func (p *Publisher) Record(ctx context.Context, event transmit.Event) error {
	if p == nil || ctx == nil {
		return ErrConfig
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrClosed
	}
	if p.failed {
		return ErrFailed
	}
	if p.rootCtx == nil {
		p.fail()
		return ErrUnavailable
	}
	ctx, cancel := bindRootContext(ctx, p.rootCtx)
	defer cancel()
	if ctx.Err() != nil {
		p.fail()
		return ErrUnavailable
	}
	operation, err := newOperationRef()
	if err != nil {
		p.fail()
		return ErrUnavailable
	}
	if err := p.journal.Record(ctx, event); err != nil {
		p.fail()
		return ErrUnavailable
	}
	candidate := p.journal.Checkpoint()
	if candidate.Sequence != p.head.Checkpoint.Sequence+1 {
		p.fail()
		return ErrUnavailable
	}
	owned, err := p.snapshot.read(ctx)
	if err != nil {
		p.fail()
		return ErrUnavailable
	}
	defer clear(owned)
	request := auditcheckpoint.Request{Epoch: p.epoch, OperationRef: operation,
		PublisherRef: p.publisher, Kind: auditcheckpoint.Append,
		ExpectedRevision: p.head.Revision, Previous: p.head.Checkpoint, Candidate: candidate}
	head, err := p.publish(ctx, request, owned, auditcheckpoint.Open)
	if err != nil {
		p.fail()
		return ErrUnavailable
	}
	p.head = head
	return nil
}

// AcknowledgedHead returns only this instance's last successfully acknowledged
// metadata. It is not an independently authenticated current-head query and
// must not be used after a failed operation as freshness or execution authority.
// A caller may persist its Checkpoint in the protected local sidecar only after
// the corresponding Open/Record succeeded; sidecar failure requires Abort.
func (p *Publisher) AcknowledgedHead() (auditcheckpoint.Head, error) {
	if p == nil {
		return auditcheckpoint.Head{}, ErrConfig
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed || p.head.Revision == 0 {
		return auditcheckpoint.Head{}, ErrFailed
	}
	return p.head, nil
}

// Seal requires the caller to have completed the actual transmitter drain. The
// Store's Host must independently attest StopAndWait, Close and no earlier
// failure when it authorizes Kind Seal. A caller's assertion is not enough.
func (p *Publisher) Seal(ctx context.Context) (result auditcheckpoint.Head, resultErr error) {
	if p == nil || ctx == nil {
		return auditcheckpoint.Head{}, ErrConfig
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return auditcheckpoint.Head{}, ErrClosed
	}
	p.closed = true
	defer func() {
		snapshotErr := p.snapshot.close()
		rootErr := p.snapshot.root.close()
		p.rootScope.Release()
		if snapshotErr != nil || rootErr != nil {
			p.fail()
			result, resultErr = auditcheckpoint.Head{}, ErrUnavailable
		}
	}()
	if p.rootCtx == nil {
		p.fail()
		_ = p.journal.Close()
		return auditcheckpoint.Head{}, ErrUnavailable
	}
	ctx, cancel := bindRootContext(ctx, p.rootCtx)
	defer cancel()
	if p.failed || ctx.Err() != nil {
		p.fail()
		_ = p.journal.Close()
		return auditcheckpoint.Head{}, ErrFailed
	}
	operation, err := newOperationRef()
	if err != nil {
		p.fail()
		_ = p.journal.Close()
		return auditcheckpoint.Head{}, ErrUnavailable
	}
	if err := p.journal.Close(); err != nil {
		p.fail()
		return auditcheckpoint.Head{}, ErrUnavailable
	}
	owned, err := p.snapshot.read(ctx)
	if err != nil {
		p.fail()
		return auditcheckpoint.Head{}, ErrUnavailable
	}
	defer clear(owned)
	request := auditcheckpoint.Request{Epoch: p.epoch, OperationRef: operation,
		PublisherRef: p.publisher, Kind: auditcheckpoint.Seal,
		ExpectedRevision: p.head.Revision, Previous: p.head.Checkpoint, Candidate: p.head.Checkpoint}
	p.witness.arm(p.head.Checkpoint)
	head, err := p.publish(ctx, request, owned, auditcheckpoint.Sealed)
	if err != nil {
		p.fail()
		return auditcheckpoint.Head{}, ErrUnavailable
	}
	p.head = head
	return head, nil
}

// Abort releases local descriptors after a failed or abandoned execution. It
// never promotes an unverified prefix to sealed and leaves the file untouched.
// The host must reconcile and mark the external head incomplete/quarantined.
func (p *Publisher) Abort() error {
	if p == nil {
		return ErrConfig
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrClosed
	}
	p.closed = true
	p.fail()
	journalErr := p.journal.Close()
	snapshotErr := p.snapshot.close()
	rootErr := p.snapshot.root.close()
	p.rootScope.Release()
	if journalErr != nil || snapshotErr != nil || rootErr != nil {
		return ErrUnavailable
	}
	return nil
}

// fail is called with Publisher.mu held. The witness has its own short lock
// and never reenters Publisher.mu or starts SQL work.
func (p *Publisher) fail() {
	p.failed = true
	p.witness.invalidate()
}

// publish retries an uncertain SQL commit only with the exact same operation
// and immutable snapshot. A historical operation receipt alone is never treated
// as a current head. Failure remains fail-closed for this Publisher.
func (p *Publisher) publish(ctx context.Context, request auditcheckpoint.Request, snapshot []byte, phase auditcheckpoint.Phase) (auditcheckpoint.Head, error) {
	receipt, err := p.store.Publish(ctx, request, bytes.NewReader(snapshot))
	if errors.Is(err, auditcheckpoint.ErrCommitUnknown) && ctx.Err() == nil {
		receipt, err = p.store.Publish(ctx, request, bytes.NewReader(snapshot))
	}
	if err != nil || ctx.Err() != nil || receipt.OperationRef != request.OperationRef ||
		receipt.Head.Revision != request.ExpectedRevision+1 ||
		receipt.Head.Phase != phase || receipt.Head.Checkpoint != request.Candidate ||
		receipt.Head.Epoch != request.Epoch || receipt.Head.PublisherRef != request.PublisherRef {
		return auditcheckpoint.Head{}, ErrUnavailable
	}
	query := auditcheckpoint.Query{Epoch: request.Epoch, Binding: auditcheckpoint.Binding{
		PublisherRef: request.PublisherRef, Scope: request.Candidate.Scope,
		JournalID: request.Candidate.JournalID, KeyRef: request.Candidate.KeyRef,
	}}
	head, err := p.store.Head(ctx, query)
	if err != nil || ctx.Err() != nil || p.rootCtx.Err() != nil || !sameHead(head, receipt.Head) || p.snapshot.current() != nil {
		return auditcheckpoint.Head{}, ErrUnavailable
	}
	return head, nil
}

func sameHead(a, b auditcheckpoint.Head) bool {
	return a.Epoch == b.Epoch && a.PublisherRef == b.PublisherRef &&
		a.Revision == b.Revision && a.Phase == b.Phase && a.Checkpoint == b.Checkpoint
}

func newOperationRef() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return "cpd-" + hex.EncodeToString(token[:]), nil
}

func bindRootContext(ctx, root context.Context) (context.Context, func()) {
	bound, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(root, cancel)
	return bound, func() { stop(); cancel() }
}

func nilRootScope(scope RootScope) bool {
	if scope == nil {
		return true
	}
	v := reflect.ValueOf(scope)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Func, reflect.Chan, reflect.Slice, reflect.Interface:
		return v.IsNil()
	}
	return false
}
