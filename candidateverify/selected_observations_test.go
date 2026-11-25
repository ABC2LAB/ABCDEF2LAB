package candidateverify

import (
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

func TestProjectTrustedSelectedDoesNotRequireTypeSpecificHeader(t *testing.T) {
	candidate, calls := v2Fixture(t, "nosniff_missing", "nosniff_missing")
	for i := range calls {
		calls[i].Execution.PolicyRevision = "policy-1"
		calls[i].Execution.HeaderMode = e2proxy.HeaderPreserve
		calls[i].Call.Observations[0].HTMLSecurity = ""
		calls[i].Call.Observations[0].Technologies = nil
	}
	rows, err := ProjectTrustedSelected(candidate.TaskID, candidate.RunRef, calls)
	if err != nil || len(rows) != 2 {
		t.Fatalf("generic projection: count=%d err=%v", len(rows), err)
	}
	for _, row := range rows {
		if !ValidSelectedStoredObservation(candidate.TaskID, candidate.RunRef, row) {
			t.Fatalf("invalid generic projection: %+v", row)
		}
	}
	corrupt := rows[0]
	corrupt.Source.Digest = "wrong"
	if ValidSelectedStoredObservation(candidate.TaskID, candidate.RunRef, corrupt) {
		t.Fatal("corrupt observation digest accepted")
	}
	corrupt = rows[0]
	corrupt.Source.Observation.Decision = "denied"
	if ValidSelectedStoredObservation(candidate.TaskID, candidate.RunRef, corrupt) {
		t.Fatal("unsent observation accepted")
	}
	if rows, err := ProjectTrustedSelected(candidate.TaskID+1, candidate.RunRef, calls); err != nil || len(rows) != 0 {
		t.Fatalf("foreign task projected: count=%d err=%v", len(rows), err)
	}
}

func TestProjectTrustedSelectedRejectsDuplicateOrUnfinishedEvidence(t *testing.T) {
	candidate, calls := v2Fixture(t, "nosniff_missing", "nosniff_missing")
	for i := range calls {
		calls[i].Execution.PolicyRevision = "policy-1"
		calls[i].Execution.HeaderMode = e2proxy.HeaderPreserve
	}
	calls[1].Call.Observations[0].AttemptRef = calls[0].Call.Observations[0].AttemptRef
	if rows, err := ProjectTrustedSelected(candidate.TaskID, candidate.RunRef, calls); err != nil || len(rows) != 0 {
		t.Fatalf("duplicate attempt projected: count=%d err=%v", len(rows), err)
	}
	calls[1].Call.Observations[0].AttemptRef = "attempt-after-save"
	calls[1].Execution.AuditComplete = false
	if rows, err := ProjectTrustedSelected(candidate.TaskID, candidate.RunRef, calls); err != nil || len(rows) != 1 {
		t.Fatalf("unfinished evidence projected: count=%d err=%v", len(rows), err)
	}
}

func TestProjectTrustedSelectedKeepsObservationWhenProofExceedsRowLimit(t *testing.T) {
	candidate, calls := v2Fixture(t, "nosniff_missing", "nosniff_missing")
	for i := range calls {
		calls[i].Execution.PolicyRevision = "policy-1"
		calls[i].Execution.HeaderMode = e2proxy.HeaderPreserve
	}
	calls[0].SelectedProof = &e2runner.SelectedAuditProof{}
	calls[0].SelectedProof.Event.Scope.PolicyRef = strings.Repeat("x", 5000)
	rows, err := ProjectTrustedSelected(candidate.TaskID, candidate.RunRef, calls)
	if err != nil || len(rows) != 2 || rows[0].SelectedProof != nil || !ValidSelectedStoredObservation(candidate.TaskID, candidate.RunRef, rows[0]) {
		t.Fatal("oversized checkpoint discarded the original observation")
	}
}
