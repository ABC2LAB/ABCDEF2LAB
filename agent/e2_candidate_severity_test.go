package agent

import (
	"encoding/json"
	"strings"
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

func TestSeverityRationaleRedactsRunSecret(t *testing.T) {
	execution, _, binding := e2WorkerFixture(t)
	state := &e2HTTPToolState{}
	result := e2runner.Result{ToolOutput: "합성 응답", Trusted: e2runner.TrustedResult{Execution: execution.SnapshotForIntent(binding.IntentID), Final: true, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, Classification: "completed", Call: e2proxy.CallResult{IntentID: binding.IntentID, CallRef: "severity-call", Final: true, AuditComplete: true, Counts: e2proxy.Counts{Responses: 1, ForwardAttempted: 1}, Observations: []e2proxy.HTTPObservation{{Sequence: 1, AttemptRef: "severity-attempt", Method: "GET", Endpoint: "/documents", Decision: "allowed", StatusCode: 200, ResponseComplete: true, ForwardAttempted: true, AuditComplete: true, ObservedAt: time.Now().UTC()}}}}}
	state.observeDiagnostic(execution, binding.IntentID, "synthetic fixture", result)
	store := &candidateMemoryStore{}
	tool := newE2DiagnosticCandidateTool(store, binding, state)
	r, err := tool.Call(t.Context(), json.RawMessage(`{"call_ref":"severity-call","vulnclass":"IDOR","name":"합성 후보","summary":"관찰","suggested_severity":"high","severity_rationale":"응답에 keep-secret 값이 함께 보여 접근통제 영향을 검토해야 합니다."}`), nil)
	if err != nil || r.IsError || len(store.candidates) != 1 {
		t.Fatal("후보 저장 실패", err)
	}
	c := store.candidates[0]
	if c.SuggestedSeverity != "high" || c.SeveritySource != db.WorkerEstimate || c.Verification != "manual_review_required" || !strings.Contains(c.SeverityRationale, "접근통제 영향") {
		t.Fatal("가림 뒤 유효한 평가·수동 검토 상태가 사라짐")
	}
	if strings.Contains(c.SeverityRationale, "keep-secret") {
		t.Fatal("실행 보호 값이 심각도 근거에 남음")
	}
}

// 긴 한국어 근거는 실행 보호 값을 가린 뒤에 자른다. 보호 값이 자르는 자리에 걸쳐도 조각이 남지 않는다.
func TestSeverityRationaleCutAfterRunSecretRedaction(t *testing.T) {
	execution, _, binding := e2WorkerFixture(t)
	state := &e2HTTPToolState{}
	result := e2runner.Result{ToolOutput: "합성 응답", Trusted: e2runner.TrustedResult{Execution: execution.SnapshotForIntent(binding.IntentID), Final: true, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, Classification: "completed", Call: e2proxy.CallResult{IntentID: binding.IntentID, CallRef: "severity-call", Final: true, AuditComplete: true, Counts: e2proxy.Counts{Responses: 1, ForwardAttempted: 1}, Observations: []e2proxy.HTTPObservation{{Sequence: 1, AttemptRef: "severity-attempt", Method: "GET", Endpoint: "/documents", Decision: "allowed", StatusCode: 200, ResponseComplete: true, ForwardAttempted: true, AuditComplete: true, ObservedAt: time.Now().UTC()}}}}}
	state.observeDiagnostic(execution, binding.IntentID, "synthetic fixture", result)
	// 자르는 자리(1,024바이트 − 잘림 표시)에 보호 값이 걸치게 한국어 앞부분 길이를 맞춘다.
	cut := db.MaxSeverityRationaleBytes - len(db.SeverityRationaleCutMarker)
	prefix := strings.Repeat("문", (cut-4)/3)
	rationale := prefix + " keep-secret " + strings.Repeat("접근통제 영향을 검토해야 합니다 ", 40)
	if start := strings.Index(rationale, "keep-secret"); start >= cut || start+len("keep-secret") <= cut {
		t.Fatal("시험 준비: 보호 값이 자르는 자리에 걸치지 않음")
	}
	input, _ := json.Marshal(map[string]string{"call_ref": "severity-call", "vulnclass": "IDOR", "name": "합성 후보", "summary": "관찰", "suggested_severity": "high", "severity_rationale": rationale})
	store := &candidateMemoryStore{}
	r, err := newE2DiagnosticCandidateTool(store, binding, state).Call(t.Context(), input, nil)
	if err != nil || r.IsError || len(store.candidates) != 1 {
		t.Fatal("후보 저장 실패", err)
	}
	got := store.candidates[0].SeverityRationale
	if store.candidates[0].SuggestedSeverity != "high" || !strings.HasSuffix(got, db.SeverityRationaleCutMarker) || len(got) > db.MaxSeverityRationaleBytes {
		t.Fatalf("긴 근거의 평가·잘림 표시가 기대와 다름 (%d바이트)", len(got))
	}
	for _, piece := range []string{"keep-secret", "keep-se", "keep"} {
		if strings.Contains(got, piece) {
			t.Fatalf("보호 값 조각 %q가 잘린 근거에 남음", piece)
		}
	}
}
