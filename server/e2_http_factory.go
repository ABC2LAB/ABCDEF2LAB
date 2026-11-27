package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"github.com/Autumn-27/artex/e3contract"
	"log"
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
	"github.com/Autumn-27/artex/e2host"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/internal/privateinput"
	"github.com/Autumn-27/artex/loginattempt"
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
	HostSocket           string
	CaptureSelectedProof bool
	Workflow             *diagnosisflow.Controller
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
	// WebSearch는 진단 Planner·Worker의 원본 검색 도구 설정이다(루프 복원 결정안 §2-1).
	WebSearch agent.E2HTTPWebSearch
	// SealChecked는 E② Host가 봉인 뒤 근거 체크포인트마다 봉인 head 포함 여부를 확인한 결과를 기록한다(E③ 판정 저장 E1,
	// docs/contracts/e3-product-verdict-store.md §6). nil이면 기록하지 않는다(표시는 봉인 미확인).
	SealChecked func(context.Context, E2HTTPSealCheck) error
}

// E2HTTPSealCheck는 회차 하나의 봉인 확인 결과다. Host가 신뢰 저장소의 sealed head로 확인했다.
type E2HTTPSealCheck struct {
	TaskID int64
	RunRef string
	Result e2host.SealResult
}

// e2HTTPSealCheckRecordTimeout은 봉인 확인 결과 기록 한 번에 쓰는 시간 상한이다. 봉인은 이미 신뢰 저장소에 게시됐으므로
// 기록이 늦거나 멈춰도 회차 마감(Release → CloseTask → audit-close)을 붙잡지 않는다(#816 리뷰). 기록하지 못한 판정은
// 봉인 미확인으로 보인다. 행 하나를 넣는 DB 쓰기라 짧게 둔다. 시험이 줄인다.
var e2HTTPSealCheckRecordTimeout = 10 * time.Second

// recordE2HTTPSealCheck는 봉인 확인 기록 훅을 부르되 e2HTTPSealCheckRecordTimeout 안에 끝나지 않으면 기다리지 않는다. 호출자의
// 취소와는 떼어(중지 정리 문맥이 이미 끝났어도 성공한 봉인의 결과는 남기려고) 별도 기한을 둔다. 훅이 문맥을 무시하고 멈춰
// 있어도 반환한다(그 훅은 끝날 때까지 혼자 돈다).
func recordE2HTTPSealCheck(parent context.Context, hook func(context.Context, E2HTTPSealCheck) error, check E2HTTPSealCheck) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), e2HTTPSealCheckRecordTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- hook(ctx, check) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// e2HostE3Verifier는 E② Host 클라이언트를 E③ 검증자로 쓴다. 근거 체크포인트도 돌려준다(e2proxy.E3CheckpointVerifier).
type e2HostE3Verifier struct{ host *e2host.Client }

func (v e2HostE3Verifier) Verified(ctx context.Context, b e3contract.RunBinding, ref e3contract.CallRef) (e2proxy.Event, error) {
	return v.host.Verified(ctx, b, ref)
}

func (v e2HostE3Verifier) VerifiedCheckpoint(ctx context.Context, b e3contract.RunBinding, ref e3contract.CallRef) (e2proxy.Event, e2proxy.JournalCheckpoint, error) {
	got, err := v.host.VerifiedAt(ctx, b, ref)
	return got.Event, got.Checkpoint, err
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
	PathMatch       string                 `json:"path_match,omitempty"`
	ExactQuery      string                 `json:"exact_query,omitempty"`
	RequestHeaders  map[string]string      `json:"request_headers,omitempty"`
	StrictHeaders   bool                   `json:"strict_headers,omitempty"`
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
	// E③ 확정 단계 몫(사용자 결정 2026-11-18 ②): 실행 하나에서 모델 단계와 따로 떼어 둔 대상 요청 수와 시간.
	// 제품 실행 정책은 늘 둘 다 적는다(확정 단계가 꺼진 회차는 0). 칸이 없는 정책(설치 템플릿·이전 회차·평가 정책)은
	// 몫을 정하지 않은 것이고 지문에서도 0과 구분된다. 실행 정책(run)에서만 받는다.
	E3ConfirmationRequests  *uint64 `json:"e3_confirmation_requests,omitempty"`
	E3ConfirmationTimeoutMS *int64  `json:"e3_confirmation_timeout_ms,omitempty"`
}

// e3ConfirmationBehavior는 정책의 확정 단계 몫을 지문 입력으로 바꾼다. 칸이 없으면 nil, 하나만 있으면 거부한다.
func (l E2HTTPFileLimits) e3ConfirmationBehavior() (*e2export.E3ConfirmationShare, bool) {
	if l.E3ConfirmationRequests == nil && l.E3ConfirmationTimeoutMS == nil {
		return nil, true
	}
	if l.E3ConfirmationRequests == nil || l.E3ConfirmationTimeoutMS == nil || *l.E3ConfirmationTimeoutMS < 0 ||
		*l.E3ConfirmationTimeoutMS > MaxFirstDiagnosisTaskSeconds*1000 || (*l.E3ConfirmationRequests == 0) != (*l.E3ConfirmationTimeoutMS == 0) {
		return nil, false
	}
	return &e2export.E3ConfirmationShare{Requests: *l.E3ConfirmationRequests, Timeout: time.Duration(*l.E3ConfirmationTimeoutMS) * time.Millisecond}, true
}

// e3ConfirmationShare는 정책의 확정 단계 몫을 읽고 실행 상한 안인지 본다: 요청 몫은 요청 상한의 1/4, 시간 몫은 유효
// 창의 1/4 이하다. 정책 문서 검사(조회)와 실행 factory(런타임 로딩)가 같은 이 검사를 쓴다(#716 이동찬 리뷰).
func (p E2HTTPFilePolicy) e3ConfirmationShare() (*e2export.E3ConfirmationShare, bool) {
	share, ok := p.Limits.e3ConfirmationBehavior()
	if !ok {
		return nil, false
	}
	if share != nil && (share.Requests > p.MaxRequests/E3ConfirmationShareDivisor ||
		share.Timeout > p.ValidUntil.Sub(p.ValidFrom)/E3ConfirmationShareDivisor) {
		return nil, false
	}
	return share, true
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
	// ExcludedPaths는 워크플로 입력의 운영자 제외 경로다. E②가 진단 흐름과 별도로 거부한다(#627). 없으면 칸을 쓰지 않는다
	// (제외 경로 없는 정책의 바이트·지문은 그대로).
	ExcludedPaths []string `json:"excluded_paths,omitempty"`
}

// excludedPaths는 정책 워크플로 칸의 제외 경로다. 워크플로가 없으면 없다.
func (p E2HTTPFilePolicy) excludedPaths() []string {
	if p.Workflow == nil {
		return nil
	}
	return p.Workflow.ExcludedPaths
}

type E2HTTPFilePolicy struct {
	// LoginAttempts는 제품 회차의 로그인 시도 판정 설정이다(계약 docs/contracts/model-login-attempts.md §4·§5). 계정을 등록하지
	// 않은 회차도 등록 밖 아이디(기본 계정 시험 등)의 연속 실패 상한을 지키도록 인증 칸과 따로 둔다(사용자 결정 2026-11-26).
	LoginAttempts      *E2HTTPLoginAttemptLimits        `json:"login_attempts,omitempty"`
	Workflow           *E2HTTPWorkflowPolicy            `json:"workflow,omitempty"`
	ApprovalMode       e2proxy.ApprovalMode             `json:"approval_mode"`
	Format             string                           `json:"format"`
	RunRef             string                           `json:"run_ref"`
	ResetRef           string                           `json:"reset_ref"`
	PolicyRef          string                           `json:"policy_ref"`
	CaseRef            string                           `json:"case_ref"`
	PolicyRevision     string                           `json:"policy_revision"`
	BudgetRef          string                           `json:"budget_ref"`
	OwnerRef           string                           `json:"owner_ref"`
	AccountRef         string                           `json:"account_ref"`
	SessionRef         string                           `json:"session_ref"`
	ValidFrom          time.Time                        `json:"valid_from"`
	ValidUntil         time.Time                        `json:"valid_until"`
	MaxRequests        uint64                           `json:"max_requests"`
	HeaderMode         e2proxy.HeaderMode               `json:"header_mode"`
	SelectedAccountRef string                           `json:"selected_account_ref,omitempty"`
	Evaluation         *e2proxy.EvaluationCallerContext `json:"evaluation"`
	StateDir           string                           `json:"state_dir"`
	Helper             string                           `json:"helper"`
	HelperSHA256       string                           `json:"helper_sha256"`
	CACert             E2HTTPFilePin                    `json:"ca_cert"`
	CAKey              E2HTTPFilePin                    `json:"ca_key"`
	AuditKey           E2HTTPFilePin                    `json:"audit_key"`
	UpstreamCA         *E2HTTPFilePin                   `json:"upstream_ca,omitempty"`
	Rules              []E2HTTPFileRule                 `json:"rules"`
	// Auth는 인증 준비 칸이다(C14). 없으면 로그인하지 않는다.
	Auth *E2HTTPFileAuth `json:"auth,omitempty"`
	// 제품 실행의 동결된 쓰기 승인과 리셋 가능 대상 판이다. 확정 요청에만 적용한다.
	E3WriteConsent bool              `json:"e3_write_consent,omitempty"`
	E3Resettable   bool              `json:"e3_resettable,omitempty"`
	Limits         E2HTTPFileLimits  `json:"limits"`
	Proxy          E2HTTPFileProxy   `json:"proxy"`
	Runtime        E2HTTPFileRuntime `json:"runtime"`
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
func loadE2HTTPRuntimeOptions(ctx context.Context, input E2HTTPFileOptions, frozen []byte, resetBinding func() (string, error), hostConnector ...func(context.Context, string, transmit.Scope, string) (*e2host.Client, error)) (E2HTTPRuntimeOptions, error) {
	var empty E2HTTPRuntimeOptions
	connectHost := e2host.OpenClient
	if len(hostConnector) == 1 && hostConnector[0] != nil {
		connectHost = hostConnector[0]
	} else if len(hostConnector) > 1 {
		return empty, ErrE2HTTPHost
	}
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
	if strictJSON(raw, &policy) != nil || !validE2HTTPRuleSelectorFields(raw) || !validE2HTTPPasswordResponseField(raw) || policy.Format != E2HTTPFilePolicyFormat || len(policy.Rules) == 0 || len(policy.Rules) > 256 || policy.MaxRequests == 0 || !policy.ValidUntil.After(policy.ValidFrom) || !policy.ValidUntil.After(time.Now()) || policy.ValidFrom.After(time.Now()) || privateinput.CheckDirectory(policy.StateDir, true) != nil {
		return empty, ErrE2HTTPHost
	}
	if !validE2HTTPFileAuth(policy.Auth, policy.HeaderMode, policy.Rules) || !validE2HTTPLoginAttempts(policy) {
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
		rules = append(rules, e2proxy.Rule{Ref: rule.Ref, Purpose: rule.Purpose, Origin: rule.Origin, ConnectIP: ip, TLSSPKISHA256: rule.TLSSPKISHA256, PathPrefix: rule.PathPrefix, PathMatch: rule.PathMatch, ExactQuery: rule.ExactQuery, StrictHeaders: rule.StrictHeaders, Headers: selectedActionHeaders(rule.RequestHeaders), PathScopeRef: rule.PathScopeRef, Methods: append([]string(nil), rule.Methods...), CredentialHeaders: credentials})
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
	if e2proxy.ValidatePolicyTemplate(e2proxy.Config{ApprovalMode: policy.ApprovalMode, Scope: transmit.Scope{RunRef: policy.RunRef}, Rules: rules, ExcludedPaths: policy.excludedPaths(), Limits: limits, HeaderMode: policy.HeaderMode, PasswordResponse: e2HTTPPasswordResponse(policy.Auth), Evaluation: policy.Evaluation}) != nil {
		clear(auditKey)
		return empty, ErrE2HTTPHost
	}
	share, shareOK := policy.e3ConfirmationShare()
	if !shareOK {
		clear(auditKey)
		return empty, ErrE2HTTPHost
	}
	fingerprint, fingerprintErr := e2export.PolicyFingerprint(e2proxy.Config{ApprovalMode: policy.ApprovalMode, Scope: transmit.Scope{RunRef: policy.RunRef}, Rules: rules, ExcludedPaths: policy.excludedPaths(), Limits: limits, HeaderMode: policy.HeaderMode, PasswordResponse: e2HTTPPasswordResponse(policy.Auth), Evaluation: policy.Evaluation}, policy.MaxRequests, e2export.PolicyBehavior{MaxWorkerTurns: policy.Runtime.MaxWorkerTurns, MaxToolCalls: policy.Runtime.MaxToolCalls, WorkerTimeout: workerTimeout, MaxPlannerRounds: policy.Runtime.MaxPlannerRounds, MaxIntentRuns: policy.Runtime.MaxIntentRuns, DrainTimeout: drainTimeout, OutputLimit: policy.Runtime.OutputLimit, ProxyHeaderTimeout: headerTimeout, ProxyRequestTimeout: proxyTimeout, ProxyMaxHeaderBytes: policy.Proxy.MaxHeaderBytes, ProxyMaxConnections: policy.Proxy.MaxConnections, E3Confirmation: share})
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
	// 다시 로그인 실패·간격은 실행 판 하나(acquire 한 번)의 계정별 세션 저장소에서 보호한다.
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
		scope := transmit.Scope{TaskRef: request.TaskID, RunRef: policy.RunRef, CaseRef: policy.CaseRef, ResetRef: resetRef, PolicyRef: policy.PolicyRef}
		var hostRecorder *e2host.Client
		if input.HostSocket != "" {
			if input.HostSocket != e2host.ProductSocketPath || policy.Evaluation != nil {
				return E2HTTPRunOptions{}, ErrE2HTTPHost
			}
			hostRecorder, createErr = connectHost(taskctx, input.HostSocket, scope, policy.PolicyRevision)
			if createErr != nil {
				return E2HTTPRunOptions{}, ErrE2HTTPHost
			}
		}
		// Host 모드의 로컬 저널은 화면 사본이다. 확정 단계 사건의 화면 투영도 싣는다(e2HTTPV3MirrorRecorder).
		createLocal := e2proxy.CreateJournal
		if hostRecorder != nil {
			createLocal = e2proxy.CreateScreenJournal
		}
		// E②를 잡았다는 기록을 화면용 저널보다 먼저 남긴다(E2AcquireRecordFile, 계약 workbench-live-data §4). 작업대 e2-flow가
		// '처음부터 E②를 잡지 않은 실행'과 '저널이 없어진 실행'을 이 기록으로 가른다. 남기지 못하면 저널을 만들지 않고 E②를
		// 잡지 않는다(아래 실패 길과 같다).
		var journal *e2proxy.Journal
		acquireRecord, createErr := json.Marshal(struct {
			Version        string         `json:"version"`
			Scope          transmit.Scope `json:"scope"`
			PolicyRevision string         `json:"policy_revision"`
		}{e2AcquireRecordVersion, scope, policy.PolicyRevision})
		if createErr == nil {
			createErr = e2HTTPRecordAcquire(policy.StateDir, append(acquireRecord, '\n'))
		}
		if createErr == nil {
			journal, createErr = createLocal(policy.StateDir, "events.jsonl", auditKey)
		}
		if createErr != nil {
			if hostRecorder != nil {
				// 아직 실행이나 송신자가 없다. 취소된 준비 문맥과 분리하여
				// 빈 Host 저널을 봉인한다. ACK 실패는 미봉인으로 남긴다.
				sealCtx, cancel := context.WithTimeout(context.WithoutCancel(taskctx), drainTimeout)
				if hostRecorder.Seal(sealCtx) != nil {
					hostRecorder.Abort()
				}
				cancel()
			}
			return E2HTTPRunOptions{}, ErrE2HTTPHost
		}
		release := func(ctx context.Context) error {
			if err := journal.Close(); err != nil {
				if hostRecorder != nil {
					hostRecorder.Abort()
				}
				return err
			}
			if hostRecorder != nil {
				result, err := hostRecorder.SealChecked(ctx)
				if err != nil {
					return err
				}
				// 봉인은 이미 저장소에 게시됐다. 확인 결과를 남기지 못해도 봉인 성공이고, 판정 표시는 봉인 미확인이다.
				if result != nil && input.SealChecked != nil {
					if recordErr := recordE2HTTPSealCheck(ctx, input.SealChecked, E2HTTPSealCheck{TaskID: request.TaskIDNumber, RunRef: scope.RunRef, Result: *result}); recordErr != nil {
						log.Printf(logT("[e2-host] task %d 봉인 확인 기록 실패(판정은 봉인 미확인)"), request.TaskIDNumber)
					}
				}
			}
			return nil
		}
		// accountAuth는 인증 준비가 돈 뒤의 계정별 결과다. 그 뒤에 실행 획득이 실패해도 실행 결과에 싣도록 실패 반환에도
		// 넣는다(계약 §3.4: 실행을 잡지 못하고 실패해도 인증 준비가 돌았으면 싣는다, #532 이동찬 리뷰).
		var accountAuth map[string]string
		failed := func(execution *e2proxy.Execution, runner *e2runner.Runner) (E2HTTPRunOptions, error) {
			// Runtime이 Stop·Close와 활성 호출 drain을 확인한 뒤에만
			// Release를 호출한다. 실패한 준비도 감사 head를 봉인해 다음
			// 회차를 허용하되, 종료 불명·ACK 실패는 미봉인으로 차단한다.
			return E2HTTPRunOptions{Runner: newLocalE2HTTPRun(runner), Execution: execution, Release: release, AccountAuth: accountAuth}, ErrE2HTTPHost
		}
		recorder := e2proxy.Recorder(journal)
		if hostRecorder != nil {
			recorder = e2HTTPV3MirrorRecorder{host: hostRecorder, legacy: journal}
		}
		if input.ObserveEvent != nil {
			recorder = e2HTTPJournalObserver{next: recorder, observe: input.ObserveEvent}
		}
		var gate e2proxy.RequestApprovalGate
		if input.RequestApprovals != nil {
			if input.RequestApprovals.bind(taskctx, request.TaskIDNumber, "ARTEX", policy.RunRef, policy.Rules) != nil {
				return failed(nil, nil)
			}
			if policy.ApprovalMode == e2proxy.ApprovalConfirmEveryRequest || policy.ApprovalMode == e2proxy.ApprovalWorkbenchV1 {
				gate = input.RequestApprovals
			}
			recorder = e2HTTPJournalObserver{next: recorder, observe: input.RequestApprovals.observe, observeE3: input.RequestApprovals.observeE3}
		}
		// 승인 틀의 이름 조각도 실행 자격값이나 시험 계정명과 같으면 가린다.
		var patternExecution atomic.Pointer[e2proxy.Execution]
		var patternUsernames atomic.Pointer[map[string]bool]
		var accountSessionStore atomic.Pointer[e2HTTPSessionStore]
		var accountLoginAttempts atomic.Pointer[loginattempt.Tracker]
		var resolveSentAccount func(http.Header) string
		var selectedSessionHeaders func() (http.Header, bool)
		credentialHeader := ""
		if policy.Auth != nil {
			credentialHeader = policy.Auth.Login.Header
			accountRefs := make(map[string]string, len(policy.Auth.Accounts))
			for _, account := range policy.Auth.Accounts {
				accountRefs[account.Label] = CandidateAccountRevision(account.Label, account.Credential.SHA256)
			}
			resolveSentAccount = func(sent http.Header) string {
				// 모델 직접 로그인 회차: 모델 요청의 계정은 성공한 로그인 세션을 실었는지로 정한다(값이 정확히 같은 헤더가 아니어도,
				// 브라우저·curl이 다른 쿠키를 같이 실어도). 성공하지 않은 세션이나 두 계정 세션은 귀속하지 않는다.
				if tracker := accountLoginAttempts.Load(); tracker != nil {
					if label := tracker.AccountOf(sent); label != "" {
						return accountRefs[label]
					}
				}
				store := accountSessionStore.Load()
				if store == nil {
					return ""
				}
				label, ok := store.matchSentAccount(sent, credentialHeader)
				if !ok {
					return ""
				}
				return accountRefs[label]
			}
			if policy.SelectedAccountRef != "" && len(policy.Auth.Accounts) == 1 &&
				accountRefs[policy.Auth.Accounts[0].Label] == policy.SelectedAccountRef {
				label := policy.Auth.Accounts[0].Label
				selectedSessionHeaders = func() (http.Header, bool) {
					store := accountSessionStore.Load()
					if store == nil {
						return nil, false
					}
					header := store.snapshot()[label]
					return header, len(header) > 0
				}
			}
		}
		observer := input.AttemptPathObserver
		if productPatterns {
			templates := make(map[string]string, len(policy.Rules))
			for _, rule := range policy.Rules {
				templates[rule.Ref] = rule.PathPrefix
			}
			observer = productAttemptPatterns(policy.StateDir, templates, func() func(string) bool {
				e := patternExecution.Load()
				if e == nil {
					return nil
				}
				names := patternUsernames.Load()
				return func(segment string) bool {
					return string(e.RedactRecord([]byte(segment))) != segment || names != nil && (*names)[segment]
				}
			})
		}
		auditVersion := e2proxy.Version
		if hostRecorder != nil {
			auditVersion = e2proxy.JournalVersionV3
		}
		e3Requests := uint64(0)
		if share != nil {
			e3Requests = share.Requests
		}
		var e3Verifier e2proxy.E3Verifier
		if hostRecorder != nil {
			e3Verifier = e2HostE3Verifier{host: hostRecorder}
		}
		execution, createErr := e2proxy.NewExecution(taskctx, e2proxy.Config{AuditVersion: auditVersion, E3Verifier: e3Verifier, E3ConfirmationRequests: e3Requests,
			E3WriteConsent: policy.E3WriteConsent, E3Resettable: policy.E3Resettable, RequestBoundary: workflowBoundary(input.Workflow), CheckAuthority: transmissionCheck, ApprovalMode: policy.ApprovalMode, RequestApproval: gate, Evaluation: policy.Evaluation, HeaderMode: policy.HeaderMode, TargetCredentialHeader: credentialHeader, ResolveSentAccount: resolveSentAccount, SelectedSessionHeaders: selectedSessionHeaders, SelectedAccountRef: policy.SelectedAccountRef, PasswordResponse: e2HTTPPasswordResponse(policy.Auth), Scope: scope, IntentID: request.IntentID, OwnerRef: policy.OwnerRef, PolicyRevision: policy.PolicyRevision, AccountRef: policy.AccountRef, SessionRef: policy.SessionRef, ExpiresAt: policy.ValidUntil, Rules: rules, ExcludedPaths: policy.excludedPaths(), Budget: transmit.CumulativeBudget{Ledger: ledger, Key: grantKey, Binding: budgetBinding}, Recorder: recorder, RootCAs: upstreamRoots, Limits: limits, AttemptPathObserver: observer})
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
		if request.WorkerCount < 0 {
			return failed(execution, nil)
		}
		slots := 1
		if request.WorkerCount > 0 {
			slots = min(request.WorkerCount, limits.MaxConcurrent)
		}
		runner, createErr := e2runner.New(e2runner.Config{BashSlots: slots, Execution: execution, Helper: policy.Helper, RootDir: callRoot, CACert: filepath.Join(dir, policy.CACert.File), Proxy: proxy, CommandTimeout: workerTimeout, DrainTimeout: drainTimeout, OutputLimit: policy.Runtime.OutputLimit,
			// 같은 intent의 Bash 호출 사이에 /work를 잇는다(C29, docs/workspace-decision.md).
			PersistWorkspace: true})
		if createErr != nil || current(taskctx) != nil {
			return failed(execution, runner)
		}
		conditions, createErr := json.Marshal(struct {
			Version                     string                 `json:"version"`
			Scope                       transmit.Scope         `json:"scope"`
			PolicyRevision              string                 `json:"policy_revision"`
			WorkerCount                 int                    `json:"worker_count"`
			ProcessLimits               e2runner.ProcessLimits `json:"process_limits"`
			QueueConsumesCommandTimeout bool                   `json:"queue_consumes_command_timeout"`
		}{"e2-runtime-conditions/v1", execution.Snapshot().Scope, policy.PolicyRevision, request.WorkerCount, runner.ProcessLimits(), true})
		if createErr != nil || e2HTTPRecordRuntimeConditions(policy.StateDir, append(conditions, '\n')) != nil {
			return failed(execution, runner)
		}
		// 모델 직접 로그인(사용자 결정 2026-11-26, docs/contracts/model-login-attempts.md): 프로그램이 실행 시작 때 로그인하지
		// 않는다. 결속된 등록 계정의 자격을 읽어 모델 지침에 넣고, 모델이 대상의 로그인 화면·API로 직접 로그인해 받은 세션을
		// 스스로 실어 보낸다. 확정 단계 다시 로그인(E③)은 아래 sessionStore.relogin이 지금 로그인 방식으로 한다.
		var auth E2HTTPAuthResult
		var credentials []agent.E2HTTPAccountCredential
		switch {
		case policy.Auth != nil && (!policy.Auth.ModelLogin || policy.SelectedAccountRef != ""):
			// 인증 준비(C14): 프로그램이 계정마다 로그인 요청을 E② 인증 준비 규칙으로 한 번 보낸다. 모델 로그인을 켜지 않은
			// 정책(이전 결속)과 선택 후보 검증 회차(#950, 모델이 없는 프로그램 회차)다. 세션 값은 출력 가림 전용 값으로 등록되고
			// 실행 메모리에만 있다. 결과(값 없음)는 계정별로 실행 기록에 남긴다.
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
		case policy.Auth != nil:
			var unread map[string]string
			credentials, unread = e2HTTPAccountCredentials(taskctx, execution, dir, *policy.Auth)
			if len(unread) > 0 {
				accountAuth = unread
			}
			if current(taskctx) != nil {
				return failed(execution, runner)
			}
		}
		// 쓸 계정이 없으면(자격을 못 읽었거나 선택 검증 로그인이 안 됨) 이 실행은 계정 없이 진행한다. 첫 요청 전에 E②를
		// preserve처럼 묶는다(C14 결정 5·6).
		if policy.HeaderMode == e2proxy.HeaderTestAccountPassthrough && len(credentials) == 0 && len(auth.Sessions) == 0 {
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
		sessionStore := newE2HTTPSessionStore(auth.Sessions, execution.Snapshot().Scope.RunRef, nil)
		accountSessionStore.Store(sessionStore)
		// 모델 직접 로그인 회차는 모델 요청마다 로그인 시도를 판정한다(계약 docs/contracts/model-login-attempts.md). Worker가
		// 첫 요청을 보내기 전에 붙인다. 판정기를 못 만들면 상한을 지킬 수 없으므로 실행을 열지 않는다.
		var loginAttempts *loginattempt.Tracker
		var prober *loginContrastProber
		// 계정 등록 여부와 상관없이 판정 설정이 있는 제품 회차면 붙인다. 이전 결속(프로그램 로그인)과 선택 후보 검증 회차는 제외다.
		if policy.LoginAttempts != nil && policy.SelectedAccountRef == "" && (policy.Auth == nil || policy.Auth.ModelLogin) {
			var trackerErr error
			prober = newLoginContrastProber(nil)
			loginAttempts, trackerErr = newE2HTTPLoginAttempts(taskctx, policy, credentials, sessionStore, prober)
			if trackerErr != nil {
				return failed(execution, runner)
			}
			execution.AttachLoginAttempts(loginAttempts)
			accountLoginAttempts.Store(loginAttempts)

		}
		programCalls := newE2HTTPProgramCalls(auth.CallRefs...)
		if prober != nil {
			// 프로그램 대조 요청은 실행 문맥 동안만 보낸다(마감·취소면 멈춤). 호출 참조는 프로그램 호출로 마감 대조에서 뺀다.
			prober.execution, prober.tracker, prober.calls = execution, loginAttempts, programCalls
			go prober.run(taskctx)
		}
		if policy.Auth != nil && policy.HeaderMode == e2proxy.HeaderTestAccountPassthrough && (len(credentials) > 0 || len(auth.Sessions) > 0) {
			authPolicy, origin := *policy.Auth, ""
			for _, rule := range policy.Rules {
				if rule.Ref == authPolicy.RuleRef {
					origin = rule.Origin
				}
			}
			// 모델 회차에는 Worker의 refresh_session이 없다(모델이 직접 다시 로그인한다, 2026-11-26). 선택 후보 검증 회차(#950)는
			// 프로그램이 같은 계정 세션을 다시 받아야 하므로(두 번째 재검증 등) 그 경로를 남긴다.
			if len(auth.Sessions) > 0 {
				refresh = func(ctx context.Context, label string) (http.Header, string) {
					// 성공하면 다음 호출도 새 세션으로 시작한다. 같은 계정 다시 로그인은 줄을 세운다.
					return sessionStore.refresh(ctx, label, func() (http.Header, string) {
						return e2HTTPRefreshSession(ctx, execution, dir, origin, authPolicy, sessionStore.refreshes, programCalls, label, false)
					})
				}
			}
			// 확정 단계 직전 다시 로그인은 지금 로그인 방식으로 프로그램이 하고, 확정 몫을 쓰는 호출로 연다(사용자 결정
			// 2026-11-20 (가)). 처음 세션이 없어도 연다: 모델 회차의 확정 단계 계정 세션은 이 다시 로그인으로만 생긴다(모델 로그인
			// 기록 재사용이 들어오기 전까지, 계약 §7).
			sessionStore.relogin = func(ctx context.Context, label string) (http.Header, string) {
				return sessionStore.refresh(ctx, label, func() (http.Header, string) {
					return e2HTTPRefreshSession(ctx, execution, dir, origin, authPolicy, sessionStore.refreshes, programCalls, label, true)
				})
			}
		}
		var verifySelected func(context.Context, e2host.ObservationRef) (e2host.Verification, error)
		if hostRecorder != nil {
			verifySelected = hostRecorder.ObservedAt
		}
		return E2HTTPRunOptions{Runner: newLocalE2HTTPRun(runner), Execution: execution, Release: release, ObserveResult: input.ObserveResult, VerifySelected: verifySelected, AccountAuth: accountAuth, Sessions: auth.Sessions, RefreshSession: refresh, Credentials: credentials, LoginAttempts: loginAttempts, sessionStore: sessionStore, programCalls: programCalls}, nil
	}
	// 진단 Worker의 insert_assets는 검사를 거친 일반 규칙과 인증 준비 규칙의 origin을 쓴다(C24).
	approvedOrigins := make([]string, 0, len(policy.Rules))
	for _, rule := range policy.Rules {
		if rule.Purpose == "" || rule.Purpose == e2proxy.PurposeAuthPrep {
			approvedOrigins = append(approvedOrigins, rule.Origin)
		}
	}
	options := E2HTTPRuntimeOptions{Workflow: input.Workflow, CaptureSelectedProof: input.CaptureSelectedProof, RequestApprovals: input.RequestApprovals, PolicyFingerprintSHA256: fingerprint, PolicyConfigSHA256: policyConfigSHA, HeaderMode: policy.HeaderMode, Evaluation: e2HTTPCloneEvaluation(policy.Evaluation), AuthorityContext: authorityCtx, CheckAuthority: current, AuthorizeIntent: authorize, Expected: expected, Acquire: acquire, RunRequests: policy.MaxRequests, MaxWorkerTurns: policy.Runtime.MaxWorkerTurns, MaxToolCalls: policy.Runtime.MaxToolCalls, WorkerTimeout: workerTimeout, MaxPlannerRounds: policy.Runtime.MaxPlannerRounds, MaxIntentRuns: policy.Runtime.MaxIntentRuns, DrainTimeout: drainTimeout, PlannerLimits: input.PlannerLimits, WebSearch: input.WebSearch, ApprovedOrigins: approvedOrigins}
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
	// observeE3는 확정 단계 사건 중 이 관찰자가 다뤄야 하는 것만 받는다(승인 이력 갱신). nil이면 확정 사건을 넘기지
	// 않는다. 확정 사건은 일반 observe에 넘기지 않는다: 평가 관찰기도 이 관찰자를 쓰고 그 오류는 감사 실패로 번진다.
	// 실패를 반환하지 않으므로 확정 사건 기록을 실패로 만들지 않는다.
	observeE3 func(context.Context, e2proxy.Event)
}

// 제품 화면의 v2 흐름(화면 사본)은 Host가 ACK한 행을 투영한다. E③ 확정 단계 행은 E3Audit을 뺀 화면 투영
// (e2proxy.E3ScreenProjection)으로 싣는다. 판정·근거 검증은 계속 Host v3 저널만 쓴다. 사본에 확정 행이 없으면 작업대
// 보낸 요청 총계·요청 목록에서 확정 요청이 빠졌다(2026-11-25 화면 회차, 사용자·이동찬 결정 (가)).
type e2HTTPV3MirrorRecorder struct {
	host   e2proxy.Recorder
	legacy *e2proxy.Journal
}

func (r e2HTTPV3MirrorRecorder) Record(ctx context.Context, event e2proxy.Event) error {
	if r.host == nil || r.legacy == nil {
		return e2proxy.ErrAudit
	}
	if err := r.host.Record(ctx, event); err != nil {
		return err
	}
	if e3ProgramEvent(event) {
		event = e2proxy.E3ScreenProjection(event)
	}
	// 사본은 Host v3 순번이 아니라 자기 MAC 체인의 다음 순번을 쓴다(11/20 회차 마감 실패에서 고침). Host v3 저널이 권위
	// 원본이다. 사본 기록 실패는 일반 행과 같게 감사 실패다(사본이 빠지면 총계가 저널과 맞지 않는다). 기록은 실행의 기록
	// 잠금으로 직렬화된다(e2proxy Execution.recordEvent).
	event.Version = e2proxy.Version
	event.Index = r.legacy.Checkpoint().Sequence + 1
	return r.legacy.Record(ctx, event)
}

func (r e2HTTPJournalObserver) Record(ctx context.Context, event e2proxy.Event) error {
	if err := r.next.Record(ctx, event); err != nil {
		return err
	}
	if e3ProgramEvent(event) {
		if r.observeE3 != nil {
			r.observeE3(ctx, event)
		}
		return nil
	}
	if err := r.observe(ctx, event); err != nil {
		return ErrE2HTTPHost
	}
	return nil
}

func e3ProgramEvent(event e2proxy.Event) bool {
	return event.Program == e2proxy.ProgramE3Identity || event.Program == e2proxy.ProgramE3Create || event.Program == e2proxy.ProgramE3Read
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
