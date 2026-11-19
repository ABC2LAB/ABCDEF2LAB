package e2host

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2checkpoint"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func fixture(t *testing.T) (*Host, *sql.DB, transmit.Scope) {
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
	schema := "e2_host_" + hex.EncodeToString(nonce[:])
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
	epoch := e2checkpoint.Epoch{StoreRef: "store-host", EpochRef: "epoch-host", Generation: 1,
		AnchorDigest: sha256.Sum256([]byte("independent-test-anchor"))}
	if _, err := pool.Exec(`INSERT INTO e2_http_audit_epochs(store_ref,epoch_ref,generation,anchor_digest) VALUES($1,$2,$3,$4)`,
		epoch.StoreRef, epoch.EpochRef, int64(epoch.Generation), epoch.AnchorDigest[:]); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "journals")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	host, err := New(pool, epoch, "key-host", bytes.Repeat([]byte{0x45}, 32), root)
	if err != nil || host.CheckEpoch(t.Context(), pool) != nil || host.CheckRestoredHeads(t.Context()) != nil {
		t.Fatal("Host 기동 검증 실패", err)
	}
	scope := transmit.Scope{RunRef: "run-host", TaskRef: "task-host", CaseRef: "case-host", ResetRef: "reset-host", PolicyRef: "policy-host"}
	return host, pool, scope
}

func hostEvent(scope transmit.Scope) e2proxy.Event {
	return e2proxy.Event{Index: 1, Stage: "result", Receipt: e2proxy.Receipt{
		Version: e2proxy.JournalVersionV3, IntentID: 1, Scope: scope, PolicyRevision: "revision-host",
		CallRef: "call-host", AttemptID: "0123456789abcdef0123456789abcdef", Sequence: 1, Method: "GET",
		Decision: "denied", Reason: e2proxy.PolicyDenied, NotSent: true, AuditComplete: true,
		At: time.Unix(1_700_000_000, 0).UTC(),
	}}
}

func TestHostPublishesCurrentAndRejectsRollback(t *testing.T) {
	host, pool, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Record(t.Context(), hostEvent(scope)); err != nil {
		t.Fatal(err)
	}
	if err := session.Seal(t.Context()); err != nil || host.CheckRestoredHeads(t.Context()) != nil {
		t.Fatal("봉인 또는 복원 기준 실패", err)
	}
	if _, err := pool.Exec(`UPDATE e2_http_audit_heads SET revision=revision-1 WHERE run_ref=$1`, scope.RunRef); err != nil {
		t.Fatal(err)
	}
	if host.CheckRestoredHeads(t.Context()) == nil {
		t.Fatal("DB head rollback 허용")
	}
	if _, err := host.Open(t.Context(), transmit.Scope{RunRef: "run-later", TaskRef: scope.TaskRef, CaseRef: scope.CaseRef, ResetRef: scope.ResetRef, PolicyRef: scope.PolicyRef}, "revision-host"); err == nil {
		t.Fatal("rollback 뒤 새 실행 허용")
	}
}

func TestHostRejectsMissingJournalForStoredHead(t *testing.T) {
	host, _, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Record(t.Context(), hostEvent(scope)); err != nil {
		t.Fatal(err)
	}
	if err := session.Seal(t.Context()); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(scope.RunRef))
	if err := os.Rename(filepath.Join(host.root, hex.EncodeToString(digest[:])), filepath.Join(t.TempDir(), "removed-journal")); err != nil {
		t.Fatal(err)
	}
	if host.CheckRestoredHeads(t.Context()) == nil {
		t.Fatal("DB head가 남았는데 저널이 없는 상태를 수용")
	}
	if _, err := host.Open(t.Context(), transmit.Scope{RunRef: "run-later", TaskRef: scope.TaskRef, CaseRef: scope.CaseRef, ResetRef: scope.ResetRef, PolicyRef: scope.PolicyRef}, "revision-host"); err == nil {
		t.Fatal("저널 누락 뒤 새 실행 허용")
	}
}

func TestHostBindingAndAbortFailClosed(t *testing.T) {
	host, pool, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	bad := hostEvent(scope)
	bad.PolicyRevision = "different"
	if session.Record(t.Context(), bad) == nil {
		t.Fatal("정책 revision 변경 허용")
	}
	bad = hostEvent(scope)
	bad.Scope.TaskRef = "other-task"
	if session.Record(t.Context(), bad) == nil {
		t.Fatal("다른 task 행사 허용")
	}
	session.Abort()
	if session.Record(t.Context(), hostEvent(scope)) == nil || session.Seal(t.Context()) == nil {
		t.Fatal("중단 뒤 기록 또는 정상 봉인 허용")
	}
	// 미봉인 open head는 종료·drain 증거가 아니다.
	if host.CheckRestoredHeads(t.Context()) == nil || host.CheckEpoch(context.Background(), pool) != nil {
		t.Fatal("미봉인 head를 새 실행 권위로 수용")
	}
	if _, err := host.Open(t.Context(), scope, "revision-host"); err == nil {
		t.Fatal("중단한 run 재사용 허용")
	}
}

func TestHostEpochChangePoisonsCurrentSession(t *testing.T) {
	host, pool, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Abort()
	if _, err := pool.Exec(`UPDATE e2_http_audit_epochs SET epoch_ref='epoch-restored' WHERE store_ref=$1`, host.epoch.StoreRef); err != nil {
		t.Fatal(err)
	}
	if session.Record(t.Context(), hostEvent(scope)) == nil {
		t.Fatal("epoch 교체 뒤 게시 허용")
	}
	if _, err := pool.Exec(`UPDATE e2_http_audit_epochs SET epoch_ref=$1 WHERE store_ref=$2`, host.epoch.EpochRef, host.epoch.StoreRef); err != nil {
		t.Fatal(err)
	}
	if session.Record(t.Context(), hostEvent(scope)) == nil || session.Seal(t.Context()) == nil {
		t.Fatal("epoch 복원 뒤 실패한 lease가 되살아남")
	}
}

func TestHostRecorderWithActualE2SyntheticHTTP(t *testing.T) {
	host, _, scope := fixture(t)
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		_, _ = w.Write([]byte("synthetic response"))
	}))
	defer target.Close()
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Abort()
	ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 1})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	grant := budget.Grant{Key: budget.Key{TaskRef: scope.TaskRef, BudgetRef: "budget-host"},
		Binding: budget.Binding{PolicyRef: scope.PolicyRef, ResetRef: scope.ResetRef}, MaxRequests: 1,
		ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute)}
	if err := ledger.Register(grant, now); err != nil {
		t.Fatal(err)
	}
	execution, err := e2proxy.NewExecution(t.Context(), e2proxy.Config{
		AuditVersion: e2proxy.JournalVersionV3, ApprovalMode: e2proxy.ApprovalPreauthorizedScope,
		Scope: scope, IntentID: 1, OwnerRef: "ARTEX", PolicyRevision: "revision-host",
		AccountRef: "anonymous", SessionRef: "anonymous-session", ExpiresAt: now.Add(time.Minute),
		Rules: []e2proxy.Rule{{Ref: "rule-host", Origin: target.URL, ConnectIP: netip.MustParseAddr("127.0.0.1"),
			PathPrefix: "/allowed", PathScopeRef: "path-host", Methods: []string{"GET"}}},
		Budget: transmit.CumulativeBudget{Ledger: ledger, Key: grant.Key, Binding: grant.Binding}, Recorder: session,
		Limits: e2proxy.Limits{MaxAttempts: 1, MaxCalls: 1, MaxConcurrent: 1, MaxBodyBytes: 1024,
			RequestTimeout: time.Second, CallTimeout: time.Second, MaxDenials: 2, MaxTransient: 2, MaxUnknown: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	call, err := execution.BeginCall(t.Context(), "command-host")
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target.URL+"/allowed", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, receipt, err := call.Do(t.Context(), request)
	if err != nil || response == nil || !receipt.AuditComplete || !receipt.ResponseComplete {
		t.Fatal("합성 HTTP의 E②·Host ACK 실패", err, receipt)
	}
	_ = response.Body.Close()
	if _, err := call.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := execution.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := session.Seal(t.Context()); err != nil || received.Load() != 1 || host.CheckRestoredHeads(t.Context()) != nil {
		t.Fatal("합성 송신·봉인 결속 실패", err, received.Load())
	}
}
