package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/report"
	"github.com/Autumn-27/artex/reproduction"
)

func TestDiagnosticBusinessRuleReviewUsesPrimarySteps(t *testing.T) {
	first := e2proxy.HTTPObservation{Sequence: 1, Method: "GET", StatusCode: 403, Decision: "allowed", AuditComplete: true, ResponseComplete: true, ForwardAttempted: true}
	second := first
	second.Sequence, second.Method, second.StatusCode = 2, "POST", 201
	related := second
	related.Sequence, related.Method, related.StatusCode = 1, "HEAD", 200
	envelope := func(ref string, observations []e2proxy.HTTPObservation) map[string]any {
		return map[string]any{"trusted": map[string]any{"call": e2proxy.CallResult{CallRef: ref, Observations: observations}}}
	}
	entry := func(primary ...e2proxy.HTTPObservation) *report.DiagnosticEntry {
		evidence := envelope("primary", primary)
		evidence["related_calls"] = []any{envelope("related", []e2proxy.HTTPObservation{related})}
		raw, err := json.Marshal(evidence)
		if err != nil {
			t.Fatal(err)
		}
		return &report.DiagnosticEntry{VulnClass: "IDOR", Evidence: string(raw), Steps: []reproduction.Step{
			{Number: 1, CallRef: "related", SourceStepID: "attempt-1"},
			{Number: 2, CallRef: "primary", SourceStepID: "attempt-1"},
			{Number: 3, CallRef: "primary", SourceStepID: "attempt-2"},
		}}
	}
	for _, tc := range []struct{ class, questionPart string }{
		{"BOLA/IDOR", "소유권"}, {"IDOR / Insecure Direct Object Reference", "소유권"},
		{"CWE-639", "소유권"}, {"A01:2021-Broken Access Control", "권한"},
		{"BFLA", "권한"}, {"Missing Authorization", "권한"}, {"Authorization Bypass", "권한"},
		{"Business Logic - Negative Quantity", "업무 상태"}, {"business_logic", "업무 상태"},
		{"CWE-841", "업무 상태"}, {"CWE-6390", ""}, {"BusinessxLogic", ""}, {"Broken Authentication", ""}, {"XSS", ""},
	} {
		t.Run(tc.class, func(t *testing.T) {
			candidate := entry(first, second)
			candidate.VulnClass = tc.class
			got := diagnosticBusinessRuleReview(candidate)
			if tc.questionPart == "" {
				if got != nil {
					t.Fatal("다른 유형에 질문을 연결함", got)
				}
				return
			}
			if got == nil || !strings.Contains(got.Question, tc.questionPart) ||
				got.Observation != "재현 단계 2 (GET, HTTP 403), 재현 단계 3 (POST, HTTP 201)" {
				t.Fatal("주 호출의 단계나 질문 불일치", got)
			}
		})
	}
	if got := diagnosticBusinessRuleReview(entry(first)); got != nil {
		t.Fatal("관련 호출의 성공을 주 호출의 관찰로 사용함", got)
	}
	unbound := entry(first, second)
	unbound.Steps = unbound.Steps[:2]
	if got := diagnosticBusinessRuleReview(unbound); got != nil {
		t.Fatal("보고서 단계에 연결되지 않은 성공을 사용함", got)
	}
}
