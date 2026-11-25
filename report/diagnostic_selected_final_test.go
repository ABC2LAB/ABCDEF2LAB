package report

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestSelectedFinalVerdictCountsAndExports(t *testing.T) {
	evidence := "{}"
	sum := sha256.Sum256([]byte(evidence))
	now := time.Now().UTC()
	base := DiagnosticReport{TaskID: 81, GeneratedAt: now, Run: &DiagnosticRun{VerificationVersion: "selected/llm-v1"}, Entries: []DiagnosticEntry{{
		ID: "candidate-1", Kind: "candidate", VulnClass: "information_disclosure", Title: "Synthetic selected candidate",
		Evidence: evidence, Digest: hex.EncodeToString(sum[:]), EvidenceVersion: 1, Outcome: "confirmed",
		Verification: "selected_model_evidence_v1", AssessmentStatus: "confirmed", AssessmentReason: "selected_model_evidence_checked",
		LatestVerification: &DiagnosticLatestVerification{State: "completed", Verdict: "confirmed", Reason: "model_evidence_checked",
			Method: "llm/v1", EvidenceDigest: hex.EncodeToString(sum[:]), RequestedAt: now,
			Sources: []DiagnosticVerificationSourceRef{{"original-call", "original-attempt", strings.Repeat("a", 64)},
				{"followup-call", "followup-attempt", strings.Repeat("b", 64)}},
			Final: &DiagnosticFinalReview{Claim: "Observed disclosure", NormalExplanation: "A public example could be expected",
				Reason: "Independent observations support the concern", Limitations: "Only approved read path checked",
				Remediation: "Remove the disclosure", VerificationCriteria: "Repeat the approved read after fixing"},
		},
	}}}
	prepared, err := PrepareDiagnostic(base)
	if err != nil || prepared.Confirmed != 1 || prepared.Inconclusive != 0 {
		t.Fatalf("confirmed report: %+v %v", prepared, err)
	}
	html, err := DiagnosticHTML(base)
	if err != nil || !strings.Contains(string(html), "Independent observations support the concern") {
		t.Fatal("HTML omitted final reasoning", err)
	}
	docx, err := DiagnosticDOCX(base)
	if err != nil || !strings.Contains(string(readWord(t, docx)["word/document.xml"]), "Independent observations support the concern") {
		t.Fatal("DOCX omitted final reasoning", err)
	}
	shared := base
	shared.Shared = true
	sharedHTML, err := DiagnosticHTML(shared)
	if err != nil || strings.Contains(string(sharedHTML), "Independent observations support the concern") {
		t.Fatal("shared report leaked final reasoning", err)
	}
	for _, test := range []struct {
		verdict, outcome, verification    string
		confirmed, negative, inconclusive int
	}{
		{"negative", "negative", "manual_review_required", 0, 1, 0},
		{"insufficient", "review", "manual_review_required", 0, 0, 1},
	} {
		copy := base
		copy.Entries = append([]DiagnosticEntry(nil), base.Entries...)
		entry := &copy.Entries[0]
		latest := *entry.LatestVerification
		entry.LatestVerification = &latest
		entry.Outcome, entry.Verification, entry.AssessmentStatus, latest.Verdict = test.outcome, test.verification, test.verdict, test.verdict
		got, err := PrepareDiagnostic(copy)
		if err != nil || got.Confirmed != test.confirmed || got.Negative != test.negative || got.Inconclusive != test.inconclusive {
			t.Fatalf("%s counts: %+v %v", test.verdict, got, err)
		}
	}
	invalid := base
	invalid.Entries = append([]DiagnosticEntry(nil), base.Entries...)
	latest := *invalid.Entries[0].LatestVerification
	latest.Final = nil
	invalid.Entries[0].LatestVerification = &latest
	if _, err := PrepareDiagnostic(invalid); err != ErrExportBlocked {
		t.Fatal("missing final evidence description exported", err)
	}
	historical := base
	historical.Entries = append([]DiagnosticEntry(nil), base.Entries...)
	he := &historical.Entries[0]
	hl := *he.LatestVerification
	he.LatestVerification = &hl
	he.Outcome, he.Verification, he.AssessmentStatus, he.AssessmentReason = "review", "manual_review_required", "insufficient", "selected_verification_required"
	hl.State, hl.Reason, hl.Verdict, hl.Final, hl.Sources = "failed", "final_model_unavailable", "", nil, nil
	hl.RequestedAt = now.Add(time.Minute)
	hl.EarlierVerdict, hl.EarlierReason, hl.EarlierRequestedAt = "confirmed", "model_evidence_checked", now
	hl.EarlierSources = []DiagnosticVerificationSourceRef{{"original-call", "original-attempt", strings.Repeat("a", 64)},
		{"followup-call", "followup-attempt", strings.Repeat("b", 64)}}
	got, err := PrepareDiagnostic(historical)
	if err != nil || got.Confirmed != 0 || got.Suspected != 1 {
		t.Fatal("failed recent verification hid historical result or became current", err)
	}
}
