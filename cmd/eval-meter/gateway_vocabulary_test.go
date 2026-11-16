package main

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/Autumn-27/artex/llmpolicy"
)

// 제품(O)의 모델 호출 기록(llmpolicy.OpenRouterCallRecord)은 이 게이트웨이(B)의 llm-requests 행과 같은 칸·결정·끝 모양
// 어휘를 쓴다. 평가 러너가 두 기록에 같은 사후 검증·무효 규칙(제공자 장애 멈춤)을 적용하는 근거라, 어느 쪽이 바뀌면 이
// 시험이 실패한다(탐색 분리 배치 결정안 D(b)·§7).
func TestGatewayRowsShareProductCallVocabulary(t *testing.T) {
	raw, _ := json.Marshal(llmRow{UnfinishedEnd: endUpstreamCutBeforeDone})
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	got := make([]string, 0, len(fields))
	for name := range fields {
		got = append(got, name)
	}
	want := append([]string(nil), llmpolicy.OpenRouterCallRecordFields...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("gateway row fields %v, product call fields %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("gateway row fields %v, product call fields %v", got, want)
		}
	}
	for gateway, product := range map[string]string{
		endClientClosedAfterDone: llmpolicy.OpenRouterEndClientClosedAfterDone, endClientClosedBeforeDone: llmpolicy.OpenRouterEndClientClosedBeforeDone,
		endUpstreamCutAfterDone: llmpolicy.OpenRouterEndUpstreamCutAfterDone, endUpstreamCutBeforeDone: llmpolicy.OpenRouterEndUpstreamCutBeforeDone,
		outcome(true, false, false): llmpolicy.OpenRouterCallForwarded, outcome(false, false, false): llmpolicy.OpenRouterCallUpstreamError,
	} {
		if gateway != product {
			t.Errorf("gateway %q, product %q", gateway, product)
		}
	}
}
