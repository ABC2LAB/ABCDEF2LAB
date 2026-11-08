//go:build linux

package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/transmit"
	"golang.org/x/sys/unix"
)

func e2HostAuthorityFixture(t *testing.T) (FirstDiagnosisHTTPHostConfig, FirstDiagnosisHTTPApproval) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("M01 requires an actual non-root operator UID")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := FirstDiagnosisHTTPHostConfig{PolicyFile: filepath.Join(dir, "policy.json"), AuthorityFile: filepath.Join(dir, "authority.json"), AuthorityRoot: dir}
	policy := []byte("synthetic policy bytes; authority-only fixture")
	digest := sha256.Sum256(policy)
	grant := FirstDiagnosisHTTPApproval{Format: FirstDiagnosisHTTPAuthorityFormat, ApprovalRef: "synthetic-operator-approval", OperatorUID: uint32(os.Geteuid()), TaskID: 11, ExplorationID: 27, PolicySHA256: hex.EncodeToString(digest[:]), ValidFrom: time.Now().Add(-time.Minute), ValidUntil: time.Now().Add(time.Minute)}
	if err := os.WriteFile(cfg.PolicyFile, policy, 0600); err != nil {
		t.Fatal(err)
	}
	e2HostWriteApproval(t, cfg, grant)
	return cfg, grant
}
func e2HostWriteApproval(t *testing.T, cfg FirstDiagnosisHTTPHostConfig, grant FirstDiagnosisHTTPApproval) {
	t.Helper()
	raw, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.AuthorityFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestE2HostProviderAuthorityHeldAndSingleStart(t *testing.T) {
	cfg, grant := e2HostAuthorityFixture(t)
	auth, err := newFirstDiagnosisHTTPAuthority(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = auth.close() })
	actual := auth.(*firstHTTPM01Authority)
	if auth.CheckRoot(t.Context()) != nil || auth.approval().TaskID != grant.TaskID || auth.approval().ExplorationID != grant.ExplorationID {
		t.Fatal("approved actual identifiers were lost")
	}
	if other, err := newFirstDiagnosisHTTPAuthority(t.Context(), cfg); err == nil || other != nil {
		t.Fatal("concurrent host reused the grant")
	}
	auth.revoke()
	if auth.Context().Err() == nil || auth.CheckRoot(t.Context()) == nil {
		t.Fatal("revoked authority remained usable")
	}
	var st unix.Stat_t
	if unix.Fstat(int(actual.root.Fd()), &st) != nil || unix.Fstat(int(actual.lock.Fd()), &st) != nil {
		t.Fatal("revoke released live FD ownership")
	}
	if err = auth.close(); err != nil {
		t.Fatal(err)
	}
	if err = auth.close(); err != nil {
		t.Fatal("close was not idempotent")
	}
	if _, err = os.Stat(filepath.Join(cfg.AuthorityRoot, ".first-http.started")); err != nil {
		t.Fatal("one-use marker disappeared")
	}
	if other, err := newFirstDiagnosisHTTPAuthority(t.Context(), cfg); err == nil || other != nil {
		t.Fatal("restart reused consumed authority")
	}
}
func TestE2HostProviderRejectsUnprotectedOrUnboundAuthority(t *testing.T) {
	modes := []string{"wrong-uid", "zero-task", "zero-exploration", "future", "expired", "overlong", "digest", "format", "unknown", "duplicate", "authority-mode", "policy-mode", "root-mode", "hardlink", "symlink", "root-alias", "marker", "lock-mode"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			cfg, grant := e2HostAuthorityFixture(t)
			switch mode {
			case "wrong-uid":
				grant.OperatorUID++
			case "zero-task":
				grant.TaskID = 0
			case "zero-exploration":
				grant.ExplorationID = 0
			case "future":
				grant.ValidFrom = time.Now().Add(time.Minute)
				grant.ValidUntil = grant.ValidFrom.Add(time.Minute)
			case "expired":
				grant.ValidFrom = time.Now().Add(-2 * time.Minute)
				grant.ValidUntil = time.Now().Add(-time.Minute)
			case "overlong":
				grant.ValidUntil = grant.ValidFrom.Add(15*time.Minute + time.Second)
			case "digest":
				grant.PolicySHA256 = strings.Repeat("0", 64)
			case "format":
				grant.Format = "unadopted-format"
			}
			e2HostWriteApproval(t, cfg, grant)
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "unknown", "duplicate":
				raw, err := os.ReadFile(cfg.AuthorityFile)
				must(err)
				suffix := `,"unknown":true}`
				if mode == "duplicate" {
					suffix = `,"task_id":11}`
				}
				must(os.WriteFile(cfg.AuthorityFile, append(raw[:len(raw)-1], suffix...), 0600))
			case "authority-mode":
				must(os.Chmod(cfg.AuthorityFile, 0640))
			case "policy-mode":
				must(os.Chmod(cfg.PolicyFile, 0644))
			case "root-mode":
				must(os.Chmod(cfg.AuthorityRoot, 0750))
			case "hardlink":
				must(os.Link(cfg.AuthorityFile, filepath.Join(cfg.AuthorityRoot, "second-link")))
			case "symlink":
				must(os.Rename(cfg.AuthorityFile, cfg.AuthorityFile+".real"))
				must(os.Symlink(cfg.AuthorityFile+".real", cfg.AuthorityFile))
			case "root-alias":
				alias := cfg.AuthorityRoot + "-alias"
				must(os.Symlink(cfg.AuthorityRoot, alias))
				t.Cleanup(func() { _ = os.Remove(alias) })
				cfg.AuthorityRoot = alias
				cfg.AuthorityFile = filepath.Join(alias, "authority.json")
				cfg.PolicyFile = filepath.Join(alias, "policy.json")
			case "marker":
				must(os.WriteFile(filepath.Join(cfg.AuthorityRoot, ".first-http.started"), nil, 0600))
			case "lock-mode":
				must(os.WriteFile(filepath.Join(cfg.AuthorityRoot, ".first-http.lock"), nil, 0644))
			}
			auth, err := newFirstDiagnosisHTTPAuthority(t.Context(), cfg)
			if auth != nil {
				_ = auth.close()
			}
			if !errors.Is(err, ErrFirstDiagnosisHTTPHost) || auth != nil {
				t.Fatal("invalid operator input acquired authority")
			}
		})
	}
}
func TestE2HostProviderDetectsReplacementAndKeepsLease(t *testing.T) {
	for _, mode := range []string{"authority", "policy", "directory", "lock", "marker"} {
		t.Run(mode, func(t *testing.T) {
			cfg, _ := e2HostAuthorityFixture(t)
			auth, err := newFirstDiagnosisHTTPAuthority(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = auth.close() })
			actual := auth.(*firstHTTPM01Authority)
			var name string
			switch mode {
			case "authority":
				name = cfg.AuthorityFile
			case "policy":
				name = cfg.PolicyFile
			case "lock":
				name = filepath.Join(cfg.AuthorityRoot, ".first-http.lock")
			case "marker":
				name = filepath.Join(cfg.AuthorityRoot, ".first-http.started")
			case "directory":
				moved := cfg.AuthorityRoot + "-held"
				if err = os.Rename(cfg.AuthorityRoot, moved); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(moved) })
				if err = os.Mkdir(cfg.AuthorityRoot, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if name != "" {
				if err = os.Rename(name, name+".old"); err != nil {
					t.Fatal(err)
				}
				content := []byte("replacement")
				if mode == "lock" || mode == "marker" {
					content = nil
				}
				if err = os.WriteFile(name, content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if auth.CheckRoot(t.Context()) == nil {
				t.Fatal("replaced protected input remained usable")
			}
			if auth.Context().Err() == nil {
				t.Fatal("detected replacement did not revoke execution")
			}
			var st unix.Stat_t
			if unix.Fstat(int(actual.lock.Fd()), &st) != nil {
				t.Fatal("failed check discarded live lease")
			}
		})
	}
}

type e2HostTestSupervisor struct {
	calls     atomic.Int64
	quiescent atomic.Bool
}

func (s *e2HostTestSupervisor) Attach(*Server) error { return nil }
func (s *e2HostTestSupervisor) AuthorizeExecution(context.Context, transmit.Scope, int64) error {
	s.calls.Add(1)
	return nil
}
func (s *e2HostTestSupervisor) CheckTaskQuiescent(context.Context, int64) error {
	if !s.quiescent.Load() {
		return ErrE2HTTPBusy
	}
	return nil
}
func TestE2HostProviderChecksExactTaskAndPolicyBeforeSupervisor(t *testing.T) {
	cfg, _ := e2HostAuthorityFixture(t)
	auth, err := newFirstDiagnosisHTTPAuthority(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer auth.close()
	supervisor := &e2HostTestSupervisor{}
	expected := E2HTTPExpectedBinding{RunRef: "run", ResetRef: "reset", PolicyRef: "policy", CaseRef: "case"}
	h := &FirstDiagnosisHTTPHost{authority: auth, supervisor: supervisor, expected: expected}
	want := transmit.Scope{TaskRef: "11", RunRef: "run", ResetRef: "reset", PolicyRef: "policy", CaseRef: "case"}
	for _, mode := range []string{"task", "run", "reset", "policy", "case", "intent"} {
		scope := want
		intent := int64(7)
		switch mode {
		case "task":
			scope.TaskRef = "12"
		case "run":
			scope.RunRef = "other"
		case "reset":
			scope.ResetRef = "other"
		case "policy":
			scope.PolicyRef = "other"
		case "case":
			scope.CaseRef = "other"
		case "intent":
			intent = 0
		}
		if h.AuthorizeExecution(t.Context(), scope, intent) == nil {
			t.Fatal("unapproved scope reached supervisor")
		}
	}
	if supervisor.calls.Load() != 0 {
		t.Fatal("binding mismatch was delegated as authority")
	}
	if h.AuthorizeExecution(t.Context(), want, 7) != nil || supervisor.calls.Load() != 1 {
		t.Fatal("approved actual request did not reach actual supervisor")
	}
}
func TestE2HostProviderCloseRetainsUntilActualQuiescence(t *testing.T) {
	cfg, _ := e2HostAuthorityFixture(t)
	auth, err := newFirstDiagnosisHTTPAuthority(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	supervisor := &e2HostTestSupervisor{}
	ctx, cancel := context.WithCancel(t.Context())
	r := &E2HTTPRuntime{ctx: ctx, cancel: cancel}
	h := &FirstDiagnosisHTTPHost{authority: auth, runtime: r, supervisor: supervisor}
	t.Cleanup(func() { supervisor.quiescent.Store(true); _ = h.Close(context.Background()) })
	expired, stop := context.WithCancel(t.Context())
	stop()
	if err = h.Close(expired); !errors.Is(err, context.Canceled) || auth.Context().Err() == nil {
		t.Fatal("expired close request left authority active")
	}
	if err = h.Close(t.Context()); !errors.Is(err, ErrE2HTTPClosure) {
		t.Fatal("non-quiescent engine completed host shutdown")
	}
	actual := auth.(*firstHTTPM01Authority)
	var st unix.Stat_t
	if unix.Fstat(int(actual.lock.Fd()), &st) != nil {
		t.Fatal("incomplete close discarded lease")
	}
	supervisor.quiescent.Store(true)
	if err = h.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !actual.closed {
		t.Fatal("quiescent host retained consumed FD")
	}
}
func TestE2HostProviderRuntimeShutdownOrderingAndUnknown(t *testing.T) {
	cfg, _ := e2HostAuthorityFixture(t)
	auth, err := newFirstDiagnosisHTTPAuthority(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	supervisor := &e2HostTestSupervisor{}
	supervisor.quiescent.Store(true)
	ctx, cancel := context.WithCancel(t.Context())
	host := &FirstDiagnosisHTTPHost{authority: auth, supervisor: supervisor, runtime: &E2HTTPRuntime{ctx: ctx, cancel: cancel}}
	r := &FirstDiagnosisHTTPRuntime{host: host, cancel: cancel, server: &Server{}}
	var stages []string
	retry := false
	r.shutdown = ControlledReadAssemblyShutdown{
		BlockAdmission: func(ctx context.Context, _ *Server) error {
			stages = append(stages, "admission")
			if !errors.Is(r.Close(ctx), ErrE2HTTPBusy) {
				t.Fatal("reentrant close deadlocked")
			}
			return nil
		},
		CancelAndDrain: func(context.Context, *Server) error { stages = append(stages, "drain"); return nil },
		CheckResults: func(context.Context, *Server) error {
			stages = append(stages, "results")
			if !retry {
				return errors.Join(ErrFirstDiagnosisOutcome, ErrControlledFindingOutcomeUnknown)
			}
			return nil
		},
	}
	expired, stop := context.WithCancel(t.Context())
	stop()
	if err = r.Close(expired); !errors.Is(err, context.Canceled) || !r.admissionStopped.Load() || auth.Context().Err() == nil {
		t.Fatal("expired runtime close left admission active")
	}
	if err = r.Close(t.Context()); err == nil || !errors.Is(err, ErrControlledFindingOutcomeUnknown) {
		t.Fatal("unknown storage result completed shutdown")
	}
	if !auth.(*firstHTTPM01Authority).closed {
		t.Fatal("quiescent failed result leaked the consumed authority")
	}
	retry = true
	if err = r.Close(t.Context()); !errors.Is(err, ErrControlledFindingOutcomeUnknown) {
		t.Fatal("unknown result became success after cleanup")
	}
	if !reflect.DeepEqual(stages, []string{"admission", "drain", "results"}) {
		t.Fatal("result failure was implicitly retried")
	}
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal("closed runtime admitted request")
	}
}
func TestE2HostProviderFormattingAndMissingProvider(t *testing.T) {
	secret := "SYNTHETIC-PRIVATE-OPERATOR-PATH"
	values := []any{FirstDiagnosisHTTPHostConfig{PolicyFile: secret}, FirstDiagnosisHTTPApproval{ApprovalRef: secret}, FirstDiagnosisHTTPStartOptions{KeyDir: secret}, &FirstDiagnosisHTTPHost{}, &FirstDiagnosisHTTPRuntime{}}
	for _, value := range values {
		for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
			if strings.Contains(fmt.Sprintf(format, value), secret) {
				t.Fatal("protected settings leaked")
			}
		}
	}
	if h, err := NewFirstDiagnosisHTTPHost(t.Context(), FirstDiagnosisHTTPHostConfig{}, nil); h != nil || err == nil {
		t.Fatal("missing actual supervisor acquired host")
	}
	if r, err := StartFirstDiagnosisHTTP(t.Context(), nil, FirstDiagnosisHTTPStartOptions{}); r != nil || err == nil {
		t.Fatal("missing actual manager started server")
	}
}

func TestE2HostProviderCloseErrorCannotBecomeSuccessOnRetry(t *testing.T) {
	cfg, _ := e2HostAuthorityFixture(t)
	auth, err := newFirstDiagnosisHTTPAuthority(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	actual := auth.(*firstHTTPM01Authority)
	if err = actual.policy.file.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(auth.close(), ErrE2HTTPClosure) || !errors.Is(auth.close(), ErrE2HTTPClosure) {
		t.Fatal("uncertain FD close became successful retry")
	}
}

// 합성 키/정책만 만드는 시험 공급자다. 실제 모델·권위·배치 검증이 아니다.
func e2HostPolicyFixture(t *testing.T) (FirstDiagnosisHTTPHostConfig, FirstDiagnosisHTTPApproval, E2HTTPFilePolicy) {
	t.Helper()
	cfg, grant := e2HostAuthorityFixture(t)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic M01 host CA"}, NotBefore: grant.ValidFrom, NotAfter: grant.ValidUntil, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pin := func(name string, raw []byte) E2HTTPFilePin {
		t.Helper()
		if err := os.WriteFile(filepath.Join(cfg.AuthorityRoot, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(raw)
		return E2HTTPFilePin{File: name, SHA256: hex.EncodeToString(digest[:])}
	}
	cert := pin("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	privatePin := pin("ca-key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
	audit := pin("audit-key.bin", []byte("01234567890123456789012345678901"))
	helper, err := filepath.EvalSymlinks("/usr/bin/true") // 이 시험은 helper를 실행하지 않는다.
	if err != nil {
		t.Fatal("native host-construction fixture executable is unavailable")
	}
	digest, err := e2HTTPExecutableDigest(helper)
	if err != nil {
		t.Fatal("native host-construction fixture executable is unprotected")
	}
	// 소켓 경로 길이 제한을 지키는 별도 소유 상태 디렉터리다. 보호 입력 root는 그대로 유지한다.
	state, err := os.MkdirTemp("/var/tmp", "e2h-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(state); err != nil {
			t.Error(err)
		}
	})
	policy := E2HTTPFilePolicy{Format: E2HTTPFilePolicyFormat, RunRef: "fixture-run", ResetRef: "fixture-reset", PolicyRef: "fixture-policy", CaseRef: "fixture-case", PolicyRevision: "fixture-revision", BudgetRef: "fixture-budget", OwnerRef: "fixture-owner", AccountRef: "fixture-account", SessionRef: "fixture-session", ValidFrom: grant.ValidFrom.Add(time.Second), ValidUntil: grant.ValidUntil.Add(-time.Second), MaxRequests: 2, HeaderMode: e2proxy.HeaderPreserve, StateDir: state, Helper: helper, HelperSHA256: hex.EncodeToString(digest[:]), CACert: cert, CAKey: privatePin, AuditKey: audit, Rules: []E2HTTPFileRule{{Ref: "fixture-rule", Origin: "https://example.invalid", ConnectIP: "127.0.0.1", PathPrefix: "/", PathScopeRef: "fixture-path", Methods: []string{"GET"}}}, Limits: E2HTTPFileLimits{MaxAttempts: 5, MaxCalls: 3, MaxConcurrent: 1, MaxBodyBytes: 1024, RequestTimeoutMS: 1000, CallTimeoutMS: 2000, MaxDenials: 2, MaxTransient: 1, MaxUnknown: 1}, Proxy: E2HTTPFileProxy{HeaderTimeoutMS: 500, RequestTimeoutMS: 1000, MaxHeaderBytes: 8192, MaxConnections: 2}, Runtime: E2HTTPFileRuntime{MaxWorkerTurns: 2, MaxToolCalls: 3, WorkerTimeoutMS: 3000, MaxPlannerRounds: 2, MaxIntentRuns: 1, DrainTimeoutMS: 2000, OutputLimit: 4096}}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.PolicyFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	policyDigest := sha256.Sum256(raw)
	grant.PolicySHA256 = hex.EncodeToString(policyDigest[:])
	e2HostWriteApproval(t, cfg, grant)
	return cfg, grant, policy
}
func TestE2HostProviderProductionFactoryUsesProtectedOperatorInputs(t *testing.T) {
	cfg, grant, policy := e2HostPolicyFixture(t)
	supervisor := &e2HostTestSupervisor{}
	supervisor.quiescent.Store(true)
	host, err := NewFirstDiagnosisHTTPHost(t.Context(), cfg, supervisor)
	if err != nil {
		t.Fatal(err)
	}
	id, exp := host.ExpectedTask()
	if id != "11" || exp != grant.ExplorationID || host.Runtime() == nil || host.CheckRoot(t.Context()) != nil {
		t.Fatal("real factory did not bind approved host inputs")
	}
	if _, err = os.Stat(filepath.Join(policy.StateDir, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatal("host forged an execution before a real intent")
	}
	if err = host.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if host.Context().Err() == nil {
		t.Fatal("host close retained unacquired key authority")
	}
}
func TestFirstDiagnosisHTTPHostRejectsEvaluationCallerPassthrough(t *testing.T) {
	cfg, grant, policy := e2HostPolicyFixture(t)
	policy.HeaderMode = e2proxy.HeaderEvaluationCallerPassthrough
	policy.Evaluation = &e2proxy.EvaluationCallerContext{
		RunRef: policy.RunRef, LLMBackend: "local", TargetKind: e2proxy.EvaluationPublicPractice,
	}
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0600) != nil {
		t.Fatal("평가 caller 시험 정책 저장 실패")
	}
	digest := sha256.Sum256(raw)
	grant.PolicySHA256 = hex.EncodeToString(digest[:])
	e2HostWriteApproval(t, cfg, grant)
	supervisor := &e2HostTestSupervisor{}
	supervisor.quiescent.Store(true)
	if host, err := NewFirstDiagnosisHTTPHost(t.Context(), cfg, supervisor); host != nil || !errors.Is(err, ErrFirstDiagnosisHTTPHost) {
		if host != nil {
			_ = host.Close(t.Context())
		}
		t.Fatal("비평가 호스트가 평가 caller 원문 전달 모드를 수용함")
	}
}

func TestE2HostProviderProductionFactoryRejectsMissingOrChangedPrivateMaterial(t *testing.T) {
	for _, mode := range []string{"ca", "key", "audit", "policy-window"} {
		t.Run(mode, func(t *testing.T) {
			cfg, grant, policy := e2HostPolicyFixture(t)
			name := ""
			switch mode {
			case "ca":
				name = policy.CACert.File
			case "key":
				name = policy.CAKey.File
			case "audit":
				name = policy.AuditKey.File
			case "policy-window":
				policy.ValidUntil = grant.ValidUntil.Add(time.Second)
				raw, err := json.Marshal(policy)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(cfg.PolicyFile, raw, 0600); err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(raw)
				grant.PolicySHA256 = hex.EncodeToString(digest[:])
				e2HostWriteApproval(t, cfg, grant)
			}
			if name != "" {
				if err := os.Remove(filepath.Join(cfg.AuthorityRoot, name)); err != nil {
					t.Fatal(err)
				}
			}
			supervisor := &e2HostTestSupervisor{}
			supervisor.quiescent.Store(true)
			host, err := NewFirstDiagnosisHTTPHost(t.Context(), cfg, supervisor)
			if host != nil || !errors.Is(err, ErrFirstDiagnosisHTTPHost) {
				if host != nil {
					_ = host.Close(t.Context())
				}
				t.Fatal("invalid private provision started production host")
			}
		})
	}
}

func TestE2HostProviderUnsafeResultCheckRetainsResources(t *testing.T) {
	cfg, _ := e2HostAuthorityFixture(t)
	auth, err := newFirstDiagnosisHTTPAuthority(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	supervisor := &e2HostTestSupervisor{}
	supervisor.quiescent.Store(true)
	ctx, cancel := context.WithCancel(t.Context())
	host := &FirstDiagnosisHTTPHost{authority: auth, supervisor: supervisor, runtime: &E2HTTPRuntime{ctx: ctx, cancel: cancel}}
	r := &FirstDiagnosisHTTPRuntime{host: host, cancel: cancel, server: &Server{}}
	safe := false
	r.shutdown = ControlledReadAssemblyShutdown{BlockAdmission: func(context.Context, *Server) error { return nil }, CancelAndDrain: func(context.Context, *Server) error { return nil }, CheckResults: func(context.Context, *Server) error {
		if !safe {
			return ErrControlledFindingOutcomeUnknown
		}
		return nil
	}}
	if err = r.Close(t.Context()); !errors.Is(err, ErrControlledFindingOutcomeUnknown) || r.ResourcesClosed() || auth.(*firstHTTPM01Authority).closed {
		t.Fatal("unproven result check freed live resources")
	}
	safe = true
	if err = r.Close(t.Context()); err != nil || !r.ResourcesClosed() {
		t.Fatal("proven safe retry could not release resources")
	}
}
