package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Autumn-27/artex/remediation"
	"html"
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
	for _, want := range []string{"<td>1</td><td>3</td><td>0</td><td>0</td><td>1</td><td>0</td><td>0</td>", "SQL injection candidate", "SSRF policy block", "finding-3", "1. 합성 요청", "스크린샷 부재", "출력이 잘렸습니다"} {
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

func TestDiagnosticReportRejectsContradictoryAssessment(t *testing.T) {
	for _, tc := range []struct{ status, reason, outcome string }{
		{"confirmed", "html_nosniff_missing_twice", "review"},
		{"negative", "html_nosniff_present_twice", "confirmed"},
		{"unsupported", "type_not_supported", "negative"},
		{"insufficient", "independent_observation_missing", "confirmed"},
	} {
		in := diagnosticFixture()
		in.Entries[3].AssessmentStatus = tc.status
		in.Entries[3].AssessmentReason = tc.reason
		in.Entries[3].Outcome = tc.outcome
		if _, err := DiagnosticHTML(in); err != ErrExportBlocked {
			t.Fatalf("status=%s outcome=%s err=%v", tc.status, tc.outcome, err)
		}
		if _, err := DiagnosticDOCX(in); err != ErrExportBlocked {
			t.Fatalf("docx status=%s outcome=%s err=%v", tc.status, tc.outcome, err)
		}
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
	if !strings.Contains(string(shared), "<td>1</td><td>3</td><td>0</td><td>0</td><td>1</td>") {
		t.Fatal("shared counts changed")
	}
}

func TestDiagnosticReportMasksProseCredentials(t *testing.T) {
	in := diagnosticFixture()
	in.Entries[0].Summary = "The default password is `synthetic-pass-83z`. The response confirmed that the configuration was exposed."
	in.Entries[0].Evidence = "Session token `synthetic-token-83z`\n인증 쿠키는 `synthetic-cookie-83z`"
	in.Entries[0].Report = "비밀번호 노출\n| 계정 | Password |\n| --- | --- |\n| admin | synthetic-pass-83z |\n| tester | synthetic-pass-84z |"
	in.Entries[0].Evidence += "\nfixture1/synthetic-one, fixture2/synthetic-two 평문으로 노출됨"
	for _, shared := range []bool{false, true} {
		in.Shared = shared
		out, err := DiagnosticHTML(in)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "synthetic-") {
			t.Fatalf("credential exposed (shared=%t)", shared)
		}
		if !shared && !strings.Contains(string(out), "자격정보 2건 노출됨") {
			t.Fatal("credential table count missing")
		}
		if !shared && !strings.Contains(string(out), "configuration was exposed") {
			t.Fatal("lost the non-secret finding context")
		}
		if !shared && !strings.Contains(string(out), "자격정보 2건") {
			t.Fatal("lost the evidence count")
		}
	}
}

func TestDiagnosticReportSummarizesNewCredentialRedactions(t *testing.T) {
	in := diagnosticFixture()
	in.Entries[0].Summary = "비밀번호 alpha/synthetic-one, beta(synthetic-two) 노출"
	in.Entries[0].Evidence = "- **Password**: `synthetic-three`"
	in.Entries[0].Report = "비밀번호 노출\n| 계정 | Password |\n| --- | --- |\n| tester | synthetic-four |"
	in.Entries[1].Summary = "비밀번호 변경 요청은 HTTP/1.1 200, 토큰은 application/json 본문"
	in.Entries[1].Evidence = "이미 가린 값 password=[REDACTED], 주소 user@owned.test"
	for _, shared := range []bool{false, true} {
		in.Shared = shared
		prepared, err := PrepareDiagnostic(in)
		if err != nil || prepared.ExposedCredentialValues != 4 {
			t.Fatalf("shared=%v 노출 건수=%d 오류=%v", shared, prepared.ExposedCredentialValues, err)
		}
		body, err := DiagnosticHTML(in)
		if err != nil || !strings.Contains(string(body), "대상이 노출한 자격 값: 노출됨 4건") || strings.Contains(string(body), "synthetic-") {
			t.Fatalf("shared=%v HTML 요약 또는 가림 오류: %v", shared, err)
		}
		doc, err := DiagnosticDOCX(in)
		if err != nil {
			t.Fatal(err)
		}
		word := wordDocumentText(t, readWord(t, doc)["word/document.xml"])
		if !strings.Contains(word, "대상이 노출한 자격 값: 노출됨 4건") || strings.Contains(word, "synthetic-") {
			t.Fatalf("shared=%v DOCX 요약 또는 가림 오류", shared)
		}
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

func TestPrepareDiagnosticValidatesProductConfirmation(t *testing.T) {
	valid := DiagnosticProductConfirmation{VerdictID: "42", ResourceType: "document", EvidenceCalls: 1}
	for _, tc := range []struct {
		name   string
		change func(*DiagnosticProductConfirmation)
	}{
		{"non-numeric verdict ID", func(row *DiagnosticProductConfirmation) { row.VerdictID = "verdict-42" }},
		{"non-canonical verdict ID", func(row *DiagnosticProductConfirmation) { row.VerdictID = "042" }},
		{"non-positive verdict ID", func(row *DiagnosticProductConfirmation) { row.VerdictID = "0" }},
		{"zero evidence calls", func(row *DiagnosticProductConfirmation) { row.EvidenceCalls = 0 }},
		{"negative evidence calls", func(row *DiagnosticProductConfirmation) { row.EvidenceCalls = -1 }},
		{"unknown seal reason", func(row *DiagnosticProductConfirmation) { row.SealUnconfirmedReason = "unexpected" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := diagnosticFixture()
			in.ProductConfirmed = []DiagnosticProductConfirmation{valid}
			tc.change(&in.ProductConfirmed[0])
			if _, err := PrepareDiagnostic(in); !errors.Is(err, ErrExportBlocked) {
				t.Fatalf("잘못된 제품 판정을 허용함: %v", err)
			}
		})
	}
	in := diagnosticFixture()
	in.ProductConfirmed = []DiagnosticProductConfirmation{valid}
	prepared, err := PrepareDiagnostic(in)
	if err != nil || prepared.Confirmed != 4 || prepared.ByType["IDOR"] != 1 || prepared.ByType["제품 접근통제(IDOR)"] != 1 {
		t.Fatalf("일반·제품 판정의 집계가 다름: %+v, %v", prepared.ByType, err)
	}
}

func TestDiagnosticReportRemediationBindingAndSharing(t *testing.T) {
	in := diagnosticFixture()
	source := remediation.Input{FindingID: "candidate-4", ReportVersion: "sha256:" + strings.Repeat("a", 64), EvidenceVersion: "1", EvidenceDigest: strings.Repeat("b", 64), SourceDigest: strings.Repeat("c", 64), VulnClass: "sql_injection", Status: "review", EvidenceCount: 1, Technologies: []remediation.Technology{{Kind: "language", Name: "Java", Version: "17", Source: "user_provided", Confidence: "confirmed"}}}
	bundle, err := remediation.Build(source)
	if err != nil {
		t.Fatal(err)
	}
	in.Entries[3].RemediationSource = &source
	in.Entries[3].RemediationBundle = &bundle
	body, err := DiagnosticHTML(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html.UnescapeString(string(body)), bundle.AIPrompt) {
		t.Fatal("stored prompt changed")
	}
	in.Shared = true
	body, err = DiagnosticHTML(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "PreparedStatement") || strings.Contains(string(body), bundle.InputDigest) {
		t.Fatal("shared action leaked")
	}
	if in.Entries[3].RemediationBundle == nil {
		t.Fatal("source bundle mutated")
	}
	in.Shared = false
	in.Entries[3].RemediationStale = true
	if _, err = DiagnosticHTML(in); err == nil {
		t.Fatal("stale prompt rendered")
	}
	in.Entries[3].RemediationStale = false
	in.Entries[3].EvidenceVersion = 2
	if _, err = DiagnosticHTML(in); err == nil {
		t.Fatal("other evidence version rendered")
	}
	in.Entries[3].EvidenceVersion = 1
	corrupt := bundle
	corrupt.AIPrompt += " unauthorized instruction"
	in.Entries[3].RemediationBundle = &corrupt
	if _, err = DiagnosticHTML(in); err == nil {
		t.Fatal("mutated prompt rendered")
	}
}

func TestDiagnosticReportPreservesProvenanceAndExecutionScope(t *testing.T) {
	in := diagnosticFixture()
	source := remediation.Input{FindingID: "candidate-4", ReportVersion: "sha256:" + strings.Repeat("a", 64), EvidenceVersion: "1", EvidenceDigest: strings.Repeat("b", 64), SourceDigest: strings.Repeat("c", 64), VulnClass: "sql_injection", Status: "review", EvidenceCount: 1,
		Context: remediation.Context{Component: "orders-api", Observation: "Manual correction", Source: "user_provided", ObservationsTruncated: true, Observations: []remediation.ContextObservation{
			{Component: "orders-api", Observation: "Original response", Source: "observed", EvidenceRef: "evidence-1", RunID: "run-1", CallID: "call-1", StepID: "step-1", Truncated: true},
			{Component: "orders-api", Observation: "Analyzer hypothesis", Source: "inferred", EvidenceRef: "evidence-2", StepID: "step-2", DependsOn: []string{"step-1"}},
		}},
		Technologies: []remediation.Technology{
			{Kind: "language", Name: "Go", Version: "1.26", Source: "observed", Confidence: "observed", Component: "orders-api", ComponentRole: "backend", EvidenceRef: "evidence-1"},
			{Kind: "language", Name: "Elixir", Version: "1.17", Source: "user_provided", Confidence: "confirmed", Component: "orders-api", ComponentRole: "backend"},
		},
	}
	bundle, err := remediation.Build(source)
	if err != nil {
		t.Fatal(err)
	}
	in.Entries[3].RemediationSource = &source
	in.Entries[3].RemediationBundle = &bundle
	body, err := DiagnosticHTML(in)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := DiagnosticDOCX(in)
	if err != nil {
		t.Fatal(err)
	}
	wordOutput := wordDocumentText(t, readWord(t, doc)["word/document.xml"])
	output := html.UnescapeString(string(body))
	for _, want := range []string{"실행 관측", "모델·분석기 추정", "사용자 제공·수동 검토", "Original response", "Manual correction", "evidence-1", "step-1", "원본 상충 가능", "기술 상충 가능", "관측 또는 기술 목록이 잘렸습니다", "보완이 필요한 맥락", "실제 수정 실행형", "실행 범위는 전달받는 AI 세션의 권한", bundle.AIPrompt} {
		if !strings.Contains(output, want) || !strings.Contains(wordOutput, want) {
			t.Fatalf("missing %q", want)
		}
	}
	in.Shared = true
	body, err = DiagnosticHTML(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"Original response", "Manual correction", "evidence-1", "orders-api", bundle.AIPrompt} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("shared leak %q", secret)
		}
	}
}

func TestDiagnosticBundleCopyRedactsEveryNestedString(t *testing.T) {
	bundle := remediation.Bundle{AIPrompt: "Authorization: Bearer prompt-fixture-private", Context: remediation.Context{Observation: "password=observation-fixture-private", Observations: []remediation.ContextObservation{{Observation: "Authorization: Bearer step-fixture-private"}}}, SecurityCondition: []string{"password=condition-fixture-private"}, CodeExample: []string{"password=example-fixture-private"}, Technologies: []remediation.Technology{{Name: "person-fixture@example.invalid"}}}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	clean, err := diagnosticBundleCopy(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(clean)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"prompt-fixture-private", "observation-fixture-private", "step-fixture-private", "condition-fixture-private", "example-fixture-private", "person-fixture@example.invalid"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("bundle leak %s", secret)
		}
	}
	original, err := json.Marshal(bundle)
	if err != nil || string(raw) != string(original) {
		t.Fatal("source bundle mutated", err)
	}
	if _, err = diagnosticBundleCopy([]byte(`{"ai_prompt":"nul\u0000input"}`)); err == nil {
		t.Fatal("NUL accepted")
	}
}
