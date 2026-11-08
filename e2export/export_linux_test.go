//go:build linux

package e2export

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
)

type publisherFixture struct {
	publisher *Publisher
	cfg       Config
	target    *httptest.Server
	received  atomic.Int32
}

func newPublisherFixture(t *testing.T) *publisherFixture {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("비특권 Linux 격리 시험")
	}
	probe := exec.CommandContext(t.Context(), "unshare", "--user", "--map-root-user", "--pid", "--fork", "--mount", "--net", "--ipc", "--uts", "true")
	if output, err := probe.CombinedOutput(); err != nil {
		if bytes.Contains(output, []byte("Operation not permitted")) {
			t.Skip("사용자 namespace 환경 차단")
		}
		t.Fatal("namespace 사전 검사 실패")
	}
	base, err := os.MkdirTemp("", "e2xp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if os.RemoveAll(base) != nil {
			t.Error("임시 fixture 정리 실패")
		}
	})
	for _, name := range []string{"runtime", "audit", "export"} {
		if os.Mkdir(filepath.Join(base, name), 0700) != nil {
			t.Fatal("시험 디렉터리 생성 실패")
		}
	}
	helper := filepath.Join(base, "e2-bash-sandbox")
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	build := exec.CommandContext(t.Context(), goBinary, "build", "-p=2", "-buildvcs=false", "-o", helper, "../cmd/e2-bash-sandbox")
	build.Env = append(os.Environ(), "GOMAXPROCS=2", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("격리 helper 빌드 실패: %s", output)
	}
	if os.Chmod(helper, 0700) != nil {
		t.Fatal("helper 보호 실패")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "publisher fixture"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(base, "ca.pem")
	if os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600) != nil {
		t.Fatal("CA fixture 저장 실패")
	}
	fixture := &publisherFixture{}
	fixture.target = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.received.Add(1)
		_, _ = w.Write([]byte("fixture"))
	}))
	t.Cleanup(fixture.target.Close)
	parsed, _ := url.Parse(fixture.target.URL)
	ledger, _ := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 4})
	grant := budget.Key{TaskRef: "17", BudgetRef: "budget_one"}
	binding := budget.Binding{PolicyRef: "policy_one", ResetRef: "reset_one"}
	if ledger.Register(budget.Grant{Key: grant, Binding: binding, MaxRequests: 4, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute)}, now) != nil {
		t.Fatal("합성 예산 fixture 실패")
	}
	auditKey := bytes.Repeat([]byte{7}, 32)
	journal, err := e2proxy.CreateJournal(filepath.Join(base, "audit"), "events.jsonl", auditKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	execution, err := e2proxy.NewExecution(t.Context(), e2proxy.Config{ApprovalMode: e2proxy.ApprovalPreauthorizedScope, HeaderMode: e2proxy.HeaderPreserve, Scope: transmit.Scope{TaskRef: "17", RunRef: "run_one", CaseRef: "case_one", ResetRef: "reset_one", PolicyRef: "policy_one"}, IntentID: 1, OwnerRef: "owner_one", PolicyRevision: "revision_one", AccountRef: "account_one", SessionRef: "session_one", ExpiresAt: now.Add(time.Minute), Recorder: journal, Rules: []e2proxy.Rule{{Ref: "rule_one", Origin: parsed.Scheme + "://" + parsed.Host, ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/allowed", PathScopeRef: "path_one", Methods: []string{"GET"}}}, Budget: transmit.CumulativeBudget{Ledger: ledger, Key: grant, Binding: binding}, Limits: e2proxy.Limits{MaxAttempts: 10, MaxCalls: 4, MaxConcurrent: 1, MaxBodyBytes: 1024, RequestTimeout: time.Second, CallTimeout: 5 * time.Second, MaxDenials: 4, MaxTransient: 4, MaxUnknown: 4}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := e2runner.New(e2runner.Config{Execution: execution, Helper: helper, RootDir: filepath.Join(base, "runtime"), CACert: ca, Proxy: e2proxy.ProxyConfig{CA: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, HeaderTimeout: time.Second, RequestTimeout: 2 * time.Second, MaxHeaderBytes: 8192, MaxConnections: 2}, CommandTimeout: 5 * time.Second, DrainTimeout: 5 * time.Second, OutputLimit: 1024})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if runner.Close(ctx) != nil || execution.Stop(ctx) != nil {
			t.Error("실행 fixture 마감 실패")
		}
	})
	fixture.cfg = Config{Execution: execution, Runner: runner, Journal: journal, JournalDirectory: filepath.Join(base, "audit"), JournalName: "events.jsonl", OutputDirectory: filepath.Join(base, "export"), AuditKey: auditKey, ExecutionRef: "execution_one", EvalRunID: "eval_one", ReservationRef: "reservation_one", ModelTag: "model_one", ResetGeneration: "reset_one", EngineTaskID: "17", PolicyFingerprintSHA256: strings.Repeat("a", 64)}
	fixture.publisher, err = New(fixture.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fixture.publisher.Dispose)
	return fixture
}
func (f *publisherFixture) run(t *testing.T) e2runner.Result {
	t.Helper()
	result, err := f.cfg.Runner.RunForIntent(t.Context(), 1, "curl --silent --show-error --max-time 2 '"+f.target.URL+"/allowed' >/dev/null")
	if err != nil || !result.Trusted.Final || !result.Trusted.ProcessDrained || f.received.Load() != 1 {
		t.Fatalf("실제 격리 요청 실패: final=%t received=%d", result.Trusted.Final, f.received.Load())
	}
	return result
}
func TestPublisherActualClosureAndWire(t *testing.T) {
	f := newPublisherFixture(t)
	result := f.run(t)
	if f.publisher.ObserveResult(t.Context(), result) != nil {
		t.Fatal("실제 종료 관찰 거부")
	}
	published, err := f.publisher.Publish(t.Context(), "close_one")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(f.cfg.OutputDirectory, ManifestFile))
	if err != nil || digest(data) != published.ManifestSHA256 {
		t.Fatal("manifest 게시 결속 실패")
	}
	var manifest Manifest
	if json.Unmarshal(data, &manifest) != nil || manifest.State != "complete" || manifest.Scope.TaskRef != "17" || manifest.Scope.RunRef == manifest.EvalRunID || manifest.CloseRequestRef != "close_one" {
		t.Fatal("manifest 문맥 실패")
	}
	attempts, _ := os.ReadFile(filepath.Join(f.cfg.OutputDirectory, AttemptsFile))
	journal, _ := os.ReadFile(filepath.Join(f.cfg.OutputDirectory, JournalFile))
	checkpoint, _ := os.ReadFile(filepath.Join(f.cfg.OutputDirectory, CheckpointFile))
	if digest(attempts) != manifest.AttemptsSHA256 || digest(checkpoint) != manifest.AuditCheckpointSHA256 {
		t.Fatal("투영/마감 digest 실패")
	}
	if summary, err := e2proxy.VerifyJournal(t.Context(), bytes.NewReader(journal), f.cfg.AuditKey, published.Checkpoint); err != nil || summary.Events != manifest.Rows {
		t.Fatal("실제 감사 검증 실패")
	}
	projection, rows, _, err := project(journal)
	if err != nil || !bytes.Equal(projection, attempts) || rows != manifest.Rows {
		t.Fatal("감사 투영 완전성 실패")
	}
	second, err := f.publisher.Publish(t.Context(), "close_one")
	if err != nil || second != published {
		t.Fatal("게시 멱등 실패")
	}
	if _, err = f.publisher.Publish(t.Context(), "close_other"); !errors.Is(err, ErrConfig) {
		t.Fatal("다른 종료 참조 수용")
	}
	f.publisher.Dispose()
	second, err = f.publisher.Publish(t.Context(), "close_one")
	if err != nil || second != published {
		t.Fatal("정리 뒤 고정 결과 소실")
	}
}
func TestPublisherRefusesMissingTrustedResult(t *testing.T) {
	f := newPublisherFixture(t)
	_ = f.run(t)
	if _, err := f.publisher.Publish(t.Context(), "close_one"); !errors.Is(err, ErrDrain) {
		t.Fatalf("종료 관찰 누락 수용: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.cfg.OutputDirectory, ManifestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("미검증 manifest 게시")
	}
}
func TestPublisherRefusesAlteredJournal(t *testing.T) {
	f := newPublisherFixture(t)
	result := f.run(t)
	if f.publisher.ObserveResult(t.Context(), result) != nil {
		t.Fatal("종료 관찰 실패")
	}
	path := filepath.Join(f.cfg.JournalDirectory, f.cfg.JournalName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[0] = '['
	if os.WriteFile(path, data, 0600) != nil {
		t.Fatal("변이 fixture 실패")
	}
	if _, err = f.publisher.Publish(t.Context(), "close_one"); !errors.Is(err, ErrAudit) {
		t.Fatalf("저널 변조 수용: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.cfg.OutputDirectory, ManifestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("변조된 manifest 게시")
	}
}
