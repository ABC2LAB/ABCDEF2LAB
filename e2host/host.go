// Package e2host는 제품 실행과 분리된 E② v3 감사 게시자다.
package e2host

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/Autumn-27/artex/e2checkpoint"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/transmit"
)

var ErrUnavailable = errors.New("e2host: product audit host unavailable")
var ErrEventMissing = errors.New("e2host: verified event missing")

// Host는 보관소 접속과 감사 키를 실행 프로세스에 넘기지 않는다.
type Host struct {
	scanMu sync.Mutex
	mu     sync.Mutex
	db     *sql.DB
	store  *e2checkpoint.Store
	epoch  e2checkpoint.Epoch
	key    []byte
	keyRef string
	root   string
	active map[string]*run
}

type run struct {
	mu          sync.Mutex
	owner       *Host
	recorder    *e2checkpoint.Recorder
	scope       transmit.Scope
	directory   string
	revision    string
	binding     e2checkpoint.Binding
	closed      bool
	sealOK      bool
	interruptOK bool
	fenced      bool
	ctx         context.Context
	cancel      context.CancelFunc
}

type lease struct{ run *run }

func (h *Host) Acquire(_ context.Context, access e2checkpoint.Access) (e2checkpoint.Lease, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.active[access.Binding.Scope.RunRef]
	if r == nil || r.binding != (e2checkpoint.Binding{}) && r.binding != access.Binding ||
		access.Epoch != h.epoch || access.Binding.KeyRef != h.keyRef || access.Binding.Scope != r.scope ||
		access.Binding.PublisherRef != "product-e2-host" || r.ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	if access.Purpose != e2checkpoint.PublishAccess && access.Purpose != e2checkpoint.HeadAccess && access.Purpose != e2checkpoint.VerifyAccess {
		return nil, ErrUnavailable
	}
	// 등록의 journal ID는 Recorder가 생성한다. 등록 직후에만 처음 결속한다.
	if r.binding == (e2checkpoint.Binding{}) {
		if access.Purpose != e2checkpoint.PublishAccess || access.Publication.Kind != e2checkpoint.Register {
			return nil, ErrUnavailable
		}
		r.binding = access.Binding
	}
	if access.Purpose == e2checkpoint.PublishAccess &&
		(r.fenced && access.Publication.Kind != e2checkpoint.Seal && access.Publication.Kind != e2checkpoint.Interrupt ||
			access.Publication.Kind == e2checkpoint.Seal && !r.sealOK ||
			access.Publication.Kind == e2checkpoint.Interrupt && !r.interruptOK) {
		return nil, ErrUnavailable
	}
	return lease{r}, nil
}
func (l lease) Context() context.Context  { return l.run.ctx }
func (l lease) Epoch() e2checkpoint.Epoch { return l.run.owner.epoch }
func (l lease) ResolveKey(_ context.Context, binding e2checkpoint.Binding) ([]byte, error) {
	h := l.run.owner
	h.mu.Lock()
	defer h.mu.Unlock()
	if l.run.ctx.Err() != nil || l.run.binding != binding {
		return nil, ErrUnavailable
	}
	return append([]byte(nil), h.key...), nil
}
func (l lease) Release() {}

// New는 Host 전용 DB 연결과 비밀을 받는다. 제품 실행 측은 이 값을 볼 수 없다.
func New(db *sql.DB, epoch e2checkpoint.Epoch, keyRef string, key []byte, root string) (*Host, error) {
	if db == nil || epoch.StoreRef == "" || epoch.EpochRef == "" || epoch.Generation == 0 ||
		epoch.AnchorDigest == ([sha256.Size]byte{}) || len(key) < 32 || keyRef == "" || root == "" {
		return nil, ErrUnavailable
	}
	h := &Host{db: db, epoch: epoch, key: append([]byte(nil), key...), keyRef: keyRef, root: root, active: make(map[string]*run)}
	store, err := e2checkpoint.New(db, h, 256<<20, 10*time.Second)
	if err != nil {
		return nil, ErrUnavailable
	}
	h.store = store
	return h, nil
}

// CheckEpoch는 Host 전용 anchor와 DB epoch의 일치를 시작 전에 확인한다.
func (h *Host) CheckEpoch(ctx context.Context, db *sql.DB) error {
	if h == nil || ctx == nil || db == nil {
		return ErrUnavailable
	}
	var ref string
	var generation int64
	var digest []byte
	if err := db.QueryRowContext(ctx, `SELECT epoch_ref,generation,anchor_digest FROM e2_http_audit_epochs WHERE store_ref=$1`, h.epoch.StoreRef).
		Scan(&ref, &generation, &digest); err != nil || ref != h.epoch.EpochRef || generation != int64(h.epoch.Generation) ||
		len(digest) != sha256.Size || string(digest) != string(h.epoch.AnchorDigest[:]) {
		return ErrUnavailable
	}
	return nil
}

func (h *Host) Open(ctx context.Context, scope transmit.Scope, policyRevision string) (*Session, error) {
	if h == nil || ctx == nil || ctx.Err() != nil || scope.RunRef == "" || policyRevision == "" {
		return nil, ErrUnavailable
	}
	h.scanMu.Lock()
	defer h.scanMu.Unlock()
	// 검증과 새 실행 등록을 직렬화하여 종료 복구와 새 송신이 경합하지 않게 한다.
	if h.checkRestoredHeads(ctx, false) != nil {
		return nil, ErrUnavailable
	}
	h.mu.Lock()
	if len(h.active) != 0 || h.active[scope.RunRef] != nil {
		h.mu.Unlock()
		return nil, ErrUnavailable
	}
	rctx, cancel := context.WithCancel(context.Background())
	r := &run{owner: h, scope: scope, revision: policyRevision, ctx: rctx, cancel: cancel}
	h.active[scope.RunRef] = r
	h.mu.Unlock()
	directory, err := createRunDirectory(h.root, scope.RunRef)
	if err != nil {
		h.remove(r)
		return nil, ErrUnavailable
	}
	r.directory = directory
	recorder, err := e2checkpoint.OpenRecorder(ctx, e2checkpoint.RecorderConfig{
		Store: h.store, Epoch: h.epoch, PublisherRef: "product-e2-host", Directory: directory,
		Filename: "events.jsonl", Scope: scope, KeyRef: h.keyRef, Key: h.key,
	})
	if err != nil {
		h.remove(r)
		return nil, ErrUnavailable
	}
	r.recorder = recorder
	if r.persistHead(ctx) != nil {
		_ = recorder.Abort()
		h.remove(r)
		return nil, ErrUnavailable
	}
	return &Session{run: r}, nil
}

func (h *Host) remove(r *run) {
	h.mu.Lock()
	delete(h.active, r.scope.RunRef)
	r.cancel()
	h.mu.Unlock()
}

// Session은 한 실행만 소유하며 Record 실패 뒤 같은 실행에서 재시도하지 않는다.
type Session struct{ run *run }

// Verified는 현재 저장소 head와 같은 저널 스냅샷에서 마지막 E③ 결과만 돌려준다.
// 호출자 제공 참조나 이전 ACK는 현재성의 근거로 사용하지 않는다.
func (s *Session) Verified(ctx context.Context, binding e3contract.RunBinding, ref e3contract.CallRef) (e2proxy.Event, error) {
	if s == nil || s.run == nil || ctx == nil || binding.Validate() != nil ||
		ref.CallRef == "" || ref.AttemptID == "" || ref.Sequence == 0 {
		return e2proxy.Event{}, ErrUnavailable
	}
	r := s.run
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owner.mu.Lock()
	fenced := r.fenced
	r.owner.mu.Unlock()
	if r.closed || fenced || r.recorder == nil || r.ctx.Err() != nil ||
		binding.RunRef != r.scope.RunRef || binding.TaskRef != r.scope.TaskRef ||
		binding.E2PolicyRef != r.scope.PolicyRef {
		return e2proxy.Event{}, ErrUnavailable
	}
	events, head, err := r.verifiedEvents(ctx)
	if err != nil || head.Phase != e2checkpoint.Open || !allAttemptsFinished(events) {
		return e2proxy.Event{}, ErrUnavailable
	}
	var latest e2proxy.Event
	for _, event := range events {
		if event.CallRef == ref.CallRef && event.Stage == "result" {
			latest = event
		}
	}
	if latest.Stage == "" {
		return e2proxy.Event{}, ErrEventMissing
	}
	if latest.AttemptID != ref.AttemptID || latest.Sequence != ref.Sequence ||
		latest.PolicyRevision != r.revision || latest.E3Audit == nil ||
		latest.E3Audit.E3.JobRef != binding.JobRef ||
		latest.E3Audit.E3.GenerationRef != binding.GenerationRef ||
		latest.E3Audit.E3.InstanceRef != binding.InstanceRef ||
		latest.E3Audit.E3.Policy != binding.Policy {
		return e2proxy.Event{}, ErrUnavailable
	}
	return latest, nil
}

func (s *Session) Record(ctx context.Context, event e2proxy.Event) error {
	if s == nil || s.run == nil || ctx == nil {
		return ErrUnavailable
	}
	r := s.run
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owner.mu.Lock()
	fenced := r.fenced
	r.owner.mu.Unlock()
	if r.closed || fenced || r.recorder == nil || event.Version != e2proxy.JournalVersionV3 ||
		event.Scope != r.scope || event.PolicyRevision != r.revision {
		return ErrUnavailable
	}
	if err := r.recorder.Record(ctx, event); err != nil {
		r.owner.mu.Lock()
		r.fenced = true
		r.owner.mu.Unlock()
		return ErrUnavailable
	}
	if r.persistHead(ctx) != nil {
		r.owner.mu.Lock()
		r.fenced = true
		r.owner.mu.Unlock()
		return ErrUnavailable
	}
	return nil
}

// Seal은 실행 측의 drain 완료 통지를 받은 뒤에만 요청된다.
func (s *Session) Seal(ctx context.Context) error {
	if s == nil || s.run == nil || ctx == nil {
		return ErrUnavailable
	}
	r := s.run
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owner.mu.Lock()
	fenced := r.fenced
	if !fenced {
		r.sealOK = true
	}
	r.owner.mu.Unlock()
	if r.closed || r.recorder == nil || fenced {
		return ErrUnavailable
	}
	// 호출자의 drain 주장만 믿지 않는다. 모든 송신 의도에 결과가 있고
	// 현재 DB head와 Host 저널이 일치해야 감사 봉인을 허용한다.
	if r.completeAudit(ctx, e2checkpoint.Open) != nil {
		return ErrUnavailable
	}
	if err := r.recorder.Seal(ctx); err != nil {
		r.owner.mu.Lock()
		r.fenced = true
		r.owner.mu.Unlock()
		return ErrUnavailable
	}
	if r.persistHead(ctx) != nil {
		r.owner.mu.Lock()
		r.fenced = true
		r.owner.mu.Unlock()
		return ErrUnavailable
	}
	r.closed = true
	r.owner.remove(r)
	return nil
}

func (s *Session) Abort() error {
	if s == nil || s.run == nil {
		return ErrUnavailable
	}
	r := s.run
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		r.owner.mu.Lock()
		r.fenced = true
		r.owner.mu.Unlock()
		var closeErr error
		if r.recorder != nil {
			closeErr = r.recorder.Abort()
		}
		r.owner.remove(r)
		if closeErr != nil {
			return ErrUnavailable
		}
	}
	return nil
}
