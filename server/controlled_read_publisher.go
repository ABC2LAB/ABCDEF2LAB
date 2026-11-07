package server

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/auditpublisher"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

var ErrControlledReadPublisher = errors.New("controlled_read_publisher_unavailable")

// These inputs are trusted Go assembly, never request/model JSON. The factory
// must obtain actual authenticated host/epoch/root authority; this server does
// not provision any of them or substitute filesystem checks for a root lease.
// On error the factory cleans all partial resources; only a successful Plan
// transfers its nonnil RootScope to the server.
type ControlledReadPublisherRequest struct {
	Scope    transmit.Scope `json:"-"`
	IntentID int64          `json:"-"`
	KeyRef   string         `json:"-"`
	Filename string         `json:"-"`
}
type ControlledReadPublisherPlan struct {
	// 실제 호스트가 결속한 별도 체크포인트 SQL 권위이며 HTTP/모델 입력으로 받지 않는다.
	Database     *sql.DB                  `json:"-"`
	Host         auditcheckpoint.Host     `json:"-"`
	Epoch        auditcheckpoint.Epoch    `json:"-"`
	PublisherRef string                   `json:"-"`
	RootScope    auditpublisher.RootScope `json:"-"`
	Limits       auditlog.ExtensionLimits `json:"-"`
}
type ControlledReadPublisherFactory func(context.Context, ControlledReadPublisherRequest) (ControlledReadPublisherPlan, error)
type controlledReadPublisherConfig struct {
	factory        ControlledReadPublisherFactory
	lifecycle      context.Context
	db             *sql.DB
	saveCheckpoint func(context.Context, auditlog.Checkpoint) error
}

func (s *Server) ConfigureControlledReadPublisher(factory ControlledReadPublisherFactory) error {
	if s == nil || s.ctx == nil || s.ctx.Err() != nil || s.m == nil || s.m.pg == nil || s.m.pg.DB == nil || s.engine == nil || factory == nil {
		return ErrControlledReadPublisher
	}
	r := s.engine.controlledReads
	if r == nil {
		return ErrControlledReadPublisher
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.mode == nil || r.publisher != nil || len(r.pending) != 0 || len(r.active) != 0 {
		return ErrControlledReadPublisher
	}
	r.publisher = &controlledReadPublisherConfig{factory: factory, lifecycle: s.ctx, db: s.m.pg.DB, saveCheckpoint: r.store.saveCheckpoint}
	return nil
}

// This private witness proves only one HTTPTransmitter's actual StopAndWait.
// It cannot establish task-wide/legacy-child quiescence or external authority.
type controlledHTTPDrain struct {
	mu      sync.Mutex
	scope   transmit.Scope
	tx      *transmit.HTTPTransmitter
	drained bool
}

func (w *controlledHTTPDrain) bind(tx *transmit.HTTPTransmitter) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.tx == nil && tx != nil {
		w.tx = tx
	}
}
func (w *controlledHTTPDrain) stopAndWait(tx *transmit.HTTPTransmitter) bool {
	w.mu.Lock()
	valid := tx != nil && w.tx == tx
	w.mu.Unlock()
	if !valid {
		return false
	}
	tx.StopAndWait()
	w.mu.Lock()
	w.drained = true
	w.mu.Unlock()
	return true
}
func (w *controlledHTTPDrain) check(scope transmit.Scope) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.tx != nil && w.drained && w.scope == scope
}

type controlledCheckpointHost struct {
	real              auditcheckpoint.Host
	epoch             auditcheckpoint.Epoch
	scope             transmit.Scope
	keyRef, publisher string
	drain             *controlledHTTPDrain
	mu                sync.Mutex
	journalID         string
	close             *auditpublisher.CloseWitness
}

func (h *controlledCheckpointHost) bindClose(w *auditpublisher.CloseWitness) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.close != nil || w == nil {
		return ErrControlledReadPublisher
	}
	h.close = w
	return nil
}
func (h *controlledCheckpointHost) Acquire(ctx context.Context, a auditcheckpoint.Access) (auditcheckpoint.Lease, error) {
	if ctx == nil || ctx.Err() != nil || a.Epoch != h.epoch || a.Binding.Scope != h.scope || a.Binding.KeyRef != h.keyRef || a.Binding.PublisherRef != h.publisher {
		return nil, ErrControlledReadPublisher
	}
	h.mu.Lock()
	if h.journalID == "" && a.Purpose == auditcheckpoint.PublishAccess && a.Publication.Kind == auditcheckpoint.Register {
		h.journalID = a.Binding.JournalID
	}
	valid := h.journalID != "" && a.Binding.JournalID == h.journalID
	closeProof := h.close
	h.mu.Unlock()
	if !valid {
		return nil, ErrControlledReadPublisher
	}
	if a.Purpose == auditcheckpoint.PublishAccess && a.Publication.Kind == auditcheckpoint.Seal {
		if !h.drain.check(h.scope) || closeProof == nil || !closeProof.Check(a) {
			return nil, ErrControlledReadPublisher
		}
	}
	// This delegation is mandatory even after both local lifecycle witnesses.
	// Host authentication, current epoch/anchor, key state and namespace/writer
	// authorization are independent responsibilities of the actual host lease.
	return h.real.Acquire(ctx, a)
}

type controlledPublisherRoot struct {
	real   auditpublisher.RootScope
	ctx    context.Context
	cancel context.CancelFunc
	stop   func() bool
	once   sync.Once
}

func (r *controlledPublisherRoot) Context() context.Context { return r.ctx }
func (r *controlledPublisherRoot) Anchor() *os.File         { return r.real.Anchor() }
func (r *controlledPublisherRoot) Directory() string        { return r.real.Directory() }
func (r *controlledPublisherRoot) Release() {
	r.once.Do(func() { r.stop(); r.cancel(); r.real.Release() })
}

type controlledPublishedJournal struct {
	publisher      *auditpublisher.Publisher
	drain          *controlledHTTPDrain
	rootCtx        context.Context
	httpCancel     context.CancelFunc
	stopHTTP       func() bool
	sealTimeout    time.Duration
	saveCheckpoint func(context.Context, auditlog.Checkpoint) error
}

func nilPublisherCapability(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Pointer, reflect.Func, reflect.Map, reflect.Interface, reflect.Chan, reflect.Slice:
		return r.IsNil()
	}
	return false
}

// Called after the short runtime reservation, with no runtime/work/DB lock.
func (r *controlledReadRuntime) openPublishedJournal(ctx context.Context, config *controlledReadPublisherConfig, spec controlledReadSpec, scope transmit.Scope) (*controlledPublishedJournal, error) {
	if config == nil || config.factory == nil || config.saveCheckpoint == nil || config.lifecycle == nil || config.lifecycle.Err() != nil || r.mode == nil || r.mode.host.CheckRoot(ctx) != nil {
		return nil, controlledPublisherFailure("configuration")
	}
	request := ControlledReadPublisherRequest{Scope: scope, IntentID: spec.IntentID, KeyRef: spec.KeyRef, Filename: scope.RunRef + ".jsonl"}
	plan, err := config.factory(ctx, request)
	if err != nil {
		return nil, controlledPublisherFailure("factory")
	}
	if nilPublisherCapability(plan.RootScope) {
		return nil, controlledPublisherFailure("plan_root")
	}
	keep := false
	defer func() {
		if !keep {
			plan.RootScope.Release()
		}
	}()
	if nilPublisherCapability(plan.Host) || plan.RootScope.Context() == nil || plan.RootScope.Context().Err() != nil || plan.RootScope.Anchor() == nil || filepath.Clean(plan.RootScope.Directory()) != r.store.journalDirectory() {
		return nil, controlledPublisherFailure("root_scope")
	}
	anchored, err := plan.RootScope.Anchor().Stat()
	current, pathErr := os.Lstat(r.store.journalDirectory())
	if err != nil || pathErr != nil || current.Mode() != os.ModeDir|0o700 || !os.SameFile(anchored, current) {
		return nil, controlledPublisherFailure("root_anchor")
	}
	rootCtx, cancel := context.WithCancel(plan.RootScope.Context())
	root := &controlledPublisherRoot{real: plan.RootScope, ctx: rootCtx, cancel: cancel, stop: context.AfterFunc(config.lifecycle, cancel)}
	// From here the once-guarded wrapper owns Release, including failed Open.
	keep = true
	owned := false
	defer func() {
		if !owned {
			root.Release()
		}
	}()
	drain := &controlledHTTPDrain{scope: scope}
	host := &controlledCheckpointHost{real: plan.Host, epoch: plan.Epoch, scope: scope, keyRef: spec.KeyRef, publisher: plan.PublisherRef, drain: drain}
	checkpointDB := config.db
	if plan.Database != nil {
		checkpointDB = plan.Database
	}
	store, err := auditcheckpoint.New(checkpointDB, host, plan.Limits)
	if err != nil {
		return nil, controlledPublisherFailure("store")
	}
	// Open consumes every nonnil RootScope even on error. The server's
	// pre-Open cleanup must stop before that transfer, not release it again.
	owned = true
	p, err := auditpublisher.Open(ctx, auditpublisher.Config{Store: store, Epoch: plan.Epoch, PublisherRef: plan.PublisherRef, RootScope: root, Filename: request.Filename, Scope: scope, KeyRef: spec.KeyRef, Key: spec.Key, MaxSnapshotBytes: plan.Limits.MaxBytes, BindCloseWitness: host.bindClose})
	if err != nil {
		return nil, controlledPublisherFailure("open")
	}
	journal := &controlledPublishedJournal{publisher: p, drain: drain, rootCtx: rootCtx, sealTimeout: plan.Limits.Timeout, saveCheckpoint: config.saveCheckpoint}
	head, err := p.AcknowledgedHead()
	if err != nil || head.Phase != auditcheckpoint.Open || head.Revision != 1 || head.Checkpoint.Sequence != 0 || head.Checkpoint.Scope != scope || head.Checkpoint.KeyRef != spec.KeyRef {
		journal.abort()
		return nil, controlledPublisherFailure("header_ack")
	}
	if journal.saveCheckpoint(ctx, head.Checkpoint) != nil {
		journal.abort()
		return nil, controlledPublisherFailure("header_sidecar")
	}
	return journal, nil
}

func (p *controlledPublishedJournal) httpContext(ctx context.Context) context.Context {
	ctx, p.httpCancel = context.WithCancel(ctx)
	p.stopHTTP = context.AfterFunc(p.rootCtx, p.httpCancel)
	if p.rootCtx.Err() != nil {
		p.httpCancel()
	}
	return ctx
}
func (p *controlledPublishedJournal) stopHTTPContext() {
	if p.stopHTTP != nil {
		p.stopHTTP()
	}
	if p.httpCancel != nil {
		p.httpCancel()
	}
}
func (p *controlledPublishedJournal) abort() {
	p.stopHTTPContext()
	if err := p.publisher.Abort(); err != nil && !errors.Is(err, auditpublisher.ErrClosed) {
		controlledPublisherFailure("abort")
	}
}

func (p *controlledPublishedJournal) record(ctx context.Context, event transmit.Event) error {
	if err := p.publisher.Record(ctx, event); err != nil {
		return controlledPublisherFailure("record")
	}
	head, err := p.publisher.AcknowledgedHead()
	if err != nil || head.Phase != auditcheckpoint.Open || head.Checkpoint.Scope != event.Scope {
		return controlledPublisherFailure("record_ack")
	}
	if p.saveCheckpoint(ctx, head.Checkpoint) != nil {
		return controlledPublisherFailure("record_sidecar")
	}
	return nil
}

// StopAndWait is done by close before acquiring the recorder lock. Host/SQL
// callbacks must never reenter this recorder or wait for transmitter drain.
func (l *controlledReadLease) closePublishedAudit(completed bool) (auditlog.Checkpoint, error) {
	p := l.recorder.published
	defer p.stopHTTPContext()
	if !completed || l.recorder.failed || l.verification != nil && l.verification.runCtx.Err() != nil {
		p.abort()
		return auditlog.Checkpoint{}, ErrControlledReadPublisher
	}
	ctx, cancel := context.WithTimeout(l.runtime.publisher.lifecycle, p.sealTimeout)
	defer cancel()
	head, err := p.publisher.Seal(ctx)
	if err != nil {
		p.abort()
		return auditlog.Checkpoint{}, controlledPublisherFailure("seal")
	}
	if head.Phase != auditcheckpoint.Sealed || head.Checkpoint.Scope != l.scope {
		return auditlog.Checkpoint{}, controlledPublisherFailure("seal_ack")
	}
	if p.saveCheckpoint(ctx, head.Checkpoint) != nil {
		return auditlog.Checkpoint{}, controlledPublisherFailure("final_sidecar")
	}
	return head.Checkpoint, nil
}

// Only static categories are logged; provider and filesystem error values stay private.
func controlledPublisherFailure(category string) error {
	log.Printf("[controlled-read] publisher_failed reason=%s", category)
	return ErrControlledReadPublisher
}
