package main

import (
	"context"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

func TestURLClosureRecoveryChecksResourcesAndExactJob(t *testing.T) {
	runtime := &savedRunRuntime{}
	runner := &urlGatewayRunner{taskID: "42", launch: &launchedDiagnosis{runtime: runtime}}
	backend := &urlPreparationRuntime{recovery: map[string]*urlGatewayRunner{"job-one": runner}}
	execution := diagnosisprep.Execution{Job: diagnosisprep.Job{ID: "job-one", TaskID: "42"}}
	if backend.VerifyClosure(t.Context(), execution) == nil {
		t.Fatal("미마감 자원을 복구로 판단")
	}
	runtime.closed = true
	wrong := execution
	wrong.Job.TaskID = "43"
	if backend.VerifyClosure(t.Context(), wrong) == nil {
		t.Fatal("다른 실행 핸들로 복구")
	}
	if backend.VerifyClosure(t.Context(), execution) != nil {
		t.Fatal("자원 종료 후 복구 실패")
	}
	if !runtime.called || backend.recovery["job-one"] != nil {
		t.Fatal("복구 정리 미완료")
	}
	if backend.VerifyClosure(t.Context(), execution) == nil {
		t.Fatal("핸들 부재를 종료 증거로 추정")
	}
}

func TestURLClosureRecoveryWaitsForOriginalRunAndLateCloseIsSafe(t *testing.T) {
	runtime := &savedRunRuntime{closed: true}
	done := make(chan struct{})
	runner := &urlGatewayRunner{taskID: "42", runStarted: true, runDone: done, launch: &launchedDiagnosis{runtime: runtime}}
	backend := &urlPreparationRuntime{recovery: map[string]*urlGatewayRunner{"job-one": runner}}
	execution := diagnosisprep.Execution{Job: diagnosisprep.Job{ID: "job-one", TaskID: "42"}}
	short, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if backend.VerifyClosure(short, execution) == nil {
		t.Fatal("원래 Run이 살아 있는데 복구 완료")
	}
	if backend.recovery["job-one"] == nil {
		t.Fatal("살아 있는 Run 핸들 제거")
	}
	close(done)
	if backend.VerifyClosure(t.Context(), execution) != nil {
		t.Fatal("Run 종료 후 복구 실패")
	}
	closure, err := runner.Close(t.Context())
	if err != nil || !closure.DrainComplete || closure.ResultReady {
		t.Fatal("늦은 Close가 복구 사실을 깨뜨리거나 결과를 성공으로 만듦")
	}
	if runner.Run(t.Context()) == nil {
		t.Fatal("복구 뒤 늦은 Run을 다시 시작")
	}
}

func TestURLPreparationRecoveryChecksExactTaskAndDrain(t *testing.T) {
	drained := false
	calls := 0
	preparation := &urlPreparationRecovery{taskID: "42", closeResources: func(context.Context) error {
		calls++
		if !drained {
			return errPreparation
		}
		return nil
	}}
	backend := &urlPreparationRuntime{preparationRecovery: map[string]*urlPreparationRecovery{"prep-job": preparation}}
	execution := diagnosisprep.Execution{Job: diagnosisprep.Job{ID: "prep-job", TaskID: "42"}}
	wrong := execution
	wrong.Job.TaskID = "43"
	if backend.VerifyClosure(t.Context(), wrong) == nil || calls != 0 {
		t.Fatal("다른 준비 작업 복구")
	}
	if backend.VerifyClosure(t.Context(), execution) == nil {
		t.Fatal("준비 자원 미마감을 복구 완료로 판단")
	}
	drained = true
	if backend.VerifyClosure(t.Context(), execution) != nil || backend.preparationRecovery["prep-job"] != nil {
		t.Fatal("준비 자원 복구 실패")
	}
}
