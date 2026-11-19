package e2proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/oracle"
)

type e3IdentityAccounts struct {
	mu      sync.RWMutex
	ref     string
	header  http.Header
	current bool
}

type e3IdentityVerifier struct{}

func (e3IdentityVerifier) Verified(context.Context, e3contract.RunBinding, e3contract.CallRef) (Event, error) {
	return Event{}, ErrAudit
}

func (a *e3IdentityAccounts) AccountBindingRef(runRef, label string) (string, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.ref, a.current && runRef == "run" && label == "owner-a"
}
func (a *e3IdentityAccounts) BoundSession(runRef, label, ref string) (http.Header, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.current || runRef != "run" || label != "owner-a" || ref != a.ref {
		return nil, ErrDenied
	}
	return a.header.Clone(), nil
}

func (a *e3IdentityAccounts) replace(ref string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ref = ref
}

func e3IdentityFixture(t *testing.T, target string, recorder Recorder) (*Execution, E3IdentityInput) {
	t.Helper()
	cfg := coreConfig(t, target, recorder, 3)
	cfg.AuditVersion = JournalVersionV3
	cfg.E3Verifier = e3IdentityVerifier{}
	cfg.E3ConfirmationRequests = 3
	cfg.Rules = append(cfg.Rules, Rule{Ref: "identity-rule", Purpose: PurposeE3Identity, Origin: target,
		ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/allowed/identity", PathScopeRef: "identity-path", Methods: []string{"GET"}})
	e, err := NewExecution(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := e3contract.NewMarker(bytes.NewReader(bytes.Repeat([]byte{1}, 128)))
	if err != nil {
		t.Fatal(err)
	}
	value := marker.Reveal()
	if err := e.RegisterProgramMarker(value); err != nil {
		t.Fatal(err)
	}
	clear(value)
	in := E3IdentityInput{Binding: e3contract.RunBinding{JobRef: "job-1", RunRef: "run", TaskRef: "task",
		E2PolicyRef: "policy", GenerationRef: "generation-1", InstanceRef: "instance-1",
		Policy: e3contract.PolicySource{Kind: e3contract.PolicyProductDeclaration, Ref: "declaration-1",
			DigestSHA256: strings.Repeat("a", 64), TargetApprovalRevision: "approval-1"}},
		Role: e3contract.RoleOwner, AccountLabel: "owner-a", Spec: e3contract.IdentitySpec{Path: "/allowed/identity", SubjectIDPath: "id"},
		Marker: marker, Accounts: &e3IdentityAccounts{ref: "acct-1", header: http.Header{"Authorization": {"Bearer fixture-session"}}, current: true},
		DigestKey: bytes.Repeat([]byte{7}, 32), Timeout: time.Second}
	return e, in
}

func TestE3IdentitySendsThroughV3Boundary(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" || r.URL.Path != "/allowed/identity" || r.Header.Get("Authorization") != "Bearer fixture-session" || r.Header.Get(AttemptHeader) == "" {
			t.Error("신원 요청의 메서드·경로·세션·시도 참조가 다름")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"subject-a"}`)
	}))
	defer server.Close()
	recorder := &coreRecorder{}
	e, in := e3IdentityFixture(t, server.URL, recorder)
	event, err := e.SendE3Identity(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || event.Stage != "result" || event.Version != JournalVersionV3 ||
		!event.AuditComplete || !event.ForwardAttempted || !event.ResponseComplete ||
		event.E3Audit == nil || event.E3Audit.Normal != oracle.StateYes || event.E3Audit.SubjectDigest == "" ||
		event.E3Audit.AccountBindingRef != "acct-1" {
		t.Fatalf("신원 결과 또는 v3 감사 칸이 빠짐: requests=%d stage=%s version=%s audit=%v forward=%v complete=%v e3=%v decision=%s reason=%s rule=%s", requests.Load(), event.Stage, event.Version, event.AuditComplete, event.ForwardAttempted, event.ResponseComplete, event.E3Audit != nil, event.Decision, event.Reason, event.RuleRef)
	}
	for _, row := range recorder.events {
		if !validJournalRecordEvent(row, JournalVersionV3) {
			t.Fatalf("잘못된 v3 행: %s", row.Stage)
		}
	}
	if event.E3Audit.SubjectDigest == event.E3Audit.BodyDigestSHA256 {
		t.Fatal("주체 digest와 본문 digest가 같음")
	}
	for _, row := range recorder.events {
		raw, err := json.Marshal(row)
		if err != nil || bytes.Contains(raw, []byte("fixture-session")) || bytes.Contains(raw, []byte("subject-a")) {
			t.Fatal("v3 사건에 세션 또는 주체 원문이 남음")
		}
	}
	for _, row := range recorder.events {
		if row.Stage == "intent" && row.E3Audit != nil && row.E3Audit.SubjectDigest != "" {
			t.Fatal("intent의 메모리 사본이 결과로 변함")
		}
	}
}

func TestE3IdentityPartialResponseIsNotNormalEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Range", "bytes 0-17/100")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, `{"id":"subject-a"}`)
	}))
	defer server.Close()
	e, in := e3IdentityFixture(t, server.URL, &coreRecorder{})
	event, err := e.SendE3Identity(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if event.ResponseComplete || event.E3Audit == nil || event.E3Audit.SubjectDigest != "" {
		t.Fatal("부분 응답을 신원 정상 근거로 사용")
	}
}

func TestE3IdentityRefusesUnboundSessionAndOldJournal(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1) }))
	defer server.Close()
	for _, change := range []func(*Execution, *E3IdentityInput){
		func(_ *Execution, in *E3IdentityInput) { in.Accounts.(*e3IdentityAccounts).current = false },
		func(_ *Execution, in *E3IdentityInput) {
			in.Binding.InstanceRef = "other-instance"
			in.Binding.RunRef = "other-run"
		},
		func(e *Execution, _ *E3IdentityInput) { e.cfg.AuditVersion = Version },
		func(e *Execution, _ *E3IdentityInput) { e.cfg.E3Verifier = nil },
	} {
		recorder := &coreRecorder{}
		e, in := e3IdentityFixture(t, server.URL, recorder)
		change(e, &in)
		if _, err := e.SendE3Identity(context.Background(), in); err == nil {
			t.Fatal("결속되지 않은 신원 요청 수용")
		}
		if len(recorder.events) != 0 {
			t.Fatal("사전 결속 실패 뒤 감사 요청 생성")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("결속 실패 요청이 대상에 도착")
	}
	if _, err := (&Execution{}).BeginProgramCall(t.Context(), ProgramE3Identity, "ref"); !errors.Is(err, ErrE3JournalUnavailable) {
		t.Fatal("기존 프로그램 API로 E③ 요청을 열 수 있음")
	}
}

func TestE3IdentityRequiresResultACK(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"subject-a"}`)
	}))
	defer server.Close()
	recorder := &coreRecorder{failStage: "result"}
	e, in := e3IdentityFixture(t, server.URL, recorder)
	if result, err := e.SendE3Identity(t.Context(), in); err == nil || result.Stage != "" {
		t.Fatalf("감사 결과 ACK 실패 뒤 관찰 반환: %v", err)
	}
	if requests.Load() != 1 || e.Snapshot().AuditComplete {
		t.Fatal("ACK 실패 뒤 실제 송신 또는 후속 차단이 누락됨")
	}
}

func TestE3IdentityRejectsSessionReplacementWhileWaiting(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1) }))
	defer server.Close()
	recorder := &coreRecorder{}
	e, in := e3IdentityFixture(t, server.URL, recorder)
	for len(e.slots) < cap(e.slots) {
		e.slots <- struct{}{}
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.SendE3Identity(t.Context(), in)
		done <- err
	}()
	// 슬롯을 기다리는 동안 C14 결속 참조가 바뀌어도 이전 세션을 보내지 않는다.
	deadline := time.After(time.Second)
	for {
		e.mu.Lock()
		started := len(e.calls) > 0
		e.mu.Unlock()
		if started {
			break
		}
		select {
		case <-deadline:
			t.Fatal("신원 호출이 슬롯 대기에 이르지 못함")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	in.Accounts.(*e3IdentityAccounts).replace("acct-2")
	<-e.slots
	if err := <-done; !errors.Is(err, e3contract.ErrAuthorityRevoked) {
		t.Fatalf("교체된 결속을 허용: %v", err)
	}
	if requests.Load() != 0 {
		t.Fatal("교체 전 세션이 대상에 전송됨")
	}
}

func TestE3IdentityRejectsSessionReplacementAfterIntent(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1) }))
	defer server.Close()
	recorder := &coreRecorder{blockStage: "intent", entered: make(chan struct{}, 1), release: make(chan struct{})}
	e, in := e3IdentityFixture(t, server.URL, recorder)
	done := make(chan error, 1)
	go func() {
		_, err := e.SendE3Identity(t.Context(), in)
		done <- err
	}()
	select {
	case <-recorder.entered:
	case <-time.After(time.Second):
		t.Fatal("intent 기록에 이르지 못함")
	}
	in.Accounts.(*e3IdentityAccounts).replace("acct-2")
	close(recorder.release)
	if err := <-done; !errors.Is(err, e3contract.ErrAuthorityRevoked) {
		t.Fatalf("intent 뒤 계정 결속 교체를 허용: %v", err)
	}
	if requests.Load() != 0 {
		t.Fatal("교체 전 세션이 대상에 전송됨")
	}
}
