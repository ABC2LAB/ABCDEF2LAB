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

const Version = "e2-http-v1"
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
	Unknown         Reason = "unknown"
	AttemptLimit    Reason = "attempt_limit"
	DenialLimit     Reason = "denial_limit"
	TransientLimit  Reason = "transient_limit"
	UnknownLimit    Reason = "unknown_limit"
	Invalid         Reason = "invalid"
)

var (
	ErrConfig = errors.New("e2proxy: invalid trusted configuration")
	ErrDenied = errors.New("e2proxy: request refused")
	ErrClosed = errors.New("e2proxy: admission closed")
	ErrAudit  = errors.New("e2proxy: audit unavailable")
	ErrDrain  = errors.New("e2proxy: drain incomplete")
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
}
type Limits struct {
	// MaxAttempts는 실행 전체에서 예산을 예약하고 송신 단계로 넘어간 시도 수의 상한이다.
	// 보내지 않은 거부·미지원은 세지 않는다. 닿으면 그 뒤 송신만 거부한다(C01).
	MaxAttempts    uint64
	MaxCalls       int
	MaxConcurrent  int
	MaxBodyBytes   int64
	RequestTimeout time.Duration
	CallTimeout    time.Duration
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
	At               time.Time      `json:"at"`
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
	Calls     int    `json:"calls"`
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
