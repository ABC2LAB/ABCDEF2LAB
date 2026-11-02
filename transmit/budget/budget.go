// Package budget accounts request reservations across executions sharing one
// host-issued budget approval. It performs no I/O and does not authorize HTTP.
// Callers must share one Ledger and supply trusted grants, attempts and time.
package budget

import (
	"errors"
	"math"
	"strings"
	"sync"
	"time"
)

const maxRefBytes = 128

var (
	ErrConfig           = errors.New("budget: invalid configuration")
	ErrInvalid          = errors.New("budget: invalid input")
	ErrUnknownApproval  = errors.New("budget: unknown approval")
	ErrConflict         = errors.New("budget: conflicting registration")
	ErrBindingMismatch  = errors.New("budget: binding mismatch")
	ErrNotYetValid      = errors.New("budget: approval not yet valid")
	ErrExpired          = errors.New("budget: approval expired")
	ErrExhausted        = errors.New("budget: request budget exhausted")
	ErrDuplicateAttempt = errors.New("budget: attempt already reserved")
	ErrCapacity         = errors.New("budget: registration capacity exhausted")
)

// Key identifies a cumulative budget, independent of execution and resource.
// BudgetRef is a dedicated approval ID, not Resource.ApprovalRef or a secret.
type Key struct {
	TaskRef   string
	BudgetRef string
}

// Binding cannot change under an existing Key, even after expiry. New policy
// or reset generations require an explicitly issued new BudgetRef.
type Binding struct {
	PolicyRef string
	ResetRef  string
}

// Attempt identifies one send reservation, not a transport retry. The host
// preserves this identity when retrying the accounting operation.
type Attempt struct {
	RunRef   string
	Sequence uint64
}

type Grant struct {
	Key         Key
	Binding     Binding
	MaxRequests uint64
	ValidFrom   time.Time
	ValidUntil  time.Time
}

// Limits bounds retained approvals and attempt identities. Expired approvals
// stay registered so eviction cannot silently recharge a previously used Key.
type Limits struct {
	MaxApprovals           int
	MaxRequestsPerApproval uint64
}

type approval struct {
	grant    Grant
	attempts map[Attempt]struct{}
	expired  bool
}

// Ledger is a single in-memory accounting authority. Do not copy it after use
// or create one per execution. Its zero value rejects all operations.
type Ledger struct {
	mu        sync.Mutex
	limits    Limits
	approvals map[Key]*approval
}

func New(limits Limits) (*Ledger, error) {
	if limits.MaxApprovals <= 0 || limits.MaxRequestsPerApproval == 0 || limits.MaxRequestsPerApproval > uint64(math.MaxInt) {
		return nil, ErrConfig
	}
	return &Ledger{limits: limits, approvals: make(map[Key]*approval)}, nil
}

// Register installs an immutable grant or preserves an identical registration.
// Future windows may be registered early. Registration is not a reservation.
// All times are supplied by the host's current clock, never historical events.
func (l *Ledger) Register(grant Grant, now time.Time) error {
	if l == nil || l.approvals == nil {
		return ErrConfig
	}
	grant.ValidFrom, grant.ValidUntil = canonicalTime(grant.ValidFrom), canonicalTime(grant.ValidUntil)
	now = canonicalTime(now)
	if !validKey(grant.Key) || !validBinding(grant.Binding) || !validTime(now) ||
		!validTime(grant.ValidFrom) || !validTime(grant.ValidUntil) || !grant.ValidFrom.Before(grant.ValidUntil) ||
		grant.MaxRequests == 0 || grant.MaxRequests > l.limits.MaxRequestsPerApproval {
		return ErrInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if existing, ok := l.approvals[grant.Key]; ok {
		if existing.grant != grant {
			return ErrConflict
		}
		if existing.expiredAt(now) {
			return ErrExpired
		}
		return nil
	}
	if !now.Before(grant.ValidUntil) {
		return ErrExpired
	}
	if len(l.approvals) >= l.limits.MaxApprovals {
		return ErrCapacity
	}
	l.approvals[grant.Key] = &approval{grant: grant}
	return nil
}

// Reserve consumes one request immediately and irreversibly on success. Only
// nil means a fresh reservation; a duplicate never grants another send. No
// refund is available, including cancellation or failure before a known send.
// The caller must separately enforce E②'s remaining conditions at send time.
func (l *Ledger) Reserve(key Key, binding Binding, attempt Attempt, now time.Time) error {
	if l == nil || l.approvals == nil {
		return ErrConfig
	}
	now = canonicalTime(now)
	if !validKey(key) || !validBinding(binding) || !validRef(attempt.RunRef) || attempt.Sequence == 0 || !validTime(now) {
		return ErrInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	a, err := l.active(key, binding, now)
	if err != nil {
		return err
	}
	if _, duplicate := a.attempts[attempt]; duplicate {
		return ErrDuplicateAttempt
	}
	if uint64(len(a.attempts)) >= a.grant.MaxRequests {
		return ErrExhausted
	}
	if a.attempts == nil {
		a.attempts = make(map[Attempt]struct{})
	}
	a.attempts[attempt] = struct{}{}
	return nil
}

// Remaining is an observation, not permission to send: another caller may
// reserve next. Exhaustion returns (0, nil); all errors return (0, error).
func (l *Ledger) Remaining(key Key, binding Binding, now time.Time) (uint64, error) {
	if l == nil || l.approvals == nil {
		return 0, ErrConfig
	}
	now = canonicalTime(now)
	if !validKey(key) || !validBinding(binding) || !validTime(now) {
		return 0, ErrInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	a, err := l.active(key, binding, now)
	if err != nil {
		return 0, err
	}
	return a.grant.MaxRequests - uint64(len(a.attempts)), nil
}

// active runs only under l.mu. Mismatched bindings cannot expire or modify a
// registered grant. A valid lookup observing expiry latches it before refusal.
func (l *Ledger) active(key Key, binding Binding, now time.Time) (*approval, error) {
	a, ok := l.approvals[key]
	if !ok {
		return nil, ErrUnknownApproval
	}
	if a.grant.Binding != binding {
		return nil, ErrBindingMismatch
	}
	if a.expiredAt(now) {
		return nil, ErrExpired
	}
	if now.Before(a.grant.ValidFrom) {
		return nil, ErrNotYetValid
	}
	return a, nil
}

func (a *approval) expiredAt(now time.Time) bool {
	if !now.Before(a.grant.ValidUntil) {
		a.expired = true
	}
	return a.expired
}

func canonicalTime(at time.Time) time.Time { return at.Round(0).UTC() }

func validTime(at time.Time) bool {
	return !at.IsZero() && at.Year() >= 1 && at.Year() <= 9999
}

func validKey(key Key) bool { return validRef(key.TaskRef) && validRef(key.BudgetRef) }

func validBinding(binding Binding) bool {
	return validRef(binding.PolicyRef) && validRef(binding.ResetRef)
}

func validRef(ref string) bool {
	if len(ref) == 0 || len(ref) > maxRefBytes || strings.Contains(ref, "://") {
		return false
	}
	for index := range len(ref) {
		c := ref[index]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if index > 0 && (c == '_' || c == '.' || c == ':' || c == '/' || c == '-') {
			continue
		}
		return false
	}
	return true
}
