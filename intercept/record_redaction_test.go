package intercept

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const recordSecret = "visible-pass-77"

func maskRecordSecret(s string) string { return strings.ReplaceAll(s, recordSecret, "[REDACTED]") }

// 저장 기록 가림이 달린 문맥에서는 감사 추적의 사용자 글·도구 입력 항목을 가리고, 승인 훅 대조 키(입력 digest)는 원문으로
// 계산한다(#886 이동찬 리뷰: model_visible_v1 비밀번호가 감사 상세에 원문으로 남는 경로).
func TestTraceRecordRedactionKeepsRawDigest(t *testing.T) {
	ctx := WithRecordRedaction(context.Background(), maskRecordSecret)
	ctx, trace := WithTrace(ctx, "log in with "+recordSecret, nil)
	input := []byte(`{"command":"curl -d pw=` + recordSecret + ` https://example.invalid/login"}`)
	trace.Start("tool-1", "Bash", input)
	call := WithCall(ctx, "Bash", input)
	a := auditFor(call, Decision{Action: "deny", ModelInput: json.RawMessage(`{"tool_input":"` + recordSecret + `"}`)}, input, "denied")
	if a.Correlation != "exact" || a.InputDigest != digestInput(input) {
		t.Fatalf("원문 대조가 깨짐: %s %s", a.Correlation, a.InputDigest)
	}
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), recordSecret) {
		t.Fatalf("감사 상세에 비밀번호: %s", raw)
	}
	if got := string(recordBytes(ctx, input)); strings.Contains(got, recordSecret) {
		t.Fatalf("저장할 도구 입력에 비밀번호: %s", got)
	}
	// 가림이 없는 문맥은 지금처럼 원문이다.
	_, plain := WithTrace(context.Background(), recordSecret, nil)
	if !strings.Contains(plain.user, recordSecret) {
		t.Fatal("가림 없는 문맥을 바꿈")
	}
}

// 실제 저장: 거부 기록(Log)의 tool_input과 감사 상세에 비밀번호가 남지 않는다.
func TestInterceptLogStoresRedactedInputAndAudit(t *testing.T) {
	d := configurationTestDB(t)
	ctx := WithRecordRedaction(t.Context(), maskRecordSecret)
	ctx, trace := WithTrace(ctx, "log in with "+recordSecret, nil)
	input := []byte(`{"command":"curl -d pw=` + recordSecret + ` https://example.invalid/login"}`)
	trace.Start("tool-2", "Bash", input)
	call := WithCall(ctx, "Bash", input)
	New(d).Log(call, 0, Decision{Action: "deny", Message: "synthetic deny", ModelInput: json.RawMessage(`{"tool_input":"` + recordSecret + `"}`)}, "Bash", input, "denied")
	var id int64
	if err := d.QueryRow(`SELECT max(id) FROM intercept_pending WHERE tool_name='Bash' AND reason='synthetic deny'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM intercept_pending WHERE id=$1`, id)
	detail, err := d.GetInterceptDetail(id)
	if err != nil || detail == nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(detail)
	if strings.Contains(string(raw), recordSecret) || !strings.Contains(string(detail.ToolInput), "[REDACTED]") {
		t.Fatalf("저장된 승인 기록에 비밀번호: %s", raw)
	}
	if detail.Audit == nil || detail.Audit.InputDigest != digestInput(input) {
		t.Fatal("원문 digest가 바뀜")
	}
}
