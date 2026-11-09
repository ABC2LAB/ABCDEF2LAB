package server

import (
	"context"
	"encoding/json"
	"errors"
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
func TestURLExternalBudgetHandoffUsesOnePolicy(t *testing.T) {
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
	limits.CostUSDMicros++
	limits.CostUSDMicros = 600001
	if _, err = NewFirstDiagnosisOpenRouterBudget(limits, 262144, 100, path, b.observe); err == nil {
		t.Fatal("approval total exceeded")
	}
}
func TestURLExternalBudgetPersistsUSDOnceBeforeTransport(t *testing.T) {
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
	// A successful-looking SDK response without validated transport accounting is unknown.
	if b.after(llm.Usage{InputTokens: 10, OutputTokens: 10}, 100, nil) == nil || b.Snapshot().UsageKnown {
		t.Fatal("unverified cost trusted")
	}
	if b.before(context.Background(), cfg, &req) == nil {
		t.Fatal("unknown call retried")
	}
}
func TestURLExternalBudgetStorageFailureAndCostExhaustion(t *testing.T) {
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
