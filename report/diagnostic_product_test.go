package report

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var productObserved = time.Date(2026, 11, 23, 10, 38, 7, 0, time.UTC)

func confirmedProductVerdict(id string) ProductVerdictEntry {
	return ProductVerdictEntry{VerdictID: id, Verified: true, ResourceType: "book", OracleValue: "confirmed",
		ReasonCodes: []string{"policy_deny_resolved", "subjects_distinct_verified", "data_nontrivial_match"}, EvidenceCalls: 5, Seal: "confirmed", ObservedAt: productObserved}
}

func productFixture(items ...ProductVerdictEntry) DiagnosticReport {
	in := diagnosticFixture()
	in.Product = ProductVerdicts{Provided: true, Total: len(items), Items: items}
	return in
}

// 진위 확인·봉인을 통과한 CONFIRMED만 요약 확정 수·심각도·유형 집계에 더해진다(사용자 결정 2026-11-23, #875 발견 1).
func TestProductVerdictsCountIntoSummary(t *testing.T) {
	base, err := PrepareDiagnostic(diagnosticFixture())
	if err != nil {
		t.Fatal(err)
	}
	unsealed := confirmedProductVerdict("2")
	unsealed.Seal = "unconfirmed"
	suspected := confirmedProductVerdict("3")
	suspected.OracleValue = "suspected"
	undecidable := confirmedProductVerdict("4")
	undecidable.OracleValue, undecidable.Seal, undecidable.EvidenceCalls = "undecidable", "", 0
	in := productFixture(confirmedProductVerdict("1"), unsealed, suspected, undecidable, ProductVerdictEntry{VerdictID: "5"}, ProductVerdictEntry{})
	got, err := PrepareDiagnostic(in)
	if err != nil {
		t.Fatal(err)
	}
	p := got.Product
	if got.Confirmed != base.Confirmed+1 || p.Confirmed != 1 || p.Unsealed != 1 || p.Suspected != 1 || p.Undecidable != 1 || p.Failed != 2 {
		t.Fatalf("counts: summary %d→%d product %+v", base.Confirmed, got.Confirmed, p)
	}
	if got.BySeverity["미평가"] != base.BySeverity["미평가"]+1 || got.ByType["접근통제(IDOR) · 확정 단계"] != 1 {
		t.Fatalf("breakdown %v %v", got.BySeverity, got.ByType)
	}
	body, err := DiagnosticHTML(in)
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	for _, want := range []string{"<h3>확정 단계 판정</h3>", "<td>4</td><td>1</td><td>1</td>", "verdict-1", "정책상 교차 계정 접근 금지 · 두 계정이 서로 다른 사용자임을 확인 · 다른 계정이 소유자 데이터를 읽음",
		"근거 봉인 미확인 확정 1(확정 수에 안 셈)", "확인 실패 2건(확정에 안 셈)", "번호 없음", "취약 의심", "판정 불가", "근거 호출 없음"} {
		if !strings.Contains(html, want) {
			t.Fatalf("HTML missing %q", want)
		}
	}
	if strings.Contains(html, "건 중 ") {
		t.Fatal("not truncated but says truncated")
	}
	doc, err := DiagnosticDOCX(in)
	if err != nil {
		t.Fatal(err)
	}
	text := string(readWord(t, doc)["word/document.xml"])
	for _, want := range []string{"확정 단계 판정", "verdict-1", "확인 실패 2건(확정에 안 셈)", "근거 봉인 미확인 확정 1(확정 수에 안 셈)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("DOCX missing %q", want)
		}
	}
	if in.Product.Items[0].ResourceType != "book" || in.Product.Items[0].ReasonCodes[0] != "policy_deny_resolved" {
		t.Fatal("mutated source")
	}
}

// 크기 상한으로 잘린 목록은 'n건 중 m건 표시'를 적는다. 목록을 읽지 못하면 그렇게 적고 확정 수에 넣지 않는다.
func TestProductVerdictsTruncatedAndUnavailable(t *testing.T) {
	in := productFixture(confirmedProductVerdict("1"), confirmedProductVerdict("2"))
	in.Product.Total = 3
	body, err := DiagnosticHTML(in)
	if err != nil || !strings.Contains(string(body), "3건 중 2건 표시(크기 상한)") {
		t.Fatalf("truncation: %v", err)
	}
	doc, err := DiagnosticDOCX(in)
	if err != nil || !strings.Contains(string(readWord(t, doc)["word/document.xml"]), "3건 중 2건 표시(크기 상한)") {
		t.Fatalf("DOCX truncation: %v", err)
	}
	base, _ := PrepareDiagnostic(diagnosticFixture())
	off := diagnosticFixture()
	off.Product = ProductVerdicts{Provided: true, Unavailable: true}
	got, err := PrepareDiagnostic(off)
	if err != nil || got.Confirmed != base.Confirmed {
		t.Fatalf("unavailable counted: %v %d", err, got.Confirmed)
	}
	body, _ = DiagnosticHTML(off)
	if !strings.Contains(string(body), "확정 단계 판정 목록을 읽지 못했습니다") {
		t.Fatal("unavailable text")
	}
	none, _ := DiagnosticHTML(diagnosticFixture())
	if strings.Contains(string(none), "확정 단계 판정</h3>") {
		t.Fatal("section without relay")
	}
	empty, _ := DiagnosticHTML(productFixture())
	if !strings.Contains(string(empty), "확정 단계 판정 0건.") {
		t.Fatal("empty list text")
	}
}

// 공유 사본은 기존 가림 규칙대로 자원 종류 이름과 유형 집계를 가린다. 판정 값·사유 코드 문구·건수는 남긴다.
func TestProductVerdictsSharedRedaction(t *testing.T) {
	in := productFixture(confirmedProductVerdict("1"))
	in.Product.Items[0].ResourceType = "고객 비밀 장부"
	in.Shared = true
	got, err := PrepareDiagnostic(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Product.Items[0].ResourceType != "[REDACTED]" || got.ByType["접근통제(IDOR) · 확정 단계"] != 0 || got.ByType["[REDACTED]"] == 0 {
		t.Fatalf("shared: %+v %v", got.Product.Items[0], got.ByType)
	}
	body, err := DiagnosticHTML(in)
	if err != nil || strings.Contains(string(body), "고객 비밀 장부") || !strings.Contains(string(body), "verdict-1") {
		t.Fatalf("shared HTML: %v", err)
	}
}

// 모양이 어긋난 입력은 보고서 전체를 막는다(진위 실패 행에 값이 남음·알 수 없는 값·중복 번호·상한).
func TestProductVerdictsRejectMalformed(t *testing.T) {
	leaked := ProductVerdictEntry{VerdictID: "9", ResourceType: "book"}
	badValue := confirmedProductVerdict("1")
	badValue.OracleValue = "vulnerable"
	badCode := confirmedProductVerdict("1")
	badCode.ReasonCodes = []string{"Policy Deny"}
	badSeal := confirmedProductVerdict("1")
	badSeal.Seal = "maybe"
	noID := confirmedProductVerdict("")
	noTime := confirmedProductVerdict("1")
	noTime.ObservedAt = time.Time{}
	cases := map[string]ProductVerdicts{
		"실패 행 값 남음":    {Provided: true, Total: 1, Items: []ProductVerdictEntry{leaked}},
		"알 수 없는 판정 값":  {Provided: true, Total: 1, Items: []ProductVerdictEntry{badValue}},
		"사유 코드 모양":     {Provided: true, Total: 1, Items: []ProductVerdictEntry{badCode}},
		"봉인 값":         {Provided: true, Total: 1, Items: []ProductVerdictEntry{badSeal}},
		"확인 행 번호 없음":   {Provided: true, Total: 1, Items: []ProductVerdictEntry{noID}},
		"관찰 시각 없음":     {Provided: true, Total: 1, Items: []ProductVerdictEntry{noTime}},
		"중복 번호":        {Provided: true, Total: 2, Items: []ProductVerdictEntry{confirmedProductVerdict("1"), confirmedProductVerdict("1")}},
		"전체 수 < 행 수":   {Provided: true, Total: 0, Items: []ProductVerdictEntry{confirmedProductVerdict("1")}},
		"전체 수 상한 초과":   {Provided: true, Total: MaxDiagnosticProductVerdicts + 1},
		"읽지 못함인데 행 있음": {Provided: true, Unavailable: true, Total: 1, Items: []ProductVerdictEntry{confirmedProductVerdict("1")}},
		"중계 밖인데 행 있음":  {Total: 1, Items: []ProductVerdictEntry{confirmedProductVerdict("1")}},
	}
	for name, p := range cases {
		in := diagnosticFixture()
		in.Product = p
		if _, err := PrepareDiagnostic(in); !errors.Is(err, ErrExportBlocked) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
