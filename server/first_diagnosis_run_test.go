package server

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestDiagnosisRunExactReadScope(t *testing.T) {
	for _, path := range []string{"/api/tasks/7/diagnosis-run", "/api/tasks/07/diagnosis-run", "/api/tasks/7/diagnosis-run?task=7", "/api/tasks/7/diagnosis-run/", "/api/tasks/-1/diagnosis-run"} {
		r := httptest.NewRequest("GET", path, nil)
		_, ok := diagnosisRunRequestTask(r)
		if ok != (path == "/api/tasks/7/diagnosis-run") {
			t.Fatal(path, ok)
		}
	}
	r := httptest.NewRequest("POST", "/api/tasks/7/diagnosis-run", nil)
	if _, ok := diagnosisRunRequestTask(r); ok {
		t.Fatal("write route opened")
	}
}
func TestDiagnosisRunFinalStateOnly(t *testing.T) {
	for _, state := range []requestApprovalRunState{
		{Phase: "running", RunRef: "run"},
		{Phase: "stopping", RunRef: "run", Outcome: "cancelled"},
		{Phase: "closed", RunRef: "run", Outcome: "completed"},
		{Phase: "closed", RunRef: "run", Outcome: "cancelled", DrainComplete: true, ClosureFailed: true},
		{Phase: "failed", RunRef: "run", Outcome: "completed"},
	} {
		if validDiagnosisRun(state) {
			t.Fatal("unproven final state", state)
		}
		if saveDiagnosisRun(context.Background(), nil, 7, state) == nil {
			t.Fatal("nil storage accepted")
		}
	}
	for _, state := range []requestApprovalRunState{
		{Phase: "closed", RunRef: "run", Outcome: "completed", DrainComplete: true},
		{Phase: "closed", RunRef: "run", Outcome: "cancelled", DrainComplete: true, StopRequested: true},
		{Phase: "failed", RunRef: "run", Outcome: "cancelled", ClosureFailed: true, StopRequested: true},
		{Phase: "failed", RunRef: "run", Outcome: "failed"},
	} {
		if !validDiagnosisRun(state) {
			t.Fatal("valid final state rejected", state)
		}
	}
}
