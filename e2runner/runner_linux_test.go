//go:build linux

package e2runner

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
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
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
)

func runnerTestDirectory(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "e2r-test-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error("own fixture directory cleanup failed")
		}
	})
	return dir
}

func runnerTestHelper(t *testing.T, dir string) string {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("requires an unprivileged Linux host identity")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	// A denied namespace creation is an environment SKIP. Once this succeeds,
	// helper setup or positive-delivery failures remain test failures.
	probe := exec.CommandContext(ctx, "unshare", "--user", "--map-root-user", "--pid", "--fork", "--mount", "--net", "--ipc", "--uts", "true")
	out, err := probe.CombinedOutput()
	if err != nil {
		if bytes.Contains(out, []byte("Operation not permitted")) {
			t.Skip("OS denied required unprivileged namespaces")
		}
		t.Fatal("namespace preflight failed")
	}
	helper := filepath.Join(dir, "e2-bash-sandbox")
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	build := exec.CommandContext(ctx, goBinary, "build", "-p", "2", "-buildvcs=false", "-o", helper, "../cmd/e2-bash-sandbox")
	build.Env = append(os.Environ(), "GOMAXPROCS=2", "GOTOOLCHAIN=local", "GOTELEMETRY=off", "GOPROXY=off", "GOSUMDB=off")
	if out, err = build.CombinedOutput(); err != nil {
		t.Fatalf("helper source build failed: %s", string(out))
	}
	if err = os.Chmod(helper, 0700); err != nil {
		t.Fatal(err)
	}
	return helper
}

type runnerFixture struct {
	runner        *Runner
	execution     *e2proxy.Execution
	cfg           Config
	journal       *e2proxy.Journal
	ledger        *budget.Ledger
	budgetKey     budget.Key
	budgetBinding budget.Binding
}

func newRunnerFixture(t *testing.T, base, helper, target string, roots *x509.CertPool) *runnerFixture {
	return newRunnerFixtureWithRecorder(t, base, helper, target, roots, nil, true)
}

func newRunnerFixtureWithRecorder(t *testing.T, base, helper, target string, roots *x509.CertPool, decorate func(e2proxy.Recorder) e2proxy.Recorder, cleanupRunner bool) *runnerFixture {
	t.Helper()
	return newRunnerFixtureConfigured(t, base, helper, target, roots, decorate, cleanupRunner, nil)
}

func newRunnerFixtureConfigured(t *testing.T, base, helper, target string, roots *x509.CertPool, decorate func(e2proxy.Recorder) e2proxy.Recorder, cleanupRunner bool, configure func(*Config)) *runnerFixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(99), Subject: pkix.Name{CommonName: "runner test root"},
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "runtime")
	if err = os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	publicCA := filepath.Join(base, "ca.pem")
	if err = os.WriteFile(publicCA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 30})
	if err != nil {
		t.Fatal(err)
	}
	grantKey := budget.Key{TaskRef: "task_runner", BudgetRef: "budget_runner"}
	binding := budget.Binding{PolicyRef: "policy_runner", ResetRef: "reset_runner"}
	if err = ledger.Register(budget.Grant{Key: grantKey, Binding: binding, MaxRequests: 30, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute)}, now); err != nil {
		t.Fatal(err)
	}
	auditDir := filepath.Join(base, "audit")
	if err = os.Mkdir(auditDir, 0700); err != nil {
		t.Fatal(err)
	}
	auditKey := make([]byte, 32)
	if _, err = rand.Read(auditKey); err != nil {
		t.Fatal(err)
	}
	journal, err := e2proxy.CreateJournal(auditDir, "events.jsonl", auditKey)
	clear(auditKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Error("durable journal close failed")
		}
	})
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	scope := transmit.Scope{TaskRef: grantKey.TaskRef, RunRef: "run_runner", CaseRef: "case_runner", ResetRef: binding.ResetRef, PolicyRef: binding.PolicyRef}
	var recorder e2proxy.Recorder = journal
	if decorate != nil {
		recorder = decorate(recorder)
	}
	execution, err := e2proxy.NewExecution(t.Context(), e2proxy.Config{ApprovalMode: e2proxy.ApprovalPreauthorizedScope,
		HeaderMode: e2proxy.HeaderProtectedSessionProposal,
		Scope:      scope, IntentID: 1, OwnerRef: "ARTEX", PolicyRevision: "revision_runner", AccountRef: "account_runner", SessionRef: "session_runner",
		ExpiresAt: now.Add(time.Minute), RootCAs: roots, Recorder: recorder,
		Rules: []e2proxy.Rule{{Ref: "rule_runner", Origin: u.Scheme + "://" + u.Host,
			ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/allowed", PathScopeRef: "path_runner",
			Methods: []string{"GET"}, CredentialHeaders: http.Header{"Authorization": {"Bearer runner-session-secret"}}}},
		Budget: transmit.CumulativeBudget{Ledger: ledger, Key: grantKey, Binding: binding},
		Limits: e2proxy.Limits{MaxAttempts: 100, MaxCalls: 10, MaxConcurrent: 8, MaxBodyBytes: 8192, RequestTimeout: 3 * time.Second,
			CallTimeout: 20 * time.Second, MaxDenials: 20, MaxTransient: 20, MaxUnknown: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Execution: execution, Helper: helper, RootDir: root, CACert: publicCA,
		Proxy: e2proxy.ProxyConfig{CA: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
			HeaderTimeout: time.Second, RequestTimeout: 4 * time.Second, MaxHeaderBytes: 8192, MaxConnections: 8},
		CommandTimeout: 15 * time.Second, DrainTimeout: 5 * time.Second, OutputLimit: 8192}
	if configure != nil {
		configure(&cfg)
	}
	runner, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cleanupRunner {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := runner.Close(ctx); err != nil {
				t.Error("runner left incomplete resources")
			}
			if err := execution.Stop(ctx); err != nil {
				t.Error("execution stop audit failed")
			}
		})
	}
	return &runnerFixture{runner: runner, execution: execution, cfg: cfg, journal: journal, ledger: ledger, budgetKey: grantKey, budgetBinding: binding}
}

type runnerStageFailureRecorder struct {
	next  e2proxy.Recorder
	stage string
}

func (r runnerStageFailureRecorder) Record(ctx context.Context, event e2proxy.Event) error {
	if event.Stage == r.stage {
		return errors.New("synthetic audit failure")
	}
	return r.next.Record(ctx, event)
}

type runnerStageDelayRecorder struct {
	next  e2proxy.Recorder
	stage string
	delay time.Duration
}

func (r runnerStageDelayRecorder) Record(ctx context.Context, event e2proxy.Event) error {
	if event.Stage == r.stage {
		timer := time.NewTimer(r.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.next.Record(ctx, event)
}

type observedDoneContext struct {
	context.Context
	observed chan struct{}
	resume   <-chan struct{}
}

func (c *observedDoneContext) Done() <-chan struct{} {
	select {
	case c.observed <- struct{}{}:
	default:
	}
	if c.resume != nil {
		<-c.resume
	}
	return c.Context.Done()
}

func waitForRunningCall(t *testing.T, runner *Runner) *runningCall {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		runner.mu.Lock()
		var state *runningCall
		for _, candidate := range runner.active {
			state = candidate
			break
		}
		runner.mu.Unlock()
		if state != nil {
			select {
			case <-state.ready:
				return state
			case <-time.After(time.Millisecond):
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("runner call did not finish initialization")
	return nil
}

func TestRunnerActualHTTPHTTPSAndDenial(t *testing.T) {
	helperBase := runnerTestDirectory(t)
	helper := runnerTestHelper(t, helperBase)
	for _, name := range []string{"http", "https", "policy_denied", "secret_response", "oversized_response"} {
		t.Run(name, func(t *testing.T) {
			base := runnerTestDirectory(t)
			var received atomic.Int64
			target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				if r.Header.Get("Authorization") != "Bearer runner-session-secret" {
					t.Error("trusted session not supplied")
				}
				attemptID := r.Header.Get(e2proxy.AttemptHeader)
				decodedID, decodeErr := hex.DecodeString(attemptID)
				if decodeErr != nil || len(attemptID) != 32 || len(decodedID) != 16 || strings.ToLower(attemptID) != attemptID {
					t.Error("attempt was not bound")
				}
				if name == "secret_response" {
					io.WriteString(w, "target-ok Bearer runner-session-secret")
					return
				}
				if name == "oversized_response" {
					// 본문 상한(8192)을 넘는다. 실제 curl은 잘린 앞부분을 정상 응답으로 받아 0으로 끝나야 한다(C16 결정안 2).
					io.WriteString(w, strings.Repeat("o", 20000))
					return
				}
				io.WriteString(w, "target-ok")
			}))
			var roots *x509.CertPool
			if name == "https" {
				target.StartTLS()
				roots = x509.NewCertPool()
				roots.AddCert(target.Certificate())
			} else {
				target.Start()
			}
			defer target.Close()
			fixture := newRunnerFixture(t, base, helper, target.URL, roots)
			path := "/allowed"
			if name == "policy_denied" {
				path = "/outside"
			}
			result, err := fixture.runner.Run(t.Context(), "curl --silent --show-error --max-time 4 '"+target.URL+path+"'")
			if err != nil {
				t.Fatalf("isolated call failed: %v; classification=%s", err, result.Trusted.Classification)
			}
			if !result.Trusted.Final || !result.Trusted.Call.AuditComplete || !result.Trusted.ProcessBashExited ||
				!result.Trusted.ProcessDrained || !result.Trusted.ProxyDrained {
				t.Fatal("call was not fully observed and finalized")
			}
			if name == "policy_denied" {
				if received.Load() != 0 || result.Trusted.Classification != "policy_not_executed" ||
					result.Trusted.Call.Counts.Denied != 1 || result.Trusted.Call.Counts.ForwardAttempted != 0 {
					t.Fatal("policy refusal became a target result")
				}
			} else if name == "oversized_response" {
				counts := result.Trusted.Call.Counts
				if received.Load() != 1 || result.Trusted.Classification != "completed" || result.Trusted.ExitCode != 0 || result.Trusted.Call.Reason != e2proxy.Withheld ||
					counts.Withheld != 1 || counts.Unknown != 0 || result.ToolOutput != strings.Repeat("o", 8192) {
					t.Fatalf("oversized response was not delivered truncated: class=%s exit=%d reason=%s counts=%+v output=%d",
						result.Trusted.Classification, result.Trusted.ExitCode, result.Trusted.Call.Reason, counts, len(result.ToolOutput))
				}
			} else if name == "secret_response" {
				counts := result.Trusted.Call.Counts
				// 보호 값을 반사한 응답은 통째로 보류한다. 대상이 답했으므로 결과 불명이 아니라 withheld다(C16).
				if received.Load() != 1 || result.Trusted.Classification != "completed" || result.Trusted.Call.Reason != e2proxy.Withheld ||
					counts.Allowed != 1 || counts.ForwardAttempted != 1 || counts.Responses != 1 || counts.Unknown != 0 || counts.Withheld != 1 || counts.Denied != 0 ||
					strings.Contains(result.ToolOutput, "runner-session-secret") || strings.Contains(result.ToolOutput, "target-ok") ||
					strings.Contains(result.ToolOutput, "[REDACTED]") {
					t.Fatal("sensitive response was released, edited into success, or lost its sent/unknown evidence")
				}
			} else {
				if received.Load() != 1 || result.ToolOutput != "target-ok" ||
					strings.Contains(result.ToolOutput, "runner-session-secret") ||
					result.Trusted.Call.Counts.Responses != 1 || result.Trusted.Classification != "completed" {
					t.Fatal("allowed request/response was not delivered unchanged")
				}
			}
			entries, err := os.ReadDir(fixture.cfg.RootDir)
			if err != nil || len(entries) != 0 {
				t.Fatal("finalized call directory was not released")
			}
		})
	}
}

func TestRunnerBashReturnBlocksBackgroundRequest(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	fixture := newRunnerFixture(t, base, helper, target.URL, nil)
	command := "(sleep 1; curl --silent --max-time 2 '" + target.URL + "/allowed') >/tmp/late-output 2>&1 & echo bash-done"
	result, err := fixture.runner.Run(t.Context(), command)
	if err != nil {
		t.Fatal("background call failed before normal Bash exit:", err)
	}
	if !result.Trusted.Final || !result.Trusted.ProcessDrained || !result.Trusted.ProcessBashExited || received.Load() != 0 {
		t.Fatal("Bash return did not fence and drain descendants")
	}
}

func TestRunnerRevocationStopsLiveCallAndNewAdmission(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	var received atomic.Int64
	observed := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		io.WriteString(w, "ok")
		select {
		case observed <- struct{}{}:
		default:
		}
	}))
	defer target.Close()
	fixture := newRunnerFixture(t, base, helper, target.URL, nil)
	done := make(chan Result, 1)
	go func() {
		result, _ := fixture.runner.Run(t.Context(), "curl --silent --max-time 3 '"+target.URL+"/allowed'; sleep 10; curl --silent '"+target.URL+"/allowed'")
		done <- result
	}()
	select {
	case <-observed:
	case <-time.After(8 * time.Second):
		t.Fatal("allowed request never reached target")
	}
	if err := fixture.execution.Revoke(t.Context()); err != nil {
		t.Fatal("revocation audit failed")
	}
	var result Result
	select {
	case result = <-done:
	case <-time.After(7 * time.Second):
		t.Fatal("revoked call did not drain")
	}
	if !result.Trusted.Final || !result.Trusted.ProcessDrained || !result.Trusted.ProxyDrained || received.Load() != 1 {
		t.Fatal("revoked call retained live work or sent again")
	}
	if _, err := fixture.runner.Run(t.Context(), "curl --silent '"+target.URL+"/allowed'"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("revocation was reset by a new call")
	}
	if received.Load() != 1 {
		t.Fatal("new call bypassed revocation")
	}
}

func TestRunnerRejectsChangedPublicTrustAnchor(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("configuration test sent traffic") }))
	defer target.Close()
	fixture := newRunnerFixture(t, base, helper, target.URL, nil)
	original, err := os.ReadFile(fixture.cfg.CACert)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherCA := &x509.Certificate{SerialNumber: big.NewInt(100), Subject: pkix.Name{CommonName: "different runner root"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	otherDER, err := x509.CreateCertificate(rand.Reader, otherCA, otherCA, otherKey.Public(), otherKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(fixture.cfg.Proxy.CA.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	cases := map[string][]byte{
		"different valid certificate":  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: otherDER}),
		"valid private key":            privatePEM,
		"certificate plus private key": append(append([]byte(nil), original...), privatePEM...),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(fixture.cfg.CACert, content, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := New(fixture.cfg); !errors.Is(err, ErrConfig) {
				t.Fatal("changed or private trust material entered the tool")
			}
			if err := os.WriteFile(fixture.cfg.CACert, original, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunnerPublicCACertificateSnapshotExcludesLaterPrivateKey(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	fixture := newRunnerFixture(t, base, helper, target.URL, nil)
	original, err := os.ReadFile(fixture.cfg.CACert)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(fixture.cfg.Proxy.CA.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	combined := append(append([]byte(nil), original...), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})...)
	if err = os.WriteFile(fixture.cfg.CACert, combined, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.runner.Run(t.Context(), "cat /run/e2/ca.pem")
	if err != nil || !strings.Contains(result.ToolOutput, "-----BEGIN CERTIFICATE-----") || strings.Contains(result.ToolOutput, "PRIVATE KEY") {
		t.Fatal("CA path replacement exposed private key material to Bash")
	}
}

func TestRunnerRejectsUnsafeConfigurationBounds(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	fixture := newRunnerFixture(t, base, helper, target.URL, nil)
	config := fixture.cfg
	config.CommandTimeout = maxRunnerCommandTimeout + time.Nanosecond
	if _, err := New(config); !errors.Is(err, ErrConfig) {
		t.Fatal("command timeout upper bound was not enforced")
	}
	config = fixture.cfg
	config.DrainTimeout = maxRunnerDrainTimeout + time.Nanosecond
	if _, err := New(config); !errors.Is(err, ErrConfig) {
		t.Fatal("drain timeout upper bound was not enforced")
	}
	config = fixture.cfg
	config.OutputLimit = maxOutputBytes + 1
	if _, err := New(config); !errors.Is(err, ErrConfig) {
		t.Fatal("output bound was not enforced")
	}
	config = fixture.cfg
	if err := os.Chmod(config.RootDir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(config); !errors.Is(err, ErrConfig) {
		t.Fatal("unprotected root directory was accepted")
	}
	if err := os.Chmod(config.RootDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(config.CACert, 0620); err != nil {
		t.Fatal(err)
	}
	if _, err := New(config); !errors.Is(err, ErrConfig) {
		t.Fatal("group-writable CA file was accepted")
	}
	if err := os.Chmod(config.CACert, 0600); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(base, "root-link")
	if err := os.Symlink(config.RootDir, rootLink); err != nil {
		t.Fatal(err)
	}
	config.RootDir = rootLink
	if _, err := New(config); !errors.Is(err, ErrConfig) {
		t.Fatal("symlinked root directory was accepted")
	}
	config = fixture.cfg
	helperLink := filepath.Join(base, "helper-link")
	if err := os.Symlink(config.Helper, helperLink); err != nil {
		t.Fatal(err)
	}
	config.Helper = helperLink
	if _, err := New(config); !errors.Is(err, ErrConfig) {
		t.Fatal("symlinked helper was accepted")
	}
	config = fixture.cfg
	caLink := filepath.Join(base, "ca-link")
	if err := os.Symlink(config.CACert, caLink); err != nil {
		t.Fatal(err)
	}
	config.CACert = caLink
	if _, err := New(config); !errors.Is(err, ErrConfig) {
		t.Fatal("symlinked CA file was accepted")
	}
	config = fixture.cfg
	original, err := os.ReadFile(config.CACert)
	if err != nil {
		t.Fatal(err)
	}
	oversizedCA := make([]byte, maxCACertificateBytes+1)
	if err := os.WriteFile(config.CACert, oversizedCA, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(config); !errors.Is(err, ErrConfig) {
		t.Fatal("oversized CA input was accepted")
	}
	if err := os.WriteFile(config.CACert, original, 0600); err != nil {
		t.Fatal(err)
	}
	longRoot := filepath.Join(base, strings.Repeat("r", maxUnixSocketPathBytes))
	if err := os.Mkdir(longRoot, 0700); err != nil {
		t.Fatal(err)
	}
	config = fixture.cfg
	config.RootDir = longRoot
	if _, err := New(config); !errors.Is(err, ErrConfig) {
		t.Fatal("Unix socket path bound was not enforced")
	}
}

func TestRunnerRejectsInvalidCommandsBeforeCallAdmission(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	fixture := newRunnerFixture(t, base, helper, target.URL, nil)
	for _, command := range []string{"printf\x00invalid", strings.Repeat("x", maxCommandBytes+1)} {
		if _, err := fixture.runner.Run(t.Context(), command); !errors.Is(err, ErrConfig) {
			t.Fatal("invalid or over-limit command was admitted")
		}
	}
	if len(fixture.execution.Calls()) != 0 {
		t.Fatal("invalid command created an audited call")
	}
}

func TestRunnerClassificationTable(t *testing.T) {
	cases := []struct {
		name    string
		state   *runningCall
		trusted TrustedResult
		want    string
	}{
		{name: "isolation unavailable", state: &runningCall{initFailed: true}, want: "isolation_unavailable"},
		{name: "incomplete", state: &runningCall{}, trusted: TrustedResult{}, want: "result_unknown"},
		{name: "audit unknown", state: &runningCall{}, trusted: TrustedResult{Final: true, Call: e2proxy.CallResult{Counts: e2proxy.Counts{Unknown: 1}}}, want: "result_unknown"},
		{name: "allowed but not forwarded", state: &runningCall{}, trusted: TrustedResult{Final: true, Call: e2proxy.CallResult{Counts: e2proxy.Counts{Allowed: 1}}}, want: ClassificationForwardNotAttempted},
		{name: "approval infrastructure failed", state: &runningCall{}, trusted: TrustedResult{Call: e2proxy.CallResult{Reason: e2proxy.AuditFailed, Counts: e2proxy.Counts{Denied: 1}}}, want: "transient_failure"},
		{name: "execution audit latch", state: &runningCall{}, trusted: TrustedResult{Final: true, Execution: e2proxy.ExecutionView{Reason: e2proxy.AuditFailed}}, want: "transient_failure"},
		{name: "audit failure with unknown", state: &runningCall{}, trusted: TrustedResult{Call: e2proxy.CallResult{Reason: e2proxy.AuditFailed, Counts: e2proxy.Counts{Unknown: 1}}}, want: "result_unknown"},
		{name: "audit failure after send", state: &runningCall{}, trusted: TrustedResult{Call: e2proxy.CallResult{Reason: e2proxy.AuditFailed, Counts: e2proxy.Counts{ForwardAttempted: 1}}}, want: "result_unknown"},
		{name: "policy only", state: &runningCall{}, trusted: TrustedResult{Final: true, Call: e2proxy.CallResult{Counts: e2proxy.Counts{Denied: 1}}}, want: "policy_not_executed"},
		{name: "unsupported only", state: &runningCall{}, trusted: TrustedResult{Final: true, Call: e2proxy.CallResult{Counts: e2proxy.Counts{Unsupported: 1}}}, want: "unsupported"},
		{name: "proxy unavailable", state: &runningCall{termination: "proxy_unavailable"}, trusted: TrustedResult{Final: true}, want: "transient_failure"},
		{name: "timeout", state: &runningCall{termination: "timeout"}, trusted: TrustedResult{Final: true}, want: "transient_failure"},
		{name: "process not exited", state: &runningCall{}, trusted: TrustedResult{Final: true}, want: "terminated"},
		{name: "tool failure", state: &runningCall{}, trusted: TrustedResult{Final: true, ProcessBashExited: true, ExitCode: 7}, want: "tool_failure"},
		{name: "partial policy denial", state: &runningCall{}, trusted: TrustedResult{Final: true, ProcessBashExited: true, Call: e2proxy.CallResult{Counts: e2proxy.Counts{Denied: 1, ForwardAttempted: 1}}}, want: "partial_policy_denial"},
		{name: "partial unsupported", state: &runningCall{}, trusted: TrustedResult{Final: true, ProcessBashExited: true, Call: e2proxy.CallResult{Counts: e2proxy.Counts{Unsupported: 1, ForwardAttempted: 1}}}, want: "partial_unsupported"},
		{name: "completed", state: &runningCall{}, trusted: TrustedResult{Final: true, ProcessBashExited: true, ExitCode: 0, Call: e2proxy.CallResult{Counts: e2proxy.Counts{Allowed: 1, ForwardAttempted: 1, Responses: 1}}}, want: "completed"},
		// C02: 연결을 맺지 못한 송신 실패는 결과 불명이 아니지만 셸이 0으로 끝나도 완료로 올리지 않는다.
		{name: "connection failed after attempt", state: &runningCall{}, trusted: TrustedResult{Final: true, ProcessBashExited: true, ExitCode: 0, Call: e2proxy.CallResult{Counts: e2proxy.Counts{Allowed: 2, ForwardAttempted: 2, Responses: 1, Transient: 1}}}, want: "transient_failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.state.classification(tc.trusted); got != tc.want {
				t.Fatalf("classification=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestRunnerDelayedIntentACKCannotBecomeCompletedWithoutForward(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		io.WriteString(w, "unexpected")
	}))
	defer target.Close()
	fixture := newRunnerFixtureWithRecorder(t, base, helper, target.URL, nil, func(next e2proxy.Recorder) e2proxy.Recorder {
		return runnerStageDelayRecorder{next: next, stage: "intent", delay: 350 * time.Millisecond}
	}, true)
	fixture.runner.cfg.Proxy.HeaderTimeout = 80 * time.Millisecond
	fixture.runner.cfg.Proxy.RequestTimeout = 120 * time.Millisecond
	result, err := fixture.runner.Run(t.Context(), "curl --silent --show-error --max-time 2 '"+target.URL+"/allowed' >/dev/null 2>&1; exit 0")
	counts := result.Trusted.Call.Counts
	if err != nil || !result.Trusted.Final || !result.Trusted.Call.AuditComplete || counts.Allowed != 1 ||
		counts.ForwardAttempted != 0 || received.Load() != 0 || result.Trusted.Classification != ClassificationForwardNotAttempted {
		t.Fatalf("delayed ACK escaped classification: err=%v final=%t allowed=%d forward=%d received=%d class=%s", err, result.Trusted.Final, counts.Allowed, counts.ForwardAttempted, received.Load(), result.Trusted.Classification)
	}
}

func TestRunnerDrainIntentRejectsPreviouslyQueuedSameIntent(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		io.WriteString(w, "ok")
	}))
	defer target.Close()
	fixture := newRunnerFixture(t, base, helper, target.URL, nil)
	firstDone := make(chan Result, 1)
	go func() {
		result, _ := fixture.runner.RunForIntent(t.Context(), 1, "sleep 5")
		firstDone <- result
	}()
	waitForRunningCall(t, fixture.runner)
	// 일시적인 fenced 관찰 대신, drain이 끝난 뒤 이전 세대 대기자를 진행시킨다.
	queuedBase, cancelQueued := context.WithCancel(t.Context())
	resumeQueued := make(chan struct{})
	defer func() {
		cancelQueued()
		select {
		case <-resumeQueued:
		default:
			close(resumeQueued)
		}
	}()
	queuedContext := &observedDoneContext{Context: queuedBase, observed: make(chan struct{}, 1), resume: resumeQueued}
	queuedDone := make(chan error, 1)
	go func() {
		_, err := fixture.runner.RunForIntent(queuedContext, 1, "curl --silent --max-time 2 '"+target.URL+"/allowed'")
		queuedDone <- err
	}()
	select {
	case <-queuedContext.observed:
	case <-time.After(time.Second):
		t.Fatal("second same-intent call did not enter the slot wait")
	}
	drainDone := make(chan error, 1)
	go func() { drainDone <- fixture.runner.DrainIntent(t.Context(), 1) }()
	select {
	case err := <-drainDone:
		if err != nil {
			t.Fatalf("intent drain failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("intent drain did not finish")
	}
	select {
	case first := <-firstDone:
		if !first.Trusted.Final {
			t.Fatal("drained first call did not finalize")
		}
	case <-time.After(time.Second):
		t.Fatal("first call did not finish")
	}
	close(resumeQueued)
	select {
	case err := <-queuedDone:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("pre-drain queued call was not refused: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued call did not return after the drain")
	}
	if received.Load() != 0 || len(fixture.execution.Calls()) != 1 {
		t.Fatal("stale same-intent work reached the target or acquired a call")
	}
}

func TestRunnerProxyDeathIsTransientFailure(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	fixture := newRunnerFixture(t, base, helper, target.URL, nil)
	caller, cancel := context.WithCancel(t.Context())
	done := make(chan Result, 1)
	go func() {
		result, _ := fixture.runner.Run(caller, "sleep 10")
		done <- result
	}()
	state := waitForRunningCall(t, fixture.runner)
	if err := state.listener.Close(); err != nil {
		cancel()
		t.Fatal(err)
	}
	select {
	case result := <-done:
		cancel()
		if !result.Trusted.Final || result.Trusted.Classification != "transient_failure" || result.Trusted.Termination != TerminationProxyUnavailable {
			t.Fatal("proxy listener death was not reported as a transient failure", result.Trusted.Termination)
		}
	case <-time.After(6 * time.Second):
		cancel()
		t.Fatal("runner did not observe proxy listener death")
	}
}

// 명령 시간 상한(호출 문맥 기한)에 닿아 멈춘 명령은 원인을 신뢰 결과에 timeout으로 남긴다(계약 열린 항목 2). 대상 송신이
// 없었으므로 결과 불명이 아니고, 드레인·감사가 끝나 최종이다.
func TestRunnerCommandTimeoutRecordsTermination(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	fixture := newRunnerFixture(t, base, helper, target.URL, nil)
	// 끝까지 실행된 명령에는 종료 원인을 남기지 않는다.
	if result, err := fixture.runner.Run(t.Context(), "true"); err != nil || result.Trusted.Termination != "" {
		t.Fatalf("ordinary command recorded a termination: err=%v termination=%q", err, result.Trusted.Termination)
	}
	caller, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	result, err := fixture.runner.Run(caller, "sleep 10")
	trusted := result.Trusted
	if !errors.Is(err, ErrUnavailable) || trusted.Classification != "transient_failure" || trusted.Termination != TerminationTimeout ||
		!trusted.Final || !trusted.ProcessDrained || !trusted.ProxyDrained || trusted.ProcessBashExited || trusted.Call.Counts.Unknown != 0 {
		t.Fatalf("timed-out command: err=%v classification=%s termination=%q final=%v drained=%v/%v bash=%v counts=%+v", err, trusted.Classification, trusted.Termination, trusted.Final, trusted.ProcessDrained, trusted.ProxyDrained, trusted.ProcessBashExited, trusted.Call.Counts)
	}
}

func TestRunnerServeErrorRetainsProtectedState(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	fixture := newRunnerFixtureWithRecorder(t, base, helper, target.URL, nil, nil, false)
	caller, cancel := context.WithCancel(t.Context())
	done := make(chan Result, 1)
	go func() {
		result, _ := fixture.runner.Run(caller, "sleep 10")
		done <- result
	}()
	state := waitForRunningCall(t, fixture.runner)
	t.Cleanup(func() {
		if state.process != nil {
			if err := state.process.Close(); err != nil {
				t.Error("test process cleanup failed")
			}
		}
	})
	if err := state.listener.SetDeadline(time.Now().Add(-time.Second)); err != nil {
		cancel()
		t.Fatal(err)
	}
	select {
	case result := <-done:
		cancel()
		entries, readErr := os.ReadDir(fixture.cfg.RootDir)
		if !result.Trusted.ProcessDrained || result.Trusted.ProxyDrained || result.Trusted.Final ||
			readErr != nil || len(entries) != 1 || len(fixture.runner.callGate) != 1 {
			t.Fatal("unexpected ServeUnix error released unverified proxy state")
		}
		if state.serveErr == nil {
			t.Fatal("ServeUnix error was discarded")
		}
	case <-time.After(6 * time.Second):
		cancel()
		t.Fatal("runner did not observe ServeUnix error")
	}
}

func TestRunnerPeerVerificationRejectsHostProcess(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		io.WriteString(w, "unexpected")
	}))
	defer target.Close()
	fixture := newRunnerFixture(t, base, helper, target.URL, nil)
	caller, cancel := context.WithCancel(t.Context())
	done := make(chan Result, 1)
	go func() {
		result, _ := fixture.runner.Run(caller, "sleep 10")
		done <- result
	}()
	state := waitForRunningCall(t, fixture.runner)
	conn, err := net.DialTimeout("unix", state.listener.Addr().String(), time.Second)
	if err != nil {
		cancel()
		t.Fatal("could not connect to the isolated proxy socket")
	}
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_, _ = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target.URL+"/allowed", strings.TrimPrefix(target.URL, "http://"))
	_ = conn.Close()
	time.Sleep(250 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("runner did not stop after caller cancellation")
	}
	if received.Load() != 0 {
		t.Fatal("host process bypassed the sandbox peer verifier")
	}
}

func TestRunnerCanceledCallerStillFinalizesAudit(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	fixture := newRunnerFixture(t, base, helper, target.URL, nil)
	caller, cancel := context.WithCancel(t.Context())
	done := make(chan Result, 1)
	go func() {
		result, _ := fixture.runner.Run(caller, "sleep 10")
		done <- result
	}()
	waitForRunningCall(t, fixture.runner)
	cancel()
	select {
	case result := <-done:
		if !result.Trusted.Final || !result.Trusted.Call.AuditComplete || result.Trusted.Execution.Reason == e2proxy.AuditFailed {
			t.Fatal("caller cancellation poisoned final audit or retained live resources")
		}
	case <-time.After(6 * time.Second):
		t.Fatal("canceled caller did not complete cleanup")
	}
}

func TestRunnerAuditFailureRetainsSlotAndRoot(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	fixture := newRunnerFixtureWithRecorder(t, base, helper, target.URL, nil, func(next e2proxy.Recorder) e2proxy.Recorder {
		return runnerStageFailureRecorder{next: next, stage: "call_closed"}
	}, false)
	result, err := fixture.runner.Run(t.Context(), "printf ok")
	entries, readErr := os.ReadDir(fixture.cfg.RootDir)
	if !errors.Is(err, ErrIncomplete) || readErr != nil || len(entries) != 1 || len(fixture.runner.callGate) != 1 ||
		result.Trusted.Final || result.Trusted.Call.AuditComplete || result.Trusted.Execution.Reason != e2proxy.AuditFailed {
		t.Fatal("audit failure did not fail closed with the call slot and root retained")
	}
	fixture.runner.mu.Lock()
	var state *runningCall
	for _, candidate := range fixture.runner.active {
		state = candidate
	}
	fixture.runner.mu.Unlock()
	if state != nil {
		t.Cleanup(func() {
			if state.process != nil {
				if err := state.process.Close(); err != nil {
					t.Error("test process cleanup failed")
				}
			}
		})
	}
	if state == nil || !result.Trusted.ProcessDrained {
		t.Fatal("audit failure did not leave a drained call for the owner to retain")
	}
}

func TestRunnerSerializesBashWithoutResettingBudget(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	var received atomic.Int64
	entered := make(chan struct{}, 1)
	release := make(chan struct{})

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		io.WriteString(w, "serial-ok")
	}))
	defer target.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	fixture := newRunnerFixture(t, base, helper, target.URL, nil)
	first := make(chan Result, 1)
	go func() {
		result, _ := fixture.runner.Run(t.Context(), "curl --silent --max-time 4 '"+target.URL+"/allowed'")
		first <- result
	}()
	select {
	case <-entered:
	case <-time.After(8 * time.Second):
		t.Fatal("first allowed call did not reach target")
	}
	queueCtx, queueCancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	queued, err := fixture.runner.Run(queueCtx, "curl --silent --max-time 4 '"+target.URL+"/allowed'")
	queueCancel()
	if !errors.Is(err, ErrUnavailable) || queued.Trusted.Call.CallRef != "" || len(fixture.execution.Calls()) != 1 || received.Load() != 1 {
		t.Fatal("queued cancellation admitted a second Bash call")
	}
	close(release)
	var firstResult Result
	select {
	case firstResult = <-first:
	case <-time.After(7 * time.Second):
		t.Fatal("first call did not finalize")
	}
	if !firstResult.Trusted.Final {
		t.Fatal("serial slot was released before finalization")
	}
	second, err := fixture.runner.Run(t.Context(), "curl --silent --max-time 4 '"+target.URL+"/allowed'")
	if err != nil || !second.Trusted.Final || received.Load() != 2 || len(fixture.execution.Calls()) != 2 {
		t.Fatal("next Bash call did not use the finalized serial slot")
	}
	remaining, err := fixture.ledger.Remaining(fixture.budgetKey, fixture.budgetBinding, time.Now())
	if err != nil || remaining != 28 {
		t.Fatal("new calls or queued cancellation reset the shared budget")
	}
}

func TestRunnerRepresentationsExcludePrivateState(t *testing.T) {
	cfg := Config{Helper: "private-marker", RootDir: "private-marker", CACert: "private-marker",
		Proxy: e2proxy.ProxyConfig{CA: tls.Certificate{PrivateKey: "private-marker"}}}
	runner := &Runner{runnerState: &runnerState{cfg: cfg}}
	formatted := fmt.Sprintf("%v %#v %+v %#v %v %#v", runner, runner, *runner, *runner, cfg, cfg)
	if strings.Contains(formatted, "private-marker") || !strings.Contains(formatted, "[protected]") {
		t.Fatal("runner formatting exposed private configuration")
	}
	encoded, err := json.Marshal(runner)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("runner serialization exposed private state")
	}
}

func TestRunnerRegisteredIntentsReuseApprovalAndDrain(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); io.WriteString(w, "approved-intent") }))
	defer target.Close()
	fixture := newRunnerFixture(t, base, helper, target.URL, nil)
	command := "curl --silent --max-time 4 '" + target.URL + "/allowed'"
	if _, err := fixture.runner.RunForIntent(t.Context(), 2, command); !errors.Is(err, ErrUnavailable) || len(fixture.execution.Calls()) != 0 {
		t.Fatal("unregistered intent acquired a call")
	}
	if err := fixture.execution.RegisterIntent(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	first, err := fixture.runner.RunForIntent(t.Context(), 2, command)
	if err != nil || !first.Trusted.Final || first.Trusted.Call.IntentID != 2 || first.Trusted.Execution.IntentID != 2 {
		t.Fatal("actual registered intent was not preserved")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	if err = fixture.runner.Drain(ctx); err != nil {
		cancel()
		t.Fatal("reusable drain failed")
	}
	cancel()
	if err = fixture.execution.RegisterIntent(t.Context(), 3); err != nil {
		t.Fatal(err)
	}
	second, err := fixture.runner.RunForIntent(t.Context(), 3, command)
	if err != nil || !second.Trusted.Final || second.Trusted.Call.IntentID != 3 || second.Trusted.Execution.IntentID != 3 ||
		first.Trusted.Call.CallRef == second.Trusted.Call.CallRef {
		t.Fatal("next actual intent could not reuse the same runner")
	}
	anchor, err := fixture.runner.Run(t.Context(), "printf anchor")
	if err != nil || anchor.Trusted.Call.IntentID != 1 {
		t.Fatal("legacy Run did not delegate to the approval anchor")
	}
	remaining, err := fixture.ledger.Remaining(fixture.budgetKey, fixture.budgetBinding, time.Now())
	if err != nil || remaining != 28 || received.Load() != 2 || len(fixture.execution.Snapshot().IntentIDs) != 3 {
		t.Fatal("intent or drain reissued the HTTP approval")
	}
	ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
	if err = fixture.runner.Close(ctx); err != nil {
		cancel()
		t.Fatal("permanent close failed")
	}
	cancel()
	if _, err = fixture.runner.RunForIntent(t.Context(), 3, command); !errors.Is(err, ErrUnavailable) {
		t.Fatal("permanent close reopened admission")
	}
	if err = fixture.runner.Drain(t.Context()); err != nil {
		t.Fatal("empty drain failed")
	}
	if _, err = fixture.runner.RunForIntent(t.Context(), 3, command); !errors.Is(err, ErrUnavailable) {
		t.Fatal("drain reopened a permanently closed runner")
	}
}

func TestRunnerDrainStopsCurrentIntentAndAllowsNext(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); io.WriteString(w, "ok") }))
	defer target.Close()
	fixture := newRunnerFixture(t, base, helper, target.URL, nil)
	for _, id := range []int64{2, 3} {
		if err := fixture.execution.RegisterIntent(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan Result, 1)
	go func() {
		result, _ := fixture.runner.RunForIntent(t.Context(), 2, "curl --silent --max-time 3 '"+target.URL+"/allowed'; sleep 10; curl --silent '"+target.URL+"/allowed'")
		done <- result
	}()
	deadline := time.Now().Add(8 * time.Second)
	for fixture.execution.Snapshot().Counts.Responses != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if fixture.execution.Snapshot().Counts.Responses != 1 {
		t.Fatal("first intent did not receive its allowed response")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	if err := fixture.runner.Drain(ctx); err != nil {
		cancel()
		t.Fatal("active intent drain failed")
	}
	cancel()
	var first Result
	select {
	case first = <-done:
	case <-time.After(time.Second):
		t.Fatal("drained call did not return")
	}
	if !first.Trusted.Final || first.Trusted.Call.IntentID != 2 || !first.Trusted.ProcessDrained || !first.Trusted.ProxyDrained || received.Load() != 1 {
		t.Fatal("drain did not close actual work before releasing the slot")
	}
	next, err := fixture.runner.RunForIntent(t.Context(), 3, "curl --silent --max-time 3 '"+target.URL+"/allowed'")
	if err != nil || !next.Trusted.Final || next.Trusted.Call.IntentID != 3 || received.Load() != 2 {
		t.Fatal("temporary drain permanently closed the next approved intent")
	}
	if fixture.execution.Snapshot().State != "active" {
		t.Fatal("drain replaced approval state with a new execution")
	}
	remaining, err := fixture.execution.BudgetRemaining()
	if err != nil || remaining != 28 {
		t.Fatal("drain or new intent reset the shared budget")
	}
}

func TestRunnerIncompleteDrainRetainsSlotAndProtectedRoot(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); io.WriteString(w, "ok") }))
	defer target.Close()
	fixture := newRunnerFixture(t, base, helper, target.URL, nil)
	for _, id := range []int64{2, 3} {
		if err := fixture.execution.RegisterIntent(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	returned := make(chan Result, 1)
	go func() {
		result, _ := fixture.runner.RunForIntent(t.Context(), 2, "curl --silent --max-time 3 '"+target.URL+"/allowed'; sleep 10")
		returned <- result
	}()
	deadline := time.Now().Add(8 * time.Second)
	for fixture.execution.Snapshot().Counts.Responses != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if fixture.execution.Snapshot().Counts.Responses != 1 {
		t.Fatal("active call did not complete an allowed request")
	}
	fixture.runner.mu.Lock()
	var state *runningCall
	for _, candidate := range fixture.runner.active {
		state = candidate
	}
	fixture.runner.mu.Unlock()
	if state == nil {
		t.Fatal("active call disappeared")
	}
	// Hold the real finalization seam while the actual namespace is fenced.
	// This is an injected cleanup delay, not a successful audit/authority stub.
	state.cleanupGate <- struct{}{}
	held := true
	defer func() {
		if held {
			<-state.cleanupGate
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	err := fixture.runner.Drain(ctx)
	cancel()
	if !errors.Is(err, ErrIncomplete) || len(fixture.runner.callGate) != 1 {
		t.Fatal("incomplete drain released the shared call slot")
	}
	entries, err := os.ReadDir(fixture.cfg.RootDir)
	if err != nil || len(entries) != 1 {
		t.Fatal("incomplete finalization discarded its protected call root")
	}
	ctx, cancel = context.WithTimeout(t.Context(), 80*time.Millisecond)
	queued, err := fixture.runner.RunForIntent(ctx, 3, "curl --silent '"+target.URL+"/allowed'")
	cancel()
	if !errors.Is(err, ErrUnavailable) || queued.Trusted.Call.CallRef != "" || len(fixture.execution.Calls()) != 1 || received.Load() != 1 {
		t.Fatal("unfinished drain admitted a new intent")
	}
	<-state.cleanupGate
	held = false
	ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
	if err = fixture.runner.Drain(ctx); err != nil {
		cancel()
		t.Fatal("drain ownership could not be resumed")
	}
	cancel()
	select {
	case result := <-returned:
		if !result.Trusted.Final {
			t.Fatal("retained call did not finalize")
		}
	case <-time.After(time.Second):
		t.Fatal("retained call never returned")
	}
	next, err := fixture.runner.RunForIntent(t.Context(), 3, "curl --silent --max-time 3 '"+target.URL+"/allowed'")
	if err != nil || !next.Trusted.Final || received.Load() != 2 {
		t.Fatal("completed drain did not reopen approved admission")
	}
	remaining, err := fixture.execution.BudgetRemaining()
	if err != nil || remaining != 28 {
		t.Fatal("incomplete drain recovery recharged approval")
	}
}

func TestRunnerDrainIntentDoesNotStopQueuedOrActiveOtherIntent(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); io.WriteString(w, "ok") }))
	defer target.Close()
	fixture := newRunnerFixture(t, base, helper, target.URL, nil)
	for _, id := range []int64{2, 3} {
		if err := fixture.execution.RegisterIntent(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	firstDone := make(chan Result, 1)
	otherDone := make(chan Result, 1)
	command := "curl --silent --max-time 3 '" + target.URL + "/allowed'; sleep 10"
	go func() { result, _ := fixture.runner.RunForIntent(t.Context(), 2, command); firstDone <- result }()
	deadline := time.Now().Add(8 * time.Second)
	for fixture.execution.Snapshot().Counts.Responses < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if fixture.execution.Snapshot().Counts.Responses != 1 {
		t.Fatal("first actual intent never completed its request")
	}
	go func() { result, _ := fixture.runner.RunForIntent(t.Context(), 3, command); otherDone <- result }()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	if err := fixture.runner.DrainIntent(ctx, 2); err != nil {
		cancel()
		t.Fatal("first intent drain failed")
	}
	cancel()
	select {
	case result := <-firstDone:
		if !result.Trusted.Final || result.Trusted.Call.IntentID != 2 {
			t.Fatal("first intent did not finalize")
		}
	case <-time.After(time.Second):
		t.Fatal("first intent still running")
	}
	deadline = time.Now().Add(8 * time.Second)
	for fixture.execution.Snapshot().Counts.Responses < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if fixture.execution.Snapshot().Counts.Responses != 2 || received.Load() != 2 {
		t.Fatal("draining the first intent canceled another intent's queued call")
	}
	// Finishing intent 2 again while intent 3 is now active must remain scoped.
	ctx, cancel = context.WithTimeout(t.Context(), time.Second)
	if err := fixture.runner.DrainIntent(ctx, 2); err != nil {
		cancel()
		t.Fatal("empty scoped drain failed")
	}
	cancel()
	select {
	case <-otherDone:
		t.Fatal("scoped drain stopped the other active Bash")
	default:
	}
	fixture.runner.mu.Lock()
	actualOtherOpen := false
	for _, state := range fixture.runner.active {
		if state.intentID == 3 {
			actualOtherOpen = state.call.Result().State == "open"
		}
	}
	fixture.runner.mu.Unlock()
	if !actualOtherOpen {
		t.Fatal("other intent's actual call was fenced")
	}
	ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
	if err := fixture.runner.DrainIntent(ctx, 3); err != nil {
		cancel()
		t.Fatal("other intent could not drain its own work")
	}
	cancel()
	select {
	case result := <-otherDone:
		if !result.Trusted.Final || result.Trusted.Call.IntentID != 3 {
			t.Fatal("other actual intent did not finalize")
		}
	case <-time.After(time.Second):
		t.Fatal("other intent did not return")
	}
	remaining, err := fixture.execution.BudgetRemaining()
	if err != nil || remaining != 28 || fixture.execution.Snapshot().State != "active" {
		t.Fatal("per-intent drain reset or revoked the shared approval")
	}
}
