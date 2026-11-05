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
type Rule struct {
	Ref          string
	Origin       string
	ConnectIP    netip.Addr
	PathPrefix   string
	PathScopeRef string
	Methods      []string
	Headers      http.Header `json:"-"`
}
type Limits struct {
	MaxAttempts    uint64
	MaxCalls       int
	MaxConcurrent  int
	MaxBodyBytes   int64
	RequestTimeout time.Duration
	CallTimeout    time.Duration
	MaxDenials     uint64
	MaxTransient   uint64
	MaxUnknown     uint64
}
type Config struct {
	Scope          transmit.Scope
	IntentID       int64
	OwnerRef       string
	PolicyRevision string
	AccountRef     string
	SessionRef     string
	ExpiresAt      time.Time
	Rules          []Rule
	Budget         transmit.CumulativeBudget `json:"-"`
	Recorder       Recorder                  `json:"-"`
	RootCAs        *x509.CertPool            `json:"-"`
	Limits         Limits
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
	Calls            int    `json:"calls"`
}
type ExecutionView struct {
	IntentIDs      []int64        `json:"intent_ids"`
	Version        string         `json:"version"`
	Scope          transmit.Scope `json:"scope"`
	IntentID       int64          `json:"intent_id"`
	OwnerRef       string         `json:"owner_ref"`
	PolicyRevision string         `json:"policy_revision"`
	State          string         `json:"state"`
	ExpiresAt      time.Time      `json:"expires_at"`
	Counts         Counts         `json:"counts"`
	Reason         Reason         `json:"reason"`
	AuditComplete  bool           `json:"audit_complete"`
}
type RuleView struct {
	Ref          string   `json:"ref"`
	Methods      []string `json:"methods"`
	PathScopeRef string   `json:"path_scope_ref"`
}
type PolicyView struct {
	BudgetRef         string     `json:"budget_ref"`
	RemainingRequests uint64     `json:"remaining_requests"`
	BudgetAvailable   bool       `json:"budget_available"`
	Version           string     `json:"version"`
	PolicyRevision    string     `json:"policy_revision"`
	ExpiresAt         time.Time  `json:"expires_at"`
	Rules             []RuleView `json:"rules"`
}
type CallResult struct {
	IntentID      int64  `json:"intent_id"`
	CallRef       string `json:"call_ref"`
	CommandRef    string `json:"command_ref"`
	State         string `json:"state"`
	Counts        Counts `json:"counts"`
	AuditComplete bool   `json:"audit_complete"`
	Final         bool   `json:"final"`
	Reason        Reason `json:"reason"`
}
