package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestE2RuntimeControlDoesNotTurnReferencesIntoReadiness(t *testing.T) {
	ctx := t.Context()
	b, err := NewE2RuntimeControlBackend(ctx, E2RuntimeControlOptions{EvalRunID: "eval-fixture", ReservationRef: "reservation-fixture", ModelTag: "model-fixture", ExecutionRef: "execution-fixture", OutputDirectory: "/unopened-fixture", ExpectedPolicyFingerprintSHA256: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := b.Ready(ctx)
	if err != nil || ready.State != "starting" || ready.PolicyFingerprintSHA256 != "" {
		t.Fatal("실제 호스트 없이 service_ready를 반환함")
	}
	head, err := b.Head(ctx)
	if err != nil || head.State != "no_execution" || head.ExecutionRef != "" {
		t.Fatal("참조만으로 실행을 생성함")
	}
	if b.BindReset(ctx, "reset-fixture") == nil {
		t.Fatal("권위 없이 reset을 결속함")
	}
	if _, err = b.CloseAdmission(ctx, "close-fixture"); err == nil {
		t.Fatal("실제 admission fence 없이 ACK를 반환함")
	}
	if _, err = b.Finalize(ctx, "close-fixture"); err == nil {
		t.Fatal("실제 종료 없이 마감함")
	}
	if b.Attach(&FirstDiagnosisHTTPRuntime{}) == nil {
		t.Fatal("불완전한 startup handle을 수용함")
	}
	if b.Dispose() != nil {
		t.Fatal("비활성 backend 정리 실패")
	}
}
func TestE2RuntimeControlRejectsInvalidAndZeroValue(t *testing.T) {
	if _, err := NewE2RuntimeControlBackend(context.Background(), E2RuntimeControlOptions{}); err == nil {
		t.Fatal("미정 평가 입력 수용")
	}
	var b E2RuntimeControlBackend
	if _, err := b.Ready(t.Context()); err == nil {
		t.Fatal("zero backend ready 수용")
	}
	if _, err := b.Head(t.Context()); err == nil {
		t.Fatal("zero backend head 수용")
	}
	if _, err := b.Finalize(t.Context(), "close-fixture"); err == nil {
		t.Fatal("zero backend 마감 수용")
	}
}

func TestE2ControlOptionsWithholdPrivateHostInputs(t *testing.T) {
	private := "synthetic-private-canary"
	values := []any{E2RuntimeControlOptions{OutputDirectory: private, EvalRunID: private}, E2ControlOptions{ControlDirectory: private, EvalRunID: private}}
	for _, v := range values {
		data, err := json.Marshal(v)
		if err != nil || string(data) != "{}" {
			t.Fatal("제어 공급 입력 JSON 노출")
		}
		if text := fmt.Sprintf("%v %#v", v, v); strings.Contains(text, private) || !strings.Contains(text, "protected") {
			t.Fatal("제어 공급 입력 진단 노출")
		}
	}
}
