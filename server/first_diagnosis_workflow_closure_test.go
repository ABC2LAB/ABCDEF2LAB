package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosis"
)

// 흐름 모드 마감: 계획이 오류 없이 끝났는데 의도가 하나도 없던 실행은 마감할 수 있고(no_intents 정상 마감으로 감),
// 계획 호출이 실패해 의도가 없는 실행은 지금처럼 마감하지 못해 실패로 끝난다(모델 고장을 숨기지 않음).
func TestFirstDiagnosisWorkflowNotReady(t *testing.T) {
	for name, tc := range map[string]struct {
		deepReady, noDeepRuns bool
		settled               int64
		lastPlanSucceeded     bool
		notReady              bool
	}{
		"진단 Worker가 모두 끝남":          {deepReady: true, settled: 2, lastPlanSucceeded: true},
		"계획 정상, 의도 0":               {noDeepRuns: true, lastPlanSucceeded: true},
		"계획 호출 실패, 의도 0":            {noDeepRuns: true, notReady: true},
		"계획 기록 없음, 의도 0":            {noDeepRuns: true, notReady: true},
		"의도가 있었는데 진단 Worker가 없음":    {noDeepRuns: true, settled: 1, lastPlanSucceeded: true, notReady: true},
		"진단 Worker가 아직 돌거나 흐름이 깨짐":  {lastPlanSucceeded: true, notReady: true},
		"진단 Worker가 끝났으면 계획 실패와 무관": {deepReady: true, settled: 1},
	} {
		if got := firstDiagnosisWorkflowNotReady(tc.deepReady, tc.noDeepRuns, tc.settled, tc.lastPlanSucceeded); got != tc.notReady {
			t.Errorf("%s: notReady=%v, want %v", name, got, tc.notReady)
		}
	}
}

// 계획 결과는 작업별 마지막 한 번만 본다. 기록이 없거나 마지막이 실패면 성공이 아니다.
func TestE2HTTPRuntimeLastPlanSucceeded(t *testing.T) {
	var nilRuntime *E2HTTPRuntime
	nilRuntime.recordPlanResult("1", true)
	if nilRuntime.lastPlanSucceeded("1") {
		t.Fatal("nil 실행기가 성공을 돌려줌")
	}
	r := &E2HTTPRuntime{}
	if r.lastPlanSucceeded("1") {
		t.Fatal("계획 기록이 없는데 성공")
	}
	r.recordPlanResult("1", true)
	r.recordPlanResult("2", false)
	if !r.lastPlanSucceeded("1") || r.lastPlanSucceeded("2") {
		t.Fatal("작업별 결과가 섞임")
	}
	r.recordPlanResult("1", false)
	if r.lastPlanSucceeded("1") {
		t.Fatal("마지막 계획이 실패인데 성공")
	}
}

// 계획이 끝날 때 부르는 기록 함수가 계획 결과를 남긴다(진단 기록이 없는 경로에서도). 마감 판정이 이 값을 읽는다.
func TestPlannerLogFinishRecordsPlanResult(t *testing.T) {
	e := &Engine{e2HTTP: &E2HTTPRuntime{}}
	task := &Task{ID: "5"}
	e.plannerLogFinish(context.Background(), task, 0, false, errors.New("model failed"), time.Now())
	if e.e2HTTP.lastPlanSucceeded("5") {
		t.Fatal("실패한 계획을 성공으로 남김")
	}
	e.plannerLogFinish(context.Background(), task, 0, false, nil, time.Now())
	if !e.e2HTTP.lastPlanSucceeded("5") {
		t.Fatal("성공한 계획을 남기지 않음")
	}
}

// 범위 선택 전 시간 상한 마감(#964, NodeGoat 회차 2 발견 5): 계획 준비가 시간 상한으로 멈췄고 진단 Worker가 한 번도 돌지
// 않았으며 끝낸 intent가 없으면 실패가 아니라 실행 상한 마감이다. 계획이 범위 선택 전에 만든 intent는 미완료로 남는다.
func TestFirstDiagnosisSelectionWaitClosure(t *testing.T) {
	incomplete := diagnosis.IntentOutcomes{Incomplete: 1}
	for name, tc := range map[string]struct {
		stopped, deepReady, noDeepRuns bool
		outcomes                       diagnosis.IntentOutcomes
		want                           bool
	}{
		"범위 선택 전, intent 0":          {stopped: true, noDeepRuns: true, want: true},
		"범위 선택 전, 계획이 만든 intent 미완료": {stopped: true, noDeepRuns: true, outcomes: incomplete, want: true},
		"시간 상한 멈춤 아님":                {noDeepRuns: true, outcomes: incomplete},
		"진단 단계가 열림":                  {stopped: true, deepReady: true, noDeepRuns: true},
		"진단 Worker가 돌았음":             {stopped: true, outcomes: incomplete},
		"끝낸 intent가 있음":              {stopped: true, noDeepRuns: true, outcomes: diagnosis.IntentOutcomes{Done: 1}},
		"정책 제한 intent가 있음":           {stopped: true, noDeepRuns: true, outcomes: diagnosis.IntentOutcomes{PolicyLimited: 1}},
	} {
		if got := firstDiagnosisSelectionWaitClosure(tc.stopped, tc.deepReady, tc.noDeepRuns, tc.outcomes); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// 시간 상한 멈춤 표시는 작업별이고, 계획 준비가 시간 상한으로 멈출 때만 생긴다.
func TestE2HTTPRuntimePlanningStoppedByTimeBudget(t *testing.T) {
	var nilRuntime *E2HTTPRuntime
	if nilRuntime.planningStoppedByTimeBudget("1") {
		t.Fatal("nil 실행기가 표시를 돌려줌")
	}
	r := &E2HTTPRuntime{}
	task := &Task{ID: "1"}
	if r.planningStoppedByTimeBudget("1") {
		t.Fatal("멈추지 않았는데 표시가 있음")
	}
	if err := r.planningTimeBudget(context.Background(), task); !errors.Is(err, ErrE2HTTPBudgetLimited) || !task.firstDiagnosisBudgetLimited.Load() {
		t.Fatal("시간 상한 계획 준비가 실행 상한이 아님", err)
	}
	if !r.planningStoppedByTimeBudget("1") || r.planningStoppedByTimeBudget("2") {
		t.Fatal("작업별 표시가 아님")
	}
}
