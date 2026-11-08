package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"net/netip"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/Autumn-27/artex/e2export"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/internal/privateinput"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
)

const E2HTTPFilePolicyFormat = "abc2lab.e2-http-host-policy/v1"

type E2HTTPPolicyAuthority interface {
	Context() context.Context
	CheckRoot(context.Context) error
	AuthorizePolicy(context.Context, [32]byte) error
}
type E2HTTPExecutionSupervisor interface {
	AuthorizeExecution(context.Context, transmit.Scope, int64) error
}

// 신뢰한 Go 조립에서만 사용하는 실제 자원이다. AuditKey 사본은 callback 반환 직후 지운다.
type E2HTTPAcquired struct {
	PolicyFingerprintSHA256 string                           `json:"-"`
	PolicyConfigSHA256      string                           `json:"-"`
	HeaderMode              e2proxy.HeaderMode               `json:"-"`
	Evaluation              *e2proxy.EvaluationCallerContext `json:"-"`
	Execution               *e2proxy.Execution               `json:"-"`
	Runner                  *e2runner.Runner                 `json:"-"`
	Journal                 *e2proxy.Journal                 `json:"-"`
	JournalDirectory        string                           `json:"-"`
	JournalName             string                           `json:"-"`
	AuditKey                []byte                           `json:"-"`
}

func (E2HTTPAcquired) String() string   { return "server.E2HTTPAcquired[protected]" }
func (E2HTTPAcquired) GoString() string { return "server.E2HTTPAcquired[protected]" }

type E2HTTPFileOptions struct {
	RequestApprovals *RequestApprovalService
	PolicyFile       string
	OnAcquire        func(context.Context, E2HTTPAcquired) error
	Authority        E2HTTPPolicyAuthority
	Supervisor       E2HTTPExecutionSupervisor
	ObserveEvent     func(context.Context, e2proxy.Event) error
	ObserveResult    func(context.Context, e2runner.Result) error
}

func (E2HTTPFileOptions) String() string   { return "server.E2HTTPFileOptions[protected]" }
func (E2HTTPFileOptions) GoString() string { return "server.E2HTTPFileOptions[protected]" }

type E2HTTPFilePin struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}
type E2HTTPFileRule struct {
	ApprovalDisplay RequestApprovalDisplay `json:"approval_display"`
	Ref             string                 `json:"ref"`
	Origin          string                 `json:"origin"`
	ConnectIP       string                 `json:"connect_ip"`
	PathPrefix      string                 `json:"path_prefix"`
	PathScopeRef    string                 `json:"path_scope_ref"`
	Methods         []string               `json:"methods"`
	Credentials     *E2HTTPFilePin         `json:"credentials,omitempty"`
}
type E2HTTPFileLimits struct {
	MaxAttempts      uint64 `json:"max_attempts"`
	MaxCalls         int    `json:"max_calls"`
	MaxConcurrent    int    `json:"max_concurrent"`
	MaxBodyBytes     int64  `json:"max_body_bytes"`
	RequestTimeoutMS int64  `json:"request_timeout_ms"`
	CallTimeoutMS    int64  `json:"call_timeout_ms"`
	MaxDenials       uint64 `json:"max_denials"`
	MaxTransient     uint64 `json:"max_transient"`
	MaxUnknown       uint64 `json:"max_unknown"`
}
type E2HTTPFileProxy struct {
	HeaderTimeoutMS  int64 `json:"header_timeout_ms"`
	RequestTimeoutMS int64 `json:"request_timeout_ms"`
	MaxHeaderBytes   int   `json:"max_header_bytes"`
	MaxConnections   int   `json:"max_connections"`
}
type E2HTTPFileRuntime struct {
	MaxWorkerTurns   int    `json:"max_worker_turns"`
	MaxToolCalls     int    `json:"max_tool_calls"`
	WorkerTimeoutMS  int64  `json:"worker_timeout_ms"`
	MaxPlannerRounds uint64 `json:"max_planner_rounds"`
	MaxIntentRuns    uint64 `json:"max_intent_runs"`
	DrainTimeoutMS   int64  `json:"drain_timeout_ms"`
	OutputLimit      int    `json:"output_limit"`
}

// task/intent는 파일 입력이 아니다. 실제 생성된 객체를 supervisor가 확인한 뒤 채운다.
func (E2HTTPFilePolicy) String() string   { return "server.E2HTTPFilePolicy[protected]" }
func (E2HTTPFilePolicy) GoString() string { return "server.E2HTTPFilePolicy[protected]" }

type E2HTTPFilePolicy struct {
	ApprovalMode   e2proxy.ApprovalMode             `json:"approval_mode"`
	Format         string                           `json:"format"`
	RunRef         string                           `json:"run_ref"`
	ResetRef       string                           `json:"reset_ref"`
	PolicyRef      string                           `json:"policy_ref"`
	CaseRef        string                           `json:"case_ref"`
	PolicyRevision string                           `json:"policy_revision"`
	BudgetRef      string                           `json:"budget_ref"`
	OwnerRef       string                           `json:"owner_ref"`
	AccountRef     string                           `json:"account_ref"`
	SessionRef     string                           `json:"session_ref"`
	ValidFrom      time.Time                        `json:"valid_from"`
	ValidUntil     time.Time                        `json:"valid_until"`
	MaxRequests    uint64                           `json:"max_requests"`
	HeaderMode     e2proxy.HeaderMode               `json:"header_mode"`
	Evaluation     *e2proxy.EvaluationCallerContext `json:"evaluation"`
	StateDir       string                           `json:"state_dir"`
	Helper         string                           `json:"helper"`
	HelperSHA256   string                           `json:"helper_sha256"`
	CACert         E2HTTPFilePin                    `json:"ca_cert"`
	CAKey          E2HTTPFilePin                    `json:"ca_key"`
	AuditKey       E2HTTPFilePin                    `json:"audit_key"`
	UpstreamCA     *E2HTTPFilePin                   `json:"upstream_ca,omitempty"`
	Rules          []E2HTTPFileRule                 `json:"rules"`
	Limits         E2HTTPFileLimits                 `json:"limits"`
	Proxy          E2HTTPFileProxy                  `json:"proxy"`
	Runtime        E2HTTPFileRuntime                `json:"runtime"`
}

func e2HTTPNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	}
	return false
}
func e2HTTPPinnedBytes(dir string, pin E2HTTPFilePin, limit int64) ([]byte, error) {
	if !e2HTTPHostRef.MatchString(pin.File) || filepath.Base(pin.File) != pin.File {
		return nil, ErrE2HTTPHost
	}
	expected, err := hex.DecodeString(pin.SHA256)
	if err != nil || len(expected) != sha256.Size || hex.EncodeToString(expected) != pin.SHA256 {
		return nil, ErrE2HTTPHost
	}
	data, err := privateinput.ReadFile(filepath.Join(dir, pin.File), limit)
	if err != nil {
		return nil, ErrE2HTTPHost
	}
	actual := sha256.Sum256(data)
	if hex.EncodeToString(actual[:]) != pin.SHA256 {
		clear(data)
		return nil, ErrE2HTTPHost
	}
	return data, nil
}
func e2HTTPMilliseconds(value int64, max time.Duration) (time.Duration, bool) {
	if value <= 0 || value > max.Milliseconds() {
		return 0, false
	}
	return time.Duration(value) * time.Millisecond, true
}

// LoadE2HTTPRuntimeOptions는 승인된 기존 파일만 소비한다. 키·epoch·회차 승인을 발급하지 않는다.
func LoadE2HTTPRuntimeOptions(ctx context.Context, input E2HTTPFileOptions) (E2HTTPRuntimeOptions, error) {
	var empty E2HTTPRuntimeOptions
	if ctx == nil || ctx.Err() != nil || e2HTTPNil(input.Authority) || e2HTTPNil(input.Supervisor) {
		return empty, ErrE2HTTPHost
	}
	authorityCtx := input.Authority.Context()
	if authorityCtx == nil || authorityCtx.Err() != nil || input.Authority.CheckRoot(ctx) != nil {
		return empty, ErrE2HTTPHost
	}
	raw, err := privateinput.ReadFile(input.PolicyFile, 256<<10)
	if err != nil {
		return empty, ErrE2HTTPHost
	}
	defer clear(raw)
	digest := sha256.Sum256(raw)
	if input.Authority.AuthorizePolicy(ctx, digest) != nil {
		return empty, ErrE2HTTPHost
	}
	var policy E2HTTPFilePolicy
	if strictJSON(raw, &policy) != nil || policy.Format != E2HTTPFilePolicyFormat || len(policy.Rules) == 0 || len(policy.Rules) > 256 || policy.MaxRequests == 0 || !policy.ValidUntil.After(policy.ValidFrom) || !policy.ValidUntil.After(time.Now()) || policy.ValidFrom.After(time.Now()) || privateinput.CheckDirectory(policy.StateDir, true) != nil {
		return empty, ErrE2HTTPHost
	}
	if policy.ApprovalMode != e2proxy.ApprovalConfirmEveryRequest && policy.ApprovalMode != e2proxy.ApprovalPreauthorizedScope {
		return empty, ErrE2HTTPHost
	}
	if policy.ApprovalMode == e2proxy.ApprovalConfirmEveryRequest && (input.RequestApprovals == nil || policy.OwnerRef != "ARTEX") {
		return empty, ErrE2HTTPHost
	}
	if e2runner.ValidateRootDirLayout(filepath.Join(policy.StateDir, "calls")) != nil {
		return empty, ErrE2HTTPHost
	}
	for _, ref := range []string{policy.RunRef, policy.ResetRef, policy.PolicyRef, policy.CaseRef, policy.PolicyRevision, policy.BudgetRef, policy.OwnerRef, policy.AccountRef, policy.SessionRef} {
		if !e2HTTPHostRef.MatchString(ref) {
			return empty, ErrE2HTTPHost
		}
	}
	helperDigest, err := e2HTTPExecutableDigest(policy.Helper)
	if err != nil || hex.EncodeToString(helperDigest[:]) != policy.HelperSHA256 {
		return empty, ErrE2HTTPHost
	}
	dir := filepath.Dir(input.PolicyFile)
	certBytes, err := e2HTTPPinnedBytes(dir, policy.CACert, 1<<20)
	if err != nil {
		return empty, err
	}
	defer clear(certBytes)
	keyBytes, err := e2HTTPPinnedBytes(dir, policy.CAKey, 1<<20)
	if err != nil {
		return empty, err
	}
	defer clear(keyBytes)
	caBlock, caRest := pem.Decode(certBytes)
	if caBlock == nil || caBlock.Type != "CERTIFICATE" || len(caBlock.Headers) != 0 || len(bytes.TrimSpace(caRest)) != 0 {
		return empty, ErrE2HTTPHost
	}
	certificate, err := tls.X509KeyPair(certBytes, keyBytes)
	if err != nil || len(certificate.Certificate) != 1 || !bytes.Equal(caBlock.Bytes, certificate.Certificate[0]) {
		return empty, ErrE2HTTPHost
	}
	auditKey, err := e2HTTPPinnedBytes(dir, policy.AuditKey, 32)
	if err != nil || len(auditKey) != 32 {
		clear(auditKey)
		return empty, ErrE2HTTPHost
	}
	// factory는 단일 획득만 허용한다. 이 사본은 실제 journal이 인수하면 즉시 지운다.
	var upstreamRoots *x509.CertPool
	if policy.UpstreamCA != nil {
		pem, loadErr := e2HTTPPinnedBytes(dir, *policy.UpstreamCA, 1<<20)
		if loadErr != nil {
			clear(auditKey)
			return empty, loadErr
		}
		upstreamRoots = x509.NewCertPool()
		ok := upstreamRoots.AppendCertsFromPEM(pem)
		clear(pem)
		if !ok {
			clear(auditKey)
			return empty, ErrE2HTTPHost
		}
	}
	rules := make([]e2proxy.Rule, 0, len(policy.Rules))
	for _, rule := range policy.Rules {
		ip, parseErr := netip.ParseAddr(rule.ConnectIP)
		if parseErr != nil {
			clear(auditKey)
			return empty, ErrE2HTTPHost
		}
		var credentials http.Header
		if rule.Credentials != nil {
			data, loadErr := e2HTTPPinnedBytes(dir, *rule.Credentials, 32<<10)
			if loadErr != nil {
				clear(auditKey)
				return empty, loadErr
			}
			var secret struct {
				Headers http.Header `json:"headers"`
			}
			decodeErr := strictJSON(data, &secret)
			clear(data)
			if decodeErr != nil {
				clear(auditKey)
				return empty, ErrE2HTTPHost
			}
			credentials = secret.Headers
		}
		rules = append(rules, e2proxy.Rule{Ref: rule.Ref, Origin: rule.Origin, ConnectIP: ip, PathPrefix: rule.PathPrefix, PathScopeRef: rule.PathScopeRef, Methods: append([]string(nil), rule.Methods...), CredentialHeaders: credentials})
	}
	requestTimeout, ok1 := e2HTTPMilliseconds(policy.Limits.RequestTimeoutMS, time.Minute)
	callTimeout, ok2 := e2HTTPMilliseconds(policy.Limits.CallTimeoutMS, time.Hour)
	headerTimeout, ok3 := e2HTTPMilliseconds(policy.Proxy.HeaderTimeoutMS, time.Minute)
	proxyTimeout, ok4 := e2HTTPMilliseconds(policy.Proxy.RequestTimeoutMS, time.Minute)
	workerTimeout, ok5 := e2HTTPMilliseconds(policy.Runtime.WorkerTimeoutMS, time.Hour)
	drainTimeout, ok6 := e2HTTPMilliseconds(policy.Runtime.DrainTimeoutMS, 2*time.Minute)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 {
		clear(auditKey)
		return empty, ErrE2HTTPHost
	}
	proxy := e2proxy.ProxyConfig{CA: certificate, HeaderTimeout: headerTimeout, RequestTimeout: proxyTimeout, MaxHeaderBytes: policy.Proxy.MaxHeaderBytes, MaxConnections: policy.Proxy.MaxConnections}
	if e2proxy.ValidateProxyConfig(proxy) != nil {
		clear(auditKey)
		return empty, ErrE2HTTPHost
	}
	if policy.HeaderMode == "" {
		policy.HeaderMode = e2proxy.HeaderPreserve
	}
	limits := e2proxy.Limits{MaxAttempts: policy.Limits.MaxAttempts, MaxCalls: policy.Limits.MaxCalls, MaxConcurrent: policy.Limits.MaxConcurrent, MaxBodyBytes: policy.Limits.MaxBodyBytes, RequestTimeout: requestTimeout, CallTimeout: callTimeout, MaxDenials: policy.Limits.MaxDenials, MaxTransient: policy.Limits.MaxTransient, MaxUnknown: policy.Limits.MaxUnknown}
	if e2proxy.ValidatePolicyTemplate(e2proxy.Config{ApprovalMode: policy.ApprovalMode, Scope: transmit.Scope{RunRef: policy.RunRef}, Rules: rules, Limits: limits, HeaderMode: policy.HeaderMode, Evaluation: policy.Evaluation}) != nil {
		clear(auditKey)
		return empty, ErrE2HTTPHost
	}
	fingerprint, fingerprintErr := e2export.PolicyFingerprint(e2proxy.Config{ApprovalMode: policy.ApprovalMode, Scope: transmit.Scope{RunRef: policy.RunRef}, Rules: rules, Limits: limits, HeaderMode: policy.HeaderMode, Evaluation: policy.Evaluation}, policy.MaxRequests, e2export.PolicyBehavior{MaxWorkerTurns: policy.Runtime.MaxWorkerTurns, MaxToolCalls: policy.Runtime.MaxToolCalls, WorkerTimeout: workerTimeout, MaxPlannerRounds: policy.Runtime.MaxPlannerRounds, MaxIntentRuns: policy.Runtime.MaxIntentRuns, DrainTimeout: drainTimeout, OutputLimit: policy.Runtime.OutputLimit, ProxyHeaderTimeout: headerTimeout, ProxyRequestTimeout: proxyTimeout, ProxyMaxHeaderBytes: policy.Proxy.MaxHeaderBytes, ProxyMaxConnections: policy.Proxy.MaxConnections})
	if fingerprintErr != nil {
		clear(auditKey)
		return empty, ErrE2HTTPHost
	}
	policyConfigSHA := hex.EncodeToString(digest[:])
	expected := E2HTTPExpectedBinding{RunRef: policy.RunRef, ResetRef: policy.ResetRef, PolicyRef: policy.PolicyRef, CaseRef: policy.CaseRef, PolicyRevision: policy.PolicyRevision, BudgetRef: policy.BudgetRef}
	current := func(checkCtx context.Context) error {
		if checkCtx == nil || checkCtx.Err() != nil || authorityCtx.Err() != nil || input.Authority.CheckRoot(checkCtx) != nil || input.Authority.AuthorizePolicy(checkCtx, digest) != nil {
			return ErrE2HTTPHost
		}
		return nil
	}
	authorize := func(checkCtx context.Context, request E2HTTPRequest) error {
		if current(checkCtx) != nil || request.TaskIDNumber <= 0 || request.IntentID <= 0 || request.ExplorationID <= 0 || strconv.FormatInt(request.TaskIDNumber, 10) != request.TaskID {
			return ErrE2HTTPHost
		}
		scope := transmit.Scope{TaskRef: request.TaskID, RunRef: policy.RunRef, CaseRef: policy.CaseRef, ResetRef: policy.ResetRef, PolicyRef: policy.PolicyRef}
		if input.Supervisor.AuthorizeExecution(checkCtx, scope, request.IntentID) != nil {
			return ErrE2HTTPHost
		}
		return nil
	}
	var acquireMu sync.Mutex
	context.AfterFunc(authorityCtx, func() { acquireMu.Lock(); clear(auditKey); acquireMu.Unlock() })
	claimed := false
	acquire := func(taskctx context.Context, request E2HTTPRequest) (E2HTTPRunOptions, error) {
		acquireMu.Lock()
		defer acquireMu.Unlock()
		if claimed || authorize(taskctx, request) != nil {
			return E2HTTPRunOptions{}, ErrE2HTTPHost
		}
		claimed = true
		defer clear(auditKey)
		if privateinput.CheckDirectory(policy.StateDir, true) != nil {
			return E2HTTPRunOptions{}, ErrE2HTTPHost
		}
		currentHelper, helperErr := e2HTTPExecutableDigest(policy.Helper)
		if helperErr != nil || currentHelper != helperDigest {
			return E2HTTPRunOptions{}, ErrE2HTTPHost
		}
		ledger, createErr := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: policy.MaxRequests})
		if createErr != nil {
			return E2HTTPRunOptions{}, ErrE2HTTPHost
		}
		grantKey := budget.Key{TaskRef: request.TaskID, BudgetRef: policy.BudgetRef}
		budgetBinding := budget.Binding{PolicyRef: policy.PolicyRef, ResetRef: policy.ResetRef}
		if ledger.Register(budget.Grant{Key: grantKey, Binding: budgetBinding, MaxRequests: policy.MaxRequests, ValidFrom: policy.ValidFrom, ValidUntil: policy.ValidUntil}, time.Now()) != nil {
			return E2HTTPRunOptions{}, ErrE2HTTPHost
		}
		journal, createErr := e2proxy.CreateJournal(policy.StateDir, "events.jsonl", auditKey)
		if createErr != nil {
			return E2HTTPRunOptions{}, ErrE2HTTPHost
		}
		release := func(context.Context) error { return journal.Close() }
		failed := func(execution *e2proxy.Execution, runner *e2runner.Runner) (E2HTTPRunOptions, error) {
			return E2HTTPRunOptions{Runner: runner, Execution: execution, Release: release}, ErrE2HTTPHost
		}
		recorder := e2proxy.Recorder(journal)
		if input.ObserveEvent != nil {
			recorder = e2HTTPJournalObserver{next: journal, observe: input.ObserveEvent}
		}
		var gate e2proxy.RequestApprovalGate
		if input.RequestApprovals != nil {
			if input.RequestApprovals.bind(taskctx, request.TaskIDNumber, "ARTEX", policy.RunRef, policy.Rules) != nil {
				return failed(nil, nil)
			}
			if policy.ApprovalMode == e2proxy.ApprovalConfirmEveryRequest {
				gate = input.RequestApprovals
			}
			recorder = e2HTTPJournalObserver{next: recorder, observe: input.RequestApprovals.observe}
		}
		execution, createErr := e2proxy.NewExecution(taskctx, e2proxy.Config{ApprovalMode: policy.ApprovalMode, RequestApproval: gate, Evaluation: policy.Evaluation, HeaderMode: policy.HeaderMode, Scope: transmit.Scope{TaskRef: request.TaskID, RunRef: policy.RunRef, CaseRef: policy.CaseRef, ResetRef: policy.ResetRef, PolicyRef: policy.PolicyRef}, IntentID: request.IntentID, OwnerRef: policy.OwnerRef, PolicyRevision: policy.PolicyRevision, AccountRef: policy.AccountRef, SessionRef: policy.SessionRef, ExpiresAt: policy.ValidUntil, Rules: rules, Budget: transmit.CumulativeBudget{Ledger: ledger, Key: grantKey, Binding: budgetBinding}, Recorder: recorder, RootCAs: upstreamRoots, Limits: limits})
		if createErr != nil {
			return failed(nil, nil)
		}
		if input.RequestApprovals != nil {
			input.RequestApprovals.attachExecution(request.TaskIDNumber, execution)
		}
		callRoot := filepath.Join(policy.StateDir, "calls")
		if e2HTTPCreatePrivateDirectory(callRoot) != nil {
			return failed(execution, nil)
		}
		runner, createErr := e2runner.New(e2runner.Config{Execution: execution, Helper: policy.Helper, RootDir: callRoot, CACert: filepath.Join(dir, policy.CACert.File), Proxy: proxy, CommandTimeout: workerTimeout, DrainTimeout: drainTimeout, OutputLimit: policy.Runtime.OutputLimit})
		if createErr != nil || current(taskctx) != nil {
			return failed(execution, runner)
		}
		if input.OnAcquire != nil {
			verifierKey := append([]byte(nil), auditKey...)
			acquireErr := input.OnAcquire(taskctx, E2HTTPAcquired{Execution: execution, Runner: runner, Journal: journal, JournalDirectory: policy.StateDir, JournalName: "events.jsonl", AuditKey: verifierKey, PolicyFingerprintSHA256: fingerprint, PolicyConfigSHA256: policyConfigSHA, HeaderMode: policy.HeaderMode, Evaluation: e2HTTPCloneEvaluation(policy.Evaluation)})
			clear(verifierKey)
			if acquireErr != nil || current(taskctx) != nil {
				return failed(execution, runner)
			}
		}
		return E2HTTPRunOptions{Runner: runner, Execution: execution, Release: release, ObserveResult: input.ObserveResult}, nil
	}
	options := E2HTTPRuntimeOptions{RequestApprovals: input.RequestApprovals, PolicyFingerprintSHA256: fingerprint, PolicyConfigSHA256: policyConfigSHA, HeaderMode: policy.HeaderMode, Evaluation: e2HTTPCloneEvaluation(policy.Evaluation), AuthorityContext: authorityCtx, CheckAuthority: current, AuthorizeIntent: authorize, Expected: expected, Acquire: acquire, MaxWorkerTurns: policy.Runtime.MaxWorkerTurns, MaxToolCalls: policy.Runtime.MaxToolCalls, WorkerTimeout: workerTimeout, MaxPlannerRounds: policy.Runtime.MaxPlannerRounds, MaxIntentRuns: policy.Runtime.MaxIntentRuns, DrainTimeout: drainTimeout}
	if current(ctx) != nil {
		clear(auditKey)
		return empty, ErrE2HTTPHost
	}
	return options, nil
}

type e2HTTPJournalObserver struct {
	next    e2proxy.Recorder
	observe func(context.Context, e2proxy.Event) error
}

func (r e2HTTPJournalObserver) Record(ctx context.Context, event e2proxy.Event) error {
	if err := r.next.Record(ctx, event); err != nil {
		return err
	}
	if err := r.observe(ctx, event); err != nil {
		return ErrE2HTTPHost
	}
	return nil
}
