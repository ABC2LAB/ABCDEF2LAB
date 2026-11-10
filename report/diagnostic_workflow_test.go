package report

import (
	"github.com/Autumn-27/artex/diagnosisflow"
	"strings"
	"testing"
)

func TestWorkflowReportTaskBindingEscapingAndSharedProjection(t *testing.T) {
	in := diagnosticFixture()
	in.Workflow = &diagnosisflow.Snapshot{Format: diagnosisflow.Version, TaskID: "7", Mode: diagnosisflow.Manual, Stage: "completed", Endpoints: []diagnosisflow.Endpoint{{Path: "/owned/<script>alert(1)</script>", Method: "GET", Visited: true, Selected: true, Status: 200}}}
	in.WorkflowApprovals = []WorkflowApproval{{Method: "POST", Path: "/private-write", State: "denied", ExecutionState: "not_sent", Attempt: "owned-attempt"}}
	internal, err := DiagnosticHTML(in)
	if err != nil || !strings.Contains(string(internal), "denied") || strings.Contains(string(internal), "<script>alert") || !strings.Contains(string(internal), "&lt;script&gt;") {
		t.Fatal("workflow evidence missing or unescaped", err)
	}
	in.Shared = true
	shared, err := DiagnosticHTML(in)
	if err != nil || strings.Contains(string(shared), "/private-write") || strings.Contains(string(shared), "/owned/") {
		t.Fatal("shared workflow exposed endpoint", err)
	}
	if in.Workflow.Endpoints[0].Path != "/owned/<script>alert(1)</script>" {
		t.Fatal("report mutated stored source")
	}
	in.Workflow.TaskID = "8"
	if _, err = DiagnosticHTML(in); err == nil {
		t.Fatal("cross-task workflow attached")
	}
}
