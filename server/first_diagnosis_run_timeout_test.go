package server

import (
	"testing"
	"time"
)

// 감독자 Run의 뒷받침 기한은 가장 긴 회차(실행 시간 상한 + 정리 시간 상한)보다 먼저 끊지 않는다. 30분 고정일 때는 실행 시간
// 약 1950초를 넘는 회차가 시간 상한 마감 전에 실패로 끊겼다(#581 윤재상 리뷰).
func TestFirstDiagnosisRunContextOutlastsTheLongestRun(t *testing.T) {
	longest := time.Duration(MaxFirstDiagnosisTaskSeconds+MaxFirstDiagnosisShutdownSeconds) * time.Second
	if FirstDiagnosisRunTimeout < longest {
		t.Fatalf("run timeout %s shorter than the longest run %s", FirstDiagnosisRunTimeout, longest)
	}
	before := time.Now()
	run, cancel := firstDiagnosisRunContext(t.Context())
	defer cancel()
	deadline, ok := run.Deadline()
	if !ok || deadline.Sub(before) < longest {
		t.Fatalf("run deadline %s after start, longest run %s", deadline.Sub(before), longest)
	}
}
