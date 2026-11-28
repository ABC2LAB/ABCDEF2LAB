package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/responseevidence"
)

// 응답 구조 근거는 선택 후보 저장 관찰에만 간다. 원본 진단 워커 모델 입력과 후보 근거 원문은 바뀌지 않아야 한다.
func TestResponseEvidenceStaysOutOfWorkerModelAndCandidateEvidence(t *testing.T) {
	execution, _, binding := e2WorkerFixture(t)
	obs := e2proxy.HTTPObservation{Sequence: 1, AttemptRef: "evidence-attempt", Method: "GET", Endpoint: "/documents", Decision: "allowed",
		StatusCode: 200, ResponseComplete: true, ForwardAttempted: true, AuditComplete: true, ObservedAt: time.Now().UTC()}
	evidence := responseevidence.Extract(responseevidence.Response{Received: true, Complete: true, StatusCode: 200, Method: "GET",
		ContentType: "application/json", Body: []byte(`{"marker_field_name":1}`)}, responseevidence.Limits{}, nil)
	result := e2runner.Result{ToolOutput: "synthetic", Trusted: e2runner.TrustedResult{Execution: execution.SnapshotForIntent(binding.IntentID),
		Final: true, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, Classification: "completed",
		Call: e2proxy.CallResult{IntentID: binding.IntentID, CallRef: "evidence-call", Final: true, AuditComplete: true,
			Observations:     []e2proxy.HTTPObservation{obs},
			ResponseEvidence: []e2proxy.ResponseEvidence{{AttemptRef: obs.AttemptRef, Sequence: obs.Sequence, Evidence: evidence}}}}}
	encoded := encodeE2HTTPResult(result, false).Flatten()
	if strings.Contains(encoded, "response_evidence") || strings.Contains(encoded, "marker_field_name") {
		t.Fatalf("worker model result carries response evidence: %s", encoded)
	}
	if len(result.Trusted.Call.ResponseEvidence) != 1 {
		t.Fatal("encoding must not modify the trusted result kept for finalization")
	}
	state := &e2HTTPToolState{}
	state.observeDiagnostic(execution, binding.IntentID, "synthetic fixture", result)
	saved := state.observations["evidence-call"].Evidence
	if saved == "" || strings.Contains(saved, "response_evidence") || strings.Contains(saved, "marker_field_name") {
		t.Fatalf("candidate evidence carries response evidence: %q", saved)
	}
}
