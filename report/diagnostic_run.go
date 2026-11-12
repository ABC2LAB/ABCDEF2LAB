package report

import (
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/diagnosis"
)

// 실행 종료 상태(저장된 실행 종료 기록). ClosureFailed는 실행 마감(drain·감사)을 확인하지 못한 경우다.
const (
	DiagnosticRunCompleted     = "completed"
	DiagnosticRunCancelled     = "cancelled"
	DiagnosticRunFailed        = "failed"
	DiagnosticRunClosureFailed = "closure_failed"
)

// DiagnosticRun은 저장된 실행 종료 기록의 상태와 실행 결과다(실행 결과 상태 값 계약 §5-5). 저장 보고서만 보는
// 사람이 일부 완료·검사 안 함을 "발견 없음"으로 읽지 않게 보고서 요약에 싣는다. 실행이 끝났다는 것은 취약점이
// 없다는 뜻이 아니다.
type DiagnosticRun struct {
	State  string
	Result *diagnosis.RunResult
}

// DiagnosticRunText는 실행 결과 한 줄이다. 화면(web urlOutcomeText)과 같은 문구를 쓴다. 기록이 없거나 형식이 맞지
// 않으면 결과를 확인하지 못했다고 쓰고, 완료·발견 없음으로 바꾸지 않는다.
func DiagnosticRunText(run *DiagnosticRun) string {
	if run == nil {
		return "저장된 실행 종료 기록 없음(실행 결과를 확인하지 못함)"
	}
	r := run.Result
	switch run.State {
	case DiagnosticRunCancelled:
		if r == nil || !r.StoppedValid() {
			return "중지됨"
		}
		c := r.IntentOutcomes
		return fmt.Sprintf("중지됨 · 끝난 %d / 끊긴 %d", c.Done+c.PolicyLimited, c.Incomplete+c.Stopped+c.Unknown)
	case DiagnosticRunFailed:
		return "실패(정상 마감되지 않음)"
	case DiagnosticRunClosureFailed:
		return "실행 마감 확인 실패(실행 결과를 확인하지 못함)"
	case DiagnosticRunCompleted:
		if r == nil {
			return "완료(작업별 실행 결과 기록 없음)"
		}
		if !r.Valid() {
			break
		}
		c := r.IntentOutcomes
		remaining := c.Incomplete + c.Stopped
		var parts []string
		switch r.Outcome {
		case diagnosis.OutcomeNoIntents:
			parts = append(parts, "시도한 계획 없음(검사 안 함)")
		case diagnosis.OutcomePartial:
			if remaining > 0 {
				parts = append(parts, fmt.Sprintf("일부 완료 · 남은 작업 %d", remaining))
			} else {
				parts = append(parts, "일부 완료")
			}
		default:
			parts = append(parts, "계획한 작업 모두 마감")
		}
		// 결과 불명은 남은 작업과 따로 보인다(미실행·판단불가를 섞지 않는다).
		if c.Unknown > 0 {
			parts = append(parts, fmt.Sprintf("결과 불명 %d", c.Unknown))
		}
		if c.PolicyLimited > 0 {
			parts = append(parts, fmt.Sprintf("정책상 검증 안 됨 %d", c.PolicyLimited))
		}
		if r.ClosingReason == diagnosis.ClosingBudgetLimited {
			parts = append(parts, "예산 상한에서 마감 · 찾은 후보는 보존")
		}
		return strings.Join(parts, " · ")
	}
	return "실행 결과를 확인할 수 없음"
}
