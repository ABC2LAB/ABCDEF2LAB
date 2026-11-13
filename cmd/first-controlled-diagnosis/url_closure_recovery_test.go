package main

import (
	"github.com/Autumn-27/artex/diagnosisprep"
	"testing"
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
