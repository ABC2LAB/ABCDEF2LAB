// Package evidencebind checks the consistency of server-collected evidence
// before persistence. It does not authenticate its caller, receipts or facts,
// execute requests, issue a storage authorization, or replace oracle.Evaluate.
package evidencebind

import (
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

// Context is supplied independently by the trusted execution host. Scope.RunRef
// names one E② work, not the evaluation runner's run_id. The host must assign
// one non-reused RunRef to one intent/work. Scope.PolicyRef is the execution
// policy; ReadApprovalRef is the resource's read approval; neither is the KG's
// expected-access policy version. No field is inferred from another.
type Context struct {
	Scope           transmit.Scope
	IntentID        int64
	ResourceRef     string
	ReadApprovalRef string
	KGPolicyVersion string
	Owner           idor.AccountRef
	Evaluated       idor.AccountRef
}

type ReadRole string

const (
	OwnerRead ReadRole = "owner"
	CrossRead ReadRole = "cross"
)

// EvidenceRef is a server-assigned, non-sensitive reference with its stored
// execution association. These fields express a binding, not a signature.
type EvidenceRef struct {
	ID       string
	IntentID int64
	Scope    transmit.Scope
	Attempt  uint64
	Role     ReadRole
}

type PolicySnapshot struct {
	Context   Context
	Assertion oracle.PolicyAssertion
}

// Observation carries no body or canary value. ObservedAt is the capture time;
// Receipt.At is the later final result time from SendDetailed.
type Observation struct {
	Context    Context
	Receipt    transmit.Receipt
	Evidence   EvidenceRef
	ObservedAt time.Time
}

// ResultSnapshot must be collected alongside the actual VerifyRead result by
// the host. Adding these fields to a model-supplied Result does not establish
// provenance. ObservedAt is the cross observation used by idor.VerifyRead.
type ResultSnapshot struct {
	Context       Context
	OwnerEvidence EvidenceRef
	CrossEvidence EvidenceRef
	ObservedAt    time.Time
	Result        idor.Result
}

type Input struct {
	Policy PolicySnapshot
	Owner  Observation
	Cross  Observation
	Result ResultSnapshot
}

type Reason string

const (
	Bound               Reason = "bound"
	MissingContext      Reason = "missing_context"
	ContextMismatch     Reason = "context_mismatch"
	ReceiptMismatch     Reason = "receipt_mismatch"
	ExecutionIncomplete Reason = "execution_incomplete"
	EvidenceRefMismatch Reason = "evidence_ref_mismatch"
	ObservationMismatch Reason = "observation_mismatch"
	VerdictMismatch     Reason = "verdict_mismatch"
)

// Outcome separates binding failures from oracle reasons. Use Check to assess
// an outcome: callers must require BindingValid before persisting the
// Recomputed verdict. Invalid inputs always return EffectiveValue=Undecidable
// and no recomputed verdict/reasons. A valid binding may still be Undecidable.
// This is not an attestation or an authorization for a finding write.
type Outcome struct {
	BindingValid   bool
	BindingReason  Reason
	EffectiveValue oracle.VerdictValue
	Recomputed     oracle.OracleVerdict
}
