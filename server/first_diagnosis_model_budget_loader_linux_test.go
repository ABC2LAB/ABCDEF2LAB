//go:build linux

package server

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/norma/llm"
)

// 소스 해시만으로는 두 실제 loader의 분기 누락을 찾을 수 없어 격리 PG 설정으로 검증한다.
// provider·engine·권위는 구성하지 않으며 endpoint는 우발 송신을 세는 소유 loopback이다.
func TestFirstDiagnosisModelBudgetLoaderOwnedPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	installer := &pgdb.DB{DB: f.Installer}
	var networkCalls atomic.Int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		networkCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer endpoint.Close()

	if installer.SetLLMRetryPolicy(pgdb.LLMRetryPolicy{
		Connect: pgdb.RetryRule{Attempts: 8, IntervalMS: 100},
		Empty:   pgdb.RetryRule{Attempts: 5, IntervalMS: 250},
		Stream:  pgdb.RetryRule{Attempts: 9, IntervalMS: 700},
	}) != nil {
		t.Fatal("소유 전역 retry 설정 실패")
	}
	configuredRetry := agent.RetryConfig{
		ConnectAttempts: 4, ConnectInterval: 125 * time.Millisecond,
		EmptyAttempts: 5, EmptyInterval: 350 * time.Millisecond,
		StreamAttempts: 6, StreamInterval: 700 * time.Millisecond,
	}
	disabledRetry := agent.RetryConfig{ConnectAttempts: -1, EmptyAttempts: -1, StreamAttempts: -1}
	for _, scenario := range []struct {
		name, key string
		missing   bool
	}{
		{name: "keyed", key: "synthetic-loader-key"},
		{name: "without_key"},
		{name: "missing", missing: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			profile := &pgdb.LLMProfile{
				Name: "owned-loader-" + scenario.name, Format: "openai",
				Model: "owned-loader-model", BaseURL: endpoint.URL + "/v1",
				APIKey: scenario.key, Proxy: endpoint.URL,
				RatePerSecond: 3.5, RatePerMinute: 71, ContextWindowK: 96,
				ThinkingType: "enabled", ReasoningEffort: "high", Streaming: false,
				MaxTokens: 321, MaxTokensField: "max_completion_tokens",
				SessionHeaderKey: "X-Owned-Session",
				Retry: pgdb.RetryOverride{
					Connect: pgdb.RetryRule{Attempts: 4, IntervalMS: 125},
					Empty:   pgdb.RetryRule{IntervalMS: 350},
					Stream:  pgdb.RetryRule{Attempts: 6},
				},
			}
			var id int64
			if scenario.missing {
				if _, err := installer.ExecContext(f.Context, "UPDATE llm_profiles SET is_default=false"); err != nil {
					t.Fatal("소유 활성 profile 제거 실패")
				}
				id = -1
			} else {
				var err error
				id, err = installer.SaveProfile(profile)
				if err != nil || id <= 0 || installer.SetActiveProfile(id) != nil {
					t.Fatal("소유 loader profile 설정 실패")
				}
			}
			expected := agent.Config{
				Format: llm.FormatOpenAI, BaseURL: profile.BaseURL, APIKey: profile.APIKey,
				Model: profile.Model, Proxy: profile.Proxy,
				RatePerSecond: profile.RatePerSecond, RatePerMinute: profile.RatePerMinute,
				ContextWindowK: profile.ContextWindowK, ThinkingType: profile.ThinkingType,
				ReasoningEffort: profile.ReasoningEffort, Stream: profile.Streaming,
				MaxTokens: profile.MaxTokens, MaxTokensField: profile.MaxTokensField,
				SessionHeaderKey: profile.SessionHeaderKey, Retry: configuredRetry,
			}
			wantOK := !scenario.missing && scenario.key != ""
			if scenario.missing {
				expected = agent.Config{}
			}
			for _, mode := range []string{"legacy", "openrouter_only", "budget_only"} {
				t.Run(mode, func(t *testing.T) {
					for _, loader := range []string{"loadLLMConfig", "loadProfileConfig"} {
						t.Run(loader, func(t *testing.T) {
							// 이 분기는 보호 객체의 존재만 판정한다. 빈 객체로 실행 권위를 만들지 않는다.
							s := &Server{m: &Manager{pg: &pgdb.DB{DB: f.Runtime}}, llmProf: "unchanged-profile"}
							switch mode {
							case "openrouter_only":
								s.openRouterPolicy = &llmpolicy.OpenRouterPolicy{}
							case "budget_only":
								s.firstModelBudget = &FirstDiagnosisModelBudget{}
							}
							want := expected
							if mode != "legacy" && !scenario.missing {
								want.Retry = disabledRetry
							}
							var got agent.Config
							var ok bool
							if loader == "loadLLMConfig" {
								got, ok = s.loadLLMConfig()
							} else {
								got, ok = s.loadProfileConfig(id)
							}
							if ok != wantOK {
								t.Fatal("retry 경계가 loader ok 의미를 변경함")
							}
							// 전체 Config를 비교하되 실패 메시지에 key·endpoint 원문을 출력하지 않는다.
							if got != want {
								t.Fatal("loader Config 또는 retry 분기가 예상과 다름")
							}
							wantName := "unchanged-profile"
							if loader == "loadLLMConfig" && wantOK {
								wantName = profile.Name
							}
							if s.llmProf != wantName {
								t.Fatal("활성 profile 이름 갱신 의미가 변경됨")
							}
						})
					}
				})
			}
		})
	}
	var tasks int
	if f.Runtime.QueryRowContext(f.Context, "SELECT count(*) FROM tasks").Scan(&tasks) != nil || tasks != 0 {
		t.Fatal("loader 조회가 실제 진단 task를 생성함")
	}
	if networkCalls.Load() != 0 {
		t.Fatal("loader 조회가 모델 또는 대상 endpoint로 송신함")
	}
}
