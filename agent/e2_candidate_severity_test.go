package agent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

func TestCandidateSeverityOptionalAndNeverConfirms(t *testing.T) {
	execution, _, binding := e2WorkerFixture(t)
	state := &e2HTTPToolState{}
	result := e2runner.Result{ToolOutput: "합성 응답", Trusted: e2runner.TrustedResult{Execution: execution.SnapshotForIntent(binding.IntentID), Final: true, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, Classification: "completed", Call: e2proxy.CallResult{IntentID: binding.IntentID, CallRef: "severity-call", Final: true, AuditComplete: true, Counts: e2proxy.Counts{Responses: 1, ForwardAttempted: 1}, Observations: []e2proxy.HTTPObservation{{Sequence: 1, AttemptRef: "severity-attempt", Method: "GET", Endpoint: "/documents", Decision: "allowed", StatusCode: 200, ResponseComplete: true, ForwardAttempted: true, AuditComplete: true, ObservedAt: time.Now().UTC()}}}}}
	state.observeDiagnostic(execution, binding.IntentID, "synthetic fixture", result)
	for _, tc := range []struct{ name, extra, want string }{
		{"omitted", "", ""},
		{"critical", `,"suggested_severity":"critical","severity_rationale":"비공개 문서 내용이 응답에 노출되어 접근통제 영향을 검토해야 합니다."`, "critical"},
		{"invalid enum", `,"suggested_severity":"urgent","severity_rationale":"비공개 문서 내용이 응답에 노출되어 접근통제 영향을 검토해야 합니다."`, ""},
		{"wrong type", `,"suggested_severity":42,"severity_rationale":"비공개 문서 내용이 응답에 노출되었습니다."`, ""},
		{"missing rationale", `,"suggested_severity":"high"`, ""},
		{"null", `,"suggested_severity":null,"severity_rationale":null`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &candidateMemoryStore{}
			tool := newE2DiagnosticCandidateTool(store, binding, state)
			r, err := tool.Call(t.Context(), json.RawMessage(`{"call_ref":"severity-call","vulnclass":"IDOR","name":"합성 후보","summary":"관찰 근거"`+tc.extra+`}`), nil)
			if err != nil || r.IsError || len(store.candidates) != 1 {
				t.Fatal("선택 평가 때문에 후보 유실", err)
			}
			c := store.candidates[0]
			if c.SuggestedSeverity != tc.want || c.Verification != "manual_review_required" {
				t.Fatal("평가가 확정 또는 잘못된 등급으로 변환됨")
			}
			if c.SeveritySource != "" && c.SeveritySource != db.WorkerEstimate {
				t.Fatal("출처 위조")
			}
		})
	}
	store := &candidateMemoryStore{}
	tool := newE2DiagnosticCandidateTool(store, binding, state)
	r, _ := tool.Call(t.Context(), json.RawMessage(`{"call_ref":"severity-call","vulnclass":"IDOR","name":"합성 후보","summary":"관찰","severity_source":"confirmed"}`), nil)
	if !r.IsError || len(store.candidates) != 0 {
		t.Fatal("모델 평가 출처 위조 허용")
	}
}
