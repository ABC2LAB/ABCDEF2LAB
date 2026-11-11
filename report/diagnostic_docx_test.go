package report

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"github.com/Autumn-27/artex/remediation"
	"github.com/Autumn-27/artex/reproduction"
	"io"
	"strings"
	"testing"
)

func readWord(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	z, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil {
		t.Fatal(e)
	}
	files := map[string][]byte{}
	for _, f := range z.File {
		r, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(r)
		_ = r.Close()
		if e != nil {
			t.Fatal(e)
		}
		files[f.Name] = b
		if strings.HasSuffix(f.Name, ".xml") || strings.HasSuffix(f.Name, ".rels") {
			d := xml.NewDecoder(bytes.NewReader(b))
			for {
				_, e := d.Token()
				if e == io.EOF {
					break
				}
				if e != nil {
					t.Fatal(f.Name, e)
				}
			}
		}
	}
	return files
}
func TestDOCXUsesSameVerdictsStepsAndStoredPromptAsHTML(t *testing.T) {
	in := diagnosticFixture()
	e := &in.Entries[0]
	e.Run = "run-a"
	e.Digest = strings.Repeat("a", 64)
	ctx := remediation.Context{Observations: []remediation.ContextObservation{{Source: "observed", Observation: "Actual response", EvidenceRef: "proof-a", ObservedAt: "2026-11-11T00:00:00Z", RunID: e.Run, CallID: "call-a", StepID: "actual-step", Endpoint: "GET /safe"}}}
	e.Steps = reproduction.FromStored(in.TaskID, e.ID, e.EvidenceVersion, e.Digest, ctx)
	input := remediation.Input{FindingID: e.ID, Status: "confirmed", VulnClass: "xss", EvidenceVersion: "1", EvidenceDigest: e.Digest, ReportVersion: "sha256:" + strings.Repeat("b", 64), SourceDigest: strings.Repeat("b", 64), EvidenceCount: 1, Context: ctx}
	bundle, err := remediation.Build(input)
	if err != nil {
		t.Fatal(err)
	}
	e.RemediationBundle = &bundle
	e.RemediationSource = &input
	html, err := DiagnosticHTML(in)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := DiagnosticDOCX(in)
	if err != nil {
		t.Fatal(err)
	}
	files := readWord(t, doc)
	text := string(files["word/document.xml"])
	for _, s := range []string{e.ID, e.Digest, "Step 1.", "actual-step", "스크린샷 미확보", "① 판정과 영향", "② 재현 절차", "③ 보안 조치", "④ AI 수정 작업용 프롬프트", bundle.InputDigest, "3장. 진단 결과 요약"} {
		if !strings.Contains(text, s) || !strings.Contains(string(html), s) {
			t.Fatalf("formats diverged: %s", s)
		}
	}
	if strings.Contains(text, "TargetMode=\"External\"") || strings.Contains(text, "<script") || len(files) != 5 {
		t.Fatal("active relationship or unexpected document")
	}
	prepared, err := PrepareDiagnostic(in)
	if err != nil || prepared.Confirmed != 3 || prepared.Review != 1 {
		t.Fatal("different confirmed aggregate")
	}
	in.Shared = true
	shared, err := DiagnosticDOCX(in)
	if err != nil {
		t.Fatal(err)
	}
	sharedFiles := readWord(t, shared)
	for _, private := range []string{"Actual response", "/safe", "run-a", bundle.InputDigest, "[Execution procedure]"} {
		if strings.Contains(string(sharedFiles["word/document.xml"]), private) {
			t.Fatalf("shared detail leaked: %s", private)
		}
	}
}
func TestDOCXBlocksUnverifiedStaleOrInvalidXMLData(t *testing.T) {
	for _, change := range []func(*DiagnosticReport){func(d *DiagnosticReport) { d.Entries[0].Stale = true }, func(d *DiagnosticReport) { d.Entries[0].Verification = "manual_review_required" }} {
		in := diagnosticFixture()
		change(&in)
		if _, e := DiagnosticDOCX(in); e == nil {
			t.Fatal("invalid or stale document emitted")
		}
	}
}

func TestDOCXLongKoreanEvidenceAndEntryPageBreaks(t *testing.T) {
	in := diagnosticFixture()
	e := &in.Entries[0]
	e.Run = "run-a"
	e.Digest = strings.Repeat("a", 64)
	context := remediation.Context{}
	for i := 0; i < 32; i++ {
		context.Observations = append(context.Observations, remediation.ContextObservation{Source: "observed", Observation: strings.Repeat("저장된 시험 응답 확인 ", 32), RunID: e.Run, StepID: fmt.Sprintf("step-%d", i), EvidenceRef: fmt.Sprintf("proof-%d", i), ObservedAt: "2026-11-11T00:00:00Z"})
	}
	input := remediation.Input{FindingID: e.ID, Status: "confirmed", VulnClass: "idor", EvidenceVersion: "1", EvidenceDigest: e.Digest, ReportVersion: "sha256:" + strings.Repeat("b", 64), SourceDigest: strings.Repeat("b", 64), EvidenceCount: 32, Context: context}
	b, err := remediation.Build(input)
	if err != nil {
		t.Fatal(err)
	}
	e.RemediationBundle = &b
	e.RemediationSource = &input
	e.Steps = reproduction.FromStored(in.TaskID, e.ID, 1, e.Digest, context)
	raw, err := DiagnosticDOCX(in)
	if err != nil {
		t.Fatal(err)
	}
	parts := readWord(t, raw)
	doc := string(parts["word/document.xml"])
	if len(b.AIPrompt) < 10000 || !strings.Contains(doc, "저장된 시험 응답 확인") || strings.Count(doc, `w:val="EntryHeading"`) != len(in.Entries)-1 || !strings.Contains(string(parts["word/styles.xml"]), "w:pageBreakBefore") {
		t.Fatal("long prompt, Korean or page breaks lost")
	}
	for name, part := range parts {
		if strings.Contains(string(part), `TargetMode="External"`) || strings.Contains(string(part), "<w:instrText") {
			t.Fatal("active external content", name)
		}
	}
	for _, change := range []func(*reproduction.Step){func(s *reproduction.Step) { s.TaskID++ }, func(s *reproduction.Step) { s.EntryID = "finding-9999" }, func(s *reproduction.Step) { s.RunRef = "foreign" }, func(s *reproduction.Step) { s.EvidenceVersion++ }} {
		bad := in
		bad.Entries = append([]DiagnosticEntry{}, in.Entries...)
		bad.Entries[0].Steps = append([]reproduction.Step{}, e.Steps...)
		change(&bad.Entries[0].Steps[0])
		if _, err := DiagnosticDOCX(bad); err == nil {
			t.Fatal("foreign step exported")
		}
	}
}

func TestDOCXReplacesUnsupportedControlsWithoutLosingEvidence(t *testing.T) {
	in := diagnosticFixture()
	in.Entries[0].Evidence = "before\x1b[31m color\f page\b back after <w:evil/>"
	doc, err := DiagnosticDOCX(in)
	if err != nil {
		t.Fatal(err)
	}
	xmlText := string(readWord(t, doc)["word/document.xml"])
	for _, text := range []string{"before", "after", "제어 문자 3개", "U+FFFD", "&lt;w:evil/&gt;"} {
		if !strings.Contains(xmlText, text) {
			t.Fatalf("missing sanitized evidence %q", text)
		}
	}
	if strings.Contains(xmlText, "<w:evil/>") {
		t.Fatal("XML injected")
	}
}

func TestNonPositiveEntriesUseNeutralHeadings(t *testing.T) {
	for _, outcome := range []string{"review", "unknown", "not_executed", "negative"} {
		in := diagnosticFixture()
		in.Entries = in.Entries[:1]
		in.Entries[0].Outcome = outcome
		html, err := DiagnosticHTML(in)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := DiagnosticDOCX(in)
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{string(html), string(readWord(t, doc)["word/document.xml"])} {
			if strings.Contains(text, "① 확인된 취약점") || !strings.Contains(text, "① 판정과 영향") {
				t.Fatal("unverified result asserted confirmed")
			}
		}
	}
}

func TestReproductionWarningsAndOrderMatchAcrossExports(t *testing.T) {
	in := diagnosticFixture()
	e := &in.Entries[0]
	e.Run = "run-a"
	e.Digest = strings.Repeat("a", 64)
	e.Truncated = true
	e.StepsTruncated = true
	e.Steps = reproduction.FromStored(in.TaskID, e.ID, e.EvidenceVersion, e.Digest, remediation.Context{Observations: []remediation.ContextObservation{{Source: "observed", Method: "GET", RunID: e.Run, StepID: "step-a", EvidenceRef: "proof-a", ObservedAt: "2026-11-11T00:00:00Z", Truncated: true}}})
	html, err := DiagnosticHTML(in)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := DiagnosticDOCX(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(html), string(readWord(t, doc)["word/document.xml"])} {
		for _, warning := range []string{"실제 도구 출력이 잘렸습니다", "이 단계의 관측이 잘렸습니다", "모델 제안과 검증된 원인은 별개"} {
			if !strings.Contains(text, warning) {
				t.Fatalf("missing warning %q", warning)
			}
		}
	}
	// A valid-looking but out-of-order step must fail before either renderer.
	in.Entries[0].Steps[0].Number = 2
	if _, err := PrepareDiagnostic(in); err == nil {
		t.Fatal("out-of-order step accepted")
	}
}

func wordDocumentText(t *testing.T, raw []byte) string {
	t.Helper()
	var out strings.Builder
	d := xml.NewDecoder(bytes.NewReader(raw))
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch v := token.(type) {
		case xml.CharData:
			out.Write(v)
		case xml.StartElement:
			if v.Name.Local == "br" {
				out.WriteByte('\n')
			}
		case xml.EndElement:
			if v.Name.Local == "p" {
				out.WriteByte('\n')
			}
		}
	}
	return out.String()
}
