//go:build linux

package diagnosisprep

import (
	"context"
)

// CandidateBudgetRecord is a conservative, immutable reservation. One
// selected request consumes one complete installed run allotment, including
// the later E² and final model stages. It is never refunded on an unknown
// outcome or cancellation.
type CandidateBudgetRecord struct {
	RequestID          int64
	TaskID             string
	EvidenceDigest     string
	DelegationRef      string
	InstallationDigest string
	Budget             Budget
}

type candidateBudgetBackend interface {
	CandidateBudgetUsage(context.Context, string) (Budget, int64, error)
	ReadCandidateBudget(context.Context, int64) (*CandidateBudgetRecord, error)
	InsertCandidateBudget(context.Context, CandidateBudgetRecord) error
}

// ReserveCandidateBudget serializes with ordinary Start under the same
// service lock. The backend's DB row survives a process restart and is counted
// by both admission paths. A repeated request never reserves a second run.
func (s *Service) ReserveCandidateBudget(ctx context.Context, originalTaskID string, requestID int64, digest string) error {
	if s == nil || ctx == nil || ctx.Err() != nil || originalTaskID == "" || requestID <= 0 || !digestPattern.MatchString(digest) {
		return ErrCandidateBudget
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, ok := s.backend.(candidateBudgetBackend)
	if !ok || s.ctx.Err() != nil || s.closed || s.failed || s.released {
		return ErrCandidateBudget
	}
	var original *receipt
	for _, row := range s.jobs {
		if row.Principal == "ARTEX" && row.Job.TaskID == originalTaskID && row.StartKey != "" && row.Job.State == Completed {
			copy := row
			if original != nil {
				return ErrCandidateBudget
			}
			original = &copy
		}
	}
	if original == nil || original.Job.Scope == nil {
		return ErrCandidateBudget
	}
	inspect, err := s.backend.Inspect(ctx, "ARTEX", original.Job.Input)
	if err != nil || !inspectionOK(inspect) || inspect.Scope == nil || inspect.ReasonCode != "" ||
		!inspect.Scope.ExpiresAt.After(s.now()) || inspect.Scope.TargetRef != original.Job.Scope.TargetRef ||
		inspect.Scope.Origin != original.Job.Scope.Origin || inspect.Scope.Model != nil && inspect.Scope.Model.ExternalTransfer && !original.ExternalTransferConfirmed {
		return ErrCandidateBudget
	}
	if len(s.candidateActive) != 0 && !s.candidateActive[requestID] {
		return ErrCandidateBudget
	}
	want := CandidateBudgetRecord{RequestID: requestID, TaskID: originalTaskID, EvidenceDigest: digest,
		DelegationRef: inspect.DelegationRef, InstallationDigest: inspect.InstallationDigest, Budget: inspect.Scope.Budget}
	existing, err := ledger.ReadCandidateBudget(ctx, requestID)
	if err != nil {
		return ErrCandidateBudget
	}
	if existing != nil {
		if *existing != want {
			return ErrCandidateBudget
		}
		if s.candidateActive == nil {
			s.candidateActive = map[int64]bool{}
		}
		s.candidateActive[requestID] = true
		return nil
	}
	var used Budget
	var runs int64
	for _, row := range s.jobs {
		if blocksStart(row) {
			return ErrCandidateBudget
		}
		if row.StartKey != "" && row.Inspection.DelegationRef == inspect.DelegationRef {
			runs++
			if !withinBudget(used, row.Inspection.Scope.Budget, inspect.AggregateBudget) {
				return ErrCandidateBudget
			}
			used = addBudget(used, row.Inspection.Scope.Budget)
		}
	}
	candidateUsed, candidateRuns, err := ledger.CandidateBudgetUsage(ctx, inspect.DelegationRef)
	if err != nil || candidateRuns < 0 || !withinBudget(used, candidateUsed, inspect.AggregateBudget) {
		return ErrCandidateBudget
	}
	used, runs = addBudget(used, candidateUsed), runs+candidateRuns
	if runs >= inspect.MaxRuns || !withinBudget(used, want.Budget, inspect.AggregateBudget) {
		return ErrCandidateBudget
	}
	if err := ledger.InsertCandidateBudget(ctx, want); err != nil {
		return ErrCandidateBudget
	}
	if s.candidateActive == nil {
		s.candidateActive = map[int64]bool{}
	}
	s.candidateActive[requestID] = true
	return nil
}

// FinishCandidateBudget releases only the live installation gate. The
// reservation remains immutable and continues to count after every outcome.
func (s *Service) FinishCandidateBudget(requestID int64) {
	if s == nil || requestID <= 0 {
		return
	}
	s.mu.Lock()
	delete(s.candidateActive, requestID)
	s.mu.Unlock()
}
