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

// urlExternalRequestQuote는 요청 하나의 비용 예약이다. 입력 예약(요청 JSON 바이트 + framing 여유)과 출력 상한으로
// 견적한다(C28 결정 1). 호출당 입력 상한 전체로 한 견적보다 작아야 한다.
func urlExternalRequestQuote(t *testing.T, b *FirstDiagnosisModelBudget, req llm.CompletionRequest) int64 {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	quote, err := b.external.QuoteInput(int64(len(raw))+firstDiagnosisFramingTokens, int64(req.MaxTokens))
	full, fullErr := b.external.Quote(int64(req.MaxTokens))
	if err != nil || fullErr != nil || quote >= full {
		t.Fatal("request-size quote is not below the full-context quote")
	}
	return quote
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
	quote := urlExternalRequestQuote(t, b, req)
	if len(persisted) != 1 || persisted[0].ReservedCostUSDMicros != quote || persisted[0].UsageKnown || b.external.Snapshot().Requests != 0 {
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
	if b.before(context.Background(), cfg, &req) == nil || b.external.Snapshot().Requests != 0 || b.Snapshot().ReservedCostUSDMicros != urlExternalRequestQuote(t, b, req) {
		t.Fatal("unpersisted reservation reached transport")
	}
	b, _, cfg = urlExternalBudget(t, func(diagnosisprep.ModelUsage) error { return nil })
	b.limits.CostUSDMicros = urlExternalRequestQuote(t, b, req) - 1
	if b.before(context.Background(), cfg, &req) == nil || b.Snapshot().Requests != 0 || b.external.Snapshot().Requests != 0 {
		t.Fatal("insufficient USD reserved")
	}
}

func TestFirstDiagnosisOpenRouterModelBudgetUSDOnlyBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		offset  int64
		allowed bool
	}{{"exact", 0, true}, {"one_micro_short", -1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			var writes int
			b, _, cfg := urlExternalBudget(t, func(diagnosisprep.ModelUsage) error { writes++; return nil })
			b.limits.CostUSDMicros = urlExternalRequestQuote(t, b, llm.CompletionRequest{MaxTokens: 100}) + tc.offset
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
				if writes != 1 || u.Requests != 1 || u.ReservedCostUSDMicros != urlExternalRequestQuote(t, b, req) {
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

// 연속 일시 오류 상한(C06)만으로 잠긴 승인은 회계를 의심할 이유가 아니다. 잠그게 만든 실패 호출은 예약량으로 닫고(C05)
// 사용량은 계속 안다. 잠긴 뒤 새 호출은 예약 없이 거절한다. 다른 원인의 막힘은 지금처럼 회계 막힘이다
// (2026-11-14 점검 회차: 429 연속으로 잠긴 뒤 실행이 model_usage_unknown으로 끝남).
func TestFirstDiagnosisOpenRouterBudgetTransientLockKeepsUsageKnown(t *testing.T) {
	for _, transientOnly := range []bool{true, false} {
		b, _, cfg := urlExternalBudget(t, func(diagnosisprep.ModelUsage) error { return nil })
		req := llm.CompletionRequest{MaxTokens: 100}
		call, err := b.reserve(context.Background(), cfg, &req)
		if err != nil {
			t.Fatal(err)
		}
		r := llmpolicy.OpenRouterCallResult{Reserved: true, ReservedInputTokens: 5000, ReservedOutputTokens: 100, ReservedCostMicros: 5100}
		call.result = func() llmpolicy.OpenRouterCallResult { return r }
		b.externalUsage = func() llmpolicy.OpenRouterUsage {
			u := b.externalBase
			u.Requests++
			u.ReservedInputTokens += r.ReservedInputTokens
			u.ReservedOutputTokens += r.ReservedOutputTokens
			u.ReservedCostMicros += r.ReservedCostMicros
			u.Errors++
			u.Blocked, u.TransientLimited = true, transientOnly
			return u
		}
		if err := b.settle(call, llm.Usage{}, errors.New("synthetic 429")); err == nil {
			t.Fatal("failed call reported success")
		}
		u := b.Snapshot()
		if !transientOnly {
			if !b.blocked || u.UsageKnown {
				t.Fatalf("non-transient external block accepted as known: %+v", u)
			}
			continue
		}
		if b.blocked || !u.UsageKnown || u.UnknownUsageCalls != 1 || u.CostUSDMicros != call.cost {
			t.Fatalf("transient lock closed as unknown accounting: %+v blocked=%v", u, b.blocked)
		}
		if _, err := b.reserve(context.Background(), cfg, &req); err == nil {
			t.Fatal("new call reserved after the transient lock")
		}
		if after := b.Snapshot(); after.Requests != u.Requests || !after.UsageKnown || b.blocked {
			t.Fatalf("refused call changed accounting: %+v blocked=%v", after, b.blocked)
		}
	}
}

// 보내지 않은 호출(transport 예약 전 거부)과 제공자가 처리하지 않았다고 확인한 429는 사용량 0으로 닫는다. 사용량을 모르는
// 호출로 세지 않고 예산을 깎지 않는다. 처리 여부를 모르는 5xx는 지금처럼 예약량으로 센다(C05 보완, 2026-11-14 실측).
func TestFirstDiagnosisOpenRouterBudgetUnbilledFailuresCostNothing(t *testing.T) {
	reservedResult := llmpolicy.OpenRouterCallResult{Reserved: true, ReservedInputTokens: 5000, ReservedOutputTokens: 100, ReservedCostMicros: 5100}
	rateLimited := reservedResult
	rateLimited.NotBilled = true
	for name, tc := range map[string]struct {
		result   llmpolicy.OpenRouterCallResult
		usage    llm.Usage
		blocked  bool
		charged  bool
		accounts bool // 회계가 막히지 않고 사용량을 아는지
		success  bool // SDK가 성공으로 돌려줌
	}{
		// SDK는 성공인데 transport 칸에 기록이 없으면 칸 없이 나간 응답이라 회계 막힘이다(청구 없음으로 닫지 않음).
		"success-without-transport": {result: llmpolicy.OpenRouterCallResult{}, success: true},
		"not-sent":                  {result: llmpolicy.OpenRouterCallResult{}, accounts: true},
		"rate-limited":              {result: rateLimited, accounts: true},
		"server-error":              {result: reservedResult, charged: true, accounts: true},
		// 사용량을 함께 보고한 실패는 청구 없음으로 닫지 않고 예약량으로 센다.
		"rate-limited-with-usage": {result: rateLimited, usage: llm.Usage{InputTokens: 10}, charged: true, accounts: true},
		// transport가 회계 원인으로 막혀 있으면 보내지 않은 호출도 지금처럼 막힘이다.
		"not-sent-accounting-blocked": {result: llmpolicy.OpenRouterCallResult{}, blocked: true},
	} {
		b, _, cfg := urlExternalBudget(t, func(diagnosisprep.ModelUsage) error { return nil })
		req := llm.CompletionRequest{MaxTokens: 100}
		call, err := b.reserve(context.Background(), cfg, &req)
		if err != nil {
			t.Fatal(err)
		}
		r := tc.result
		call.result = func() llmpolicy.OpenRouterCallResult { return r }
		blockedTransport := tc.blocked
		b.externalUsage = func() llmpolicy.OpenRouterUsage {
			u := b.externalBase
			u.Blocked = blockedTransport
			if r.Reserved {
				u.Requests++
				u.ReservedInputTokens += r.ReservedInputTokens
				u.ReservedOutputTokens += r.ReservedOutputTokens
				u.ReservedCostMicros += r.ReservedCostMicros
				u.Errors++
			}
			return u
		}
		var callErr error
		if !tc.success {
			callErr = errors.New("synthetic failed call")
		}
		if err := b.settle(call, tc.usage, callErr); err == nil {
			t.Fatalf("%s: unsettled call reported success", name)
		}
		u := b.Snapshot()
		if tc.accounts == (b.blocked || !u.UsageKnown) {
			t.Fatalf("%s: accounting state blocked=%v usage=%+v", name, b.blocked, u)
		}
		if !tc.accounts {
			continue
		}
		if tc.charged != (u.CostUSDMicros == call.cost && u.UnknownUsageCalls == 1) || !tc.charged && (u.CostUSDMicros != 0 || u.InputTokens != 0 || u.OutputTokens != 0 || u.UnknownUsageCalls != 0) {
			t.Fatalf("%s: charged=%v usage=%+v reserved cost=%d", name, tc.charged, u, call.cost)
		}
	}
}
