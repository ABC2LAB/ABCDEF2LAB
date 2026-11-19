// Package e3fake는 e3contract 계약의 가짜 구현이다. 세 갈래가 상대 쪽 없이 개발하고, 계약 시험이 실패 상태를 하나씩
// 만들어 CONFIRMED로 이어지지 않음을 고정하는 데 쓴다. 제품 코드에서 쓰지 않는다.
package e3fake

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/oracle"
)

// Key는 가짜 대상 응답을 고르는 열쇠다. 신원 요청의 Type은 빈 값이다.
type Key struct {
	Purpose e3contract.Purpose
	Role    e3contract.Role
	Type    string
}

// Behavior는 요청 하나에 대한 가짜 E²·대상 동작이다. 영값은 정상 응답(200, 정상, 표식 검사 완료)이다.
type Behavior struct {
	// Err는 Transport가 돌려줄 오류다(결과 불명이나 권한 철회).
	Err error
	// NotSent는 보내지 않은 사유다.
	NotSent e3contract.NotSentReason
	// Incomplete는 보냈지만 응답을 다 받지 못한 경우다.
	Incomplete bool
	// TimedOut은 보낸 뒤 확정 요청 시간 상한이 지나 끊긴 경우다(Incomplete와 같이 결과 불명).
	TimedOut bool
	Status   int
	Normal   oracle.CheckState
	// MarkerStatus·Presence는 조회 응답의 표식 검사 결과다. 영값이면 complete·present다.
	MarkerStatus e3contract.MarkerStatus
	Presence     e3contract.MarkerPresence
	// MarkerInRequest는 보낸 조회 요청에 표식이 들어갔다고 E²가 본 경우다(반사 배제 실패).
	MarkerInRequest bool
	// NoObjectID는 생성 응답에서 객체 ID를 꺼내지 못한 경우다.
	NoObjectID bool
	// SubjectID는 신원 응답의 사용자 식별자다. 빈 값이면 식별자를 못 찾은 경우다.
	SubjectID string
}

// Call은 Transport가 받은 요청 기록이다(표식 값은 남기지 않는다).
type Call struct {
	Key     Key
	Account string
	// Timeout은 요청이 가져온 확정 요청 시간 상한이다.
	Timeout time.Duration
}

// Transport는 e3contract.Transport와 e3contract.Journal의 가짜 구현이다. 받은 요청마다 관찰을 만들고, 같은 값을
// 저널에 봉인한다. E²처럼 승인 미리보기·로그·영수증·평가 내보내기에 기록을 쓰되 등록한 표식은 가린다.
type Transport struct {
	mu        sync.Mutex
	Behaviors map[Key]Behavior
	// RegisterErr는 RegisterMarker가 돌려줄 오류다(ErrNotImplemented 등).
	RegisterErr error
	// Tamper는 돌려줄 관찰을 바꾼다. 저널에는 바꾸기 전 값이 봉인된다(저널 대조 실패를 만든다).
	Tamper func(Call, *e3contract.Observation)
	// TamperBoth는 돌려줄 관찰과 저널 모두를 바꾼다(결속 불일치를 만든다).
	TamperBoth func(Call, *e3contract.Observation)
	// DropJournal이 참이면 그 요청의 저널 이벤트를 남기지 않는다.
	DropJournal func(Call) bool
	// SealedErr가 있으면 Sealed가 그 오류를 돌려준다(검증 실패 등, v3 계약 §5).
	SealedErr error
	// Clock은 관찰 시각이다. 없으면 고정 시각에서 1초씩 늘린다.
	Clock func() time.Time

	registered map[string][][]byte
	journal    map[string]e3contract.Observation
	calls      []Call
	seq        uint64
	base       time.Time
	sinks      map[string]*bytes.Buffer
}

// New는 빈 가짜 E²다.
func New() *Transport {
	return &Transport{Behaviors: map[Key]Behavior{}, registered: map[string][][]byte{},
		journal: map[string]e3contract.Observation{}, base: time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC),
		sinks: map[string]*bytes.Buffer{"journal": {}, "receipts": {}, "approval_preview": {}, "log": {}, "export": {}}}
}

// Calls는 받은 요청 기록의 사본이다.
func (t *Transport) Calls() []Call {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Call(nil), t.calls...)
}

// Sinks는 가짜 E²가 쓴 기록 바이트다(저널·영수증·승인 미리보기·로그·평가 내보내기).
func (t *Transport) Sinks() map[string][]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[string][]byte{}
	for name, b := range t.sinks {
		out[name] = bytes.Clone(b.Bytes())
	}
	return out
}

func (t *Transport) RegisterMarker(_ context.Context, b e3contract.RunBinding, m e3contract.Marker) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.RegisterErr != nil {
		return t.RegisterErr
	}
	if !m.Valid() {
		return e3contract.ErrMarkerInvalid
	}
	t.registered[b.RunRef] = append(t.registered[b.RunRef], e3contract.MarkerForms(m)...)
	return nil
}

func (t *Transport) Identity(_ context.Context, req e3contract.IdentityRequest) (e3contract.Observation, error) {
	return t.do(Call{Key: Key{e3contract.PurposeIdentity, req.Role, ""}, Account: req.AccountLabel, Timeout: req.Timeout}, req.Binding,
		e3contract.CallRef{}, "", nil)
}

func (t *Transport) Create(_ context.Context, req e3contract.CreateRequest) (e3contract.Observation, error) {
	// 생성 요청 본문을 만든다. 실제 E²처럼 본문 틀의 표식 칸에 표식을 넣는다.
	body := []byte(fmt.Sprintf(`{"template":%q,"%s":"%s"}`, req.Spec.BodyTemplate, req.Spec.MarkerField, req.Marker.Reveal()))
	return t.do(Call{Key: Key{e3contract.PurposeCreate, e3contract.RoleOwner, req.ResourceType}, Account: req.AccountLabel, Timeout: req.Timeout},
		req.Binding, e3contract.CallRef{}, "", body)
}

func (t *Transport) Read(_ context.Context, req e3contract.ReadRequest) (e3contract.Observation, error) {
	return t.do(Call{Key: Key{e3contract.PurposeRead, req.Role, req.ResourceType}, Account: req.AccountLabel, Timeout: req.Timeout},
		req.Binding, req.Create, req.MarkerField, req.Marker.Reveal())
}

// do는 요청 하나를 처리한다. markerBytes는 생성이면 보낼 본문, 조회면 검사할 표식이다.
func (t *Transport) do(c Call, b e3contract.RunBinding, create e3contract.CallRef, field string,
	markerBytes []byte) (e3contract.Observation, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, c)
	if c.Key.Purpose != e3contract.PurposeIdentity && len(t.registered[b.RunRef]) == 0 {
		return e3contract.Observation{}, e3contract.ErrMarkerNotRegistered
	}
	be := t.Behaviors[c.Key]
	if be.Err != nil {
		return e3contract.Observation{}, be.Err
	}
	t.seq++
	ref := e3contract.CallRef{CallRef: fmt.Sprintf("fake-call-%d", t.seq), AttemptID: fmt.Sprintf("fake-attempt-%d", t.seq),
		Sequence: t.seq}
	o := e3contract.Observation{Binding: b, Purpose: c.Key.Purpose, Role: c.Key.Role, AccountLabel: c.Account,
		ResourceType: c.Key.Type, Ref: ref, Create: create, At: t.now()}
	if c.Key.Purpose == e3contract.PurposeCreate {
		// 승인 미리보기에는 보내려던 본문이 들어간다. 실제 E²처럼 등록한 표식을 가린 뒤 쓴다.
		t.write("approval_preview", markerBytes)
		t.write("log", []byte("create "+c.Key.Type+" body="+string(markerBytes)))
	}
	switch {
	case be.NotSent != "":
		o.NotSent = be.NotSent
	case be.TimedOut:
		o.Sent, o.TimedOut = true, true
	case be.Incomplete:
		o.Sent = true
	default:
		t.respond(&o, be, b, field)
	}
	sealed := o
	if t.TamperBoth != nil {
		t.TamperBoth(c, &o)
		sealed = o
	}
	if t.Tamper != nil {
		t.Tamper(c, &o)
	}
	if t.DropJournal == nil || !t.DropJournal(c) {
		t.journal[sealed.Ref.CallRef+"\x00"+sealed.Ref.AttemptID] = sealed
		receipt, _ := json.Marshal(sealed)
		t.write("journal", receipt)
		t.write("receipts", receipt)
		t.write("export", receipt)
	}
	return o, nil
}

func (t *Transport) respond(o *e3contract.Observation, be Behavior, b e3contract.RunBinding, field string) {
	o.Sent, o.ResponseComplete = true, true
	o.StatusCode, o.Normal = 200, oracle.StateYes
	if be.Status != 0 {
		o.StatusCode = be.Status
	}
	if be.Normal != "" {
		o.Normal = be.Normal
	}
	o.BodyComplete = true
	o.BodyDigestSHA256 = digest("body", o.Ref.CallRef)
	switch o.Purpose {
	case e3contract.PurposeIdentity:
		if be.SubjectID != "" {
			o.SubjectDigest = digest(b.RunRef, be.SubjectID)
		}
		o.RequestMarkerFree = true
	case e3contract.PurposeCreate:
		if !be.NoObjectID {
			o.ObjectIDDigest = digest(b.RunRef, o.Ref.CallRef)
		}
	case e3contract.PurposeRead:
		o.RequestMarkerFree = !be.MarkerInRequest
		status := be.MarkerStatus
		if status == "" {
			status = e3contract.MarkerComplete
		}
		o.Marker = e3contract.MarkerCheck{Status: status, Field: field}
		if status == e3contract.MarkerComplete {
			o.Marker.Presence = be.Presence
			if o.Marker.Presence == "" {
				o.Marker.Presence = e3contract.MarkerPresent
			}
			if o.Marker.Presence == e3contract.MarkerPresent {
				o.Marker.Method = e3contract.MatchExact
			}
		} else {
			o.BodyComplete = status != e3contract.MarkerPartial
		}
	}
}

func (t *Transport) now() time.Time {
	if t.Clock != nil {
		return t.Clock()
	}
	return t.base.Add(time.Duration(t.seq) * time.Second)
}

// write는 등록한 표식(모든 인코딩 모양)을 가린 뒤 기록에 쓴다. 실제 E²의 실행 가림을 흉내 낸다.
func (t *Transport) write(sink string, data []byte) {
	masked := data
	for _, forms := range t.registered {
		for _, f := range forms {
			masked = replaceFold(masked, f, []byte("[REDACTED]"))
		}
	}
	t.sinks[sink].Write(masked)
	t.sinks[sink].WriteByte('\n')
}

// Sealed는 저널의 봉인된 이벤트를 돌려준다.
func (t *Transport) Sealed(_ context.Context, _ e3contract.RunBinding, ref e3contract.CallRef) (e3contract.Observation, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.SealedErr != nil {
		return e3contract.Observation{}, t.SealedErr
	}
	o, ok := t.journal[ref.CallRef+"\x00"+ref.AttemptID]
	if !ok {
		return e3contract.Observation{}, e3contract.ErrJournalEventMissing
	}
	return o, nil
}

func digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func replaceFold(data, old, repl []byte) []byte {
	if len(old) == 0 {
		return data
	}
	// ASCII만 소문자로 바꿔 바이트 위치가 원문과 같게 한다(표식 모양은 ASCII다).
	lower, lowOld := asciiLower(data), asciiLower(old)
	var out []byte
	for {
		i := bytes.Index(lower, lowOld)
		if i < 0 {
			return append(out, data...)
		}
		out = append(append(out, data[:i]...), repl...)
		data, lower = data[i+len(old):], lower[i+len(old):]
	}
}

// Declarations는 DeclarationReader의 가짜 구현이다. 저장된 동결본을 그대로 돌려준다.
type Declarations struct {
	Stored map[string]e3contract.Declaration
	Err    error
}

func (d Declarations) ReadFrozen(_ context.Context, p e3contract.PolicySource) (e3contract.Declaration, error) {
	if d.Err != nil {
		return e3contract.Declaration{}, d.Err
	}
	decl, ok := d.Stored[p.Ref]
	if !ok {
		return e3contract.Declaration{}, e3contract.ErrPolicyUnavailable
	}
	return decl, nil
}

// Reset은 ResetGate의 가짜 구현이다.
type Reset struct {
	State e3contract.ResetState
	Err   error
}

func (r Reset) Check(context.Context, e3contract.RunBinding, string) (e3contract.ResetState, error) {
	return r.State, r.Err
}

func asciiLower(b []byte) []byte {
	out := bytes.Clone(b)
	for i, c := range out {
		if 'A' <= c && c <= 'Z' {
			out[i] = c + ('a' - 'A')
		}
	}
	return out
}
