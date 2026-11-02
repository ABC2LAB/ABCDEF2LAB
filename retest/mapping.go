// Package retest maps host-collected read-only E③ evidence to a retest conclusion.
// It performs no execution, persistence, issuance or finding-state transition.
package retest

import (
	"errors"

	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

// Status uses ARTEX's terminal retest lifecycle names. The host must supply the
// final status after execution has drained, rather than a model's staged claim.
type Status string

const (
	Completed Status = "completed"
	Failed    Status = "failed"
	Stopped   Status = "stopped"
)

// Verdict deliberately excludes fixed: read-only evidence cannot prove repair.
type Verdict string

const (
	Reproduced   Verdict = "reproduced"
	Inconclusive Verdict = "inconclusive"
)

type Reason string

const (
	ConfirmedEvidence Reason = "confirmed_evidence"
	NotConfirmed      Reason = "not_confirmed"
	EvidenceRejected  Reason = "evidence_rejected"
	ExecutionStopped  Reason = "execution_stopped"
)

var (
	ErrInvalidStatus    = errors.New("retest: invalid terminal status")
	ErrExecutionFailed  = errors.New("retest: execution failed")
	ErrAuditUnavailable = errors.New("retest: required audit unavailable")
)

// Result is a projection, not authorization to write a finding or retest. Check
// preserves recomputed oracle and binding reasons; it is zero when stopped.
type Result struct {
	Verdict Verdict
	Reason  Reason
	Check   evidencebind.Outcome
}

// Map accepts only host-supplied lifecycle and evidence, never a staged verdict.
// Finding ownership, issuance provenance and transaction checks belong to the
// future storage adapter. Errors return a zero Result with no conclusion.
func Map(status Status, expected evidencebind.Context, input evidencebind.Input) (Result, error) {
	// Final lifecycle wins over an earlier result, even a valid CONFIRMED.
	switch status {
	case Failed:
		return Result{}, ErrExecutionFailed
	case Stopped:
		return Result{Verdict: Inconclusive, Reason: ExecutionStopped}, nil
	case Completed:
	default:
		return Result{}, ErrInvalidStatus
	}
	// Audit integrity failure must not become a successfully mapped conclusion.
	// Transport errors/timeouts instead describe an inconclusive execution.
	if input.Owner.Receipt.Reason == transmit.AuditUnavailable || input.Cross.Receipt.Reason == transmit.AuditUnavailable {
		return Result{}, ErrAuditUnavailable
	}
	checked := evidencebind.Check(expected, input)
	if !checked.BindingValid {
		return Result{Verdict: Inconclusive, Reason: EvidenceRejected, Check: checked}, nil
	}
	if checked.EffectiveValue == oracle.Confirmed {
		return Result{Verdict: Reproduced, Reason: ConfirmedEvidence, Check: checked}, nil
	}
	return Result{Verdict: Inconclusive, Reason: NotConfirmed, Check: checked}, nil
}
