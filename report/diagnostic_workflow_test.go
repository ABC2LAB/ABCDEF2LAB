package report

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/diagnosisflow"
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

func TestWorkflowReportEndpointLimits(t *testing.T) {
	for _, count := range []int{58, diagnosisflow.MaxWorkflowEndpoints} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			in := diagnosticFixture()
			in.Workflow = &diagnosisflow.Snapshot{Format: diagnosisflow.Version, TaskID: "7", Mode: diagnosisflow.Manual, Stage: "completed"}
			for i := 1; i <= count; i++ {
				in.Workflow.Endpoints = append(in.Workflow.Endpoints, diagnosisflow.Endpoint{
					Method: "GET", Path: fmt.Sprintf("/stored-endpoint-%03d", i),
				})
			}

			for _, shared := range []bool{false, true} {
				in.Shared = shared
				html, err := DiagnosticHTML(in)
				if err != nil {
					t.Fatalf("HTML (shared=%t): %v", shared, err)
				}
				docx, err := DiagnosticDOCX(in)
				if err != nil {
					t.Fatalf("DOCX (shared=%t): %v", shared, err)
				}
				word := readWord(t, docx)["word/document.xml"]
				row := "/stored-endpoint-"
				if shared {
					row = "[공유용 경로 비공개]"
					if strings.Contains(string(html), "/stored-endpoint-") || strings.Contains(string(word), "/stored-endpoint-") {
						t.Fatal("shared export exposed stored paths")
					}
				}
				if got := strings.Count(string(html), row); got != count {
					t.Errorf("HTML (shared=%t) shows %d endpoint rows, want %d", shared, got, count)
				}
				if got := strings.Count(string(word), row); got != count {
					t.Errorf("DOCX (shared=%t) shows %d endpoint rows, want %d", shared, got, count)
				}
			}
		})
	}

	in := diagnosticFixture()
	in.Workflow = &diagnosisflow.Snapshot{
		Format: diagnosisflow.Version, TaskID: "7",
		Endpoints: make([]diagnosisflow.Endpoint, diagnosisflow.MaxWorkflowEndpoints+1),
	}
	if _, err := PrepareDiagnostic(in); !errors.Is(err, ErrExportBlocked) {
		t.Fatalf("above installation limit: %v", err)
	}
}

func TestWorkflowReportShowsTruncatedApprovalHistory(t *testing.T) {
	in := diagnosticFixture()
	for i := 1; i <= MaxWorkflowApprovals; i++ {
		in.WorkflowApprovals = append(in.WorkflowApprovals, WorkflowApproval{
			Method: "POST", Path: fmt.Sprintf("/historical-approval-%03d", i), State: "denied", ExecutionState: "not_sent",
		})
	}
	const summary = "승인 이력 101건 중 앞 100건을 표시합니다."
	for _, total := range []int64{MaxWorkflowApprovals, MaxWorkflowApprovals + 1} {
		for _, shared := range []bool{false, true} {
			in.Shared, in.WorkflowApprovalTotal = shared, total
			html, err := DiagnosticHTML(in)
			if err != nil {
				t.Fatal(err)
			}
			docx, err := DiagnosticDOCX(in)
			if err != nil {
				t.Fatal(err)
			}
			for format, output := range map[string][]byte{"HTML": html, "DOCX": readWord(t, docx)["word/document.xml"]} {
				mark := "/historical-approval-"
				if shared {
					mark = "[공유용 경로 비공개]"
					if strings.Contains(string(output), "/historical-approval-") {
						t.Fatalf("%s shared report exposed approval paths", format)
					}
				}
				if strings.Count(string(output), mark) != MaxWorkflowApprovals || strings.Contains(string(output), summary) != (total > MaxWorkflowApprovals) {
					t.Fatalf("%s shared=%t total=%d: wrong approval rows or truncation summary", format, shared, total)
				}
			}
		}
	}
}
