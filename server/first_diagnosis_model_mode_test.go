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

// 저장 회차 사용량은 실행 단계만이다: 준비 단계 스냅숏을 계수마다 뺀다. 엔진 샘플링 칸을 덮어쓴 수도 같다(#646 리뷰).
// 준비 단계(Planner만)에서 덮어쓰기 2번, 실행 단계에서 3번 더 → 실행 기록 3. 기준값이 null이면 그대로다.
func TestExecutionModelUsageSubtractsPreparationOverwrites(t *testing.T) {
	count := func(v int64) *int64 { return &v }
	preparation := llmpolicy.OpenRouterUsage{Requests: 2, Errors: 1, InputTokens: 100, CostMicros: 7, SamplingOverwrites: count(2)}
	afterRun := llmpolicy.OpenRouterUsage{Requests: 6, Errors: 1, InputTokens: 400, CostMicros: 20, SamplingOverwrites: count(5)}
	got := executionModelUsage(afterRun, preparation)
	if got.Requests != 4 || got.Errors != 0 || got.InputTokens != 300 || got.CostMicros != 13 || got.SamplingOverwrites == nil || *got.SamplingOverwrites != 3 {
		t.Fatalf("execution usage %+v overwrites %v", got, got.SamplingOverwrites)
	}
	if *afterRun.SamplingOverwrites != 5 || *preparation.SamplingOverwrites != 2 {
		t.Fatal("subtraction changed the input snapshots")
	}
	if *got.SamplingOverwrites > got.Requests {
		t.Fatal("overwrites exceed execution requests")
	}
	// v1·v2 승인(덮어쓰기 없음)은 두 값 모두 null이고 결과도 null이다. 준비 스냅숏이 없을 때(기준값 0값)는 현재 값 그대로다.
	if got := executionModelUsage(llmpolicy.OpenRouterUsage{Requests: 3}, llmpolicy.OpenRouterUsage{Requests: 1}); got.SamplingOverwrites != nil || got.Requests != 2 {
		t.Fatalf("v1/v2 usage %+v", got)
	}
	if got := executionModelUsage(afterRun, llmpolicy.OpenRouterUsage{}); got.SamplingOverwrites == nil || *got.SamplingOverwrites != 5 {
		t.Fatalf("missing baseline changed overwrites %v", got.SamplingOverwrites)
	}
	// 준비 단계 덮어쓰기 2 뒤 실행 단계에서 0·1·3번 → 실행 기록 0·1·3(준비 값만으로 0이 아니게 되지 않는다).
	for _, run := range []int64{0, 1, 3} {
		got := executionModelUsage(llmpolicy.OpenRouterUsage{Requests: 2 + run, SamplingOverwrites: count(2 + run)}, preparation)
		if got.SamplingOverwrites == nil || *got.SamplingOverwrites != run {
			t.Fatalf("execution overwrites %v, want %d", got.SamplingOverwrites, run)
		}
	}
}

// 실제 메서드도 같은 차분을 쓴다: v3 정책과 같은 정책의 준비 스냅숏(실행 서버를 만들 때 잡는 기준값)을 주면 실행 기록의
// 덮어쓴 수는 0이고 null이 아니다. 돌려받은 값을 바꿔도 다음 호출·정책 계수는 그대로다. v1 정책은 null이다.
func TestFirstDiagnosisModelUsageOverwritesAreExecutionOnly(t *testing.T) {
	v3 := map[string]any{"version": 3, "profile_id": 71, "format": "openai", "base_url": llmpolicy.OpenRouterBaseURL, "model": llmpolicy.OpenRouterModel,
		"provider_slug": "parasail/fp8", "expected_provider_name": "Parasail", "approval_id": "synthetic-owned-approval",
		"expires_at": time.Now().Add(time.Hour).UTC(), "strict_privacy_ack": true, "account_spend_cap_ack": true, "max_requests": 2,
		"max_input_tokens": 524288, "max_output_tokens": 8192, "max_cost_usd_micros": 1000000, "max_input_tokens_per_call": 262144,
		"max_output_tokens_per_call": 4096, "timeout_seconds": 30, "max_concurrent": 1, "max_input_price_usd_micros_per_million": 150000,
		"max_output_price_usd_micros_per_million": 1000000, "max_consecutive_transient_errors": 4,
		"sampling": map[string]any{"temperature": 1.0, "top_p": 0.95, "top_k": 20, "min_p": 0, "presence_penalty": 1.5, "repetition_penalty": 1.0, "seed": 42}}
	raw, _ := json.Marshal(v3)
	policy, err := llmpolicy.ParseOpenRouterManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{firstDiagnosis: true, openRouterPolicy: policy, openRouterBaseline: policy.Snapshot()}
	got := s.FirstDiagnosisModelUsage()
	if got == nil || got.SamplingOverwrites == nil || *got.SamplingOverwrites != 0 {
		t.Fatalf("v3 execution overwrites %+v", got)
	}
	*got.SamplingOverwrites = 7
	if again := s.FirstDiagnosisModelUsage(); *again.SamplingOverwrites != 0 || *policy.Snapshot().SamplingOverwrites != 0 {
		t.Fatal("returned usage shares the counter")
	}
	path, _ := externalManifestFixture(t)
	_, v1, err := (FirstDiagnosisModelOptions{Mode: "openrouter", OpenRouterManifestPath: path}).load()
	if err != nil {
		t.Fatal(err)
	}
	s = &Server{firstDiagnosis: true, openRouterPolicy: v1, openRouterBaseline: v1.Snapshot()}
	if got := s.FirstDiagnosisModelUsage(); got == nil || got.SamplingOverwrites != nil {
		t.Fatalf("v1 execution overwrites %+v", got)
	}
}

// 자동 넘김 모드의 제공자별 응답 수도 실행 구간만 남긴다(준비 단계 호출은 뺀다). 고정 모드는 빈 값 그대로다.
func TestExecutionModelUsageReturnedProviders(t *testing.T) {
	after := llmpolicy.OpenRouterUsage{Requests: 5, ReturnedProviders: llmpolicy.OpenRouterProviderCounts{{Slug: "first/fp8", Responses: 3}, {Slug: "second/fp8", Responses: 2}}}
	preparation := llmpolicy.OpenRouterUsage{Requests: 3, ReturnedProviders: llmpolicy.OpenRouterProviderCounts{{Slug: "first/fp8", Responses: 3}}}
	got := executionModelUsage(after, preparation)
	if got.Requests != 2 || got.ReturnedProviders != (llmpolicy.OpenRouterProviderCounts{{Slug: "second/fp8", Responses: 2}}) {
		t.Fatalf("%+v", got)
	}
	if got := executionModelUsage(llmpolicy.OpenRouterUsage{Requests: 2}, llmpolicy.OpenRouterUsage{Requests: 1}); got.ReturnedProviders != (llmpolicy.OpenRouterProviderCounts{}) {
		t.Fatalf("고정 모드: %+v", got)
	}
}
