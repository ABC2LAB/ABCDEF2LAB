package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/norma/llm"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func externalManifestFixture(t *testing.T) (string, llmpolicy.ProfileTarget) {
	t.Helper()
	target := llmpolicy.ProfileTarget{ID: 71, Format: "openai", BaseURL: llmpolicy.OpenRouterBaseURL, Model: llmpolicy.OpenRouterModel}
	m := map[string]any{"version": 1, "profile_id": target.ID, "format": target.Format, "base_url": target.BaseURL, "model": target.Model, "provider_slug": "parasail/fp8", "expected_provider_name": "Parasail", "approval_id": "synthetic-owned-approval", "expires_at": time.Now().Add(time.Hour).UTC(), "strict_privacy_ack": true, "account_spend_cap_ack": true, "max_requests": 2, "max_input_tokens": 524288, "max_output_tokens": 8192, "max_cost_usd_micros": 1000000, "max_input_tokens_per_call": 262144, "max_output_tokens_per_call": 4096, "timeout_seconds": 30, "max_concurrent": 1, "max_input_price_usd_micros_per_million": 150000, "max_output_price_usd_micros_per_million": 1000000}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "model.json")
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path, target
}
func TestFirstDiagnosisExternalModeKeepsLocalPolicy(t *testing.T) {
	path, target := externalManifestFixture(t)
	options := FirstDiagnosisModelOptions{Mode: "openrouter", OpenRouterManifestPath: path}
	if err := CheckFirstDiagnosisModelOptions(options, target); err != nil {
		t.Fatal(err)
	}
	for name, o := range map[string]FirstDiagnosisModelOptions{"missing": {}, "implicit-external": {OpenRouterManifestPath: path}, "conflict": {Mode: "openrouter", LocalManifestPath: path, OpenRouterManifestPath: path}, "unknown": {Mode: "external", OpenRouterManifestPath: path}, "local-public": {Mode: "local_only", LocalManifestPath: path}} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := o.load(); err == nil {
				t.Fatal("ambiguous or local-public model policy accepted")
			}
		})
	}
	local, external, err := options.load()
	if err != nil || local != nil || external == nil {
		t.Fatal("external policy did not stay separate")
	}
	other := target
	other.ID++
	if err := CheckFirstDiagnosisModelOptions(options, other); !errors.Is(err, ErrFirstDiagnosisModel) {
		t.Fatal("wrong DB profile accepted")
	}
	other = target
	other.BaseURL = "https://example.com/v1"
	if err := CheckFirstDiagnosisModelOptions(options, other); !errors.Is(err, ErrFirstDiagnosisModel) {
		t.Fatal("wrong DB endpoint accepted")
	}
	s := &Server{firstDiagnosis: true, openRouterPolicy: external}
	cfg := agent.Config{Format: llm.FormatOpenAI, BaseURL: target.BaseURL, Model: target.Model, APIKey: "synthetic-not-a-secret", MaxTokens: 4096, ContextWindowK: 262, Stream: false}
	if _, err := s.newProfileProvider(target.ID, cfg); err != nil {
		t.Fatal("valid protected provider rejected", err)
	}
	cfg.BaseURL = "https://example.com/v1"
	if _, err := s.newProfileProvider(target.ID, cfg); !errors.Is(err, ErrFirstDiagnosisModel) {
		t.Fatal("external provider silently changed endpoint")
	}
	if got := s.FirstDiagnosisModelUsage(); got == nil || got.Requests != 0 {
		t.Fatal("construction sent or consumed a model request")
	}
	if (&Server{}).hasModelPolicy() || !s.hasModelPolicy() {
		t.Fatal("guarded provider classification failed")
	}
	if _, err := newServerConfiguredWithOptions(context.Background(), nil, "", "", "", nil, serverStartupOptions{openRouterPolicy: external}, nil); !errors.Is(err, ErrFirstDiagnosisModel) {
		t.Fatal("external policy escaped first-diagnosis startup")
	}
}

// 서버 시작이 모델 설정의 외부 API 여부를 호스트 설정에 싣는다. 호스트는 그 값으로 인증 준비 정책을 거부한다(#522 재상 지적 2).
func TestFirstDiagnosisHTTPModelConfigMarksExternalModel(t *testing.T) {
	path, _ := externalManifestFixture(t)
	host := FirstDiagnosisHTTPHostConfig{PolicyFile: "policy.json", ExternalTestAccounts: true}
	_, got, err := firstDiagnosisHTTPModelConfig(FirstDiagnosisHTTPStartOptions{Host: host, ModelExecutionMode: "openrouter", OpenRouterManifestPath: path})
	if err != nil || !got.ExternalModel || got.PolicyFile != host.PolicyFile || !got.ExternalTestAccounts {
		t.Fatalf("external model not marked: %v %+v", err, got.ExternalModel)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(root, "models.json")
	if err := os.WriteFile(local, []byte(`{"version":1,"profiles":[{"profile_id":7,"format":"openai","model":"local:fixture","base_url":"http://127.0.0.1:11434/v1","addresses":["127.0.0.1"]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, got, err = firstDiagnosisHTTPModelConfig(FirstDiagnosisHTTPStartOptions{Host: host, LocalLLMManifestPath: local})
	if err != nil || got.ExternalModel {
		t.Fatalf("local model marked external: %v %+v", err, got.ExternalModel)
	}
	if _, _, err := firstDiagnosisHTTPModelConfig(FirstDiagnosisHTTPStartOptions{Host: host, ModelExecutionMode: "external", OpenRouterManifestPath: path}); err == nil {
		t.Fatal("unknown model mode accepted")
	}
}

// 검색은 검색 제공자로 나가므로 외부 API 실행에서만 켠다. 로컬 모델 실행·잘못된 설정은 준비·실행 서버 모두 거부한다
// (루프 복원 결정안 §2-1, 규칙 8).
func TestFirstDiagnosisWebSearchOnlyWithExternalModel(t *testing.T) {
	path, _ := externalManifestFixture(t)
	search := agent.E2HTTPWebSearch{Backend: "ddgs"}
	if _, got, err := firstDiagnosisHTTPModelConfig(FirstDiagnosisHTTPStartOptions{Host: FirstDiagnosisHTTPHostConfig{WebSearch: search}, ModelExecutionMode: "openrouter", OpenRouterManifestPath: path}); err != nil || got.WebSearch != search {
		t.Fatal("external run with search rejected", err)
	}
	if _, _, err := (FirstDiagnosisModelOptions{Mode: "openrouter", OpenRouterManifestPath: path, WebSearch: search}).load(); err != nil {
		t.Fatal("external preparation with search rejected", err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(root, "models.json")
	if err := os.WriteFile(local, []byte(`{"version":1,"profiles":[{"profile_id":7,"format":"openai","model":"local:fixture","base_url":"http://127.0.0.1:11434/v1","addresses":["127.0.0.1"]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := firstDiagnosisHTTPModelConfig(FirstDiagnosisHTTPStartOptions{Host: FirstDiagnosisHTTPHostConfig{WebSearch: search}, LocalLLMManifestPath: local}); err == nil {
		t.Fatal("local run accepted search")
	}
	if _, _, err := (FirstDiagnosisModelOptions{LocalManifestPath: local, WebSearch: search}).load(); err == nil {
		t.Fatal("local preparation accepted search")
	}
	if _, _, err := firstDiagnosisHTTPModelConfig(FirstDiagnosisHTTPStartOptions{Host: FirstDiagnosisHTTPHostConfig{WebSearch: agent.E2HTTPWebSearch{Backend: "brave-free"}}, ModelExecutionMode: "openrouter", OpenRouterManifestPath: path}); err == nil {
		t.Fatal("invalid search setting accepted")
	}
}
