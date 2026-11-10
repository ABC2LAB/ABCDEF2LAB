package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Autumn-27/artex/llmpolicy"
)

func openRouterConstructorPolicy(t *testing.T) *llmpolicy.OpenRouterPolicy {
	t.Helper()
	manifest := map[string]any{
		"version": 1, "profile_id": 7, "format": "openai", "base_url": llmpolicy.OpenRouterBaseURL, "model": llmpolicy.OpenRouterModel,
		"provider_slug": "fixture/endpoint", "expected_provider_name": "Fixture", "approval_id": "constructor-fixture", "expires_at": time.Now().Add(time.Hour),
		"strict_privacy_ack": true, "account_spend_cap_ack": true, "max_requests": 3, "max_input_tokens": 3 * 262144, "max_output_tokens": 300, "max_cost_usd_micros": 1000000,
		"max_input_tokens_per_call": 262144, "max_output_tokens_per_call": 100, "timeout_seconds": 2, "max_concurrent": 1,
		"max_input_price_usd_micros_per_million": 1000000, "max_output_price_usd_micros_per_million": 1000000,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "policy.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	policy, err := llmpolicy.LoadOpenRouterManifestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}
func TestOpenRouterProviderConstructorRejectsStreamAndProxy(t *testing.T) {
	policy := openRouterConstructorPolicy(t)
	if provider, err := openRouterSDKConfig().NewOpenRouterProvider(policy); err != nil || provider == nil {
		t.Fatal("valid constructor control failed", err)
	}
	for _, field := range []string{"stream", "proxy"} {
		t.Run(field, func(t *testing.T) {
			cfg := openRouterSDKConfig()
			if field == "stream" {
				cfg.Stream = true
			} else {
				cfg.Proxy = "http://127.0.0.1:18888"
			}
			if provider, err := cfg.NewOpenRouterProvider(policy); !errors.Is(err, ErrOpenRouterProvider) || provider != nil {
				t.Fatal("unsupported constructor setting accepted", field, err)
			}
			if policy.Snapshot().Requests != 0 {
				t.Fatal("constructor consumed a model request")
			}
		})
	}
}
