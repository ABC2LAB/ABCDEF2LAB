// Package e2proxy implements an explicit, separately versioned general HTTP
// policy path. It does not widen transmit's registered-read contract.
package e2proxy

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"time"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/transmit"
)

// Version은 이 코드가 쓰는 신뢰 결과·감사 기록의 판이다. v2에서 사유 `withheld`, 계수 `Counts.Withheld`, 영수증 칸
// `response_truncated`가 생겼다(C16). 저장된 v1 기록은 검증기·내보내기·채점기가 v1 규칙으로 읽는다.
const Version = "e2-http-v2"

// VersionV1은 C16 전 판이다. 새로 쓰지 않고 저장된 기록을 읽을 때만 받는다.
const VersionV1 = "e2-http-v1"
const AttemptHeader = "X-ABC2LAB-E2-Attempt"

type Reason string

const (
	Complete        Reason = "complete"
	Unsupported     Reason = "unsupported"
	PolicyDenied    Reason = "policy_denied"
	WorkflowPaused  Reason = "workflow_paused"
	Closed          Reason = "closed"
	AuditFailed     Reason = "audit_failed"
	Expired         Reason = "expired"
	Revoked         Reason = "revoked"
	BudgetExhausted Reason = "budget_exhausted"
	TransportFailed Reason = "transport_failed"
	// Withheld는 대상이 답했지만(상태 줄·헤더 수신) 자격 응답·보호 값 반사·압축·본문 상한 때문에 E2가 응답 표현을
	// 도구에 주지 않은 것이다. 대상이 처리한 것은 알므로 결과 불명이 아니고 결과 불명 상한에 세지 않는다(C16, e2-http-v2).
	Withheld       Reason = "withheld"
	Unknown        Reason = "unknown"
	AttemptLimit   Reason = "attempt_limit"
	DenialLimit    Reason = "denial_limit"
	TransientLimit Reason = "transient_limit"
	UnknownLimit   Reason = "unknown_limit"
	Invalid        Reason = "invalid"
)

var (
	ErrConfig = errors.New("e2proxy: invalid trusted configuration")
	ErrDenied = errors.New("e2proxy: request refused")
	ErrClosed = errors.New("e2proxy: admission closed")
	// ErrModelStageClosed는 모델 단계 마감(CloseModelStage)으로 call 문맥이 끝났다는 취소 원인이다(context.Cause). 승인
	// 대기가 이 까닭으로 닫혔는지를 승인 이력이 가린다(closed_reason model_stage_closed).
	ErrModelStageClosed = errors.New("e2proxy: model stage closed")
	ErrAudit            = errors.New("e2proxy: audit unavailable")
	ErrDrain            = errors.New("e2proxy: drain incomplete")
	// ErrWithheld는 대상이 답했지만 E2가 응답 표현을 주지 않은 요청이다(사유 withheld).
	ErrWithheld = errors.New("e2proxy: response withheld")
	// ErrAuthSession은 인증 준비 응답을 받았지만 설정한 세션 위치에서 세션을 찾지 못했다는 뜻이다(로그인 실패 포함).
	ErrAuthSession = errors.New("e2proxy: auth session not found")
	// ErrSessionRedactCapacity는 시험 계정 세션 가림 칸(SessionRedactCapacity, 모델 단계는 확정 몫을 뺀 칸)이 모자라 새
	// 세션을 받지 않은 것이다. ErrConfig와 같은 갈래로도 본다(errors.Is).
	ErrSessionRedactCapacity = fmt.Errorf("%w: session redaction capacity", ErrConfig)
	// ErrE3JournalUnavailable은 E③ 송신에 필요한 v3 감사·계정 결속이 아직 연결되지 않았다는 뜻이다.
	ErrE3JournalUnavailable = errors.New("e2proxy: e3 journal v3 unavailable")
)

// Recorder must acknowledge durable storage, serialize concurrent writes, and
// honor ctx at I/O boundaries. It must not call back into this execution.
type Recorder interface {
	Record(context.Context, Event) error
}

// E3Verifier는 독립 Host가 현재 head를 확인한 결과만 돌려주는 경계다.
type E3Verifier interface {
	Verified(context.Context, e3contract.RunBinding, e3contract.CallRef) (Event, error)
}

// E3CheckpointVerifier는 E3Verifier와 같고, 독립 Host가 검증에 쓴 현재 head 체크포인트를 함께 돌려준다(E③ 판정 저장 근거,
// docs/contracts/e3-product-verdict-store.md §2). 이 인터페이스가 없는 검증자는 근거 체크포인트를 주지 못한다.
type E3CheckpointVerifier interface {
	VerifiedCheckpoint(context.Context, e3contract.RunBinding, e3contract.CallRef) (Event, JournalCheckpoint, error)
}

// Rule is privileged input from the trusted approval issuer. A method grant
// does not assert that an application operation has no side effects.
// HeaderMode is a transfer-support selection, not an approval or policy digest.
type HeaderMode string

const (
	HeaderPreserve HeaderMode = "preserve"
	// Evaluation-only caller credentials; requires a matching Evaluation context.
	HeaderEvaluationCallerPassthrough HeaderMode = "evaluation_caller_passthrough"
	// This opt-in host seam is a Draft proposal, not an adopted general-mode
	// contract or an authorization to activate protected session injection.
	HeaderProtectedSessionProposal HeaderMode = "protected_session_proposal"
	// 시험 계정 실행(C14 결정 4): 모델이 쓴 Authorization·Cookie를 승인 origin에 그대로 보낸다(평가 caller 전달과 같은 검사).
	// 평가 문맥은 받지 않는다. 자격 응답 헤더(Set-Cookie 등)도 보류하지 않는다. 보호 값 반사 보류·출력 가림은 그대로다.
	HeaderTestAccountPassthrough HeaderMode = "test_account_passthrough"
)

type Rule struct {
	TLSSPKISHA256 string `json:"tls_spki_sha256,omitempty"`
	Ref           string
	Origin        string
	ConnectIP     netip.Addr
	// PathMatch가 비면 기존 경로 조각 접두, exact면 경로 및 선택한 query 하나만 승인한다.
	PathMatch     string `json:"path_match,omitempty"`
	ExactQuery    string `json:"exact_query,omitempty"`
	StrictHeaders bool   `json:"-"`
	PathPrefix    string
	PathScopeRef  string
	Methods       []string
	// Headers explicitly declares ordinary, noncredential constraints. Values
	// must already match the caller; they are never injected or overwritten.
	Headers http.Header `json:"-"`
	// CredentialHeaders explicitly declares protected values, including custom
	// credential names. It is accepted only by the opt-in proposal mode.
	CredentialHeaders http.Header `json:"-"`
	// Purpose가 비면 모델 요청(Do)의 규칙이다. 값이 있으면 프로그램 전용이며 모델 규칙 찾기에서 제외한다.
	// E③ 규칙은 송신 권한이 아니다. 선언 동결본·계정 결속·v3 감사가 연결되기 전에는 프로그램 호출을 거부한다.
	Purpose string `json:"-"`
}

// PurposeAuthPrep은 인증 준비(로그인) 전용 규칙 표시다.
const PurposeAuthPrep = "auth_prep"

// 확정 단계 학습 로그인 재사용(docs/contracts/model-login-attempts.md §6-2)의 송신 목적이다. 규칙에 적는 표시가 아니라
// 호출 때만 쓰는 목적이고, 확정 단계 다시 로그인 호출(BeginE3ReloginCall)만 쓸 수 있다. 전용 규칙 대신 그 실행의 Worker
// 승인 규칙(대상 승인 경로·메서드 = 쓰기 승인 범위)과 운영자 제외 경로를 그대로 적용한다(§2-2: 확정 단계 로그인은 예외 없이
// 쓰기 승인 범위를 따른다). 응답은 도구·모델로 가지 않고 호출자의 판정 함수만 본다.
const (
	// PurposeLearnedLogin은 학습한 방법으로 다시 만든 로그인 POST다(질의 없음).
	PurposeLearnedLogin = "learned_login"
	// PurposeLoginPrep은 로그인 전 로그인 화면 GET(CSRF·로그인 전 쿠키)과 세션 없는 대조 GET이다(본문·질의 없음).
	PurposeLoginPrep = "login_prep"
)

// learnedLoginPurpose는 Worker 승인 규칙으로 판정하는 확정 단계 로그인 목적인지다.
func learnedLoginPurpose(purpose string) bool {
	return purpose == PurposeLearnedLogin || purpose == PurposeLoginPrep
}

const (
	PurposeE3Identity = "e3_identity"
	PurposeE3Create   = "e3_create"
	PurposeE3Read     = "e3_read"
)

type Limits struct {
	// MaxAttempts는 실행 전체에서 예산을 예약하고 송신 단계로 넘어간 시도 수의 상한이다.
	// 보내지 않은 거부·미지원은 세지 않는다. 닿으면 그 뒤 송신만 거부한다(C01).
	MaxAttempts   uint64
	MaxCalls      int
	MaxConcurrent int
	// MaxBodyBytes는 요청 본문 상한이다(넘으면 송신 전 미지원). 응답 상한을 따로 주지 않으면 응답 본문 상한도 된다.
	MaxBodyBytes int64
	// MaxResponseBodyBytes는 도구에 줄 응답 본문 상한이다(넘으면 앞부분만, C16 결정안 2·3). 0이면 MaxBodyBytes다.
	MaxResponseBodyBytes int64
	// MaxResponseScanBytes는 자격 반사 검사를 위해 읽는 응답 상한이다. 0이면 응답 상한 + 가장 긴 보호 값 길이다.
	// 주면 응답 상한 + 가장 긴 보호 값 길이 − 1 이상이어야 한다(전달 구간 끝에 걸친 보호 값도 통째로 검사 범위에 듦).
	MaxResponseScanBytes int64
	RequestTimeout       time.Duration
	CallTimeout          time.Duration
	// MaxDenials·MaxTransient·MaxUnknown은 call 하나의 거부+미지원·송신 실패·결과 불명 상한이다.
	// 닿으면 그 call의 새 요청만 막고 실행 전체와 다른 call은 계속된다(C01).
	MaxDenials   uint64
	MaxTransient uint64
	MaxUnknown   uint64
}
type Config struct {
	// E3Verifier가 없으면 v3 저널 선택만으로 프로그램 송신이 열리지 않는다.
	E3Verifier E3Verifier `json:"-"`
	// E③ 몫은 회차 전체 대상 예산 안에서 별도로 센다. 0이면 프로그램 송신은 닫힌다.
	E3ConfirmationRequests uint64 `json:"-"`
	// SessionRedactCapacity는 시험 계정 세션(인증 준비·다시 로그인, AddRedactValues)으로 등록할 수 있는 출력 가림 값 수다.
	// 0이면 기본 64개다. 상한은 MaxSessionRedactValues다. 대상 응답에서 받은 발급 세션은 따로 센다(maxIssuedRedactValues).
	SessionRedactCapacity int `json:"-"`
	// ConfirmationRedactReserve는 그 칸 안에서 확정 단계 직전 다시 로그인 몫으로 처음부터 떼어 두는 값 수다. 모델 단계가
	// 마감되기 전(CloseModelStage 전)의 등록은 칸 − 몫까지만 쓴다. 칸의 절반을 넘을 수 없다.
	ConfirmationRedactReserve int `json:"-"`
	// 생성은 제품 대상의 명시적 쓰기 동의와 리셋 가능 상태가 모두 확인된 실행만 허용한다.
	E3WriteConsent bool `json:"-"`
	E3Resettable   bool `json:"-"`
	// AuditVersion은 신뢰된 호스트가 선택하는 저널 행 판이다. 빈 값은 v2다.
	// v3 선택만으로 E③ 프로그램 송신 권한이 생기지 않는다.
	AuditVersion string `json:"-"`
	// CheckAuthority는 신뢰 호스트가 연결하는 위임 철회 검사다. nil은 기존 모드다.
	// 짧고 제한된 로컬 검사만 수행하며 Execution으로 재진입하면 안 된다.
	CheckAuthority func() error `json:"-"`
	// AttemptPathObserver는 평가 회차 전용 관찰 훅이다. nil이면 동작은 그대로다(제품·공식 회차).
	AttemptPathObserver AttemptPathObserver `json:"-"`
	RequestBoundary     RequestBoundary     `json:"-"`
	ApprovalMode        ApprovalMode
	// RequestApproval은 호스트가 명시적으로 연결하는 요청별 확인 경계다.
	RequestApproval RequestApprovalGate `json:"-"`
	Evaluation      *EvaluationCallerContext
	// PasswordResponse는 보호된 호스트 정책이다. 빈 값은 기존 전체 보류다.
	PasswordResponse string
	HeaderMode       HeaderMode
	// TargetCredentialHeader is the approved test login's custom session
	// header name. Values are never copied into an observation.
	TargetCredentialHeader string `json:"-"`
	// ResolveSentAccount is supplied by the protected session store. It may
	// return a non-secret stable account revision only after matching the
	// credentials in the actual sent request, never a model-provided label.
	ResolveSentAccount func(http.Header) string `json:"-"`
	// SelectedSessionHeaders is only for a separately approved candidate
	// execution. It injects one protected test session after path admission;
	// the credential never appears in a Bash command or model input.
	SelectedSessionHeaders func() (http.Header, bool) `json:"-"`
	SelectedAccountRef     string                     `json:"-"`
	Scope                  transmit.Scope
	IntentID               int64
	OwnerRef               string
	PolicyRevision         string
	AccountRef             string
	SessionRef             string
	ExpiresAt              time.Time
	Rules                  []Rule
	// ExcludedPaths는 운영자가 선언한 제외 경로 패턴이다(진단 워크플로 입력). 모델 요청과 E③ 목적 요청의 풀린 경로가
	// 하나라도 걸리면 policy_denied로 거부한다. 진단 흐름과 같은 해석·일치 함수(pathscope.Excluded)를 쓴다.
	// 인증 준비는 대상 등록 때 승인한 로그인 경로를 사용한다.
	ExcludedPaths []string
	Budget        transmit.CumulativeBudget `json:"-"`
	Recorder      Recorder                  `json:"-"`
	RootCAs       *x509.CertPool            `json:"-"`
	Limits        Limits
}
type Restriction struct {
	PolicyRevision string    `json:"policy_revision"`
	ExpiresAt      time.Time `json:"expires_at"`
	RuleRefs       []string  `json:"rule_refs"`
	Methods        []string  `json:"methods"`
}

// ProgramCallKind는 호스트가 연 프로그램 호출의 귀속이다. Worker intent와 배타적이다.
type ProgramCallKind string

const (
	ProgramAuthPrep       ProgramCallKind = "auth_prep"
	ProgramRefreshSession ProgramCallKind = "refresh_session"
	ProgramE3Identity     ProgramCallKind = ProgramCallKind(PurposeE3Identity)
	ProgramE3Create       ProgramCallKind = ProgramCallKind(PurposeE3Create)
	ProgramE3Read         ProgramCallKind = ProgramCallKind(PurposeE3Read)
)

// Valid는 현재 영수증·저널에서 인정하는 프로그램 호출만 판별한다.
func (k ProgramCallKind) Valid() bool { return k == ProgramAuthPrep || k == ProgramRefreshSession }

func (k ProgramCallKind) isE3() bool {
	return k == ProgramE3Identity || k == ProgramE3Create || k == ProgramE3Read
}

// ValidCallAttribution은 실재 Worker intent 또는 명시적인 프로그램 호출 중 하나만 허용한다.
func ValidCallAttribution(intentID int64, program ProgramCallKind) bool {
	return intentID > 0 && program == "" || intentID == 0 && program.Valid()
}

type Receipt struct {
	*E3Audit
	Program          ProgramCallKind `json:"program,omitempty"`
	IntentID         int64           `json:"intent_id"`
	Version          string          `json:"version"`
	Scope            transmit.Scope  `json:"scope"`
	CallRef          string          `json:"call_ref,omitempty"`
	CommandRef       string          `json:"command_ref,omitempty"`
	AttemptID        string          `json:"attempt_id,omitempty"`
	PolicyRevision   string          `json:"policy_revision"`
	RuleRef          string          `json:"rule_ref,omitempty"`
	Sequence         uint64          `json:"sequence"`
	Method           string          `json:"method,omitempty"`
	Decision         string          `json:"decision,omitempty"`
	Reason           Reason          `json:"reason"`
	ForwardAttempted bool            `json:"forward_attempted"`
	RequestWritten   bool            `json:"request_written"`
	ResponseReceived bool            `json:"response_received"`
	ResponseComplete bool            `json:"response_complete"`
	NotSent          bool            `json:"not_sent"`
	AuditComplete    bool            `json:"audit_complete"`
	StatusCode       int             `json:"status_code,omitempty"`
	// ResponseTruncated는 응답 본문 앞부분만 도구에 준 것이다(e2-http-v2, 사유는 withheld).
	ResponseTruncated bool      `json:"response_truncated,omitempty"`
	At                time.Time `json:"at"`
}
type Event struct {
	Index uint64 `json:"index"`
	Stage string `json:"stage"`
	Receipt
}
type Counts struct {
	Attempts         uint64 `json:"attempts"`
	Allowed          uint64 `json:"allowed"`
	Denied           uint64 `json:"denied"`
	Unsupported      uint64 `json:"unsupported"`
	ForwardAttempted uint64 `json:"forward_attempted"`
	Responses        uint64 `json:"responses"`
	Unknown          uint64 `json:"unknown"`
	// Transient는 대상 연결을 끝내 맺지 못해 요청을 전달하지 않은 송신 실패(transport_failed) 수다.
	// 결과 불명(Unknown)과 따로 센다(C02).
	Transient uint64 `json:"transient"`
	// Withheld는 대상이 답했지만 표현을 보류하거나 자른 응답 수다. Unknown에 넣지 않는다(C16).
	Withheld uint64 `json:"withheld"`
	Calls    int    `json:"calls"`
}
type ExecutionView struct {
	ApprovalMode   ApprovalMode             `json:"approval_mode"`
	Evaluation     *EvaluationCallerContext `json:"evaluation,omitempty"`
	HeaderMode     HeaderMode               `json:"header_mode"`
	IntentIDs      []int64                  `json:"intent_ids"`
	Version        string                   `json:"version"`
	Scope          transmit.Scope           `json:"scope"`
	IntentID       int64                    `json:"intent_id"`
	OwnerRef       string                   `json:"owner_ref"`
	PolicyRevision string                   `json:"policy_revision"`
	State          string                   `json:"state"`
	ExpiresAt      time.Time                `json:"expires_at"`
	Counts         Counts                   `json:"counts"`
	// IntentCounts는 IntentID에 결속된 call들만의 누계다. Counts는 실행 전체 누계로 그대로 둔다.
	// Worker는 다른 intent의 불명·거부로 멈추지 않도록 이 값을 본다(C03).
	IntentCounts  Counts `json:"intent_counts"`
	ProgramCounts Counts `json:"program_counts"`
	Reason        Reason `json:"reason"`
	AuditComplete bool   `json:"audit_complete"`
}
type RuleView struct {
	Ref          string   `json:"ref"`
	Methods      []string `json:"methods"`
	PathScopeRef string   `json:"path_scope_ref"`
}
type PolicyView struct {
	ApprovalMode      ApprovalMode             `json:"approval_mode"`
	Evaluation        *EvaluationCallerContext `json:"evaluation,omitempty"`
	HeaderMode        HeaderMode               `json:"header_mode"`
	BudgetRef         string                   `json:"budget_ref"`
	RemainingRequests uint64                   `json:"remaining_requests"`
	BudgetAvailable   bool                     `json:"budget_available"`
	Version           string                   `json:"version"`
	PolicyRevision    string                   `json:"policy_revision"`
	ExpiresAt         time.Time                `json:"expires_at"`
	Rules             []RuleView               `json:"rules"`
}
type CallResult struct {
	Program               ProgramCallKind   `json:"program,omitempty"`
	Observations          []HTTPObservation `json:"observations,omitempty"`
	ObservationsTruncated bool              `json:"observations_truncated,omitempty"`
	IntentID              int64             `json:"intent_id"`
	CallRef               string            `json:"call_ref"`
	CommandRef            string            `json:"command_ref"`
	State                 string            `json:"state"`
	Counts                Counts            `json:"counts"`
	AuditComplete         bool              `json:"audit_complete"`
	Final                 bool              `json:"final"`
	Reason                Reason            `json:"reason"`
}
