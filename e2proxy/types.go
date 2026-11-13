// Package e2proxy implements an explicit, separately versioned general HTTP
// policy path. It does not widen transmit's registered-read contract.
package e2proxy

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/netip"
	"time"

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
	ErrAudit  = errors.New("e2proxy: audit unavailable")
	ErrDrain  = errors.New("e2proxy: drain incomplete")
	// ErrWithheld는 대상이 답했지만 E2가 응답 표현을 주지 않은 요청이다(사유 withheld).
	ErrWithheld = errors.New("e2proxy: response withheld")
	// ErrAuthSession은 인증 준비 응답을 받았지만 설정한 세션 위치에서 세션을 찾지 못했다는 뜻이다(로그인 실패 포함).
	ErrAuthSession = errors.New("e2proxy: auth session not found")
)

// Recorder must acknowledge durable storage, serialize concurrent writes, and
// honor ctx at I/O boundaries. It must not call back into this execution.
type Recorder interface {
	Record(context.Context, Event) error
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
	PathPrefix    string
	PathScopeRef  string
	Methods       []string
	// Headers explicitly declares ordinary, noncredential constraints. Values
	// must already match the caller; they are never injected or overwritten.
	Headers http.Header `json:"-"`
	// CredentialHeaders explicitly declares protected values, including custom
	// credential names. It is accepted only by the opt-in proposal mode.
	CredentialHeaders http.Header `json:"-"`
	// Purpose가 비면 모델 요청(Do)의 규칙이다. PurposeAuthPrep이면 프로그램이 부르는 인증 준비(Call.AuthPrep) 전용이고
	// 모델 요청의 규칙 찾기는 이 규칙을 건너뛴다(C14 구현 설계 1). 메서드는 POST 하나, 경로는 접두가 아니라 정확히 그 경로다.
	Purpose string `json:"-"`
}

// PurposeAuthPrep은 인증 준비(로그인) 전용 규칙 표시다.
const PurposeAuthPrep = "auth_prep"

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
	HeaderMode      HeaderMode
	Scope           transmit.Scope
	IntentID        int64
	OwnerRef        string
	PolicyRevision  string
	AccountRef      string
	SessionRef      string
	ExpiresAt       time.Time
	Rules           []Rule
	Budget          transmit.CumulativeBudget `json:"-"`
	Recorder        Recorder                  `json:"-"`
	RootCAs         *x509.CertPool            `json:"-"`
	Limits          Limits
}
type Restriction struct {
	PolicyRevision string    `json:"policy_revision"`
	ExpiresAt      time.Time `json:"expires_at"`
	RuleRefs       []string  `json:"rule_refs"`
	Methods        []string  `json:"methods"`
}
type Receipt struct {
	IntentID         int64          `json:"intent_id"`
	Version          string         `json:"version"`
	Scope            transmit.Scope `json:"scope"`
	CallRef          string         `json:"call_ref,omitempty"`
	CommandRef       string         `json:"command_ref,omitempty"`
	AttemptID        string         `json:"attempt_id,omitempty"`
	PolicyRevision   string         `json:"policy_revision"`
	RuleRef          string         `json:"rule_ref,omitempty"`
	Sequence         uint64         `json:"sequence"`
	Method           string         `json:"method,omitempty"`
	Decision         string         `json:"decision,omitempty"`
	Reason           Reason         `json:"reason"`
	ForwardAttempted bool           `json:"forward_attempted"`
	RequestWritten   bool           `json:"request_written"`
	ResponseReceived bool           `json:"response_received"`
	ResponseComplete bool           `json:"response_complete"`
	NotSent          bool           `json:"not_sent"`
	AuditComplete    bool           `json:"audit_complete"`
	StatusCode       int            `json:"status_code,omitempty"`
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
