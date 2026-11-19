package e2live

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3v3"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
)

// 시험 저장소는 변환과 실제 E② 송신만 확인한다. 독립 Host의 Sync·ACK·MAC 시험은 이 fixture의 범위 밖이다.
type bridgeFixtureJournal struct {
	mu     sync.Mutex
	events []e2proxy.Event
}

func (j *bridgeFixtureJournal) Record(_ context.Context, ev e2proxy.Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.events = append(j.events, ev)
	return nil
}

func (j *bridgeFixtureJournal) Verified(_ context.Context, _ e3contract.RunBinding, ref e3contract.CallRef) (e2proxy.Event, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for i := len(j.events) - 1; i >= 0; i-- {
		ev := j.events[i]
		if ev.Stage == "result" && ev.CallRef == ref.CallRef && ev.AttemptID == ref.AttemptID && ev.Sequence == ref.Sequence {
			return ev, nil
		}
	}
	return e2proxy.Event{}, errors.New("fixture event missing")
}

type bridgeFixtureAccounts struct{}

func (bridgeFixtureAccounts) AccountBindingRef(runRef, label string) (string, bool) {
	return "acct-1", runRef == "run" && label == "owner-a"
}
func (bridgeFixtureAccounts) BoundSession(runRef, label, ref string) (http.Header, error) {
	if runRef != "run" || label != "owner-a" || ref != "acct-1" {
		return nil, e2proxy.ErrDenied
	}
	return http.Header{"Authorization": {"Bearer fixture-session"}}, nil
}

func TestLiveBridgeIdentityUsesRealE2SendAndConvertsVerifiedEvent(t *testing.T) {
	received := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received++
		if r.Method != "GET" || r.URL.Path != "/allowed/me" || r.Header.Get("Authorization") != "Bearer fixture-session" {
			t.Error("E②가 요청 범위나 현재 세션을 다르게 적용함")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"owner-a"}`)
	}))
	defer target.Close()
	journal := &bridgeFixtureJournal{}
	now := time.Now()
	ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 10})
	if err != nil {
		t.Fatal(err)
	}
	grant := budget.Grant{Key: budget.Key{TaskRef: "task", BudgetRef: "budget"}, Binding: budget.Binding{PolicyRef: "policy", ResetRef: "reset"},
		MaxRequests: 5, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour)}
	if err := ledger.Register(grant, now); err != nil {
		t.Fatal(err)
	}
	cfg := e2proxy.Config{AuditVersion: e2proxy.JournalVersionV3, E3Verifier: journal, E3ConfirmationRequests: 2,
		ApprovalMode: e2proxy.ApprovalPreauthorizedScope,
		Scope:        transmit.Scope{TaskRef: "task", RunRef: "run", CaseRef: "case", ResetRef: "reset", PolicyRef: "policy"},
		IntentID:     1, OwnerRef: "ARTEX", PolicyRevision: "revision1", AccountRef: "anonymous", SessionRef: "anonymous-session", ExpiresAt: now.Add(time.Hour),
		Rules: []e2proxy.Rule{{Ref: "worker", Origin: target.URL, ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/allowed", PathScopeRef: "worker-path", Methods: []string{"GET"}},
			{Ref: "identity", Purpose: e2proxy.PurposeE3Identity, Origin: target.URL, ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/allowed/me", PathScopeRef: "identity-path", Methods: []string{"GET"}}},
		Budget: transmit.CumulativeBudget{Ledger: ledger, Key: grant.Key, Binding: grant.Binding}, Recorder: journal,
		Limits: e2proxy.Limits{MaxAttempts: 10, MaxCalls: 10, MaxConcurrent: 1, MaxBodyBytes: 1 << 20,
			RequestTimeout: time.Second, CallTimeout: time.Minute, MaxDenials: 10, MaxTransient: 10, MaxUnknown: 10}}
	execution, err := e2proxy.NewExecution(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	binding := e3contract.RunBinding{JobRef: "job-1", RunRef: "run", TaskRef: "task", E2PolicyRef: "policy", GenerationRef: "generation-1", InstanceRef: "instance-1",
		Policy: e3contract.PolicySource{Kind: e3contract.PolicyProductDeclaration, Ref: "declaration-1", DigestSHA256: strings.Repeat("a", 64), TargetApprovalRevision: "approval-1"}}
	live, err := NewLive(execution, bridgeFixtureAccounts{}, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	adapter, err := e3v3.New(live)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := e3contract.NewMarker(bytes.NewReader(bytes.Repeat([]byte{1}, 128)))
	if err != nil || adapter.RegisterMarker(t.Context(), binding, marker) != nil {
		t.Fatal("표식 등록 실패", err)
	}
	observed, err := adapter.Identity(t.Context(), e3contract.IdentityRequest{Binding: binding, Spec: e3contract.IdentitySpec{Path: "/allowed/me", SubjectIDPath: "id"},
		Role: e3contract.RoleOwner, AccountLabel: "owner-a", Timeout: time.Second})
	if err != nil || received != 1 || !observed.Sent || !observed.ResponseComplete || observed.SubjectDigest == "" {
		t.Fatalf("실제 E② 송신 또는 관찰 변환 실패: %v received=%d", err, received)
	}
	sealed, err := adapter.Sealed(t.Context(), binding, observed.Ref)
	if err != nil || sealed.Ref != observed.Ref || sealed.SubjectDigest != observed.SubjectDigest {
		t.Fatalf("v3 사건 조회 또는 관찰 대조 실패: %v", err)
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	for _, ev := range journal.events {
		if raw := ev.E3Audit; raw != nil && raw.SubjectDigest == "owner-a" {
			t.Fatal("식별자 원문이 감사 사건에 남음")
		}
	}
}
