package report

import (
	"strings"
	"testing"

	"github.com/Autumn-27/artex/diagnosis"
)

// 실행 결과 한 줄은 화면과 같은 문구이고, 기록이 없거나 틀리면 완료·발견 없음으로 바꾸지 않는다(계약 §4·§5-5).
func TestDiagnosticRunText(t *testing.T) {
	completed := func(r diagnosis.RunResult) *DiagnosticRun {
		return &DiagnosticRun{State: DiagnosticRunCompleted, Result: &r}
	}
	for _, tc := range []struct {
		name string
		run  *DiagnosticRun
		want string
	}{
		{"none", nil, "저장된 실행 종료 기록 없음(실행 결과를 확인하지 못함)"},
		{"complete", completed(diagnosis.RunResult{Outcome: diagnosis.OutcomeComplete, IntentOutcomes: diagnosis.IntentOutcomes{Done: 2}}), "계획한 작업 모두 마감"},
		{"complete-policy", completed(diagnosis.RunResult{Outcome: diagnosis.OutcomeComplete, IntentOutcomes: diagnosis.IntentOutcomes{Done: 1, PolicyLimited: 1}}), "계획한 작업 모두 마감 · 정책상 검증 안 됨 1"},
		{"partial", completed(diagnosis.RunResult{Outcome: diagnosis.OutcomePartial, IntentOutcomes: diagnosis.IntentOutcomes{Done: 1, Incomplete: 2, Stopped: 1, Unknown: 1}}), "일부 완료 · 남은 작업 3 · 결과 불명 1"},
		{"partial-unknown-only", completed(diagnosis.RunResult{Outcome: diagnosis.OutcomePartial, IntentOutcomes: diagnosis.IntentOutcomes{Done: 1, Unknown: 1}}), "일부 완료 · 결과 불명 1"},
		{"no-intents-budget", completed(diagnosis.RunResult{Outcome: diagnosis.OutcomeNoIntents, ClosingReason: diagnosis.ClosingBudgetLimited}), "시도한 계획 없음(검사 안 함) · 예산 상한에서 마감 · 찾은 후보는 보존"},
		{"completed-old-record", &DiagnosticRun{State: DiagnosticRunCompleted}, "완료(작업별 실행 결과 기록 없음)"},
		{"completed-inconsistent", completed(diagnosis.RunResult{Outcome: diagnosis.OutcomeComplete, IntentOutcomes: diagnosis.IntentOutcomes{Incomplete: 1}}), "실행 결과를 확인할 수 없음"},
		{"cancelled", &DiagnosticRun{State: DiagnosticRunCancelled, Result: &diagnosis.RunResult{IntentOutcomes: diagnosis.IntentOutcomes{Done: 1, PolicyLimited: 1, Incomplete: 1, Unknown: 1}}}, "중지됨 · 끝난 2 / 끊긴 2"},
		{"cancelled-no-counts", &DiagnosticRun{State: DiagnosticRunCancelled}, "중지됨"},
		{"failed", &DiagnosticRun{State: DiagnosticRunFailed}, "실패(정상 마감되지 않음)"},
		{"closure-failed", &DiagnosticRun{State: DiagnosticRunClosureFailed}, "실행 마감 확인 실패(실행 결과를 확인하지 못함)"},
		{"unreadable", &DiagnosticRun{}, "실행 결과를 확인할 수 없음"},
	} {
		if got := DiagnosticRunText(tc.run); got != tc.want {
			t.Fatalf("%s: %q", tc.name, got)
		}
	}
}

// HTML·DOCX 모두 요약 장에 실행 결과 한 줄을 싣는다(공유 사본 포함).
func TestDiagnosticExportsCarryRunResult(t *testing.T) {
	in := diagnosticFixture()
	in.Run = &DiagnosticRun{State: DiagnosticRunCompleted, Result: &diagnosis.RunResult{Outcome: diagnosis.OutcomePartial, IntentOutcomes: diagnosis.IntentOutcomes{Done: 1, Incomplete: 1}}}
	want := "실행 결과: 일부 완료 · 남은 작업 1"
	for _, shared := range []bool{false, true} {
		in.Shared = shared
		html, err := DiagnosticHTML(in)
		if err != nil || !strings.Contains(string(html), want) {
			t.Fatalf("html shared=%v err=%v", shared, err)
		}
		docx, err := DiagnosticDOCX(in)
		if err != nil || !strings.Contains(string(readWord(t, docx)["word/document.xml"]), want) {
			t.Fatalf("docx shared=%v err=%v", shared, err)
		}
	}
	in.Run = nil
	html, err := DiagnosticHTML(in)
	if err != nil || !strings.Contains(string(html), "실행 결과: 저장된 실행 종료 기록 없음") {
		t.Fatal("missing run record not stated", err)
	}
}
