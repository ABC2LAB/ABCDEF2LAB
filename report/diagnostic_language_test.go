package report

import (
	"strings"
	"testing"
	"time"
)

// 한국어 칸 언어 섞임 표시(2026-11-24): 다시 써도 한자·가나가 남은 후보는 내부용·공유용 보고서 둘 다 그 사실을 적는다.
func TestDiagnosticLanguageMixedNote(t *testing.T) {
	const note = "다른 언어 섞임"
	entry := DiagnosticEntry{ID: "candidate-1", Kind: "candidate", Outcome: "review", Verification: "manual_review_required", Status: "completed", Title: "합성 후보", Summary: "관찰", EvidenceVersion: 1}
	for _, shared := range []bool{false, true} {
		for _, mixed := range []bool{false, true} {
			e := entry
			e.LanguageMixed = mixed
			in := DiagnosticReport{TaskID: 1, GeneratedAt: time.Now(), Shared: shared, Entries: []DiagnosticEntry{e}}
			html, err := DiagnosticHTML(in)
			if err != nil {
				t.Fatal(err)
			}
			word, err := DiagnosticDOCX(in)
			if err != nil {
				t.Fatal(err)
			}
			doc := string(readWord(t, word)["word/document.xml"])
			if strings.Contains(string(html), note) != mixed || strings.Contains(doc, note) != mixed {
				t.Fatalf("shared=%v mixed=%v: HTML %v Word %v", shared, mixed, strings.Contains(string(html), note), strings.Contains(doc, note))
			}
		}
	}
}
