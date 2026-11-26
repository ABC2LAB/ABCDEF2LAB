package report

import (
	"strings"
	"testing"
	"time"
)

func TestSelectedRequestKeepsSuspectReportWithoutVerdict(t *testing.T) {
	now := time.Now().UTC()
	in := DiagnosticReport{TaskID: 42, GeneratedAt: now, Run: &DiagnosticRun{VerificationVersion: "selected/llm-v1"}, Entries: []DiagnosticEntry{{
		ID: "candidate-1", Kind: "candidate", Title: "Synthetic concern", Outcome: "review", Verification: "manual_review_required",
		AssessmentStatus: "insufficient", AssessmentReason: "selected_verification_required", EvidenceVersion: 1,
		Digest: strings.Repeat("a", 64), LatestVerification: &DiagnosticLatestVerification{
			State: "requested", Reason: "selected_by_user", Method: "llm/v1", EvidenceDigest: strings.Repeat("a", 64), RequestedAt: now,
		},
	}}}
	prepared, err := PrepareDiagnostic(in)
	if err != nil || prepared.Confirmed != 0 || prepared.Suspected != 1 || prepared.Inconclusive != 0 {
		t.Fatalf("selected request changed assessment: confirmed=%d suspected=%d inconclusive=%d err=%v", prepared.Confirmed, prepared.Suspected, prepared.Inconclusive, err)
	}
	if _, err := DiagnosticHTML(in); err != nil {
		t.Fatal("HTML rejected selected request", err)
	}
	if _, err := DiagnosticDOCX(in); err != nil {
		t.Fatal("DOCX rejected selected request", err)
	}
	for _, change := range []func(*DiagnosticReport){
		func(r *DiagnosticReport) { r.Entries[0].LatestVerification.Verdict = "confirmed" },
		func(r *DiagnosticReport) {
			r.Entries[0].LatestVerification.Sources = []DiagnosticVerificationSourceRef{{CallRef: "fabricated"}}
		},
		func(r *DiagnosticReport) { r.Entries[0].Outcome = "confirmed" },
		func(r *DiagnosticReport) { r.Run.VerificationVersion = "passive/v2" },
	} {
		copy := in
		copy.Run = &DiagnosticRun{VerificationVersion: in.Run.VerificationVersion}
		copy.Entries = append([]DiagnosticEntry(nil), in.Entries...)
		latest := *in.Entries[0].LatestVerification
		copy.Entries[0].LatestVerification = &latest
		change(&copy)
		if _, err := PrepareDiagnostic(copy); err != ErrExportBlocked {
			t.Fatalf("unverified selected request exported: %v", err)
		}
	}
}
