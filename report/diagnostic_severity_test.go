package report

import (
	"strings"
	"testing"
	"time"
)

func TestEstimatedSeverityInternalSharedAndVerdictSeparation(t *testing.T) {
	in := DiagnosticReport{TaskID: 1, GeneratedAt: time.Now(), Entries: []DiagnosticEntry{{ID: "candidate-1", Kind: "candidate", Outcome: "review", Verification: "manual_review_required", Status: "completed", Severity: "high", SeveritySource: "worker_estimate", SeverityRationale: "합성 근거", EvidenceVersion: 1}}}
	// 한국어 표시 근거는 원문을 안전하게 가리고, 집계는 확정만 센다.
	in.Entries[0].SeverityRationale = "비공개 문서 응답으로 접근통제 영향을 검토해야 합니다. Authorization: Bearer synthetic-rationale-secret"
	prepared, err := PrepareDiagnostic(in)
	if err != nil || prepared.Confirmed != 0 || prepared.Review != 1 || len(prepared.BySeverity) != 0 {
		t.Fatal("예상 평가가 확정 집계에 들어감", err)
	}
	html, err := DiagnosticHTML(in)
	if err != nil || !strings.Contains(string(html), "예상 높음") || strings.Contains(string(html), "synthetic-rationale-secret") {
		t.Fatal("내부 HTML 평가·DLP 불일치", err)
	}
	word, err := DiagnosticDOCX(in)
	if err != nil || len(word) == 0 {
		t.Fatal("DOCX 실패", err)
	}
	in.Shared = true
	shared, err := PrepareDiagnostic(in)
	if err != nil || shared.Entries[0].Severity != "미평가" || shared.Entries[0].SeverityRationale != "" || shared.Entries[0].SeveritySource != "" {
		t.Fatal("공유 평가 노출", err)
	}
	raw, err := DiagnosticHTML(in)
	if err != nil || strings.Contains(string(raw), "예상 높음") || strings.Contains(string(raw), "비공개 문서 응답") {
		t.Fatal("공유 HTML 평가 노출", err)
	}
	for _, outcome := range []string{"unknown", "not_executed", "negative"} {
		c := in
		c.Shared = false
		c.Entries = append([]DiagnosticEntry(nil), in.Entries...)
		c.Entries[0].Outcome = outcome
		p, err := PrepareDiagnostic(c)
		if err != nil || p.Entries[0].Severity != "미평가" || p.Entries[0].SeveritySource != "" {
			t.Fatal("불명·미실행·정상 대조 등급 유지", err)
		}
	}
	for _, status := range []string{"result_unknown", "policy_not_executed", "partial_policy_denial"} {
		c := in
		c.Shared = false
		c.Entries = append([]DiagnosticEntry(nil), in.Entries...)
		c.Entries[0].Status = status
		p, err := PrepareDiagnostic(c)
		if err != nil || p.Entries[0].Severity != "미평가" {
			t.Fatal("불충분한 실행 상태의 평가 표시", err)
		}
	}

	c := in
	c.Shared = false
	c.Entries = append([]DiagnosticEntry(nil), in.Entries...)
	c.Entries[0].SourceLoadFailed = true
	p, err := PrepareDiagnostic(c)
	if err != nil || p.Entries[0].Severity != "미평가" {
		t.Fatal("근거 손상 평가 유지", err)
	}
	c.Entries[0].SourceLoadFailed = false
	c.Entries[0].Outcome = "confirmed"
	if _, err = PrepareDiagnostic(c); err == nil {
		t.Fatal("critical 후보도 확정될 수 없음")
	}
}
