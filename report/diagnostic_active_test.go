package report

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestActiveCandidateVerdictCountsAndExports(t *testing.T) {
	evidence := "{}"
	sum := sha256.Sum256([]byte(evidence))
	now := time.Now().UTC()
	base := DiagnosticReport{TaskID: 73, GeneratedAt: now, Run: &DiagnosticRun{VerificationVersion: "passive/v2"}, Entries: []DiagnosticEntry{{
		ID: "candidate-1", Kind: "candidate", VulnClass: "security_headers", Title: "Synthetic header candidate",
		Evidence: evidence, Digest: hex.EncodeToString(sum[:]), EvidenceVersion: 1, Outcome: "confirmed",
		Verification: "active_observation_v1", AssessmentStatus: "confirmed", AssessmentReason: "html_nosniff_missing_followup_twice",
		LatestVerification: &DiagnosticLatestVerification{State: "completed", Verdict: "confirmed", Reason: "html_nosniff_missing_followup_twice",
			Method: "active/v1", EvidenceDigest: hex.EncodeToString(sum[:]), RequestedAt: now,
			InitialVerdict: "insufficient", InitialReason: "independent_observation_missing",
			Sources: []DiagnosticVerificationSourceRef{{"call-one", "attempt-one", strings.Repeat("a", 64)}, {"call-two", "attempt-two", strings.Repeat("b", 64)}},
		},
	}}}
	prepared, err := PrepareDiagnostic(base)
	if err != nil || prepared.Confirmed != 1 || prepared.Inconclusive != 0 {
		t.Fatalf("active confirmation not counted: %+v %v", prepared, err)
	}
	html, err := DiagnosticHTML(base)
	if err != nil || !strings.Contains(string(html), "최초 자동 검증") || !strings.Contains(string(html), "attempt-two") {
		t.Fatal("HTML omitted active and initial evidence", err)
	}
	docx, err := DiagnosticDOCX(base)
	if err != nil || !strings.Contains(string(readWord(t, docx)["word/document.xml"]), "attempt-two") {
		t.Fatal("DOCX omitted active evidence", err)
	}
	for _, tc := range []struct {
		verdict, reason, outcome, verification string
		confirmed, negative, inconclusive      int
	}{
		{"negative", "html_nosniff_present_followup_twice", "negative", "manual_review_required", 0, 1, 0},
		{"insufficient", "observations_inconsistent", "review", "manual_review_required", 0, 0, 1},
	} {
		in := base
		in.Entries = append([]DiagnosticEntry(nil), base.Entries...)
		in.Entries[0].LatestVerification = &DiagnosticLatestVerification{}
		*in.Entries[0].LatestVerification = *base.Entries[0].LatestVerification
		entry := &in.Entries[0]
		entry.Outcome, entry.Verification, entry.AssessmentStatus, entry.AssessmentReason = tc.outcome, tc.verification, tc.verdict, tc.reason
		entry.LatestVerification.Verdict, entry.LatestVerification.Reason = tc.verdict, tc.reason
		got, err := PrepareDiagnostic(in)
		if err != nil || got.Confirmed != tc.confirmed || got.Negative != tc.negative || got.Inconclusive != tc.inconclusive {
			t.Fatalf("%s counts: %+v %v", tc.verdict, got, err)
		}
	}
	wrong := base
	wrong.Entries = append([]DiagnosticEntry(nil), base.Entries...)
	wrong.Entries[0].LatestVerification = nil
	if _, err := PrepareDiagnostic(wrong); err != ErrExportBlocked {
		t.Fatal("active confirmation without bound evidence exported", err)
	}
	storedAfterActive := base
	storedAfterActive.Entries = append([]DiagnosticEntry(nil), base.Entries...)
	storedAfterActive.Entries[0].LatestVerification = &DiagnosticLatestVerification{
		State: "completed", Verdict: "insufficient", Reason: "independent_observation_missing", Method: "stored/v1",
		EvidenceDigest: hex.EncodeToString(sum[:]), RequestedAt: now.Add(time.Second),
		EarlierVerdict: "confirmed", EarlierReason: "html_nosniff_missing_followup_twice", EarlierRequestedAt: now,
		EarlierSources: base.Entries[0].LatestVerification.Sources,
	}
	if got, err := PrepareDiagnostic(storedAfterActive); err != nil || got.Confirmed != 1 {
		t.Fatalf("stored review erased previous active confirmation: %+v %v", got, err)
	}
	if html, err := DiagnosticHTML(storedAfterActive); err != nil || !strings.Contains(string(html), "이전 유효 추가 검증") {
		t.Fatal("HTML omitted earlier active confirmation", err)
	}
	if docx, err := DiagnosticDOCX(storedAfterActive); err != nil || !strings.Contains(string(readWord(t, docx)["word/document.xml"]), "이전 유효 추가 검증") {
		t.Fatal("DOCX omitted earlier active confirmation", err)
	}
	failedAfterActive := storedAfterActive
	failedAfterActive.Entries = append([]DiagnosticEntry(nil), storedAfterActive.Entries...)
	failedAfterActive.Entries[0].Outcome = "review"
	failedAfterActive.Entries[0].Verification = "manual_review_required"
	failedAfterActive.Entries[0].AssessmentStatus = "insufficient"
	failedAfterActive.Entries[0].AssessmentReason = "independent_observation_missing"
	failedAfterActive.Entries[0].LatestVerification = &DiagnosticLatestVerification{}
	*failedAfterActive.Entries[0].LatestVerification = *storedAfterActive.Entries[0].LatestVerification
	failedAfterActive.Entries[0].LatestVerification.State = "failed"
	failedAfterActive.Entries[0].LatestVerification.Verdict = ""
	failedAfterActive.Entries[0].LatestVerification.Reason = "execution_interrupted"
	failedAfterActive.Entries[0].LatestVerification.Method = "active/v1"
	failedAfterActive.Entries[0].LatestVerification.InitialVerdict = "insufficient"
	failedAfterActive.Entries[0].LatestVerification.InitialReason = "independent_observation_missing"
	if got, err := PrepareDiagnostic(failedAfterActive); err != nil || got.Confirmed != 0 || got.Inconclusive != 1 {
		t.Fatalf("failed latest attempt presented historical confirmation as current: %+v %v", got, err)
	}
}
