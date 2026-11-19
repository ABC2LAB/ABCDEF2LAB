package e2live

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2checkpoint"
	"github.com/Autumn-27/artex/e2host"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3fake"
	"github.com/Autumn-27/artex/e3contract/e3v3"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// 이 시험은 실제 E② 송신과 PostgreSQL head·MAC 저널을 쓴다. 제품 Compose·실대상 회차는 별도다.
func liveHostFixture(t *testing.T) (*e2host.Session, transmit.Scope) {
	t.Helper()
	dsn := os.Getenv("ARTEX_AUDIT_TEST_DSN")
	if dsn == "" {
		t.Skip("일회용 PostgreSQL 16이 제공되지 않음")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = admin.Close() })
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "e3_live_" + hex.EncodeToString(nonce[:])
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	pool := stdlib.OpenDB(*config)
	t.Cleanup(func() {
		_ = pool.Close()
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error(err)
		}
	})
	if err := e2checkpoint.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	epoch := e2checkpoint.Epoch{StoreRef: "store-live", EpochRef: "epoch-live", Generation: 1,
		AnchorDigest: sha256.Sum256([]byte("independent-live-anchor"))}
	if _, err := pool.Exec(`INSERT INTO e2_http_audit_epochs(store_ref,epoch_ref,generation,anchor_digest) VALUES($1,$2,$3,$4)`,
		epoch.StoreRef, epoch.EpochRef, int64(epoch.Generation), epoch.AnchorDigest[:]); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "journals")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	host, err := e2host.New(pool, epoch, "key-live", bytes.Repeat([]byte{0x65}, 32), root)
	if err != nil || host.CheckEpoch(t.Context(), pool) != nil || host.CheckRestoredHeads(t.Context()) != nil {
		t.Fatal("독립 Host 기동 검증 실패", err)
	}
	scope := transmit.Scope{RunRef: "run-live", TaskRef: "task-live", CaseRef: "case-live",
		ResetRef: "reset-live", PolicyRef: "policy-live"}
	session, err := host.Open(t.Context(), scope, "revision-live")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Abort() })
	return session, scope
}

type liveAccounts struct{}

func (liveAccounts) AccountBindingRef(runRef, label string) (string, bool) {
	if runRef != "run-live" || label != "owner-a" && label != "cross-b" {
		return "", false
	}
	return "acct-" + label, true
}

func (a liveAccounts) BoundSession(runRef, label, ref string) (http.Header, error) {
	want, ok := a.AccountBindingRef(runRef, label)
	if !ok || ref != want {
		return nil, e2proxy.ErrDenied
	}
	return http.Header{"Authorization": {"Bearer fixture-" + label}}, nil
}

func TestLiveE3RunWithCurrentHostHeadAndSyntheticAccounts(t *testing.T) {
	for _, crossAllowed := range []bool{true, false} {
		name := "cross_denied"
		if crossAllowed {
			name = "cross_visible"
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var marker string
			var requests int
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				requests++
				account := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer fixture-")
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/me" && (account == "owner-a" || account == "cross-b"):
					_ = json.NewEncoder(w).Encode(map[string]string{"id": account})
				case r.Method == http.MethodPost && r.URL.Path == "/notes" && account == "owner-a":
					var body map[string]string
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["body"] == "" {
						t.Error("생성 표식 본문 없음")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					marker = body["body"]
					_ = json.NewEncoder(w).Encode(map[string]string{"id": "item-1"})
				case r.Method == http.MethodGet && r.URL.Path == "/notes/item-1" &&
					(account == "owner-a" || account == "cross-b" && crossAllowed):
					_ = json.NewEncoder(w).Encode(map[string]string{"body": marker})
				default:
					w.WriteHeader(http.StatusForbidden)
					_, _ = io.WriteString(w, `{"error":"denied"}`)
				}
			}))
			defer target.Close()
			session, scope := liveHostFixture(t)
			now := time.Now().UTC()
			ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 10})
			if err != nil {
				t.Fatal(err)
			}
			grant := budget.Grant{Key: budget.Key{TaskRef: scope.TaskRef, BudgetRef: "budget-live"},
				Binding:     budget.Binding{PolicyRef: scope.PolicyRef, ResetRef: scope.ResetRef},
				MaxRequests: 5, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour)}
			if err := ledger.Register(grant, now); err != nil {
				t.Fatal(err)
			}
			ip := netip.MustParseAddr("127.0.0.1")
			rule := func(ref, purpose, path string, methods ...string) e2proxy.Rule {
				return e2proxy.Rule{Ref: ref, Purpose: purpose, Origin: target.URL, ConnectIP: ip,
					PathPrefix: path, PathScopeRef: ref + "-path", Methods: methods}
			}
			config := e2proxy.Config{AuditVersion: e2proxy.JournalVersionV3, E3Verifier: session,
				E3ConfirmationRequests: 5, E3WriteConsent: true, E3Resettable: true,
				ApprovalMode: e2proxy.ApprovalPreauthorizedScope, Scope: scope, IntentID: 1,
				OwnerRef: "ARTEX", PolicyRevision: "revision-live", AccountRef: "anonymous",
				SessionRef: "anonymous-session", ExpiresAt: now.Add(time.Hour),
				Rules: []e2proxy.Rule{rule("worker", "", "/", "GET", "POST"),
					rule("identity", e2proxy.PurposeE3Identity, "/me", "GET"),
					rule("create", e2proxy.PurposeE3Create, "/notes", "POST"),
					rule("read", e2proxy.PurposeE3Read, "/notes/{id}", "GET")},
				Budget: transmit.CumulativeBudget{Ledger: ledger, Key: grant.Key, Binding: grant.Binding}, Recorder: session,
				Limits: e2proxy.Limits{MaxAttempts: 10, MaxCalls: 10, MaxConcurrent: 1, MaxBodyBytes: 1 << 20,
					RequestTimeout: time.Second, CallTimeout: time.Minute, MaxDenials: 10, MaxTransient: 10, MaxUnknown: 10}}
			execution, err := e2proxy.NewExecution(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			decl := e3contract.Declaration{Version: e3contract.DeclarationVersion, TargetRef: "target-live",
				TargetApprovalRevision: "approval-live", DeclaredBy: "operator-live", DeclaredAt: now.Add(-time.Minute),
				Identity: e3contract.IdentitySpec{Path: "/me", SubjectIDPath: "id"},
				Types: []e3contract.TypeDeclaration{{Type: "note", Sharing: e3contract.SharingOwnerOnly,
					OwnerOnlyFields: []string{"body"}, Create: e3contract.CreateSpec{Path: "/notes", Method: "POST",
						Kind: e3contract.BodyJSON, BodyTemplate: `{"title":"test"}`, MarkerField: "body", IDPath: "id"},
					Read: e3contract.ReadSpec{PathTemplate: "/notes/{id}", DataPath: "."}}},
				OwnerAccountLabel: "owner-a", CrossAccountLabel: "cross-b", AccountsSamePrivilege: true}
			policy, err := e3contract.Freeze(decl, "frozen-live")
			if err != nil {
				t.Fatal(err)
			}
			binding := e3contract.RunBinding{JobRef: "job-live", RunRef: scope.RunRef, TaskRef: scope.TaskRef,
				E2PolicyRef: scope.PolicyRef, GenerationRef: "generation-live", InstanceRef: "instance-live", Policy: policy}
			live, err := NewLive(execution, liveAccounts{}, binding)
			if err != nil {
				t.Fatal(err)
			}
			defer live.Close()
			adapter, err := e3v3.New(live)
			if err != nil {
				t.Fatal(err)
			}
			writes := &e3fake.Writes{}
			result := e3contract.Run(t.Context(), e3contract.Deps{Transport: adapter, Journal: adapter,
				Declarations: e3fake.Declarations{Stored: map[string]e3contract.Declaration{policy.Ref: decl}},
				Reset:        e3fake.Reset{State: e3contract.ResetState{Residue: e3contract.ResidueAbsent}},
				Writes:       writes, Now: time.Now}, e3contract.Input{Binding: binding, TargetRef: decl.TargetRef,
				WriteConsent: true, Owner: e3contract.Account{Label: "owner-a", Authenticated: true},
				Cross:           e3contract.Account{Label: "cross-b", Authenticated: true},
				PolicyValidFrom: now.Add(-time.Minute), PolicyValidUntil: now.Add(time.Hour),
				AdmittedTypes: 1, RequestTimeout: time.Second,
				Candidates: []e3contract.Candidate{{Ref: "candidate-note", Method: "GET", PathPattern: "/notes/{id}"}}})
			mu.Lock()
			count := requests
			mu.Unlock()
			if result.Status != e3contract.StatusCompleted || result.Requests != 5 || count != 5 ||
				len(result.Verdicts) != 1 || len(result.Candidates) != 1 || len(writes.Events()) != 2 {
				t.Fatalf("실제 E②→Host→E③ 회차 불완전: status=%s reason=%s requests=%d target=%d verdicts=%d candidates=%d writes=%d",
					result.Status, result.Reason, result.Requests, count, len(result.Verdicts), len(result.Candidates), len(writes.Events()))
			}
			got := result.Verdicts[0].Value
			level := result.Candidates[0].Level
			if crossAllowed && (got != oracle.Confirmed || level != oracle.Confirmed) ||
				!crossAllowed && (got != oracle.Undecidable || level == oracle.Confirmed) {
				t.Fatalf("교차 조회 정책 판정 오류: cross_allowed=%v verdict=%s candidate=%s", crossAllowed, got, level)
			}
			if err := session.Seal(t.Context()); err != nil {
				t.Fatal("실제 감사 head 봉인 실패", err)
			}
		})
	}
}
