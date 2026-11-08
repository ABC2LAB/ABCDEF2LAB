package server

import (
	"context"
	"errors"
	"github.com/Autumn-27/artex/transmit/budget"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/transmit"
)

// 서비스 잠금 아래에서만 호출하는 합성 저장소. hook으로 취소/결정 경합을 고정한다.
type approvalReviewStore struct {
	requestApprovalStore
	row    db.RequestApproval
	insert func(context.Context) error
	get    func()
	swap   func(context.Context, db.RequestApproval, bool) error
}

func (s *approvalReviewStore) InsertRequestApproval(ctx context.Context, a db.RequestApproval) error {
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded approval insert")
	}
	if s.insert != nil {
		if err := s.insert(ctx); err != nil {
			return err
		}
	}
	s.row = a
	return nil
}
func (s *approvalReviewStore) GetRequestApproval(ctx context.Context, _ int64, _, _ string) (db.RequestApproval, error) {
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded approval get")
	}
	if s.get != nil {
		s.get()
	}
	return s.row, nil
}
func (s *approvalReviewStore) SwapRequestApproval(ctx context.Context, a db.RequestApproval, fresh bool) error {
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded approval swap")
	}
	if s.swap != nil {
		if err := s.swap(ctx, a, fresh); err != nil {
			return err
		}
	}
	s.row = a
	return nil
}
func approvalReviewFixture(t *testing.T, store *approvalReviewStore) (*RequestApprovalService, e2proxy.RequestApprovalProposal) {
	t.Helper()
	s := newRequestApprovalService(store)
	if err := s.bind(t.Context(), 1, "ARTEX", "run", []E2HTTPFileRule{{Ref: "rule", PathPrefix: "/objects"}}); err != nil {
		t.Fatal(err)
	}
	p := e2proxy.RequestApprovalProposal{OwnerRef: "ARTEX", Method: "GET", URL: "http://example.invalid/objects/private-fixture", ExpiresAt: time.Now().Add(time.Minute), Receipt: e2proxy.Receipt{Scope: transmit.Scope{TaskRef: "1", RunRef: "run"}, RuleRef: "rule", AttemptID: "attempt"}}
	return s, p
}
func TestRequestApprovalStoreCancellationPreservesCause(t *testing.T) {
	for _, stage := range []string{"insert", "consume"} {
		for _, genuineFailure := range []bool{false, true} {
			name := stage + "/context"
			if genuineFailure {
				name = stage + "/storage"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				store := &approvalReviewStore{}
				s, p := approvalReviewFixture(t, store)
				fail := func(context.Context) error {
					cancel()
					if genuineFailure {
						return errors.New("synthetic storage failure")
					}
					return ctx.Err()
				}
				if stage == "insert" {
					store.insert = fail
				} else {
					// DB 1차 조회에서 이미 확정된 허용을 반환해 실제 소비 CAS까지 진행한다.
					store.get = func() { store.row.State = "allowed" }
					store.swap = func(c context.Context, _ db.RequestApproval, fresh bool) error {
						if !fresh {
							t.Fatal("unexpected lifecycle write")
						}
						return fail(c)
					}
					// 만료 타이머가 아니라 실제 waiter를 깨운다.
					store.insert = func(context.Context) error {
						go func() { s.mu.Lock(); close(s.waiters[p.Receipt.AttemptID].decision); s.mu.Unlock() }()
						return nil
					}
				}
				allowed, err := s.AwaitRequestApproval(ctx, p)
				want := error(context.Canceled)
				if genuineFailure {
					want = errRequestApproval
				}
				if allowed || !errors.Is(err, want) {
					t.Fatalf("allowed=%t err=%v want=%v", allowed, err, want)
				}
				if store.row.Consumed {
					t.Fatal("failed consumption persisted")
				}
			})
		}
	}
}
func TestRequestApprovalWakeRechecksCancellationAndExpiry(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "cancelled"
		if expired {
			name = "expired"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			store := &approvalReviewStore{}
			s, p := approvalReviewFixture(t, store)
			if expired {
				p.ExpiresAt = time.Now().Add(20 * time.Millisecond)
			}
			store.insert = func(context.Context) error {
				go func() { s.mu.Lock(); close(s.waiters[p.Receipt.AttemptID].decision); s.mu.Unlock() }()
				return nil
			}
			store.get = func() {
				store.row.State = "allowed"
				if expired {
					time.Sleep(time.Until(p.ExpiresAt) + time.Millisecond)
				} else {
					cancel()
				}
			}
			allowed, err := s.AwaitRequestApproval(ctx, p)
			if allowed || err != nil || store.row.State != name || store.row.Consumed || store.row.ExecutionState != "not_sent" {
				t.Fatalf("wake result: allowed=%t err=%v state=%s", allowed, err, store.row.State)
			}
		})
	}
}
func TestRequestApprovalPrivatePathIsRedacted(t *testing.T) {
	_, p := approvalReviewFixture(t, &approvalReviewStore{})
	_, path, _, err := approvalPreview(p, E2HTTPFileRule{PathPrefix: "/objects"})
	if err != nil || path != "/objects/[REDACTED]" {
		t.Fatalf("private path exposed: %q %v", path, err)
	}
}

func TestRequestApprovalInsertHasBoundedPersistence(t *testing.T) {
	store := &approvalReviewStore{insert: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}
	s, p := approvalReviewFixture(t, store)
	started := time.Now()
	allowed, err := s.AwaitRequestApproval(t.Context(), p)
	if allowed || !errors.Is(err, errRequestApproval) || time.Since(started) > 2*requestApprovalStoreTimeout {
		t.Fatalf("unbounded or hidden storage timeout: %t %v", allowed, err)
	}
}

// DB 집행 컬럼만 만료시켜 Go 시계의 선행 검사가 CAS 가드를 가리지 않게 한다.
type approvalDBExpiredStore struct {
	*db.DB
	consume bool
}

func (s approvalDBExpiredStore) SwapRequestApproval(ctx context.Context, a db.RequestApproval, fresh bool) error {
	if a.Consumed == s.consume {
		if _, err := s.ExecContext(ctx, `UPDATE request_approvals SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, a.ID); err != nil {
			return err
		}
	}
	return s.DB.SwapRequestApproval(ctx, a, fresh)
}
func TestRequestApprovalStorageCancellationLeavesExecutionUsable(t *testing.T) {
	for _, stage := range []string{"insert", "consume"} {
		t.Run(stage, func(t *testing.T) {
			var hits atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
			defer target.Close()
			store := &approvalReviewStore{}
			s, _ := approvalReviewFixture(t, store)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fail := func(c context.Context) error { cancel(); <-c.Done(); return c.Err() }
			if stage == "insert" {
				store.insert = fail
			} else {
				store.insert = func(context.Context) error {
					go func() { s.mu.Lock(); close(s.waiters[store.row.ID].decision); s.mu.Unlock() }()
					return nil
				}
				store.get = func() { store.row.State = "allowed" }
				store.swap = func(c context.Context, _ db.RequestApproval, fresh bool) error {
					if fresh {
						return fail(c)
					}
					return nil
				}
			}
			now := time.Now()
			ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 2})
			if err != nil {
				t.Fatal(err)
			}
			key := budget.Key{TaskRef: "1", BudgetRef: "budget"}
			binding := budget.Binding{PolicyRef: "policy", ResetRef: "reset"}
			if err = ledger.Register(budget.Grant{Key: key, Binding: binding, MaxRequests: 2, ValidFrom: now.Add(-time.Second), ValidUntil: now.Add(time.Minute)}, now); err != nil {
				t.Fatal(err)
			}
			cfg := e2proxy.Config{ApprovalMode: e2proxy.ApprovalConfirmEveryRequest, RequestApproval: s, Scope: transmit.Scope{TaskRef: "1", RunRef: "run", CaseRef: "case", ResetRef: "reset", PolicyRef: "policy"}, IntentID: 1, OwnerRef: "ARTEX", PolicyRevision: "revision", AccountRef: "account", SessionRef: "session", ExpiresAt: now.Add(time.Minute), Budget: transmit.CumulativeBudget{Ledger: ledger, Key: key, Binding: binding}, Recorder: &approvalTestRecorder{service: s}, Rules: []e2proxy.Rule{{Ref: "rule", Origin: target.URL, ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/objects", PathScopeRef: "objects", Methods: []string{"GET"}}}, Limits: e2proxy.Limits{MaxAttempts: 8, MaxCalls: 2, MaxConcurrent: 1, MaxBodyBytes: 1024, RequestTimeout: time.Second, CallTimeout: time.Minute, MaxDenials: 8, MaxTransient: 8, MaxUnknown: 8}}
			execution, err := e2proxy.NewExecution(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			call, err := execution.BeginCall(t.Context(), "synthetic")
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequest("GET", target.URL+"/objects", nil)
			if err != nil {
				t.Fatal(err)
			}
			_, receipt, _ := call.Do(ctx, request)
			if receipt.Reason != e2proxy.Closed || !receipt.NotSent || hits.Load() != 0 || execution.Snapshot().Reason == e2proxy.AuditFailed {
				t.Fatalf("call cancellation closed execution: %+v", receipt)
			}
			remaining, err := ledger.Remaining(key, binding, time.Now())
			if err != nil || remaining != 2 {
				t.Fatal("cancelled approval reserved budget", err)
			}
			if _, err = execution.BeginCall(t.Context(), "next"); err != nil {
				t.Fatal("unrelated call was blocked", err)
			}
		})
	}
}

func TestRequestApprovalResultReleasesOnlyPersistedWaiter(t *testing.T) {
	for _, failed := range []bool{false, true} {
		store := &approvalReviewStore{row: db.RequestApproval{ID: "attempt", TaskID: 1, State: "allowed", Consumed: true, ExecutionState: "dispatch_pending"}}
		if failed {
			store.swap = func(context.Context, db.RequestApproval, bool) error { return errors.New("synthetic storage failure") }
		}
		s, _ := approvalReviewFixture(t, store)
		waiter := &requestApprovalWaiter{ctx: t.Context(), decision: make(chan struct{})}
		s.waiters["attempt"] = waiter
		event := e2proxy.Event{Stage: "result", Receipt: e2proxy.Receipt{AttemptID: "attempt", Scope: transmit.Scope{TaskRef: "1"}, ResponseComplete: true}}
		err := s.observe(t.Context(), event)
		if failed {
			if err == nil || s.waiters["attempt"] != waiter {
				t.Fatal("failed result discarded waiter")
			}
		} else if err != nil || s.waiters["attempt"] != nil || store.row.ExecutionState != "completed" {
			t.Fatal("persisted result retained waiter", err)
		}
	}
}
