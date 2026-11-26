// Package e2host는 제품 실행과 분리된 E② v3 감사 게시자다.
package e2host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Autumn-27/artex/e2checkpoint"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/transmit"
)

var ErrUnavailable = errors.New("e2host: product audit host unavailable")
var ErrEventMissing = errors.New("e2host: verified event missing")
var ErrBindingMismatch = errors.New("e2host: verified event binding mismatch")

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
	// issued는 이 세션이 Verified로 내준 근거 체크포인트다. 봉인 때 봉인 head에 포함되는지 확인한다.
	issued []e2proxy.JournalCheckpoint
	ctx    context.Context
	cancel context.CancelFunc
}

// maxSealChecks는 한 실행에서 근거로 내줄 수 있는 서로 다른 체크포인트 수의 상한이다. E③ 확정 단계와
// 후보별 일반 E② 관찰 모두 이 목록을 사용한다. 상한은 봉인 응답이 IPC 한 줄 상한 안에 들도록 둔 것이다.
const maxSealChecks = 128

// Verification은 검증한 결과 사건과 그 사건을 검증한 현재 head 체크포인트다. 판정은 이 체크포인트를 근거로
// 저장하고, 봉인 뒤 그 체크포인트가 봉인 저널 앞부분에 있는지 따로 확인한다(E1, 2026-11-20 결정).
type Verification struct {
	Event      e2proxy.Event
	Checkpoint e2proxy.JournalCheckpoint
}

// ObservationRef identifies one ordinary E² result. It grants no E③
// ownership verdict; the Host only attests that this exact completed event
// exists in the currently authenticated journal prefix.
type ObservationRef struct {
	CallRef   string `json:"call_ref"`
	AttemptID string `json:"attempt_id"`
	Sequence  uint64 `json:"sequence"`
}

// SealCheck는 내준 근거 체크포인트 하나의 봉인 포함 결과다. 같은 저널의 체크포인트라 저널 ID·범위는 Head와 같다.
type SealCheck struct {
	Sequence uint64 `json:"sequence"`
	MAC      string `json:"mac"`
	Included bool   `json:"included"`
}

// SealResult는 마감 head와 근거 체크포인트별 포함 결과다. Included=false는 마감 저널이 그 체크포인트를 앞부분으로
// 갖지 않는다는 뜻이다(되감기·다른 분기). Phase는 마감 종류다. 봉인 응답은 늘 sealed이고, 연결이 끊겨 Host가
// interrupted로 마감한 저널은 Host 볼륨 기록에만 interrupted로 남는다 — 포함돼도 정상 봉인으로 보지 않는다.
type SealResult struct {
	Phase  e2checkpoint.Phase        `json:"phase"`
	Head   e2proxy.JournalCheckpoint `json:"head"`
	Checks []SealCheck               `json:"checks"`
}

const (
	sealChecksVersion = "jindo.e2host-seal-checks/v1"
	issuedVersion     = "jindo.e2host-evidence-checkpoints/v1"
	sealChecksFile    = "seal-checks"
	issuedFile        = "evidence-checkpoints"
)

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
	if h.checkRestoredHeads(ctx, false, "", nil) != nil {
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
	v, err := s.VerifiedAt(ctx, binding, ref)
	return v.Event, err
}

// VerifiedAt은 Verified와 같고, 검증에 쓴 현재 head 체크포인트를 함께 돌려준다. 돌려준 체크포인트는 봉인 때 포함
// 여부를 확인할 목록에 남긴다.
func (s *Session) VerifiedAt(ctx context.Context, binding e3contract.RunBinding, ref e3contract.CallRef) (Verification, error) {
	event, checkpoint, err := s.verified(ctx, binding, ref)
	if err != nil {
		return Verification{}, err
	}
	return Verification{Event: event, Checkpoint: checkpoint}, nil
}

func (s *Session) ObservedAt(ctx context.Context, ref ObservationRef) (Verification, error) {
	if s == nil || s.run == nil || ctx == nil || ref.CallRef == "" || ref.AttemptID == "" || ref.Sequence == 0 {
		return Verification{}, ErrBindingMismatch
	}
	r := s.run
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owner.mu.Lock()
	fenced := r.fenced
	r.owner.mu.Unlock()
	if r.closed || fenced || r.recorder == nil || r.ctx.Err() != nil {
		return Verification{}, ErrUnavailable
	}
	events, head, err := r.verifiedEvents(ctx)
	if err != nil || head.Phase != e2checkpoint.Open || !allAttemptsFinished(events) ||
		head.Checkpoint.Version != e2proxy.JournalVersionV3 || head.Checkpoint.Scope != r.scope {
		return Verification{}, ErrUnavailable
	}
	var matched e2proxy.Event
	for _, event := range events {
		if event.Stage == "result" && event.CallRef == ref.CallRef && event.AttemptID == ref.AttemptID && event.Sequence == ref.Sequence {
			if matched.Stage != "" {
				return Verification{}, ErrBindingMismatch
			}
			matched = event
		}
	}
	if matched.Stage == "" {
		return Verification{}, ErrEventMissing
	}
	if matched.Scope != r.scope || matched.PolicyRevision != r.revision || matched.Program != "" || matched.E3Audit != nil ||
		matched.Decision != "allowed" || !matched.ForwardAttempted || !matched.ResponseComplete || !matched.AuditComplete ||
		matched.NotSent || matched.ResponseTruncated {
		return Verification{}, ErrBindingMismatch
	}
	if !r.issue(head.Checkpoint) {
		return Verification{}, ErrUnavailable
	}
	return Verification{Event: matched, Checkpoint: head.Checkpoint}, nil
}

func (s *Session) verified(ctx context.Context, binding e3contract.RunBinding, ref e3contract.CallRef) (e2proxy.Event, e2proxy.JournalCheckpoint, error) {
	none := e2proxy.JournalCheckpoint{}
	if s == nil || s.run == nil || ctx == nil {
		return e2proxy.Event{}, none, ErrUnavailable
	}
	if binding.Validate() != nil || ref.CallRef == "" || ref.AttemptID == "" || ref.Sequence == 0 {
		return e2proxy.Event{}, none, ErrBindingMismatch
	}
	r := s.run
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owner.mu.Lock()
	fenced := r.fenced
	r.owner.mu.Unlock()
	if r.closed || fenced || r.recorder == nil || r.ctx.Err() != nil {
		return e2proxy.Event{}, none, ErrUnavailable
	}
	if binding.RunRef != r.scope.RunRef || binding.TaskRef != r.scope.TaskRef ||
		binding.E2PolicyRef != r.scope.PolicyRef {
		return e2proxy.Event{}, none, ErrBindingMismatch
	}
	events, head, err := r.verifiedEvents(ctx)
	if err != nil || head.Phase != e2checkpoint.Open || !allAttemptsFinished(events) ||
		head.Checkpoint.Version != e2proxy.JournalVersionV3 || head.Checkpoint.Scope != r.scope {
		return e2proxy.Event{}, none, ErrUnavailable
	}
	var latest e2proxy.Event
	for _, event := range events {
		if event.CallRef == ref.CallRef && event.Stage == "result" {
			latest = event
		}
	}
	if latest.Stage == "" {
		return e2proxy.Event{}, none, ErrEventMissing
	}
	if latest.AttemptID != ref.AttemptID || latest.Sequence != ref.Sequence ||
		latest.PolicyRevision != r.revision || latest.E3Audit == nil ||
		latest.E3Audit.E3.JobRef != binding.JobRef ||
		latest.E3Audit.E3.GenerationRef != binding.GenerationRef ||
		latest.E3Audit.E3.InstanceRef != binding.InstanceRef ||
		latest.E3Audit.E3.Policy != binding.Policy {
		return e2proxy.Event{}, none, ErrBindingMismatch
	}
	if !r.issue(head.Checkpoint) {
		return e2proxy.Event{}, none, ErrUnavailable
	}
	return latest, head.Checkpoint, nil
}

// issue는 내줄 체크포인트를 봉인 확인 목록에 넣는다. 같은 head면 한 번만 넣고, 상한을 넘는 새 체크포인트는 내주지
// 않는다(봉인 때 확인하지 못할 근거를 주지 않는다). 목록은 내주기 전에 Host 볼륨에 남겨, 연결이 끊겨 Host가
// interrupted로 마감할 때(다른 run 객체·재기동 뒤)도 같은 목록으로 대조한다. 호출자는 run 잠금을 보유한다.
func (r *run) issue(checkpoint e2proxy.JournalCheckpoint) bool {
	for _, issued := range r.issued {
		if issued == checkpoint {
			return true
		}
	}
	if len(r.issued) >= maxSealChecks {
		return false
	}
	next := append(append([]e2proxy.JournalCheckpoint(nil), r.issued...), checkpoint)
	data, err := json.Marshal(issuedRecord{Version: issuedVersion, Checkpoints: next})
	if err != nil || writePrivateFile(r.directory, issuedFile, data) != nil {
		return false
	}
	r.issued = next
	return true
}

type issuedRecord struct {
	Version     string                      `json:"version"`
	Checkpoints []e2proxy.JournalCheckpoint `json:"checkpoints"`
}

// readIssued는 Host 볼륨의 근거 목록을 읽는다. 파일이 없으면 내준 근거가 없다.
func readIssued(directory string) ([]e2proxy.JournalCheckpoint, error) {
	path := filepath.Join(directory, issuedFile+".json")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	raw, err := readSnapshot(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	var record issuedRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || record.Version != issuedVersion || len(record.Checkpoints) > maxSealChecks {
		return nil, ErrUnavailable
	}
	return record.Checkpoints, nil
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
	_, err := s.SealChecked(ctx)
	return err
}

// SealChecked는 Seal과 같고, 봉인한 뒤 이 세션이 내준 근거 체크포인트마다 봉인 head에 포함되는지 확인한 결과를
// Host 볼륨(seal-checks.json)에 남기고 돌려준다. 확인 자체를 못 하면(읽기 실패 등) 봉인은 성공으로 두고 결과만
// nil이다 — 호출자는 이를 "봉인 미확인"으로 다룬다.
func (s *Session) SealChecked(ctx context.Context) (*SealResult, error) {
	if s == nil || s.run == nil || ctx == nil {
		return nil, ErrUnavailable
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
		return nil, ErrUnavailable
	}
	// 호출자의 drain 주장만 믿지 않는다. 모든 송신 의도에 결과가 있고
	// 현재 DB head와 Host 저널이 일치해야 감사 봉인을 허용한다.
	if r.completeAudit(ctx, e2checkpoint.Open) != nil {
		return nil, ErrUnavailable
	}
	if err := r.recorder.Seal(ctx); err != nil {
		r.owner.mu.Lock()
		r.fenced = true
		r.owner.mu.Unlock()
		return nil, ErrUnavailable
	}
	if r.persistHead(ctx) != nil {
		r.owner.mu.Lock()
		r.fenced = true
		r.owner.mu.Unlock()
		return nil, ErrUnavailable
	}
	// 확인을 못 해도(읽기·기록 실패) 봉인은 이미 저장소에 게시됐다. 결과 없음으로 돌려줘 호출자가 "봉인 미확인"으로
	// 남기게 한다(봉인 실패·ACK 불명과는 오류 여부로 갈린다).
	result, err := r.phaseChecks(ctx, e2checkpoint.Sealed, r.issued)
	if err != nil {
		result = nil
	}
	r.closed = true
	r.owner.remove(r)
	return result, nil
}

// phaseChecks는 신뢰 저장소가 인증한 현재 마감 head(VerifyCurrent)와 같은 스냅샷에서 근거 체크포인트마다
// VerifyJournalV3Extension(근거 → 마감 head)을 돌리고 Host 볼륨에 남긴다. 인증 체인이 맞지 않으면
// (ErrJournalIntegrity) 포함 실패이고, 그 밖의 오류는 확인 불가다. 호출자는 run 잠금을 보유한다(복구 중의 새
// run은 다른 호출자가 볼 수 없다).
func (r *run) phaseChecks(ctx context.Context, phase e2checkpoint.Phase, issued []e2proxy.JournalCheckpoint) (*SealResult, error) {
	snapshot, current, err := r.verifiedSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer clear(snapshot)
	if current.Phase != phase || current.Checkpoint.Version != e2proxy.JournalVersionV3 || current.Checkpoint.Scope != r.scope {
		return nil, ErrUnavailable
	}
	out := &SealResult{Phase: phase, Head: current.Checkpoint, Checks: make([]SealCheck, 0, len(issued))}
	for _, checkpoint := range issued {
		_, err := e2proxy.VerifyJournalV3Extension(ctx, bytes.NewReader(snapshot), r.owner.key, checkpoint, current.Checkpoint)
		if err != nil && !errors.Is(err, e2proxy.ErrJournalIntegrity) {
			return nil, ErrUnavailable
		}
		out.Checks = append(out.Checks, SealCheck{Sequence: checkpoint.Sequence, MAC: checkpoint.MAC, Included: err == nil})
	}
	data, err := json.Marshal(struct {
		Version string `json:"version"`
		*SealResult
	}{sealChecksVersion, out})
	if err != nil || writePrivateFile(r.directory, sealChecksFile, data) != nil {
		return nil, ErrUnavailable
	}
	return out, nil
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
