package report

import (
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/diagnosis"
)

// 실행 종료 상태(저장된 실행 종료 기록). ClosureFailed는 실행 마감(drain·감사)을 확인하지 못한 경우다.
const (
	DiagnosticRunCompleted     = "completed"
	DiagnosticRunCancelled     = "cancelled"
	DiagnosticRunFailed        = "failed"
	DiagnosticRunClosureFailed = "closure_failed"
	DiagnosticRunRunning       = "running"
	// DiagnosticRunInterrupted는 종료 기록이 없고, 준비 서비스가 실행 도중 끝나 다음 점검이 그 작업을 마감 상태로
	// 돌리며 고정 사유 표지를 남긴 경우다(#927). 결과는 확인하지 못했다.
	DiagnosticRunInterrupted = "interrupted"
)

// DiagnosticRun은 저장된 실행 종료 기록의 상태와 실행 결과다(실행 결과 상태 값 계약 §5-5). 저장 보고서만 보는
// 사람이 일부 완료·검사 안 함을 "발견 없음"으로 읽지 않게 보고서 요약에 싣는다. 실행이 끝났다는 것은 취약점이
// 없다는 뜻이 아니다.
type DiagnosticRun struct {
	State  string
	RunRef string
	Result *diagnosis.RunResult
	// VerificationVersion은 수동적 독립 관찰 검증 이전의 저장 보고서에는 없다.
	VerificationVersion           string
	VerificationSourceUnavailable bool
	CandidateAssessments          []CandidateAssessment
}

type CandidateAssessment struct {
	CandidateID    int64
	EvidenceDigest string
	Status         string
	Reason         string
	AttemptRefs    []string
	Sources        []candidateverify.SourceV2
}

// DiagnosticRunText는 실행 결과 한 줄이다. 화면(web urlOutcomeText)과 같은 문구를 쓴다. 기록이 없거나 형식이 맞지
// 않으면 결과를 확인하지 못했다고 쓰고, 완료·발견 없음으로 바꾸지 않는다.
func DiagnosticRunText(run *DiagnosticRun) string {
	if run == nil {
		return "저장된 실행 종료 기록 없음(실행 결과를 확인하지 못함)"
	}
	r := run.Result
	switch run.State {
	case DiagnosticRunRunning:
		return "진행 중(후보 검증 및 실행 종료 기록 없음)"
	case DiagnosticRunCancelled:
		if r == nil || !r.StoppedValid() {
			return "중지됨"
		}
		c := r.IntentOutcomes
		// 목표 달성으로 멈춘 작업은 검사를 끝낸 것도 끊긴 것도 아니라 따로 보인다(#875 발견 6, #894 경준 리뷰).
		if c.GoalMet > 0 {
			return fmt.Sprintf("중지됨 · 끝난 %d · 목표 달성(계획자 판단)으로 중단 %d / 끊긴 %d", c.Done+c.PolicyLimited, c.GoalMet, c.Incomplete+c.Stopped+c.Unknown)
		}
		return fmt.Sprintf("중지됨 · 끝난 %d / 끊긴 %d", c.Done+c.PolicyLimited, c.Incomplete+c.Stopped+c.Unknown)
	case DiagnosticRunFailed:
		return "실패(정상 마감되지 않음)"
	case DiagnosticRunClosureFailed:
		return "실행 마감 확인 실패(실행 결과를 확인하지 못함)"
	case DiagnosticRunInterrupted:
		return "준비 서비스 종료로 중단됨(실행 결과를 확인하지 못함)"
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
		// 목표 달성으로 작업이 끝나 멈춘 작업은 남은 작업이 아니라 따로 보인다(#875 발견 6). 목표 달성은 계획자(모델) 판단이지
		// 프로그램이 검증한 사실이 아니라서 문구에 그렇게 적는다(#894 경준 리뷰, 절대 규칙 4).
		if c.GoalMet > 0 {
			parts = append(parts, fmt.Sprintf("목표 달성(계획자 판단)으로 중단 %d", c.GoalMet))
		}
		if r.ClosingReason == diagnosis.ClosingBudgetLimited {
			parts = append(parts, "예산 상한에서 마감 · 찾은 후보는 보존")
		}
		return strings.Join(parts, " · ")
	}
	return "실행 결과를 확인할 수 없음"
}
