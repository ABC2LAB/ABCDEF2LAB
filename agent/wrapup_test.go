package agent

import (
	"strings"
	"testing"
	"unicode"

	"github.com/Autumn-27/artex/config"
	"github.com/Autumn-27/norma/harness"
)

// #272의 실제 설정 해석을 거쳐 운영 훅을 배선하면 옛 en 값도 영어 꼬리를 붙이지 않는다.
func TestWrapupRuntimeWithKoreanOnlyConfiguration(t *testing.T) {
	for _, stored := range []string{"en", "ko", "zh", "es", "ja"} {
		t.Run(stored, func(t *testing.T) {
			t.Setenv("ARTEX_LANG", stored)
			ctx := WithRuntimeHooks(t.Context(), RuntimeHooks{OutputLanguage: config.Language})
			for _, key := range []string{"worker", "planner", "mainagent", "unknown-agent"} {
				settlement := wrapupSettlementForTask(ctx, key, nil, true)
				if settlement.Prompt != WrapupDefault(key) {
					t.Fatalf("%s: conflicting language directive appended", key)
				}
				if want := TaskTimeoutWrapupDefault(key); want != "" && settlement.PromptByReason[harness.ReasonTimeout] != want {
					t.Fatalf("%s timeout: conflicting language directive appended", key)
				}
			}
		})
	}
}

// TestWrapupPromptsAreEnglish 는 마무리 지시문이 모델이 읽는 영어로 유지되는지 고정한다. 사람에게 보이는 마지막
// 한 문장은 한국어로 쓰라고 지시해야 한다. DB 시드는 이 칸을 비워 두므로 이 상수들이 유일한 원천이다.
func TestWrapupPromptsAreEnglish(t *testing.T) {
	all := map[string]string{
		"settleWrapUpPrompt":        settleWrapUpPrompt,
		"plannerWrapUpDefault":      plannerWrapUpDefault,
		"mainAgentWrapUpDefault":    mainAgentWrapUpDefault,
		"genericWrapUpDefault":      genericWrapUpDefault,
		"workerTaskTimeoutDefault":  workerTaskTimeoutDefault,
		"plannerTaskTimeoutDefault": plannerTaskTimeoutDefault,
	}
	for name, p := range all {
		if strings.IndexFunc(p, func(r rune) bool { return unicode.Is(unicode.Hangul, r) || unicode.Is(unicode.Han, r) }) >= 0 {
			t.Errorf("%s: 모델이 읽는 지시문에 영어 아닌 글자가 있다: %q", name, p)
		}
	}

	// 도구 이름은 식별자이므로 번역하지 않고 그대로 보존되어야 한다.
	// worker 계열(per-run·task-timeout)은 record_fact 로 결론을, report_finding 으로
	// 취약점을 쓰고, 마지막에 한 문장 순수 텍스트로 요약하라는 지시를 유지한다.
	mustContain := func(name, p string, subs ...string) {
		for _, s := range subs {
			if !strings.Contains(p, s) {
				t.Errorf("%s: 지시 의미 %q 가 보존되어야 하는데 없다", name, s)
			}
		}
	}
	mustContain("settleWrapUpPrompt", settleWrapUpPrompt,
		"insert_assets", "record_fact", "report_finding", "one sentence of plain text", "in Korean")
	mustContain("workerTaskTimeoutDefault", workerTaskTimeoutDefault,
		"insert_assets", "record_fact", "report_finding", "one sentence of plain text", "in Korean")
	mustContain("genericWrapUpDefault", genericWrapUpDefault, "one sentence of plain text", "in Korean")
	mustContain("mainAgentWrapUpDefault", mainAgentWrapUpDefault, "one sentence of plain text", "in Korean")
	// planner 는 요약 문장을 내지 않고(판정만 하고 종료) 의도·목표·할일 도구를 유지한다.
	mustContain("plannerWrapUpDefault", plannerWrapUpDefault, "add_intent", "prove_goal", "TodoWrite")
	mustContain("plannerTaskTimeoutDefault", plannerTaskTimeoutDefault, "prove_goal")
}

// per-run 과 task-timeout 은 의미가 달라야 한다(특히 planner): per-run 은 "이번 라운드만
// 끝난다"이고 task-timeout 은 "작업 전체가 끝난다"이다. 상수 매핑이 바뀌어 섞이면 안 된다.
func TestWrapupDefaultsRouting(t *testing.T) {
	if WrapupDefault("worker") != settleWrapUpPrompt {
		t.Error("worker per-run 기본값이 settleWrapUpPrompt 가 아니다")
	}
	if WrapupDefault("planner") != plannerWrapUpDefault {
		t.Error("planner per-run 기본값이 plannerWrapUpDefault 가 아니다")
	}
	if WrapupDefault("mainagent") != mainAgentWrapUpDefault {
		t.Error("mainagent per-run 기본값이 mainAgentWrapUpDefault 가 아니다")
	}
	// 미등록 키(커스텀 에이전트)는 generic 으로 떨어진다.
	if WrapupDefault("unknown-agent") != genericWrapUpDefault {
		t.Error("미등록 키가 genericWrapUpDefault 로 떨어지지 않는다")
	}
	// task-timeout 은 worker/planner 에만 있고, 그 외는 빈 문자열(호출부가 per-run 으로 회귀).
	if TaskTimeoutWrapupDefault("worker") != workerTaskTimeoutDefault {
		t.Error("worker task-timeout 기본값이 workerTaskTimeoutDefault 가 아니다")
	}
	if TaskTimeoutWrapupDefault("planner") != plannerTaskTimeoutDefault {
		t.Error("planner task-timeout 기본값이 plannerTaskTimeoutDefault 가 아니다")
	}
	if TaskTimeoutWrapupDefault("mainagent") != "" {
		t.Error("mainagent 은 task-timeout 문구가 없어야 한다(빈 문자열)")
	}
	// per-run 과 task-timeout 문구가 동일하면 의미 구분이 사라진 것이다.
	if workerTaskTimeoutDefault == settleWrapUpPrompt {
		t.Error("worker 의 per-run 과 task-timeout 문구가 동일하다")
	}
	if plannerTaskTimeoutDefault == plannerWrapUpDefault {
		t.Error("planner 의 per-run 과 task-timeout 문구가 동일하다")
	}
}
