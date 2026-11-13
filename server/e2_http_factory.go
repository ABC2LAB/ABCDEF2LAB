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
	"sync/atomic"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/e2config"
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
	Workflow *diagnosisflow.Controller
	// 새 URL 준비의 상위 설치 위임을 재확인한다. 기존 호출자는 nil을 유지한다.
	CheckAuthority   func() error
	RequestApprovals *RequestApprovalService
	PolicyFile       string
	OnAcquire        func(context.Context, E2HTTPAcquired) error
	Authority        E2HTTPPolicyAuthority
	Supervisor       E2HTTPExecutionSupervisor
	ObserveEvent     func(context.Context, e2proxy.Event) error
	ObserveResult    func(context.Context, e2runner.Result) error
	// AttemptPathObserver는 수용시험 회차(eval-e2 설정 v2)에서만 설정한다. nil이면 실행 동작은 그대로다.
	AttemptPathObserver e2proxy.AttemptPathObserver
	// PlannerLimits는 Plan당 상한이다(C26·C32). 0이면 기본값이다.
	PlannerLimits agent.E2HTTPPlannerLimits
}

func (E2HTTPFileOptions) String() string   { return "server.E2HTTPFileOptions[protected]" }
func (E2HTTPFileOptions) GoString() string { return "server.E2HTTPFileOptions[protected]" }

type E2HTTPFilePin struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}
type E2HTTPFileRule struct {
	TLSSPKISHA256   string                 `json:"tls_spki_sha256,omitempty"`
	ApprovalDisplay RequestApprovalDisplay `json:"approval_display"`
	Ref             string                 `json:"ref"`
	Origin          string                 `json:"origin"`
	ConnectIP       string                 `json:"connect_ip"`
	PathPrefix      string                 `json:"path_prefix"`
	PathScopeRef    string                 `json:"path_scope_ref"`
	Methods         []string               `json:"methods"`
	Credentials     *E2HTTPFilePin         `json:"credentials,omitempty"`
	// Purpose는 인증 준비 전용 규칙 표시다(e2proxy.PurposeAuthPrep). 비면 모델 요청 규칙이다.
	Purpose string `json:"purpose,omitempty"`
}
type E2HTTPFileLimits struct {
	MaxAttempts   uint64 `json:"max_attempts"`
	MaxCalls      int    `json:"max_calls"`
	MaxConcurrent int    `json:"max_concurrent"`
	MaxBodyBytes  int64  `json:"max_body_bytes"`
	// 응답 전달 상한·검사 상한(C16 결정안 3). 없으면 요청 상한과 계산값이고 정책 지문도 그대로다.
	MaxResponseBodyBytes int64  `json:"max_response_body_bytes,omitempty"`
	MaxResponseScanBytes int64  `json:"max_response_scan_bytes,omitempty"`
	RequestTimeoutMS     int64  `json:"request_timeout_ms"`
	CallTimeoutMS        int64  `json:"call_timeout_ms"`
	MaxDenials           uint64 `json:"max_denials"`
	MaxTransient         uint64 `json:"max_transient"`
	MaxUnknown           uint64 `json:"max_unknown"`
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

type E2HTTPWorkflowPolicy struct {
	Format      string `json:"format"`
	Mode        string `json:"mode"`
	InputSHA256 string `json:"input_sha256"`
}

type E2HTTPFilePolicy struct {
	Workflow       *E2HTTPWorkflowPolicy            `json:"workflow,omitempty"`
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
	// Auth는 인증 준비 칸이다(C14). 없으면 로그인하지 않는다.
	Auth    *E2HTTPFileAuth   `json:"auth,omitempty"`
	Limits  E2HTTPFileLimits  `json:"limits"`
	Proxy   E2HTTPFileProxy   `json:"proxy"`
	Runtime E2HTTPFileRuntime `json:"runtime"`
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
	return loadE2HTTPRuntimeOptions(ctx, input, nil, nil)
}

// 평가 backend만 동결 원문과 단일 reset 결속을 제공한다. 일반 M01은 파일의 고정 reset을 유지한다.
func loadE2HTTPRuntimeOptions(ctx context.Context, input E2HTTPFileOptions, frozen []byte, resetBinding func() (string, error)) (E2HTTPRuntimeOptions, error) {
	var empty E2HTTPRuntimeOptions
	if ctx == nil || ctx.Err() != nil || e2HTTPNil(input.Authority) || e2HTTPNil(input.Supervisor) {
		return empty, ErrE2HTTPHost
	}
	authorityCtx := input.Authority.Context()
	if authorityCtx == nil || authorityCtx.Err() != nil || input.Authority.CheckRoot(ctx) != nil {
		return empty, ErrE2HTTPHost
	}
	raw := append([]byte(nil), frozen...)
	var err error
	if frozen == nil {
		raw, err = privateinput.ReadFile(input.PolicyFile, e2config.MaxPolicyBytes)
	}
	if err != nil {
		return empty, ErrE2HTTPHost
	}
	defer clear(raw)
	if len(raw) == 0 || len(raw) > e2config.MaxPolicyBytes {
		return empty, ErrE2HTTPHost
	}
	digest := sha256.Sum256(raw)
	if input.Authority.AuthorizePolicy(ctx, digest) != nil {
		return empty, ErrE2HTTPHost
	}
	var policy E2HTTPFilePolicy
	if strictJSON(raw, &policy) != nil || policy.Format != E2HTTPFilePolicyFormat || len(policy.Rules) == 0 || len(policy.Rules) > 256 || policy.MaxRequests == 0 || !policy.ValidUntil.After(policy.ValidFrom) || !policy.ValidUntil.After(time.Now()) || policy.ValidFrom.After(time.Now()) || privateinput.CheckDirectory(policy.StateDir, true) != nil {
		return empty, ErrE2HTTPHost
	}
	if !validE2HTTPFileAuth(policy.Auth, policy.HeaderMode, policy.Rules) {
		return empty, ErrE2HTTPHost
	}
	if policy.Workflow != nil {
		if policy.Workflow.Format != diagnosisflow.Version || !input.Workflow.Matches(policy.Workflow.Mode, policy.Workflow.InputSHA256) {
			return empty, ErrE2HTTPHost
		}
	} else if input.Workflow != nil {
		return empty, ErrE2HTTPHost
	}
	// 제품 실행은 시도별 화면용 경로 패턴을 상태 디렉터리에 남긴다(작업대 e2-flow path_pattern). 평가 회차와 이미 관찰
	// 훅을 받은 실행(수용시험 경로 투영)은 그대로 둔다. 허용 판단·송신·감사·계수에는 영향이 없다.
	productPatterns := input.AttemptPathObserver == nil && policy.Evaluation == nil
	if policy.ApprovalMode != e2proxy.ApprovalConfirmEveryRequest && policy.ApprovalMode != e2proxy.ApprovalPreauthorizedScope && policy.ApprovalMode != e2proxy.ApprovalWorkbenchV1 {
		return empty, ErrE2HTTPHost
	}
	if (policy.ApprovalMode == e2proxy.ApprovalConfirmEveryRequest || policy.ApprovalMode == e2proxy.ApprovalWorkbenchV1) && (input.RequestApprovals == nil || policy.OwnerRef != "ARTEX") {
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
		rules = append(rules, e2proxy.Rule{Ref: rule.Ref, Purpose: rule.Purpose, Origin: rule.Origin, ConnectIP: ip, TLSSPKISHA256: rule.TLSSPKISHA256, PathPrefix: rule.PathPrefix, PathScopeRef: rule.PathScopeRef, Methods: append([]string(nil), rule.Methods...), CredentialHeaders: credentials})
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
	limits := e2proxy.Limits{MaxAttempts: policy.Limits.MaxAttempts, MaxCalls: policy.Limits.MaxCalls, MaxConcurrent: policy.Limits.MaxConcurrent, MaxBodyBytes: policy.Limits.MaxBodyBytes, MaxResponseBodyBytes: policy.Limits.MaxResponseBodyBytes, MaxResponseScanBytes: policy.Limits.MaxResponseScanBytes, RequestTimeout: requestTimeout, CallTimeout: callTimeout, MaxDenials: policy.Limits.MaxDenials, MaxTransient: policy.Limits.MaxTransient, MaxUnknown: policy.Limits.MaxUnknown}
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
		if input.CheckAuthority != nil && input.CheckAuthority() != nil {
			return ErrE2HTTPHost
		}
		if checkCtx == nil || checkCtx.Err() != nil || authorityCtx.Err() != nil || input.Authority.CheckRoot(checkCtx) != nil || input.Authority.AuthorizePolicy(checkCtx, digest) != nil {
			return ErrE2HTTPHost
		}
		return nil
	}
	var transmissionCheck func() error
	if input.CheckAuthority != nil {
		transmissionCheck = func() error { return current(authorityCtx) }
	}
	reset := func() (string, error) {
		if resetBinding != nil {
			return resetBinding()
		}
		return policy.ResetRef, nil
	}
	authorize := func(checkCtx context.Context, request E2HTTPRequest) error {
		resetRef, resetErr := reset()
		if resetErr != nil {
			return ErrE2HTTPHost
		}
		if current(checkCtx) != nil || request.TaskIDNumber <= 0 || request.IntentID <= 0 || request.ExplorationID <= 0 || strconv.FormatInt(request.TaskIDNumber, 10) != request.TaskID {
			return ErrE2HTTPHost
		}
		scope := transmit.Scope{TaskRef: request.TaskID, RunRef: policy.RunRef, CaseRef: policy.CaseRef, ResetRef: resetRef, PolicyRef: policy.PolicyRef}
		if input.Supervisor.AuthorizeExecution(checkCtx, scope, request.IntentID) != nil {
			return ErrE2HTTPHost
		}
		return nil
	}
	var acquireMu sync.Mutex
	context.AfterFunc(authorityCtx, func() { acquireMu.Lock(); clear(auditKey); acquireMu.Unlock() })
	claimed := false
	// 다시 로그인(refresh_session) 횟수는 실행 전체(실행 판 하나, acquire 한 번)에서 계정당 센다.
	refreshes := &e2HTTPRefreshCounter{}
	acquire := func(taskctx context.Context, request E2HTTPRequest) (E2HTTPRunOptions, error) {
		acquireMu.Lock()
		defer acquireMu.Unlock()
		if claimed || authorize(taskctx, request) != nil {
			return E2HTTPRunOptions{}, ErrE2HTTPHost
		}
		diagnosisRequests, allowanceErr := e2HTTPDiagnosisRequests(policy.MaxRequests, request)
		if allowanceErr != nil {
			return E2HTTPRunOptions{}, allowanceErr
		}
		resetRef, resetErr := reset()
		if resetErr != nil {
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
		ledger, createErr := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: diagnosisRequests})
		if createErr != nil {
			return E2HTTPRunOptions{}, ErrE2HTTPHost
		}
		grantKey := budget.Key{TaskRef: request.TaskID, BudgetRef: policy.BudgetRef}
		budgetBinding := budget.Binding{PolicyRef: policy.PolicyRef, ResetRef: resetRef}
		if ledger.Register(budget.Grant{Key: grantKey, Binding: budgetBinding, MaxRequests: diagnosisRequests, ValidFrom: policy.ValidFrom, ValidUntil: policy.ValidUntil}, time.Now()) != nil {
			return E2HTTPRunOptions{}, ErrE2HTTPHost
		}
		journal, createErr := e2proxy.CreateJournal(policy.StateDir, "events.jsonl", auditKey)
		if createErr != nil {
			return E2HTTPRunOptions{}, ErrE2HTTPHost
		}
		release := func(context.Context) error { return journal.Close() }
		// accountAuth는 인증 준비가 돈 뒤의 계정별 결과다. 그 뒤에 실행 획득이 실패해도 실행 결과에 싣도록 실패 반환에도
		// 넣는다(계약 §3.4: 실행을 잡지 못하고 실패해도 인증 준비가 돌았으면 싣는다, #532 이동찬 리뷰).
		var accountAuth map[string]string
		failed := func(execution *e2proxy.Execution, runner *e2runner.Runner) (E2HTTPRunOptions, error) {
			return E2HTTPRunOptions{Runner: newLocalE2HTTPRun(runner), Execution: execution, Release: release, AccountAuth: accountAuth}, ErrE2HTTPHost
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
			if policy.ApprovalMode == e2proxy.ApprovalConfirmEveryRequest || policy.ApprovalMode == e2proxy.ApprovalWorkbenchV1 {
				gate = input.RequestApprovals
			}
			recorder = e2HTTPJournalObserver{next: recorder, observe: input.RequestApprovals.observe}
		}
		// 표시용 패턴은 이 실행의 보호·가림 값(비밀번호·세션)이 든 조각과 결속 시험 계정 사용자명과 같은 조각을 가린다
		// (S09·절대 규칙 2). 실행을 만들기 전에 관찰 훅을 넘기므로 실행 참조는 만든 뒤 채우고, 그 전에는 줄을 쓰지 않는다.
		var patternExecution atomic.Pointer[e2proxy.Execution]
		var patternUsernames atomic.Pointer[map[string]bool]
		observer := input.AttemptPathObserver
		if productPatterns {
			observer = productAttemptPatterns(policy.StateDir, func() func(string) bool {
				e := patternExecution.Load()
				if e == nil {
					return nil
				}
				names := patternUsernames.Load()
				return func(segment string) bool {
					return string(e.RedactOutput([]byte(segment))) != segment || names != nil && (*names)[segment]
				}
			})
		}
		execution, createErr := e2proxy.NewExecution(taskctx, e2proxy.Config{RequestBoundary: workflowBoundary(input.Workflow), CheckAuthority: transmissionCheck, ApprovalMode: policy.ApprovalMode, RequestApproval: gate, Evaluation: policy.Evaluation, HeaderMode: policy.HeaderMode, Scope: transmit.Scope{TaskRef: request.TaskID, RunRef: policy.RunRef, CaseRef: policy.CaseRef, ResetRef: resetRef, PolicyRef: policy.PolicyRef}, IntentID: request.IntentID, OwnerRef: policy.OwnerRef, PolicyRevision: policy.PolicyRevision, AccountRef: policy.AccountRef, SessionRef: policy.SessionRef, ExpiresAt: policy.ValidUntil, Rules: rules, Budget: transmit.CumulativeBudget{Ledger: ledger, Key: grantKey, Binding: budgetBinding}, Recorder: recorder, RootCAs: upstreamRoots, Limits: limits, AttemptPathObserver: observer})
		if createErr != nil {
			return failed(nil, nil)
		}
		patternExecution.Store(execution)
		if policy.Auth != nil {
			names := e2HTTPAccountUsernames(dir, *policy.Auth)
			patternUsernames.Store(&names)
		}
		if input.RequestApprovals != nil {
			input.RequestApprovals.attachExecution(request.TaskIDNumber, execution)
		}
		callRoot := filepath.Join(policy.StateDir, "calls")
		if e2HTTPCreatePrivateDirectory(callRoot) != nil {
			return failed(execution, nil)
		}
		runner, createErr := e2runner.New(e2runner.Config{Execution: execution, Helper: policy.Helper, RootDir: callRoot, CACert: filepath.Join(dir, policy.CACert.File), Proxy: proxy, CommandTimeout: workerTimeout, DrainTimeout: drainTimeout, OutputLimit: policy.Runtime.OutputLimit,
			// 같은 intent의 Bash 호출 사이에 /work를 잇는다(C29, docs/workspace-decision.md).
			PersistWorkspace: true})
		if createErr != nil || current(taskctx) != nil {
			return failed(execution, runner)
		}
		// 인증 준비(C14): 계정마다 로그인 요청을 E② 인증 준비 규칙으로 한 번 보낸다. 세션 값은 출력 가림 전용 값으로 등록되고
		// 실행 메모리에만 있다. 결과(값 없음)는 계정별로 실행 기록에 남긴다.
		var auth E2HTTPAuthResult
		if policy.Auth != nil {
			origin := ""
			for _, rule := range policy.Rules {
				if rule.Ref == policy.Auth.RuleRef {
					origin = rule.Origin
				}
			}
			var authErr error
			auth, authErr = e2HTTPAuthPrep(taskctx, execution, dir, origin, *policy.Auth)
			accountAuth = auth.AccountAuth
			if authErr != nil || current(taskctx) != nil {
				return failed(execution, runner)
			}
		}
		// 인증된 시험 계정이 없으면 이 실행은 계정 없이 진행한다. 첫 모델 요청 전에 E②를 preserve처럼 묶는다(C14 결정 5·6).
		if policy.HeaderMode == e2proxy.HeaderTestAccountPassthrough && len(auth.Sessions) == 0 {
			execution.DisableTestAccountPassthrough()
		}
		if input.OnAcquire != nil {
			verifierKey := append([]byte(nil), auditKey...)
			acquireErr := input.OnAcquire(taskctx, E2HTTPAcquired{Execution: execution, Runner: runner, Journal: journal, JournalDirectory: policy.StateDir, JournalName: "events.jsonl", AuditKey: verifierKey, PolicyFingerprintSHA256: fingerprint, PolicyConfigSHA256: policyConfigSHA, HeaderMode: policy.HeaderMode, Evaluation: e2HTTPCloneEvaluation(policy.Evaluation)})
			clear(verifierKey)
			if acquireErr != nil || current(taskctx) != nil {
				return failed(execution, runner)
			}
		}
		var refresh func(context.Context, string) (http.Header, string)
		sessionStore := newE2HTTPSessionStore(auth.Sessions)
		programCalls := newE2HTTPProgramCalls(auth.CallRefs...)
		if policy.Auth != nil && policy.HeaderMode == e2proxy.HeaderTestAccountPassthrough && len(auth.Sessions) > 0 {
			authPolicy, origin := *policy.Auth, ""
			for _, rule := range policy.Rules {
				if rule.Ref == authPolicy.RuleRef {
					origin = rule.Origin
				}
			}
			refresh = func(ctx context.Context, label string) (http.Header, string) {
				// 성공하면 다음 Worker도 새 세션으로 시작한다. 같은 계정 다시 로그인은 줄을 세운다.
				return sessionStore.refresh(ctx, label, func() (http.Header, string) {
					return e2HTTPRefreshSession(ctx, execution, dir, origin, authPolicy, refreshes, label)
				})
			}
		}
		return E2HTTPRunOptions{Runner: newLocalE2HTTPRun(runner), Execution: execution, Release: release, ObserveResult: input.ObserveResult, AccountAuth: auth.AccountAuth, Sessions: auth.Sessions, RefreshSession: refresh, sessionStore: sessionStore, programCalls: programCalls}, nil
	}
	// 진단 Worker의 insert_assets가 받을 수 있는 자산 범위다(C24). 검사를 거친 정책 규칙의 origin만 쓴다.
	approvedOrigins := make([]string, 0, len(policy.Rules))
	for _, rule := range policy.Rules {
		approvedOrigins = append(approvedOrigins, rule.Origin)
	}
	options := E2HTTPRuntimeOptions{Workflow: input.Workflow, RequestApprovals: input.RequestApprovals, PolicyFingerprintSHA256: fingerprint, PolicyConfigSHA256: policyConfigSHA, HeaderMode: policy.HeaderMode, Evaluation: e2HTTPCloneEvaluation(policy.Evaluation), AuthorityContext: authorityCtx, CheckAuthority: current, AuthorizeIntent: authorize, Expected: expected, Acquire: acquire, RunRequests: policy.MaxRequests, MaxWorkerTurns: policy.Runtime.MaxWorkerTurns, MaxToolCalls: policy.Runtime.MaxToolCalls, WorkerTimeout: workerTimeout, MaxPlannerRounds: policy.Runtime.MaxPlannerRounds, MaxIntentRuns: policy.Runtime.MaxIntentRuns, DrainTimeout: drainTimeout, PlannerLimits: input.PlannerLimits, ApprovedOrigins: approvedOrigins}
	if current(ctx) != nil {
		clear(auditKey)
		return empty, ErrE2HTTPHost
	}
	options.PromptProjection = true
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

func workflowBoundary(c *diagnosisflow.Controller) e2proxy.RequestBoundary {
	if c == nil {
		return nil
	}
	return c
}

// e2HTTPDiagnosisRequests는 회차 승인 수 N에서 같은 회차 탐색 몫을 뺀 진단 E② 예산이다. 몫을 모르거나 남지 않으면
// 진단 실행을 만들지 않는다. 탐색과 진단이 각각 N을 받아 합계 2N이 되는 조립을 막는다(P1-E).
func e2HTTPDiagnosisRequests(runRequests uint64, request E2HTTPRequest) (uint64, error) {
	if runRequests == 0 || request.ExplorationUnknown || request.ExplorationRequests >= runRequests {
		return 0, ErrE2HTTPBudgetLimited
	}
	return runRequests - request.ExplorationRequests, nil
}
