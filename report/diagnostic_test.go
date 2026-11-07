package report

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func diagnosticFixture() DiagnosticReport {
	return DiagnosticReport{TaskID: 7, GeneratedAt: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC), Entries: []DiagnosticEntry{
		{ID: "finding-1", Kind: "finding", Title: "XSS", VulnClass: "XSS", Severity: "high", Outcome: "confirmed", Verification: "stored_verification", EvidenceVersion: 1, Evidence: "synthetic escaped response", Report: "1. 합성 요청\n2. 실제 관찰과 예상 결과 비교"},
		{ID: "finding-2", Kind: "finding", Title: "정보 노출", VulnClass: "information disclosure", Severity: "medium", Outcome: "confirmed", Verification: "stored_verification", EvidenceVersion: 1},
		{ID: "finding-3", Kind: "finding", Title: "IDOR", VulnClass: "IDOR", Severity: "high", Outcome: "confirmed", Verification: "stored_verification", EvidenceVersion: 1},
		{ID: "candidate-4", Kind: "candidate", Title: "SQL injection candidate", VulnClass: "SQLi", Severity: "미평가", Outcome: "review", Verification: "manual_review_required", Status: "completed", EvidenceVersion: 1, Truncated: true},
		{ID: "candidate-5", Kind: "candidate", Title: "SSRF policy block", VulnClass: "SSRF", Severity: "미평가", Outcome: "not_executed", Verification: "manual_review_required", Status: "policy_not_executed", EvidenceVersion: 1},
	}}
}
func TestDiagnosticReportMixedSyntheticComposition(t *testing.T) {
	in := diagnosticFixture()
	body, err := DiagnosticHTML(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{"<td>3</td><td>1</td><td>1</td><td>0</td><td>0</td>", "SQL injection candidate", "SSRF policy block", "finding-3", "1. 합성 요청", "스크린샷 부재", "출력이 잘렸습니다"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(s, "<script") || strings.Contains(s, "<img") || strings.Contains(s, "href=") {
		t.Fatal("active resource in static report")
	}
	if in.Entries[0].Title != "XSS" {
		t.Fatal("mutated source")
	}
}
func TestDiagnosticReportInternalSharedAndEscaping(t *testing.T) {
	in := diagnosticFixture()
	in.Entries[0].Title = "<img src=x onerror=alert(1)>"
	in.Entries[0].Summary = "Authorization: Bearer synthetic-secret\npassword=fixture-password\nhttps://user:password@example.invalid/a?token=private\nuser@example.invalid"
	in.Entries[0].Evidence = "<script>alert('bad')</script>"
	internal, err := DiagnosticHTML(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"synthetic-secret", "fixture-password", "user:password", "token=private", "user@example.invalid", "<script>alert", "<img src=x"} {
		if strings.Contains(string(internal), secret) {
			t.Fatalf("unsafe output %q", secret)
		}
	}
	if !strings.Contains(string(internal), "&lt;script&gt;") {
		t.Fatal("evidence was not escaped")
	}
	in.Shared = true
	shared, err := DiagnosticHTML(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(shared), "SQL injection candidate") || strings.Contains(string(shared), "&lt;script&gt;") {
		t.Fatal("shared free text leaked")
	}
	if !strings.Contains(string(shared), "<td>3</td><td>1</td><td>1</td>") {
		t.Fatal("shared counts changed")
	}
}
func TestDiagnosticReportRejectsUnverifiedConfirmationAndBounds(t *testing.T) {
	for _, modify := range []func(*DiagnosticReport){
		func(in *DiagnosticReport) { in.Entries[3].Outcome = "confirmed" },
		func(in *DiagnosticReport) { in.Entries[0].Stale = true },
		func(in *DiagnosticReport) { in.Entries[0].Verification = "manual_review_required" },
		func(in *DiagnosticReport) { in.Entries[1].ID = in.Entries[0].ID },
		func(in *DiagnosticReport) { in.TaskID = 0 },
		func(in *DiagnosticReport) { in.Entries[0].ID = "arbitrary secret" },
		func(in *DiagnosticReport) { in.Entries[3].Verification = "arbitrary secret" },
		func(in *DiagnosticReport) { in.Entries[0].Evidence = strings.Repeat("x", MaxDiagnosticReportBytes+1) },
		func(in *DiagnosticReport) { in.Entries[0].Evidence = "null\x00value" },
	} {
		in := diagnosticFixture()
		modify(&in)
		if _, err := DiagnosticHTML(in); err == nil {
			t.Fatal("invalid report accepted")
		}
	}
	in := diagnosticFixture()
	in.Entries = nil
	for i := 0; i < MaxFindings+1; i++ {
		in.Entries = append(in.Entries, DiagnosticEntry{ID: fmt.Sprint(i), Outcome: "review"})
	}
	if _, err := DiagnosticHTML(in); err == nil {
		t.Fatal("overflow accepted")
	}
}
