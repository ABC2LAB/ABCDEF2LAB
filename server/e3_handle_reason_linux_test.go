package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// notLocalE2HTTPRun은 이 프로세스에 Execution이 없는 실행(원격)을 흉내 낸다.
type notLocalE2HTTPRun struct{ E2HTTPLeaseRunner }

// E3Handle이 확정 단계에 E② 연결을 주지 못하는 조건마다 고정 사유 코드가 나온다(#805 뒤 실제 제품 합성 회차의 not_wired를
// 가리려고). 연결은 열린 작업·유효한 로컬 실행·C14 저장소·살아 있는 실행 문맥이 다 있을 때만 준다.
func TestE3HandleReasonCodesOwnedPostgres(t *testing.T) {
	_, task, intent := firstDiagnosisActualTask(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var nilRuntime *E2HTTPRuntime
	if _, _, reason := nilRuntime.E3HandleReason(task.ID); reason != E3HandleRuntimeUnavailable {
		t.Fatal("없는 runtime", reason)
	}
	runtime, _ := e2HTTPRuntimeFixture(t, func(o *E2HTTPRuntimeOptions) { o.Expected = firstDiagnosisExpectedE2() })
	if _, _, reason := runtime.E3HandleReason(task.ID); reason != E3HandleTaskMissing {
		t.Fatal("작업 받기 전", reason)
	}
	if err := runtime.AdmitTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, _, reason := runtime.E3HandleReason(task.ID + "0"); reason != E3HandleTaskMismatch {
		t.Fatal("다른 작업 번호", reason)
	}
	if _, _, reason := runtime.E3HandleReason(task.ID); reason != E3HandleLeaseMissing {
		t.Fatal("실행 잡기 전", reason)
	}
	helper := filepath.Join(firstDiagnosisPrivateTemp(t), "never-run-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runner := firstDiagnosisAcquiredRunner(t, ctx, E2HTTPRequest{TaskID: task.ID, TaskIDNumber: mustFirstDiagnosisTaskNumber(task.ID), ExplorationID: task.ExpID, IntentID: intent.ID}, helper, "https://owned.test", firstDiagnosisExpectedE2().RunRef)
	store := newE2HTTPSessionStore(nil, firstDiagnosisExpectedE2().RunRef, nil)
	local := newLocalE2HTTPRun(runner)
	set := func(lease *E2HTTPRunOptions, valid bool) {
		runtime.task.mu.Lock()
		runtime.task.lease, runtime.task.validLease = lease, valid
		runtime.task.mu.Unlock()
	}
	for name, tc := range map[string]struct {
		lease *E2HTTPRunOptions
		valid bool
		want  string
	}{
		"lease_invalid":    {&E2HTTPRunOptions{Runner: local, sessionStore: store}, false, E3HandleLeaseInvalid},
		"runner_missing":   {&E2HTTPRunOptions{sessionStore: store}, true, E3HandleRunnerMissing},
		"runner_not_local": {&E2HTTPRunOptions{Runner: notLocalE2HTTPRun{local}, sessionStore: store}, true, E3HandleRunnerNotLocal},
		"accounts_missing": {&E2HTTPRunOptions{Runner: local}, true, E3HandleAccountsMissing},
		"ok":               {&E2HTTPRunOptions{Runner: local, sessionStore: store}, true, ""},
	} {
		set(tc.lease, tc.valid)
		exec, accounts, reason := runtime.E3HandleReason(task.ID)
		if reason != tc.want || (reason == "") != (exec != nil && accounts != nil) {
			t.Fatalf("%s: 사유 %q 실행 %v", name, reason, exec != nil)
		}
		if _, _, ok := runtime.E3Handle(task.ID); ok != (tc.want == "") {
			t.Fatalf("%s: E3Handle %v", name, ok)
		}
	}
	set(&E2HTTPRunOptions{Runner: local, sessionStore: store}, true)
	host := &FirstDiagnosisHTTPRuntime{host: &FirstDiagnosisHTTPHost{runtime: runtime}}
	if _, _, reason := host.E3HandleReason(task.ID); reason != "" {
		t.Fatal("제품 runtime 정상", reason)
	}
	host.closing.Store(true)
	if _, _, reason := host.E3HandleReason(task.ID); reason != E3HandleRuntimeClosing {
		t.Fatal("마감 중", reason)
	}
	host.closing.Store(false)
	host.closed.Store(true)
	if _, _, reason := host.E3HandleReason(task.ID); reason != E3HandleRuntimeClosed {
		t.Fatal("마감 뒤", reason)
	}
	var nilHost *FirstDiagnosisHTTPRuntime
	if _, _, ok := nilHost.E3Handle(task.ID); ok {
		t.Fatal("없는 제품 runtime")
	}
	// 실행 문맥이 끝났으면(작업 문맥 취소 등) 핸들이 있어도 송신할 수 없어 주지 않는다.
	if err := runner.Execution().Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, reason := runtime.E3HandleReason(task.ID); reason != E3HandleExecutionStopped {
		t.Fatal("멈춘 실행", reason)
	}
	if err := runtime.FenceTask(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, reason := runtime.E3HandleReason(task.ID); reason != E3HandleTaskFenced {
		t.Fatal("닫힌 작업", reason)
	}
}

// 모델 단계 마감(사용자 결정 2026-11-20): 런타임의 CloseModelStage는 지금 실행의 모델 단계를 마감하되 E② 실행 연결은
// 열어 둔다(확정 단계가 쓴다). 다른 작업이면 아무 일도 하지 않는다. 실행을 잡기 전에 마감하면 표시만 남기고, 실행 전체를
// 닫는 것은 감독자의 FenceTask다.
func TestE2HTTPRuntimeCloseModelStageOwnedPostgres(t *testing.T) {
	_, task, intent := firstDiagnosisActualTask(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	runtime, _ := e2HTTPRuntimeFixture(t, func(o *E2HTTPRuntimeOptions) { o.Expected = firstDiagnosisExpectedE2() })
	var nilRuntime *E2HTTPRuntime
	nilRuntime.CloseModelStage(task.ID) // nil이면 아무 일도 하지 않는다.
	if err := runtime.AdmitTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	// 실행을 잡기 전 마감은 표시만 남긴다.
	runtime.CloseModelStage(task.ID)
	runtime.task.mu.Lock()
	early := runtime.task.modelClosed
	runtime.task.modelClosed = false
	runtime.task.mu.Unlock()
	if !early {
		t.Fatal("실행 전 모델 단계 마감 표시가 없음")
	}
	helper := filepath.Join(firstDiagnosisPrivateTemp(t), "never-run-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runner := firstDiagnosisAcquiredRunner(t, ctx, E2HTTPRequest{TaskID: task.ID, TaskIDNumber: mustFirstDiagnosisTaskNumber(task.ID), ExplorationID: task.ExpID, IntentID: intent.ID}, helper, "https://owned.test", firstDiagnosisExpectedE2().RunRef)
	store := newE2HTTPSessionStore(nil, firstDiagnosisExpectedE2().RunRef, nil)
	runtime.task.mu.Lock()
	runtime.task.lease, runtime.task.validLease = &E2HTTPRunOptions{Runner: newLocalE2HTTPRun(runner), sessionStore: store}, true
	runtime.task.mu.Unlock()
	runtime.CloseModelStage(task.ID + "0")
	if runner.Execution().ModelStageClosed() {
		t.Fatal("다른 작업 번호로 모델 단계가 마감됨")
	}
	runtime.CloseModelStage(task.ID)
	if !runner.Execution().ModelStageClosed() {
		t.Fatal("모델 단계가 마감되지 않음")
	}
	if _, err := runner.Execution().BeginCall(ctx, "late-worker"); err == nil {
		t.Fatal("모델 단계 마감 뒤 Worker 호출이 열림")
	}
	// 확정 단계는 E② 연결을 그대로 받는다.
	if exec, _, reason := runtime.E3HandleReason(task.ID); reason != "" || exec == nil {
		t.Fatal("모델 단계 마감이 E② 실행 연결까지 닫음", reason)
	}
}
