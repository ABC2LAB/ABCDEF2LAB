// Package evidencerevision persists identifiers for trusted, server-collected
// initial observations. It issues neither execution nor finding authority.
package evidencerevision

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/taskmode"
	"github.com/Autumn-27/artex/transmit"
)

var (
	ErrInput          = errors.New("evidence_revision_input_invalid")
	ErrScope          = errors.New("evidence_revision_scope_unavailable")
	ErrBinding        = errors.New("evidence_revision_binding_rejected")
	ErrMode           = errors.New("evidence_revision_mode_rejected")
	ErrConflict       = errors.New("evidence_revision_conflict")
	ErrNotFound       = errors.New("evidence_revision_not_found")
	ErrStore          = errors.New("evidence_revision_storage_unavailable")
	ErrOutcomeUnknown = errors.New("evidence_revision_commit_outcome_unknown")
)

type Purpose string

const (
	IssueInitial Purpose = "issue_initial"
	ReadReceipt  Purpose = "read_receipt"
)

// Access is trusted Go input to the actual host authority. It is not a claim
// that the database authenticated any execution, account, approval or KG ref.
type Access struct {
	Purpose       Purpose
	TaskID        int64
	Scope         transmit.Scope
	IntentID      int64
	OperationRef  string
	RequestDigest [32]byte
}

// ScopeProvider must authenticate the exact caller/purpose and keep its actual
// root/writer/marker/restore scope valid until Release. Acquire and filesystem
// I/O happen before any SQL lock; no default/always-true provider exists here.
// On error the provider cleans all partial resources and returns no lease.
// Only a successful nonnil lease transfers ownership to this core, which
// releases it exactly once after the transaction/read has ended.
type ScopeProvider interface {
	Acquire(context.Context, Access) (Lease, error)
}
type Lease interface {
	Context() context.Context
	MarkerSnapshot() taskmode.Marker
	Release()
}

// Collected is immutable metadata, not provenance or authorization. Only the
// real private collector may call Capture in production; serializing model data
// into equivalent Go fields cannot establish that it came from execution.
type Collected struct {
	expected evidencebind.Context
	digest   [32]byte
	binding  []byte
	valid    bool
}

func (Collected) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// Capture validates/recomputes the actual owner/cross observation bundle, then
// retains only refs and digests. It never stores policy or receipt source data.
func Capture(expected evidencebind.Context, input evidencebind.Input, canaryFields []string) (Collected, error) {
	if !evidencebind.Check(expected, input).BindingValid {
		return Collected{}, ErrInput
	}
	fields := slices.Clone(canaryFields)
	if len(fields) > 256 {
		return Collected{}, ErrInput
	}
	slices.Sort(fields)
	for i, field := range fields {
		if len(field) == 0 || len(field) > 256 || !utf8.ValidString(field) || i > 0 && field == fields[i-1] {
			return Collected{}, ErrInput
		}
		for _, r := range field {
			if r < 0x20 || r == 0x7f {
				return Collected{}, ErrInput
			}
		}
	}
	if input.Result.Result.Verdict.Value == oracle.Confirmed && len(fields) == 0 {
		return Collected{}, ErrInput
	}
	digest, err := e3record.EvidenceDigest(input)
	if err != nil {
		return Collected{}, ErrInput
	}
	binding, err := json.Marshal(struct {
		Version        int
		Expected       evidencebind.Context
		EvidenceDigest [32]byte
		Owner, Cross   evidencebind.EvidenceRef
		CanaryFields   []string
	}{1, expected, digest, input.Owner.Evidence, input.Cross.Evidence, fields})
	if err != nil || len(binding) > 64<<10 {
		return Collected{}, ErrInput
	}
	return Collected{expected: expected, digest: digest, binding: binding, valid: true}, nil
}

type Request struct {
	OperationRef          string    `json:"-"`
	Collected             Collected `json:"-"`
	ExpectedExplorationID int64     `json:"-"`
	ExpectedModeVersion   uint64    `json:"-"`
}

// Receipt is historical observation metadata. Revision/ModeVersion never grant
// current execution or E3 storage permission, including when Lookup succeeds.
type Receipt struct {
	OperationRef                    string
	Revision                        uint64
	TaskID, ExplorationID, IntentID int64
	Scope                           transmit.Scope
	ModeVersion                     uint64
	EvidenceDigest                  [32]byte
	BindingDigest                   [32]byte
	IssuedAt, RecordedAt            time.Time
}

type Store struct {
	db       *sql.DB
	provider ScopeProvider
	commit   func(*sql.Tx) error
}

func validRef(s string) bool {
	if len(s) < 1 || len(s) > 128 || strings.Contains(s, "://") {
		return false
	}
	for i, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') && (i == 0 || !strings.ContainsRune("_.:/-", rune(c))) {
			return false
		}
	}
	return true
}

func requestInfo(r Request, purpose Purpose) (Access, [32]byte, error) {
	if !r.Collected.valid || !validRef(r.OperationRef) || r.ExpectedExplorationID <= 0 || r.ExpectedModeVersion == 0 || r.ExpectedModeVersion > math.MaxInt64 {
		return Access{}, [32]byte{}, ErrInput
	}
	expected := r.Collected.expected
	id, err := strconv.ParseInt(expected.Scope.TaskRef, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != expected.Scope.TaskRef {
		return Access{}, [32]byte{}, ErrInput
	}
	encoded, err := json.Marshal(struct {
		Version       int
		ExplorationID int64
		ModeVersion   uint64
		Binding       json.RawMessage
	}{1, r.ExpectedExplorationID, r.ExpectedModeVersion, r.Collected.binding})
	if err != nil {
		return Access{}, [32]byte{}, ErrInput
	}
	digest := sha256.Sum256(append([]byte("artex.initial.evidence.revision.v1\x00"), encoded...))
	return Access{Purpose: purpose, TaskID: id, Scope: expected.Scope, IntentID: expected.IntentID, OperationRef: r.OperationRef, RequestDigest: digest}, digest, nil
}
