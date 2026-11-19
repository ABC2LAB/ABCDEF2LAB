// Package v3fake는 E② 영수증·저널 v3의 가짜다(E③↔E② v3 계약 §9). 실제 v3가 들어오기 전에 어댑터를 만들고, 계약의
// 반례(신뢰 체크포인트 없음·되감기·과거 체크포인트·MAC 손상·다른 실행·이벤트 없음·관찰 불일치)를 시험으로 고정하는 데 쓴다.
// 대상·E² 응답 동작은 e3fake.Transport(Behaviors)를 그대로 쓰고, 그 관찰을 v3 이벤트로 저널에 쌓는다. 제품 코드에서 쓰지 않는다.
package v3fake

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3fake"
	"github.com/Autumn-27/artex/e3contract/e3v3"
)

// Checkpoint는 저널 head다. 신뢰 저장소가 실행마다 현재 값을 인증해 둔다.
type Checkpoint struct {
	RunRef string
	Index  uint64
	MAC    [sha256.Size]byte
}

// Trusted는 저널과 따로 둔 신뢰 저장소다(계약 §3). 조회 시점의 현재 head만 돌려준다.
type Trusted struct {
	mu      sync.Mutex
	current map[string]Checkpoint
	// Fail이 있으면 현재 head를 확인하지 못한다.
	Fail error
}

// Current는 그 실행의 현재 head다.
func (t *Trusted) Current(_ context.Context, runRef string) (Checkpoint, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Fail != nil {
		return Checkpoint{}, t.Fail
	}
	cp, ok := t.current[runRef]
	if !ok {
		return Checkpoint{}, errors.New("v3fake: 현재 head 없음")
	}
	return cp, nil
}

func (t *Trusted) advance(cp Checkpoint) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		t.current = map[string]Checkpoint{}
	}
	t.current[cp.RunRef] = cp
}

// Record는 저널 한 줄이다(이벤트 JSON과 체인 MAC).
type Record struct {
	Raw []byte
	MAC [sha256.Size]byte
}

// E2는 가짜 E² v3다. e3v3.E2를 구현한다.
type E2 struct {
	// Target은 대상·E² 응답 동작이다(e3fake.Behaviors로 정한다).
	Target  *e3fake.Transport
	Trusted *Trusted

	// RegisterErr가 있으면 표식 등록이 실패한다(v3 감사 미연결 등).
	RegisterErr error
	// Returned는 돌려줄 이벤트만 바꾼다(저널에는 바꾸기 전 값이 남음, Transport ≠ 봉인).
	Returned func(*e3v3.Event)
	// Recorded는 저널에 남길 이벤트와 돌려줄 이벤트를 같이 바꾼다(모양 위반·결속 불일치를 만든다).
	Recorded func(*e3v3.Event)
	// Unrecorded가 참이면 그 이벤트(intent·result 각각)를 저널에 남기지 않는다(이벤트 없음·불완전 기록).
	Unrecorded func(e3v3.Event) bool
	// PublishFail이 참이면 그 이벤트를 로컬 저널에 Sync한 뒤 정본 head 게시가 실패한다(ACK 없음). Send는 관찰 없이 오류이고
	// 그 뒤 송신은 모두 거부한다(감사 계약: recorder 실패 상태).
	PublishFail func(e3v3.Event) bool
	// Snapshot은 조회 스냅샷을 바꾼다(끝 행 삭제·되감기·MAC 손상·다른 실행 끼우기).
	Snapshot func([]Record) []Record
	// AccountRefs는 지금 결속된 계정 결속 참조("<run>/<label>")를 바꾼다(다시 로그인 등). 이벤트에 실리는 값은 요청 때의
	// 결속("acct-<run>-<label>")이다. AccountRefErr가 있으면 지금 결속을 확인하지 못한다.
	AccountRefs   map[string]string
	AccountRefErr error

	mu      sync.Mutex
	key     []byte
	records []Record
	index   uint64
	failed  bool
}

// ErrPublish는 정본 head 게시 실패다(결과 불명).
var ErrPublish = errors.New("v3fake: 정본 head 게시 실패")

// New는 대상 동작과 실행별 감사 키로 가짜 E² v3를 만든다.
func New(target *e3fake.Transport, key []byte) *E2 {
	return &E2{Target: target, Trusted: &Trusted{}, key: append([]byte(nil), key...)}
}

// Records는 저널 사본이다.
func (f *E2) Records() []Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Record(nil), f.records...)
}

// RegisterMarker는 대상 동작 모델에 표식을 등록한다.
func (f *E2) RegisterMarker(ctx context.Context, b e3contract.RunBinding, m e3contract.Marker) error {
	if f.RegisterErr != nil {
		return f.RegisterErr
	}
	return f.Target.RegisterMarker(ctx, b, m)
}

// AccountBindingRef는 그 실행·라벨의 계정 결속 참조다.
func (f *E2) AccountBindingRef(_ context.Context, b e3contract.RunBinding, label string) (string, error) {
	if f.AccountRefErr != nil {
		return "", f.AccountRefErr
	}
	if ref, ok := f.AccountRefs[b.RunRef+"/"+label]; ok {
		return ref, nil
	}
	return sentAccountRef(b, label), nil
}

func sentAccountRef(b e3contract.RunBinding, label string) string {
	return "acct-" + b.RunRef + "-" + label
}

// Send는 대상 동작 모델로 관찰을 만들고, (intent+)result 이벤트를 저널에 쌓은 뒤 마지막 이벤트(result)를 돌려준다.
func (f *E2) Send(ctx context.Context, req e3v3.Request) (e3v3.Event, error) {
	f.mu.Lock()
	failed := f.failed
	f.mu.Unlock()
	if failed {
		return e3v3.Event{}, ErrPublish
	}
	var (
		o   e3contract.Observation
		err error
	)
	switch req.Program {
	case e3contract.PurposeIdentity:
		o, err = f.Target.Identity(ctx, e3contract.IdentityRequest{Binding: req.Binding, Spec: req.Identity, Role: req.Role,
			AccountLabel: req.AccountLabel, Timeout: req.Timeout})
	case e3contract.PurposeCreate:
		o, err = f.Target.Create(ctx, e3contract.CreateRequest{Binding: req.Binding, ResourceType: req.ResourceType,
			Spec: req.Create, AccountLabel: req.AccountLabel, Marker: req.Marker, Timeout: req.Timeout})
	case e3contract.PurposeRead:
		o, err = f.Target.Read(ctx, e3contract.ReadRequest{Binding: req.Binding, ResourceType: req.ResourceType, Spec: req.Read,
			MarkerField: req.MarkerField, Role: req.Role, AccountLabel: req.AccountLabel, Create: req.CreateRef, Marker: req.Marker,
			Timeout: req.Timeout})
	default:
		return e3v3.Event{}, fmt.Errorf("v3fake: 목적 %q", req.Program)
	}
	if err != nil {
		return e3v3.Event{}, err
	}
	acct := sentAccountRef(req.Binding, o.AccountLabel)
	base := e3v3.Event{Version: e3v3.Format, Program: o.Purpose, Scope: e3v3.Scope{RunRef: o.Binding.RunRef,
		TaskRef: o.Binding.TaskRef, PolicyRef: o.Binding.E2PolicyRef}, CallRef: o.Ref.CallRef, AttemptID: o.Ref.AttemptID,
		Sequence: o.Ref.Sequence, PolicyRevision: "policy-rev-1", Method: method(req), At: o.At, Role: o.Role,
		AccountLabel: o.AccountLabel, AccountBindingRef: acct, E3: e3v3.Binding{JobRef: o.Binding.JobRef,
			GenerationRef: o.Binding.GenerationRef, InstanceRef: o.Binding.InstanceRef, Policy: o.Binding.Policy},
		ResourceType: o.ResourceType, CreateRef: o.Create, AuditComplete: true}
	var last e3v3.Event
	if !o.Sent {
		// 송신 전 거부: intent 없이 result 하나(not_sent).
		last = base
		last.Stage, last.Decision, last.Reason, last.NotSent, last.NotSentReason = e3v3.StageResult, e3v3.DecisionDenied, string(o.NotSent), true, o.NotSent
	} else {
		intent := base
		intent.Stage, intent.Decision, intent.Reason, intent.RuleRef = e3v3.StageIntent, e3v3.DecisionAllowed, "purpose_rule", "rule-"+string(o.Purpose)
		result := intent
		result.Stage = e3v3.StageResult
		result.ForwardAttempted, result.RequestWritten = true, true
		result.ResponseReceived, result.ResponseComplete, result.TimedOut = o.ResponseComplete, o.ResponseComplete, o.TimedOut
		result.StatusCode, result.Normal, result.RequestMarkerFree, result.Marker = o.StatusCode, o.Normal, o.RequestMarkerFree, o.Marker
		result.BodyDigestSHA256, result.BodyComplete = o.BodyDigestSHA256, o.BodyComplete
		result.ObjectIDDigest, result.SubjectDigest = o.ObjectIDDigest, o.SubjectDigest
		if f.Unrecorded == nil || !f.Unrecorded(intent) {
			f.append(intent)
		}
		last = result
	}
	if f.Recorded != nil {
		f.Recorded(&last)
	}
	if f.Unrecorded == nil || !f.Unrecorded(last) {
		if f.PublishFail != nil && f.PublishFail(last) {
			f.appendLocal(last)
			return e3v3.Event{}, ErrPublish
		}
		last = f.append(last)
	}
	out := last
	if f.Returned != nil {
		f.Returned(&out)
	}
	return out, nil
}

func method(req e3v3.Request) string {
	if req.Program == e3contract.PurposeCreate {
		return req.Create.Method
	}
	return "GET"
}

// appendLocal은 로컬 저널에만 쌓고(Sync) 정본 head는 옮기지 않는다. recorder는 실패 상태가 된다.
func (f *E2) appendLocal(e e3v3.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = true
	f.index++
	e.Index = f.index
	raw, _ := json.Marshal(e)
	var prev [sha256.Size]byte
	if n := len(f.records); n > 0 {
		prev = f.records[n-1].MAC
	}
	f.records = append(f.records, Record{Raw: raw, MAC: chain(f.key, prev, raw)})
}

// append는 이벤트에 순번을 매겨 체인 MAC과 함께 쌓고(Sync), 신뢰 저장소의 현재 head를 옮긴다(게시·ACK).
func (f *E2) append(e e3v3.Event) e3v3.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.index++
	e.Index = f.index
	raw, _ := json.Marshal(e)
	var prev [sha256.Size]byte
	if n := len(f.records); n > 0 {
		prev = f.records[n-1].MAC
	}
	rec := Record{Raw: raw, MAC: chain(f.key, prev, raw)}
	f.records = append(f.records, rec)
	f.Trusted.advance(Checkpoint{RunRef: e.Scope.RunRef, Index: e.Index, MAC: rec.MAC})
	return e
}

func chain(key []byte, prev [sha256.Size]byte, raw []byte) [sha256.Size]byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("e3v3-fake-event\x00"))
	m.Write(prev[:])
	m.Write(raw)
	var out [sha256.Size]byte
	copy(out[:], m.Sum(nil))
	return out
}

// Verify는 스냅샷을 처음부터 MAC·체인으로 검증하고 끝이 체크포인트와 같은지 본다. 다른 실행의 이벤트가 있으면 실패다.
// 검증된 이벤트를 돌려준다. 체크포인트가 신뢰 저장소의 현재 값인지는 부르는 쪽이 정한다(Verified).
func Verify(key []byte, snap []Record, cp Checkpoint) ([]e3v3.Event, error) {
	var prev [sha256.Size]byte
	events := make([]e3v3.Event, 0, len(snap))
	for i, rec := range snap {
		if !hmac.Equal(rec.MAC[:], func() []byte { m := chain(key, prev, rec.Raw); return m[:] }()) {
			return nil, fmt.Errorf("%w: MAC·체인", e3contract.ErrJournalIntegrity)
		}
		var e e3v3.Event
		if err := json.Unmarshal(rec.Raw, &e); err != nil || e.Index != uint64(i)+1 {
			return nil, fmt.Errorf("%w: 형식·순번", e3contract.ErrJournalIntegrity)
		}
		if e.Scope.RunRef != cp.RunRef {
			return nil, fmt.Errorf("%w: 다른 실행", e3contract.ErrJournalIntegrity)
		}
		events = append(events, e)
		prev = rec.MAC
	}
	if uint64(len(snap)) != cp.Index || prev != cp.MAC {
		return nil, fmt.Errorf("%w: 체크포인트와 스냅샷 끝이 다름", e3contract.ErrJournalIntegrity)
	}
	return events, nil
}

// Verified는 신뢰 저장소의 현재 head를 조회 시점에 읽고, 같은 스냅샷을 그 head까지 검증한 뒤 그 호출의 마지막 이벤트를
// 돌려준다(계약 §5). 기다리지 않는다.
func (f *E2) Verified(ctx context.Context, b e3contract.RunBinding, ref e3contract.CallRef) (e3v3.Event, error) {
	cp, err := f.Trusted.Current(ctx, b.RunRef)
	if err != nil {
		return e3v3.Event{}, fmt.Errorf("%w: 현재 head 확인 불가", e3contract.ErrJournalIntegrity)
	}
	snap := f.Records()
	if f.Snapshot != nil {
		snap = f.Snapshot(snap)
	}
	events, err := Verify(f.key, snap, cp)
	if err != nil {
		return e3v3.Event{}, err
	}
	var last *e3v3.Event
	for i := range events {
		if events[i].CallRef == ref.CallRef && events[i].AttemptID == ref.AttemptID {
			last = &events[i]
		}
	}
	if last == nil {
		return e3v3.Event{}, e3contract.ErrJournalEventMissing
	}
	return *last, nil
}
