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
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3v3"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
)

// checkpointJournal은 검증에 쓴 현재 head 체크포인트도 돌려주는 시험 저장소다(E② Host VerifiedAt 자리).
type checkpointJournal struct {
	bridgeFixtureJournal
	checkpoint e2proxy.JournalCheckpoint
}

func (j *checkpointJournal) VerifiedCheckpoint(ctx context.Context, b e3contract.RunBinding, ref e3contract.CallRef) (e2proxy.Event, e2proxy.JournalCheckpoint, error) {
	ev, err := j.Verified(ctx, b, ref)
	return ev, j.checkpoint, err
}

var checkpointScope = transmit.Scope{TaskRef: "task", RunRef: "run", CaseRef: "case", ResetRef: "reset", PolicyRef: "policy"}

func checkpointExecution(t *testing.T, verifier e2proxy.E3Verifier, recorder e2proxy.Recorder, target string) *e2proxy.Execution {
	t.Helper()
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
	cfg := e2proxy.Config{AuditVersion: e2proxy.JournalVersionV3, E3Verifier: verifier, E3ConfirmationRequests: 2,
		ApprovalMode: e2proxy.ApprovalPreauthorizedScope, Scope: checkpointScope,
		IntentID: 1, OwnerRef: "ARTEX", PolicyRevision: "revision1", AccountRef: "anonymous", SessionRef: "anonymous-session", ExpiresAt: now.Add(time.Hour),
		Rules: []e2proxy.Rule{{Ref: "worker", Origin: target, ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/allowed", PathScopeRef: "worker-path", Methods: []string{"GET"}},
			{Ref: "identity", Purpose: e2proxy.PurposeE3Identity, Origin: target, ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/allowed/me", PathScopeRef: "identity-path", Methods: []string{"GET"}}},
		Budget: transmit.CumulativeBudget{Ledger: ledger, Key: grant.Key, Binding: grant.Binding}, Recorder: recorder,
		Limits: e2proxy.Limits{MaxAttempts: 10, MaxCalls: 10, MaxConcurrent: 1, MaxBodyBytes: 1 << 20,
			RequestTimeout: time.Second, CallTimeout: time.Minute, MaxDenials: 10, MaxTransient: 10, MaxUnknown: 10}}
	execution, err := e2proxy.NewExecution(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return execution
}

// 확정 단계 근거 하나를 Host에 다시 검증받으면 그 검증에 쓴 현재 head 체크포인트가 E② 실행 → 실제 브리지 → 제품 지연
// 어댑터를 그대로 지나온다(E1 계약 §2). 이 실행 범위의 v3 체크포인트가 아니면 받지 않는다.
func TestLiveVerifiedCheckpointPassesHostCheckpoint(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"owner-a"}`)
	}))
	defer target.Close()
	binding := e3contract.RunBinding{JobRef: "job-1", RunRef: "run", TaskRef: "task", E2PolicyRef: "policy", GenerationRef: "generation-1", InstanceRef: "instance-1",
		Policy: e3contract.PolicySource{Kind: e3contract.PolicyProductDeclaration, Ref: "declaration-1", DigestSHA256: strings.Repeat("a", 64), TargetApprovalRevision: "approval-1"}}
	good := e2proxy.JournalCheckpoint{Version: e2proxy.JournalVersionV3, JournalID: "journal-1", Sequence: 4, MAC: strings.Repeat("c", 64), Scope: checkpointScope}
	otherScope := good
	otherScope.Scope.RunRef = "other-run"
	v2 := good
	v2.Version = "e2-http-v2"
	header := good
	header.Sequence = 0
	for name, tc := range map[string]struct {
		checkpoint *e2proxy.JournalCheckpoint
		ok         bool
	}{
		"current_head": {&good, true},
		"other_scope":  {&otherScope, false},
		"v2_journal":   {&v2, false},
		"header_only":  {&header, false},
		"no_support":   {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			var verifier e2proxy.E3Verifier
			var recorder e2proxy.Recorder
			if tc.checkpoint != nil {
				j := &checkpointJournal{checkpoint: *tc.checkpoint}
				verifier, recorder = j, j
			} else {
				j := &bridgeFixtureJournal{}
				verifier, recorder = j, j
			}
			execution := checkpointExecution(t, verifier, recorder, target.URL)
			lazy, err := e3v3.NewLazy(func(b e3contract.RunBinding) (e3v3.Bridge, error) {
				return NewLive(execution, bridgeFixtureAccounts{}, b)
			})
			if err != nil {
				t.Fatal(err)
			}
			defer lazy.Close()
			adapter, err := e3v3.New(lazy)
			if err != nil {
				t.Fatal(err)
			}
			marker, err := e3contract.NewMarker(bytes.NewReader(bytes.Repeat([]byte{1}, 128)))
			if err != nil || adapter.RegisterMarker(t.Context(), binding, marker) != nil {
				t.Fatal("표식 등록 실패", err)
			}
			observed, err := adapter.Identity(t.Context(), e3contract.IdentityRequest{Binding: binding, Spec: e3contract.IdentitySpec{Path: "/allowed/me", SubjectIDPath: "id"},
				Role: e3contract.RoleOwner, AccountLabel: "owner-a", Timeout: time.Second})
			if err != nil || !observed.ResponseComplete {
				t.Fatal("근거 송신 실패", err)
			}
			ev, cp, err := lazy.VerifiedCheckpoint(t.Context(), binding, observed.Ref)
			if tc.ok {
				if err != nil || ev.CallRef != observed.Ref.CallRef || cp != (e3v3.Checkpoint{JournalID: good.JournalID, Sequence: good.Sequence, MAC: good.MAC}) {
					t.Fatalf("체크포인트: %+v %v", cp, err)
				}
				return
			}
			if err == nil || cp != (e3v3.Checkpoint{}) {
				t.Fatalf("받지 않아야 할 체크포인트를 받음: %+v", cp)
			}
		})
	}
	var _ e3v3.CheckpointE2 = (*Live)(nil)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := (&Live{}).VerifiedCheckpoint(canceled, binding, e3contract.CallRef{}); !errors.Is(err, context.Canceled) {
		t.Fatal("중지를 감사 장애로 바꿈", err)
	}
}
