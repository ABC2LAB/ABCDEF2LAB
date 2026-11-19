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

func hostSentEvents(scope transmit.Scope) (e2proxy.Event, e2proxy.Event) {
	intent := hostEvent(scope)
	intent.Stage = "intent"
	intent.Decision = "allowed"
	intent.Reason = e2proxy.Complete
	intent.RuleRef = "rule-host"
	result := intent
	result.Index = 2
	result.Stage = "result"
	result.NotSent = false
	result.ForwardAttempted = true
	result.RequestWritten = true
	result.ResponseReceived = true
	result.ResponseComplete = true
	result.StatusCode = 200
	return intent, result
}

func TestHostUnfinishedSendCannotSealOrRecover(t *testing.T) {
	host, _, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	intent, _ := hostSentEvents(scope)
	if err := session.Record(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	if err := session.Seal(t.Context()); err == nil {
		t.Fatal("결과가 없는 송신 의도를 정상 봉인")
	}
	session.Abort()
	if host.RecoverDisconnectedHeads(t.Context()) == nil || host.CheckRestoredHeads(t.Context()) == nil {
		t.Fatal("미완료 송신을 복구 마감으로 승격")
	}
	next := scope
	next.RunRef = "run-after-unfinished"
	if _, err := host.Open(t.Context(), next, "revision-host"); err == nil {
		t.Fatal("미완료 송신 뒤 새 실행 허용")
	}
}

func TestHostRejectsUnmatchedOrForgedResult(t *testing.T) {
	for _, tc := range []struct {
		name         string
		changeResult func(*e2proxy.Event)
	}{
		{"allowed without intent", nil},
		{"wrong call reference", func(event *e2proxy.Event) { event.CallRef = "other-call" }},
		{"wrong rule on same attempt", func(event *e2proxy.Event) { event.RuleRef = "other-rule" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, _, scope := fixture(t)
			session, err := host.Open(t.Context(), scope, "revision-host")
			if err != nil {
				t.Fatal(err)
			}
			intent, result := hostSentEvents(scope)
			if tc.changeResult == nil {
				result.Index = 1
			} else {
				if err := session.Record(t.Context(), intent); err != nil {
					t.Fatal(err)
				}
				tc.changeResult(&result)
			}
			if err := session.Record(t.Context(), result); err != nil {
				t.Fatal(err)
			}
			if err := session.Seal(t.Context()); err == nil {
				t.Fatal("잘못 결속한 결과 사건으로 감사 마감")
			}
			session.Abort()
			if host.RecoverDisconnectedHeads(t.Context()) == nil {
				t.Fatal("잘못 결속한 결과 사건을 중단 마감")
			}
		})
	}
}

func TestHostInterruptedRecoveryRejectsEpochAndMirrorRollback(t *testing.T) {
	for _, change := range []string{"epoch", "head", "mirror", "journal"} {
		t.Run(change, func(t *testing.T) {
			host, pool, scope := fixture(t)
			session, err := host.Open(t.Context(), scope, "revision-host")
			if err != nil {
				t.Fatal(err)
			}
			if err := session.Record(t.Context(), hostEvent(scope)); err != nil {
				t.Fatal(err)
			}
			session.Abort()
			switch change {
			case "epoch":
				if _, err := pool.Exec(`UPDATE e2_http_audit_epochs SET epoch_ref='rollback' WHERE store_ref=$1`, host.epoch.StoreRef); err != nil {
					t.Fatal(err)
				}
			case "head":
				if _, err := pool.Exec(`UPDATE e2_http_audit_heads SET revision=revision-1 WHERE run_ref=$1`, scope.RunRef); err != nil {
					t.Fatal(err)
				}
			case "mirror":
				digest := sha256.Sum256([]byte(scope.RunRef))
				path := filepath.Join(host.root, hex.EncodeToString(digest[:]), "head.json")
				if err := os.WriteFile(path, []byte(`{"rollback":true}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "journal":
				digest := sha256.Sum256([]byte(scope.RunRef))
				path := filepath.Join(host.root, hex.EncodeToString(digest[:]), "events.jsonl")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				data[len(data)-12] ^= 1
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if host.RecoverDisconnectedHeads(t.Context()) == nil {
				t.Fatal("현재 권위 불일치를 안전 마감으로 승격")
			}
		})
	}
}

func TestHostRecoverySkipsActiveRunBeforeStoreScan(t *testing.T) {
	host, pool, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Abort() })

	// Host는 실행을 한 회차만 연다. DB의 유일한 접속을 잡아 복구가
	// 저장소 조회에 들어가기 전에 거부되는지 확인한다.
	pool.SetMaxOpenConns(1)
	held, err := pool.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	before := pool.Stats().WaitCount
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	recoverErr := host.RecoverDisconnectedHeads(ctx)
	cancel()
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if recoverErr == nil || pool.Stats().WaitCount != before {
		t.Fatal("활성 회차가 있는 동안 복구가 저장소 조회에 진입", recoverErr)
	}

	var phase string
	query := "SELECT phase FROM e2_http_audit_heads WHERE run_ref=$1"
	if err := pool.QueryRowContext(t.Context(), query, scope.RunRef).Scan(&phase); err != nil || phase != string(e2checkpoint.Open) {
		t.Fatalf("활성 회차의 head가 open이 아님: %q, %v", phase, err)
	}
	if err := session.Record(t.Context(), hostEvent(scope)); err != nil {
		t.Fatal("복구 거부 뒤 활성 회차 기록 실패", err)
	}
	if err := session.Seal(t.Context()); err != nil {
		t.Fatal("복구 거부 뒤 활성 회차 봉인 실패", err)
	}
	if err := pool.QueryRowContext(t.Context(), query, scope.RunRef).Scan(&phase); err != nil || phase != string(e2checkpoint.Sealed) {
		t.Fatalf("정상 종료한 회차의 head가 sealed가 아님: %q, %v", phase, err)
	}
}

func TestHostRestartRecoversVerifiedInterruptedRun(t *testing.T) {
	host, pool, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Record(t.Context(), hostEvent(scope)); err != nil {
		t.Fatal(err)
	}
	if host.RecoverDisconnectedHeads(t.Context()) == nil {
		t.Fatal("활성 소켓이 있는 실행을 복구")
	}
	session.Abort() // 이전 Host 프로세스가 사라진 상태를 합성한다.
	restarted, err := New(pool, host.epoch, host.keyRef, host.key, host.root)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.CheckRestoredHeads(t.Context()) == nil {
		t.Fatal("명시적 검증 전 open head를 수용")
	}
	if err := restarted.RecoverDisconnectedHeads(t.Context()); err != nil {
		t.Fatal("검증된 중단 회차 복구 실패", err)
	}
	if err := restarted.CheckRestoredHeads(t.Context()); err != nil {
		t.Fatal("복구된 저널 검증 실패", err)
	}
	next := scope
	next.RunRef = "run-after-host-restart"
	later, err := restarted.Open(t.Context(), next, "revision-host")
	if err != nil {
		t.Fatal("재기동 뒤 다음 회차 거부", err)
	}
	if err := later.Seal(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestHostSealRacesNewSendIntent(t *testing.T) {
	host, _, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	intent, _ := hostSentEvents(scope)
	start := make(chan struct{})
	recorded, sealed := make(chan error, 1), make(chan error, 1)
	go func() { <-start; recorded <- session.Record(t.Context(), intent) }()
	go func() { <-start; sealed <- session.Seal(t.Context()) }()
	close(start)
	recordErr, sealErr := <-recorded, <-sealed
	if recordErr == nil && sealErr == nil {
		t.Fatal("미완료 송신과 정상 봉인이 모두 성공")
	}
	if recordErr != nil && sealErr != nil {
		t.Fatal("정상 경쟁의 두 경로가 모두 실패", recordErr, sealErr)
	}
	if recordErr == nil && sealErr != nil {
		if host.CheckRestoredHeads(t.Context()) == nil {
			t.Fatal("미완료 송신을 봉인")
		}
	}
	session.Abort()
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

func TestHostConfirmedPreparationFailureSealsBeforeNextRun(t *testing.T) {
	host, _, scope := fixture(t)
	session, err := host.Open(t.Context(), scope, "revision-host")
	if err != nil {
		t.Fatal(err)
	}
	// 준비 호출은 정책에 의해 송신 전에 거부됐다. 결과 기록만으로는
	// 실행 종료가 확인되지 않으므로 그 사이에는 새 회차를 열 수 없다.
	if err := session.Record(t.Context(), hostEvent(scope)); err != nil {
		t.Fatal(err)
	}
	next := scope
	next.RunRef = "run-after-preparation-failure"
	if _, err := host.Open(t.Context(), next, "revision-host"); err == nil {
		t.Fatal("준비 실행 마감 전에 다음 회차가 열림")
	}
	// 실행 소유자가 Stop·drain을 확인한 뒤에만 Seal ACK를 보낸다.
	if err := session.Seal(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := host.CheckRestoredHeads(t.Context()); err != nil {
		t.Fatal("준비 실패의 봉인된 저널을 검증하지 못함", err)
	}
	later, err := host.Open(t.Context(), next, "revision-host")
	if err != nil {
		t.Fatal("마감이 확인된 준비 실패 뒤 다음 회차가 막힘", err)
	}
	if err := later.Seal(t.Context()); err != nil {
		t.Fatal("다음 회차 마감 실패", err)
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
