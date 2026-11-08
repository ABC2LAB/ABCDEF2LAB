// Package auditcheckpoint implements the inactive CPD persistence core. The host
// supplies authenticated authority leases; this package issues no identities,
// store epochs, restore anchors, keys, or lifecycle attestations.
package auditcheckpoint

import (
	"context"
	"crypto/sha256"
	"errors"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

var (
	ErrConfig        = errors.New("auditcheckpoint: invalid configuration")
	ErrRequest       = errors.New("auditcheckpoint: invalid request")
	ErrAuthority     = errors.New("auditcheckpoint: authority unavailable")
	ErrEpoch         = errors.New("auditcheckpoint: current store epoch unavailable")
	ErrNotFound      = errors.New("auditcheckpoint: record unavailable")
	ErrConflict      = errors.New("auditcheckpoint: publication conflict")
	ErrTerminal      = errors.New("auditcheckpoint: journal is terminal")
	ErrVerification  = errors.New("auditcheckpoint: snapshot verification failed")
	ErrStorage       = errors.New("auditcheckpoint: storage unavailable")
	ErrCommitUnknown = errors.New("auditcheckpoint: commit outcome unknown")
)

type Phase string

const (
	Open        Phase = "open"
	Sealed      Phase = "sealed"
	Incomplete  Phase = "incomplete"
	Quarantined Phase = "quarantined"
)

type Kind string

const (
	Register       Kind = "register"
	Append         Kind = "append"
	Seal           Kind = "seal"
	MarkIncomplete Kind = "incomplete"
	Quarantine     Kind = "quarantined"
)

// Epoch must originate in an independently verified host/restore authority. The
// matching database row must already exist; Migrate never creates one.
type Epoch struct {
	StoreRef     string
	EpochRef     string
	Generation   uint64
	AnchorDigest [sha256.Size]byte
}

type Binding struct {
	PublisherRef string
	Scope        transmit.Scope
	JournalID    string
	KeyRef       string
}

type Request struct {
	Epoch            Epoch
	OperationRef     string
	PublisherRef     string
	Kind             Kind
	ExpectedRevision uint64
	Previous         auditlog.Checkpoint
	Candidate        auditlog.Checkpoint
}

type Query struct {
	Epoch        Epoch
	Binding      Binding
	OperationRef string // only for Operation; never substitutes for Head
}

type Head struct {
	Epoch        Epoch
	PublisherRef string
	Revision     uint64
	Phase        Phase
	Checkpoint   auditlog.Checkpoint
	RecordedAt   time.Time // diagnostic, not an ordering/freshness authority
}

// Receipt records one durable operation outcome. Its Head can be historical.
// Obtain the current head with a separate authorized Head call.
type Receipt struct {
	OperationRef string
	ReceiptRef   string
	Head         Head
	CommittedAt  time.Time
}

type Purpose string

const (
	PublishAccess   Purpose = "publish"
	HeadAccess      Purpose = "head"
	OperationAccess Purpose = "operation"
)

type Access struct {
	Purpose      Purpose
	Epoch        Epoch
	Binding      Binding
	OperationRef string
	Publication  Request // populated only for PublishAccess
}

// Host is trusted Go bootstrap, never model/HTTP JSON authority. Acquire must
// independently authenticate the principal and exact scope, and verify a seal's
// real StopAndWait/Close/no-earlier-failure evidence. The lease must cover the
// whole operation, including SQL commit or read. Revocation/epoch change must
// cancel its Context or wait for Release without holding SQL row locks.
type Host interface {
	Acquire(context.Context, Access) (Lease, error)
}

// Lease must fence authority/anchor changes for its lifetime. Repeating a
// callback or echoing caller fields is not this guarantee. Epoch is a stable,
// nonblocking value; ResolveKey returns a currently authorized immutable mapping
// for the access purpose. Key ownership remains with the host; the core clones
// and clears its own copy. Release must not initiate database work or drain.
type Lease interface {
	Context() context.Context
	Epoch() Epoch
	ResolveKey(context.Context, Binding) ([]byte, error)
	Release()
}

var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$`)

func validRef(s string) bool { return refPattern.MatchString(s) && !strings.Contains(s, "://") }
func validEpoch(e Epoch) bool {
	return validRef(e.StoreRef) && validRef(e.EpochRef) && e.Generation > 0 && e.Generation <= math.MaxInt64 && e.AnchorDigest != ([32]byte{})
}
func binding(publisher string, cp auditlog.Checkpoint) Binding {
	return Binding{PublisherRef: publisher, Scope: cp.Scope, JournalID: cp.JournalID, KeyRef: cp.KeyRef}
}
func matches(b Binding, h Head) bool { return b == binding(h.PublisherRef, h.Checkpoint) }
