package server

import (
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"testing"
)

func TestFirstDiagnosisRuntimeResultsPreserveNestedEvidence(t *testing.T) {
	original := e2runner.TrustedResult{Call: e2proxy.CallResult{CallRef: "owned", Observations: []e2proxy.HTTPObservation{{Endpoint: "/orders/{resource}", Technologies: []e2proxy.HTTPTechnology{{Name: "Example", Version: "1"}}}}}}
	stored := e2HTTPCloneTrusted(original)
	original.Call.Observations[0].Technologies[0].Name = "caller mutation"
	runtime := &E2HTTPRuntime{task: &e2HTTPRuntimeTask{id: "owned-task", results: []e2runner.TrustedResult{stored}}}
	first := runtime.Results("owned-task")
	if len(first) != 1 || first[0].Call.Observations[0].Technologies[0].Name != "Example" {
		t.Fatal("stored evidence aliases caller")
	}
	first[0].Call.Observations[0].Endpoint = "/changed"
	first[0].Call.Observations[0].Technologies[0].Name = "result mutation"
	again := runtime.Results("owned-task")
	if len(again) != 1 || again[0].Call.Observations[0].Endpoint != "/orders/{resource}" || again[0].Call.Observations[0].Technologies[0].Name != "Example" {
		t.Fatal("returned evidence aliases stored result")
	}
	if runtime.Results("different-task") != nil {
		t.Fatal("foreign result exposed")
	}
}
