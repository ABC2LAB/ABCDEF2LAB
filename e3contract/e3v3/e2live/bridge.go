package e2live

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"sync"

	"github.com/Autumn-27/artex/e2host"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3v3"
)

// Live는 한 제품 실행의 E② Host·현재 C14 계정 저장소에 연결된 e3v3.E2 구현이다.
type Live struct {
	mu       sync.Mutex
	exec     *e2proxy.Execution
	accounts e2proxy.E3AccountBindings
	binding  e3contract.RunBinding
	marker   e3contract.Marker
	digest   []byte
	closed   bool
}

var _ e3v3.E2 = (*Live)(nil)

// NewLive는 v3 Host 실행과 계정 저장소가 모두 있을 때만 브리지를 연다.
func NewLive(exec *e2proxy.Execution, accounts e2proxy.E3AccountBindings, binding e3contract.RunBinding) (*Live, error) {
	if exec == nil || accounts == nil || exec.CheckE3Binding(binding) != nil {
		return nil, e2proxy.ErrE3JournalUnavailable
	}
	key := make([]byte, sha256.Size)
	if _, err := rand.Read(key); err != nil {
		return nil, e2proxy.ErrE3JournalUnavailable
	}
	return &Live{exec: exec, accounts: accounts, binding: binding, digest: key}, nil
}

// Close는 회차 전용 digest 키와 표식 참조를 버린다.
func (l *Live) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	clear(l.digest)
	l.digest, l.marker, l.closed = nil, e3contract.Marker{}, true
}

func (l *Live) check(binding e3contract.RunBinding) error {
	if l == nil || l.closed || binding != l.binding || l.exec.CheckE3Binding(binding) != nil {
		return e2proxy.ErrE3JournalUnavailable
	}
	return nil
}

func (l *Live) RegisterMarker(ctx context.Context, binding e3contract.RunBinding, marker e3contract.Marker) error {
	if ctx == nil || ctx.Err() != nil || l == nil || !marker.Valid() {
		return e3contract.ErrMarkerNotRegistered
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.check(binding); err != nil {
		return err
	}
	value := marker.Reveal()
	defer clear(value)
	if l.marker.Valid() {
		old := l.marker.Reveal()
		same := bytes.Equal(old, value)
		clear(old)
		if !same {
			return e3contract.ErrMarkerNotRegistered
		}
	}
	if err := l.exec.RegisterProgramMarker(value); err != nil {
		return e3contract.ErrMarkerNotRegistered
	}
	l.marker = marker
	return nil
}

func (l *Live) Send(ctx context.Context, req e3v3.Request) (e3v3.Event, error) {
	if ctx == nil || l == nil {
		return e3v3.Event{}, e2proxy.ErrE3JournalUnavailable
	}
	if err := ctx.Err(); err != nil {
		return e3v3.Event{}, err
	}
	l.mu.Lock()
	if err := l.check(req.Binding); err != nil {
		l.mu.Unlock()
		return e3v3.Event{}, err
	}
	if !l.marker.Valid() {
		l.mu.Unlock()
		return e3v3.Event{}, e3contract.ErrMarkerNotRegistered
	}
	marker := l.marker
	key := bytes.Clone(l.digest)
	l.mu.Unlock()
	defer clear(key)
	if req.Program == e3contract.PurposeCreate || req.Program == e3contract.PurposeRead {
		want, got := marker.Reveal(), req.Marker.Reveal()
		same := req.Marker.Valid() && bytes.Equal(want, got)
		clear(want)
		clear(got)
		if !same {
			return e3v3.Event{}, e3contract.ErrMarkerNotRegistered
		}
	}
	var ev e2proxy.Event
	var err error
	switch req.Program {
	case e3contract.PurposeIdentity:
		ev, err = l.exec.SendE3Identity(ctx, e2proxy.E3IdentityInput{Binding: req.Binding, Role: req.Role,
			AccountLabel: req.AccountLabel, Spec: req.Identity, Marker: marker, Accounts: l.accounts,
			DigestKey: key, Timeout: req.Timeout})
	case e3contract.PurposeCreate:
		ev, err = l.exec.SendE3Create(ctx, e2proxy.E3CreateInput{Binding: req.Binding, ResourceType: req.ResourceType,
			AccountLabel: req.AccountLabel, Spec: req.Create, Marker: marker, Body: req.Body,
			ObjectID: req.ObjectID, Accounts: l.accounts, DigestKey: key, Timeout: req.Timeout})
	case e3contract.PurposeRead:
		ev, err = l.exec.SendE3Read(ctx, e2proxy.E3ReadInput{Binding: req.Binding, ResourceType: req.ResourceType,
			Role: req.Role, AccountLabel: req.AccountLabel, Spec: req.Read, MarkerField: req.MarkerField,
			CreateRef: req.CreateRef, ObjectID: req.ObjectID, Marker: marker, Accounts: l.accounts,
			DigestKey: key, Timeout: req.Timeout})
	default:
		return e3v3.Event{}, e2proxy.ErrConfig
	}
	if err != nil {
		return e3v3.Event{}, err
	}
	return liveEvent(ev)
}

func (l *Live) AccountBindingRef(ctx context.Context, binding e3contract.RunBinding, label string) (string, error) {
	if ctx == nil || l == nil {
		return "", e2proxy.ErrE3JournalUnavailable
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.check(binding); err != nil {
		return "", err
	}
	ref, ok := l.accounts.AccountBindingRef(binding.RunRef, label)
	if !ok || ref == "" {
		return "", e3contract.ErrAuthorityRevoked
	}
	return ref, nil
}

func (l *Live) Verified(ctx context.Context, binding e3contract.RunBinding, ref e3contract.CallRef) (e3v3.Event, error) {
	if ctx == nil || l == nil {
		return e3v3.Event{}, e2proxy.ErrE3JournalUnavailable
	}
	if err := ctx.Err(); err != nil {
		return e3v3.Event{}, err
	}
	l.mu.Lock()
	if err := l.check(binding); err != nil {
		l.mu.Unlock()
		return e3v3.Event{}, err
	}
	l.mu.Unlock()
	ev, err := l.exec.VerifiedE3(ctx, binding, ref)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return e3v3.Event{}, ctxErr
		}
		if errors.Is(err, e2host.ErrEventMissing) {
			return e3v3.Event{}, e3contract.ErrJournalEventMissing
		}
		if errors.Is(err, e2host.ErrBindingMismatch) {
			return e3v3.Event{}, e3contract.ErrJournalBindingMismatch
		}
		return e3v3.Event{}, e3contract.ErrJournalIntegrity
	}
	return liveEvent(ev)
}

// VerifiedCheckpoint는 Verified와 같고, Host가 그 검증에 쓴 현재 head 체크포인트를 함께 돌려준다(E③ 판정 저장 근거). 오류
// 분류는 Verified와 같다.
func (l *Live) VerifiedCheckpoint(ctx context.Context, binding e3contract.RunBinding, ref e3contract.CallRef) (e3v3.Event, e3v3.Checkpoint, error) {
	if ctx == nil || l == nil {
		return e3v3.Event{}, e3v3.Checkpoint{}, e2proxy.ErrE3JournalUnavailable
	}
	if err := ctx.Err(); err != nil {
		return e3v3.Event{}, e3v3.Checkpoint{}, err
	}
	l.mu.Lock()
	if err := l.check(binding); err != nil {
		l.mu.Unlock()
		return e3v3.Event{}, e3v3.Checkpoint{}, err
	}
	l.mu.Unlock()
	ev, checkpoint, err := l.exec.VerifiedE3Checkpoint(ctx, binding, ref)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return e3v3.Event{}, e3v3.Checkpoint{}, ctxErr
		}
		if errors.Is(err, e2host.ErrEventMissing) {
			return e3v3.Event{}, e3v3.Checkpoint{}, e3contract.ErrJournalEventMissing
		}
		if errors.Is(err, e2host.ErrBindingMismatch) {
			return e3v3.Event{}, e3v3.Checkpoint{}, e3contract.ErrJournalBindingMismatch
		}
		return e3v3.Event{}, e3v3.Checkpoint{}, e3contract.ErrJournalIntegrity
	}
	out, err := liveEvent(ev)
	if err != nil {
		return e3v3.Event{}, e3v3.Checkpoint{}, err
	}
	return out, e3v3.Checkpoint{JournalID: checkpoint.JournalID, Sequence: checkpoint.Sequence, MAC: checkpoint.MAC}, nil
}

// liveEvent는 E②의 확장 영수증을 E③ v3 계약의 평탄한 사건으로 옮긴다.
func liveEvent(ev e2proxy.Event) (e3v3.Event, error) {
	a := ev.E3Audit
	if ev.Version != e2proxy.JournalVersionV3 || ev.Stage != "result" || a == nil || !ev.AuditComplete {
		return e3v3.Event{}, e3contract.ErrJournalIntegrity
	}
	out := e3v3.Event{Version: ev.Version, Index: ev.Index, Stage: e3v3.Stage(ev.Stage),
		Program: e3contract.Purpose(ev.Program), IntentID: ev.IntentID,
		Scope:   e3v3.Scope{RunRef: ev.Scope.RunRef, TaskRef: ev.Scope.TaskRef, PolicyRef: ev.Scope.PolicyRef},
		CallRef: ev.CallRef, AttemptID: ev.AttemptID, Sequence: ev.Sequence,
		PolicyRevision: ev.PolicyRevision, RuleRef: ev.RuleRef, Method: ev.Method,
		Decision: ev.Decision, Reason: string(ev.Reason), ForwardAttempted: ev.ForwardAttempted,
		RequestWritten: ev.RequestWritten, ResponseReceived: ev.ResponseReceived,
		ResponseComplete: ev.ResponseComplete, NotSent: ev.NotSent, NotSentReason: a.NotSentReason,
		AuditComplete: ev.AuditComplete, StatusCode: ev.StatusCode, At: ev.At,
		Role: a.Role, AccountLabel: a.AccountLabel, AccountBindingRef: a.AccountBindingRef,
		E3: e3v3.Binding{JobRef: a.E3.JobRef, GenerationRef: a.E3.GenerationRef,
			InstanceRef: a.E3.InstanceRef, Policy: a.E3.Policy}, ResourceType: a.ResourceType,
		CreateRef: a.CreateRef, TimedOut: a.TimedOut, Normal: a.Normal,
		Marker: a.Marker, BodyDigestSHA256: a.BodyDigestSHA256,
		ObjectIDDigest: a.ObjectIDDigest, SubjectDigest: a.SubjectDigest}
	if a.RequestMarkerFree != nil {
		out.RequestMarkerFree = *a.RequestMarkerFree
	}
	if a.BodyComplete != nil {
		out.BodyComplete = *a.BodyComplete
	}
	if err := out.Check(); err != nil {
		return e3v3.Event{}, e3contract.ErrJournalIntegrity
	}
	return out, nil
}
