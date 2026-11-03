// Package auditkeys is a metadata-only audit-key lifecycle registry.
// It never loads keys, issues runtime admission, or installs host authentication.
package auditkeys

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"
)

type State string

const (
	Prepared    State = "prepared"
	Active      State = "active"
	Retiring    State = "retiring"
	Retired     State = "retired"
	Compromised State = "compromised"
	Destroyed   State = "destroyed"
)

var (
	ErrInput          = errors.New("audit_key_registry_input_invalid")
	ErrTrust          = errors.New("audit_key_registry_trust_unavailable")
	ErrConflict       = errors.New("audit_key_registry_conflict")
	ErrTransition     = errors.New("audit_key_registry_transition_rejected")
	ErrNotFound       = errors.New("audit_key_registry_not_found")
	ErrStore          = errors.New("audit_key_registry_storage_unavailable")
	ErrOutcomeUnknown = errors.New("audit_key_registry_outcome_unknown")
)

type Config struct{ Namespace, PolicyRef string }
type Binding struct{ KeyRef, SecretVersionRef, Identity, PredecessorRef string }

// Command contains no caller-supplied actor, key bytes, or trust declarations.
// Register uses ExpectedRevision=0 and Next=Prepared; Change requires revision>0.
type Command struct {
	KeyRef, OperationRef string
	ExpectedRevision     uint64
	Next                 State
}

// TrustRequest is an exact, host-only challenge. Current is a DB projection,
// never proof of the secret, policy, operator identity, or restore epoch.
type TrustRequest struct {
	Namespace, PolicyRef string
	Binding              Binding
	Command              Command
	CurrentRevision      uint64
	CurrentState         State
	Inspect              bool
}

// Attestation must be issued only by an authenticated secret/policy authority.
// Identity is opaque, not a hash/fingerprint or filename. The provider guarantees
// that one actual key has one stable identity inside the configured namespace.
// MappingRef attests the exact immutable secret version/key binding.
// ActivationRef additionally attests that the actual bound key is available and
// verified for activation. A file's existence or length is insufficient.
type Attestation struct {
	Request                                                                         TrustRequest
	Identity, ActorRef                                                              string
	PolicyRevision                                                                  uint64
	MappingRef, AuthorizationRef                                                    string
	ActivationRef, DrainRef, DependencyRef, CopyLifetimeRef, DisposalRef, UnusedRef string
}

// Authority must acquire a real authenticated, policy-bound trust scope before
// invoking consume exactly once, synchronously, and keep it valid until consume
// returns. Acquisition/secret I/O happens before consume and outside DB row locks.
// Comparing two callbacks' answers is not a lease or an ABA/restore defense.
// No production implementation or default/fallback is supplied by this package.
type Authority interface {
	WithTrust(context.Context, TrustRequest, func(Attestation) error) error
}

// Record is metadata, not a key-use or admission capability.
type Record struct {
	Namespace, KeyRef, SecretVersionRef, PredecessorRef string
	State                                               State
	Revision                                            uint64
	UpdatedAt                                           time.Time
}

// Outcome separates the original immutable operation receipt from current state.
// An exact retry after later retirement/compromise never reports an old state as
// current authority. Neither field authorizes journal creation/admission.
type Outcome struct {
	Revision uint64
	State    State
	Current  Record
	Replayed bool
}

type Store struct {
	db        *sql.DB
	config    Config
	authority Authority
}

func New(db *sql.DB, config Config, authority Authority) (*Store, error) {
	if db == nil || authority == nil || !validRef(config.Namespace) || !validRef(config.PolicyRef) {
		return nil, ErrInput
	}
	return &Store{db: db, config: config, authority: authority}, nil
}

const maxRefBytes = 128

func validRef(s string) bool {
	if len(s) < 1 || len(s) > maxRefBytes || strings.Contains(s, "://") {
		return false
	}
	for i, c := range []byte(s) {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || !strings.ContainsRune("_.:/-", rune(c))) {
			return false
		}
	}
	return true
}
func validRevision(v uint64) bool { return v > 0 && v <= math.MaxInt64 }
func allowed(from, to State) bool {
	if to == Compromised {
		return from == Prepared || from == Active || from == Retiring || from == Retired
	}
	switch from {
	case Prepared:
		return to == Active || to == Destroyed
	case Active:
		return to == Retiring
	case Retiring:
		return to == Retired
	case Retired, Compromised:
		return to == Destroyed
	}
	return false
}
