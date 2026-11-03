// Package e3binding compares host-supplied original finding, retest and execution
// references. It performs no authentication, issuance, verdict or persistence.
package e3binding

import "github.com/Autumn-27/artex/evidencebind"

// Target is selected independently by the caller, not copied from the records
// being checked. OriginalEvidenceRevision is the original finding evidence generation.
type Target struct {
	TaskRef                  string
	FindingID                int64
	RetestID                 int64
	OriginalEvidenceRevision uint64
}

// Finding is a projection of a trusted original finding and its provenance.
// OriginalEvidenceRevision is a proposed host record, not an existing findings DB column.
type Finding struct {
	ID                       int64
	TaskRef                  string
	ResourceRef              string
	SourceRunRef             string
	OriginalEvidenceRevision uint64
}

// Retest binds the original snapshot to one independently issued new execution.
// Expected must be frozen before collecting the actual execution/evidence.
type Retest struct {
	ID                       int64
	FindingID                int64
	OriginalEvidenceRevision uint64
	Expected                 evidencebind.Context
}

// Execution comes from the host's actual execution association. Matching these
// fields cannot authenticate their origin or prove execution completion.
type Execution struct {
	RetestID                 int64
	FindingID                int64
	OriginalEvidenceRevision uint64
	Context                  evidencebind.Context
}

type Input struct {
	Finding   Finding
	Retest    Retest
	Execution Execution
}

type Reason string

const (
	Unknown               Reason = "unknown"
	Consistent            Reason = "references_consistent"
	InvalidTarget         Reason = "target_invalid"
	InvalidFinding        Reason = "finding_invalid"
	InvalidRetest         Reason = "retest_invalid"
	InvalidExecution      Reason = "execution_invalid"
	FindingMismatch       Reason = "finding_mismatch"
	RetestMismatch        Reason = "retest_mismatch"
	SourceExecutionReused Reason = "source_execution_reused"
	ExecutionMismatch     Reason = "execution_mismatch"
	ContextMismatch       Reason = "execution_context_mismatch"
)

// Decision expresses input consistency only. It retains no identifiers and is
// not a cached execution/storage authorization. Its zero value is inconsistent.
type Decision struct {
	consistent bool
	reason     Reason
}

func (d Decision) Consistent() bool { return d.consistent }
func (d Decision) Reason() Reason {
	if d.reason == "" {
		return Unknown
	}
	return d.reason
}
