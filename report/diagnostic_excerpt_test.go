package report

import (
	"html"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Autumn-27/artex/remediation"
	"github.com/Autumn-27/artex/reproduction"
)

func TestDiagnosticExportShowsMaskedEvidenceExcerpts(t *testing.T) {
	in := diagnosticFixture()
	entry := &in.Entries[0]
	const password = "sample-account-value-96z"
	padding := strings.Repeat("가", diagnosticExcerptRunes)
	entry.Evidence = "POST /api/login\npassword=" + password + "\n응답 상태 200\n" + padding + " 증거 전문 끝"
	entry.Report = "상세 설명 시작 " + padding + " 상세 전문 끝"
	entry.Run = "run-owned"
	entry.Digest = strings.Repeat("a", 64)
	entry.Steps = reproduction.FromStored(in.TaskID, entry.ID, entry.EvidenceVersion, entry.Digest, remediation.Context{
		Observations: []remediation.ContextObservation{{
			Source: "observed", ObservedAt: "2026-11-24T12:00:00Z", RunID: entry.Run,
			CallID: "call-owned", StepID: "attempt-1", EvidenceRef: "ref-owned",
			Endpoint: "GET /safe", Observation: "저장 관측 시작 " + padding + " 관측 전문 끝",
		}},
	})
	if len(entry.Steps) != 1 {
		t.Fatal("test needs a bound observation")
	}
	originalEvidence, originalReport, originalObservation := entry.Evidence, entry.Report, entry.Steps[0].Observation
	prepared, err := prepareDiagnosticExport(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(prepared.Entries[0].Evidence, "POST /api/login\n***\n응답 상태 200\n") {
		t.Fatal("masked excerpt lost the public request or response context")
	}

	for _, shared := range []bool{false, true} {
		in.Shared = shared
		htmlDoc, err := DiagnosticHTML(in)
		if err != nil {
			t.Fatal(err)
		}
		docx, err := DiagnosticDOCX(in)
		if err != nil {
			t.Fatal(err)
		}
		word := wordDocumentText(t, readWord(t, docx)["word/document.xml"])
		for format, output := range map[string]string{"HTML": html.UnescapeString(string(htmlDoc)), "Word": word} {
			for _, hidden := range []string{"[REDACTED]", password, "sample-account", "증거 전문 끝", "상세 전문 끝", "관측 전문 끝"} {
				if strings.Contains(output, hidden) {
					t.Errorf("%s shared=%t exposed %q", format, shared, hidden)
				}
			}
			if shared {
				for _, context := range []string{"POST /api/login", "상세 설명 시작", "저장 관측 시작"} {
					if strings.Contains(output, context) {
						t.Errorf("%s shared disclosed %q", format, context)
					}
				}
				if !strings.Contains(output, "***") {
					t.Errorf("%s shared masked marker missing", format)
				}
				continue
			}
			for _, context := range []string{"POST /api/login", "응답 상태 200", "상세 설명 시작", "저장 관측 시작", "가…"} {
				if !strings.Contains(output, context) {
					t.Errorf("%s internal omitted excerpt context %q", format, context)
				}
			}
		}
	}

	if entry.Evidence != originalEvidence || entry.Report != originalReport || entry.Steps[0].Observation != originalObservation {
		t.Fatal("export modified the stored report data")
	}
}

func TestDiagnosticExcerptUnicodeBoundary(t *testing.T) {
	const sample = "저장된 관찰 [REDACTED]"
	if got := diagnosticExcerpt(sample); got != "저장된 관찰 ***" {
		t.Fatalf("short excerpt produced %q", got)
	}
	exact := strings.Repeat("가", diagnosticExcerptRunes)
	if got := diagnosticExcerpt(exact); got != exact {
		t.Fatal("excerpt trimmed text already at the limit")
	}
	if got := diagnosticExcerpt(exact + "나"); got != exact+"…" || !utf8.ValidString(got) {
		t.Fatal("excerpt split a character or lost the ellipsis")
	}
}
