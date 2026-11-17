package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// 실제 SDK의 harness.execOne과 진단 기록 래퍼를 통과한다. 모델·대상 호출은 없다.
func candidateThroughHarness(t *testing.T, tool actool.CoreTool, input json.RawMessage) {
	t.Helper()
	turn := 0
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		turn++
		events := []llm.StreamEvent{{Type: llm.SETextDelta, Text: "done"}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
		if turn == 1 {
			events = []llm.StreamEvent{
				{Type: llm.SEToolUseStart, ToolID: "candidate-call", ToolName: tool.Name()},
				{Type: llm.SEToolInputJSON, Text: string(input)},
				{Type: llm.SEMessageDelta, StopReason: "tool_use"},
			}
		}
		for _, event := range events {
			if !yield(event, nil) {
				return
			}
		}
	}}
	session := agentcore.NewSession(agentcore.Options{
		Provider: provider, Tools: []actool.CoreTool{&diagnosticTool{CoreTool: tool}},
		PermissionMode: permission.ModeBypass, DisableBackgroundTasks: true, MaxTurns: 3, MaxDuration: 3 * time.Second,
	})
	defer session.Close()
	for range session.Prompt(t.Context(), "synthetic candidate") {
	}
	if turn != 2 {
		t.Fatalf("합성 후보 도구 왕복 미완료: %d turns", turn)
	}
}

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
		{"rationale type", `,"suggested_severity":"high","severity_rationale":[]`, ""},
		{"long English", `,"suggested_severity":"high","severity_rationale":"` + strings.Repeat("text", 300) + `"`, ""},
		{"long multiline", `,"suggested_severity":"high","severity_rationale":"` + strings.Repeat("문", 400) + `\n추가 설명"`, ""},
		{"missing rationale", `,"suggested_severity":"high"`, ""},
		{"null", `,"suggested_severity":null,"severity_rationale":null`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &candidateMemoryStore{}
			tool := newE2DiagnosticCandidateTool(store, binding, state)
			candidateThroughHarness(t, tool, json.RawMessage(`{"call_ref":"severity-call","vulnclass":"IDOR","name":"합성 후보","summary":"관찰 근거"`+tc.extra+`}`))
			if len(store.candidates) != 1 {
				t.Fatal("선택 평가 때문에 후보 유실")
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
	for _, input := range []string{
		`{"call_ref":"severity-call","vulnclass":"IDOR","name":"합성 후보"}`,
		`{"call_ref":"severity-call","vulnclass":"IDOR","name":null,"summary":"관찰"}`,
		`{"call_ref":"severity-call","vulnclass":"IDOR","name":42,"summary":"관찰"}`,
		`{"call_ref":"severity-call","vulnclass":"IDOR","name":"합성 후보","summary":"관찰","severity_source":"confirmed"}`,
		`{"call_ref":"severity-call","vulnclass":"IDOR","name":"합성 후보","summary":"관찰","related_call_refs":null}`,
		`{"call_ref":"severity-call","vulnclass":"IDOR","name":"합성 후보","summary":"관찰","remediation":null}`,
		`null`, `[]`,
	} {
		candidateThroughHarness(t, tool, json.RawMessage(input))
		if len(store.candidates) != 0 {
			t.Fatal("핵심 입력·기존 선택 필드 검사를 완화함")
		}
	}
}

func TestCandidateLongKoreanRationaleThroughHarness(t *testing.T) {
	execution, _, binding := e2WorkerFixture(t)
	state := &e2HTTPToolState{}
	result := e2runner.Result{ToolOutput: "합성 응답", Trusted: e2runner.TrustedResult{Execution: execution.SnapshotForIntent(binding.IntentID), Final: true, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, Classification: "completed", Call: e2proxy.CallResult{IntentID: binding.IntentID, CallRef: "severity-call", Final: true, AuditComplete: true, Counts: e2proxy.Counts{Responses: 1, ForwardAttempted: 1}, Observations: []e2proxy.HTTPObservation{{Sequence: 1, AttemptRef: "severity-attempt", Method: "GET", Endpoint: "/documents", Decision: "allowed", StatusCode: 200, ResponseComplete: true, ForwardAttempted: true, AuditComplete: true, ObservedAt: time.Now().UTC()}}}}}
	state.observeDiagnostic(execution, binding.IntentID, "synthetic fixture", result)
	for _, rationale := range []string{strings.Repeat("문", 341), strings.Repeat("문", 400), strings.Repeat("문", 330) + "keep-secret" + strings.Repeat("🙂", 30)} {
		store := &candidateMemoryStore{}
		input, err := json.Marshal(map[string]string{"call_ref": "severity-call", "vulnclass": "IDOR", "name": "합성 후보", "summary": "관찰", "suggested_severity": "high", "severity_rationale": rationale})
		if err != nil {
			t.Fatal(err)
		}
		candidateThroughHarness(t, newE2DiagnosticCandidateTool(store, binding, state), input)
		if len(store.candidates) != 1 {
			t.Fatal("긴 근거로 후보가 사라짐")
		}
		c := store.candidates[0]
		if c.SuggestedSeverity != "high" || c.SeveritySource != db.WorkerEstimate || !utf8.ValidString(c.SeverityRationale) || len(c.SeverityRationale) > db.MaxSeverityRationaleBytes {
			t.Fatal("긴 한국어 근거의 유효한 평가·바이트 경계 유실")
		}
		if len(rationale) > db.MaxSeverityRationaleBytes && !strings.HasSuffix(c.SeverityRationale, "[근거 일부 생략]") {
			t.Fatal("근거 잘림 표시 없음")
		}
		if strings.Contains(c.SeverityRationale, "keep") || c.OutputTruncated || c.EvidenceDigest != db.DiagnosticEvidenceDigest(c.Evidence) {
			t.Fatal("자르기 전 보호 값 가림 또는 관찰·근거 지문 보존 실패")
		}
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
