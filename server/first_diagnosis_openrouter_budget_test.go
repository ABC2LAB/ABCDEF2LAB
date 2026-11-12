package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/norma/llm"
)

func urlExternalBudget(t *testing.T, observe func(diagnosisprep.ModelUsage) error) (*FirstDiagnosisModelBudget, string, agent.Config) {
	t.Helper()
	dir := t.TempDir()
	if os.Chmod(dir, 0700) != nil {
		t.Fatal("private directory")
	}
	path := filepath.Join(dir, "model.json")
	raw, _ := json.Marshal(map[string]any{
		"version": 1, "profile_id": 7, "format": "openai", "base_url": llmpolicy.OpenRouterBaseURL, "model": llmpolicy.OpenRouterModel,
		"provider_slug": "fixture/endpoint", "expected_provider_name": "Fixture", "approval_id": "url-budget-test", "expires_at": time.Now().Add(time.Hour),
		"strict_privacy_ack": true, "account_spend_cap_ack": true, "max_requests": 2, "max_input_tokens": 524288, "max_output_tokens": 200, "max_cost_usd_micros": 600000,
		"max_input_tokens_per_call": 262144, "max_output_tokens_per_call": 100, "timeout_seconds": 2, "max_concurrent": 1,
		"max_input_price_usd_micros_per_million": 1000000, "max_output_price_usd_micros_per_million": 1000000,
	})
	if os.WriteFile(path, raw, 0600) != nil {
		t.Fatal("manifest")
	}
	b, err := NewFirstDiagnosisOpenRouterBudget(diagnosisprep.Budget{ModelRequests: 2, InputTokens: 524288, OutputTokens: 200, CostUSDMicros: 600000}, 262144, 100, path, observe)
	if err != nil {
		t.Fatal(err)
	}
	return b, path, agent.Config{Format: llm.FormatOpenAI, Model: llmpolicy.OpenRouterModel, BaseURL: llmpolicy.OpenRouterBaseURL, APIKey: "synthetic", ContextWindowK: 256, MaxTokens: 100}
}
func TestFirstDiagnosisOpenRouterModelBudgetHandoffUsesOnePolicy(t *testing.T) {
	b, path, _ := urlExternalBudget(t, func(diagnosisprep.ModelUsage) error { return nil })
	options := FirstDiagnosisModelOptions{Mode: "openrouter", OpenRouterManifestPath: path, Budget: b}
	_, planner, err := options.load()
	if err != nil {
		t.Fatal(err)
	}
	_, worker, err := options.load()
	if err != nil || planner != worker || planner != b.external {
		t.Fatal("approval reset at handoff")
	}
	options.OpenRouterManifestPath += ".other"
	if _, _, err = options.load(); err == nil {
		t.Fatal("different manifest accepted")
	}
	limits := b.limits
	limits.CostUSDMicros = 600001
	if _, err = NewFirstDiagnosisOpenRouterBudget(limits, 262144, 100, path, b.observe); err == nil {
		t.Fatal("approval total exceeded")
	}
}
func TestFirstDiagnosisOpenRouterModelBudgetPersistsUSDOnceBeforeTransport(t *testing.T) {
	var persisted []diagnosisprep.ModelUsage
	b, _, cfg := urlExternalBudget(t, func(u diagnosisprep.ModelUsage) error { persisted = append(persisted, u); return nil })
	req := llm.CompletionRequest{MaxTokens: 100}
	if err := b.before(context.Background(), cfg, &req); err != nil {
		t.Fatal(err)
	}
	quote, err := b.external.Quote(100)
	if err != nil || quote != 262244 || len(persisted) != 1 || persisted[0].ReservedCostUSDMicros != quote || persisted[0].UsageKnown || b.external.Snapshot().Requests != 0 {
		t.Fatal("reservation did not precede transport exactly once")
	}
	// SDK 응답이 성공처럼 보여도 transport 비용 검증이 없으면 불명으로 남긴다.
	if b.after(llm.Usage{InputTokens: 10, OutputTokens: 10}, 100, nil) == nil || b.Snapshot().UsageKnown {
		t.Fatal("unverified cost trusted")
	}
	if b.before(context.Background(), cfg, &req) == nil {
		t.Fatal("unknown call retried")
	}
}
func TestFirstDiagnosisOpenRouterModelBudgetStorageFailureAndCostExhaustion(t *testing.T) {
	b, _, cfg := urlExternalBudget(t, func(diagnosisprep.ModelUsage) error { return errors.New("synthetic storage failure") })
	req := llm.CompletionRequest{MaxTokens: 100}
	if b.before(context.Background(), cfg, &req) == nil || b.external.Snapshot().Requests != 0 || b.Snapshot().ReservedCostUSDMicros != 262244 {
		t.Fatal("unpersisted reservation reached transport")
	}
	b, _, cfg = urlExternalBudget(t, func(diagnosisprep.ModelUsage) error { return nil })
	b.limits.CostUSDMicros = 262243
	if b.before(context.Background(), cfg, &req) == nil || b.Snapshot().Requests != 0 || b.external.Snapshot().Requests != 0 {
		t.Fatal("insufficient USD reserved")
	}
}

func TestFirstDiagnosisOpenRouterModelBudgetUSDOnlyBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cap     int64
		allowed bool
	}{{"exact", 262244, true}, {"one_micro_short", 262243, false}} {
		t.Run(tc.name, func(t *testing.T) {
			var writes int
			b, _, cfg := urlExternalBudget(t, func(diagnosisprep.ModelUsage) error { writes++; return nil })
			b.limits.CostUSDMicros = tc.cap
			// 요청·입출력 토큰은 첫 호출 예약보다 넉넉하며 USD만 달라진다.
			if b.limits.ModelRequests != 2 || b.limits.InputTokens != 524288 || b.limits.OutputTokens != 200 {
				t.Fatal("다른 한도에 막힐 수 있는 fixture")
			}
			req := llm.CompletionRequest{MaxTokens: 100}
			err := b.before(context.Background(), cfg, &req)
			if (err == nil) != tc.allowed {
				t.Fatal("USD만 부족한 호출의 허용 판정 오류")
			}
			u := b.Snapshot()
			if tc.allowed {
				if writes != 1 || u.Requests != 1 || u.ReservedCostUSDMicros != 262244 {
					t.Fatal("경계값의 예약 누락")
				}
			} else if writes != 0 || u.Requests != 0 || u.ReservedInputTokens != 0 || u.ReservedOutputTokens != 0 || u.ReservedCostUSDMicros != 0 {
				t.Fatal("USD 거절 후 부작용")
			}
			if b.external.Snapshot().Requests != 0 {
				t.Fatal("예산 검사에서 송신 발생")
			}
		})
	}
}
func TestFirstDiagnosisOpenRouterModelBudgetRejectsConsumedApprovalAtConstruction(t *testing.T) {
	b, path, _ := urlExternalBudget(t, func(diagnosisprep.ModelUsage) error { return nil })
	if _, err := NewFirstDiagnosisOpenRouterBudget(b.limits, 262144, 100, path, b.observe); err != nil {
		t.Fatal("미소비 승인 대조군 실패")
	}
	marker := filepath.Join(filepath.Dir(path), fmt.Sprintf(".openrouter-%x.used", sha256.Sum256([]byte("url-budget-test"))))
	if os.WriteFile(marker, nil, 0600) != nil {
		t.Fatal("소비 marker 설치 실패")
	}
	// 구문·보호 파일 검증은 여전히 통과한다. 생성자의 소비 검사가 거부해야 한다.
	if _, err := llmpolicy.LoadOpenRouterManifestFile(path); err != nil {
		t.Fatal("소비 이외의 검사가 먼저 실패함")
	}
	if _, err := NewFirstDiagnosisOpenRouterBudget(b.limits, 262144, 100, path, b.observe); err == nil {
		t.Fatal("소비된 승인이 생성자를 통과함")
	}
}

// 동시 모델 호출 수(C32): OpenRouter는 승인 max_concurrent 이하, 로컬 모델은 1만, 첫 호출 전에만 바꾼다. 넓힌 gate는
// 그 수만큼 호출을 동시에 들인다.
func TestFirstDiagnosisModelBudgetConfigureConcurrency(t *testing.T) {
	b, path, cfg := urlExternalBudget(t, func(diagnosisprep.ModelUsage) error { return nil })
	if b.ConfigureConcurrency(2) == nil || b.ConfigureConcurrency(0) == nil {
		t.Fatal("concurrency above the approval or zero accepted")
	}
	if err := b.ConfigureConcurrency(1); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var manifest map[string]any
	_ = json.Unmarshal(raw, &manifest)
	manifest["max_concurrent"] = 2
	raw, _ = json.Marshal(manifest)
	if os.WriteFile(path, raw, 0600) != nil {
		t.Fatal("manifest")
	}
	wide, err := NewFirstDiagnosisOpenRouterBudget(b.limits, 262144, 100, path, b.observe)
	if err != nil {
		t.Fatal(err)
	}
	if err := wide.ConfigureConcurrency(2); err != nil {
		t.Fatal("approved concurrency rejected", err)
	}
	// 좁은 gate면 두 번째가 기다리므로 시한을 둬 실패로 끝나게 한다.
	admit, stopAdmit := context.WithTimeout(context.Background(), time.Second)
	defer stopAdmit()
	first, err1 := wide.acquireCall(admit)
	second, err2 := wide.acquireCall(admit)
	if err1 != nil || err2 != nil {
		t.Fatal("gate did not admit two calls", err1, err2)
	}
	blocked, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := wide.acquireCall(blocked); err == nil {
		t.Fatal("gate admitted more calls than configured")
	}
	first()
	second()
	req := llm.CompletionRequest{MaxTokens: 100}
	call, err := wide.reserve(context.Background(), cfg, &req)
	if err != nil {
		t.Fatal(err)
	}
	// 정산까지 끝난 뒤에도(진행 중 호출 없음) 첫 호출 뒤에는 바꾸지 않는다.
	_ = wide.settle(call, llm.Usage{}, errors.New("synthetic transport failure"))
	if wide.ConfigureConcurrency(1) == nil {
		t.Fatal("concurrency changed after a call")
	}
	local, err := NewFirstDiagnosisModelBudget(diagnosisprep.Budget{ModelRequests: 2, InputTokens: 524288, OutputTokens: 200}, 262144, 100, func(diagnosisprep.ModelUsage) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if local.ConfigureConcurrency(2) == nil || local.ConfigureConcurrency(1) != nil {
		t.Fatal("local model concurrency rule")
	}
	if got := wide.external.Limits().MaxConcurrent; got != 2 {
		t.Fatalf("manifest max_concurrent not exposed: %d", got)
	}
}
