// Package e3record prepares immutable, non-sensitive records from one trusted
// execution. A SourceReader is an authority installed by the host, not a model
// DTO: it must authenticate KG, fixture generation, identity, and final audit
// persistence. Nil or unavailable authority is a hard refusal.
package e3record

import (
	"context"
	"errors"
	"time"

	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

// MaxCanonicalBytes is the inclusive v1 canonical byte ceiling confirmed in
// PR #142 review 5751671737 and docs/contracts/e3-storage-gate.md.
const MaxCanonicalBytes = 64 << 10

// 저장 판정과 읽기 projection이 같은 의미 값을 사용하도록 한 곳에서 관리한다.
const (
	AccessPolicyViolationCandidate  = "위반_후보"
	AccessPolicyNoViolationObserved = "위반_미관찰"
	AccessPolicyUndecidable         = "판정_불가"
)

var (
	ErrInvalid    = errors.New("e3_record_invalid")
	ErrSource     = errors.New("e3_source_unavailable")
	ErrIncomplete = errors.New("e3_execution_incomplete")
	ErrPrecision  = errors.New("e3_timestamp_precision_mismatch")
)

// Source is a host authority's independently read execution projection.
// GenerationRef identifies the verified harness token; it is not that token's
// raw value. Snapshot/epoch/identity truth must never be inferred from strings.
type Source struct {
	Expected                evidencebind.Context
	ExplorationID           int64
	ModeVersion             uint64
	EvidenceRevision        uint64
	EvidenceDigest          [32]byte
	KGSnapshotRef           string
	KGSnapshotDigest        [32]byte
	GenerationRef           string
	InstanceRef             string
	RecordCreatedAt         time.Time
	ModelTag                string
	EvalRunID               string
	ResetGeneration         string
	Policy                  oracle.PolicyAssertion
	CanaryFields            []string
	TrafficEvidenceIDs      []int64
	Severity                string
	FinalCheckpointRef      string
	FinalCheckpointDigest   [32]byte
	FinalCheckpointScope    transmit.Scope
	FinalCheckpointSequence uint64
	// Completed is successful engine disposition, not just cleanup being run.
	Completed bool
	// Finalized requires successful Journal.Close and protected checkpoint save.
	Finalized bool
}

type SourceReader func(context.Context, evidencebind.Context) (Source, error)

type Input struct {
	Expected     evidencebind.Context
	Evidence     evidencebind.Input
	CanaryFields []string
}

// Prepared has no exported state and cannot be populated by JSON input.
type Prepared struct {
	valid    bool
	snapshot Snapshot
}

// Snapshot is a copy for persistence. Its byte slices must not be exposed as
// model authority or reused to manufacture a new Prepared.
type Snapshot struct {
	TaskID         int64
	Source         Source
	Value          oracle.VerdictValue
	Canonical      []byte
	Manifest       []byte
	Binding        []byte
	PayloadDigest  [32]byte
	ManifestDigest [32]byte
	InputDigest    [32]byte
	OperationKey   [32]byte
}

func cloneSource(s Source) Source {
	s.CanaryFields = append([]string(nil), s.CanaryFields...)
	s.TrafficEvidenceIDs = append([]int64(nil), s.TrafficEvidenceIDs...)
	return s
}

func (p Prepared) Snapshot() (Snapshot, bool) {
	if !p.valid {
		return Snapshot{}, false
	}
	s := p.snapshot
	s.Source = cloneSource(s.Source)
	s.Canonical = append([]byte(nil), s.Canonical...)
	s.Manifest = append([]byte(nil), s.Manifest...)
	s.Binding = append([]byte(nil), s.Binding...)
	return s, true
}
