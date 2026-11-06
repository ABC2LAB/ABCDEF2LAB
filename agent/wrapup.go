package agent

import (
	"context"
	"strings"

	"github.com/Autumn-27/norma/harness"
)

// 收尾提示词(wrap-up / settlement prompt):当 agent 因【步数耗尽(MaxTurns)】或
// 【超时(run_seconds/MaxDuration)】被终止时,SDK 的 settlement 阶段会注入这段提示,
// 让 agent 先把已识别但未写回的内容落库、再输出一句总结,避免烂尾。
//
// 每个 agent 的收尾提示词可在后台按需覆盖(存 agents.wrapup_prompt),留空则用这里的
// 内置默认。仅【提示词正文】可编辑;禁用哪些工具、收尾自身给几轮预算属代码固定策略。

// WrapupOverride, if set, returns the stored wrap-up prompt for an agent key and
// whether a non-empty one exists. Wired by the server to the agents table (like
// PromptOverride for system prompts). nil / empty → the built-in default is used.

// WrapupMaxTurnsOverride, if set, returns the admin-configured turn budget for the
// wrap-up phase of an agent and whether a positive one exists. Wired to the agents
// table. nil / ≤0 → the built-in per-agent default (wrapupTurnDefaults) is used.

// 内置默认收尾提示词,按 agent key 索引。worker 复用历史上硬编码的 settleWrapUpPrompt
// (定义在 worker.go),planner/mainagent 各有一版;未命中的(自定义 agent)走通用兜底。
var wrapupDefaults = map[string]string{
	"worker":    settleWrapUpPrompt,
	"planner":   plannerWrapUpDefault,
	"mainagent": mainAgentWrapUpDefault,
}

// wrapupTurnDefaults: 各 agent 收尾阶段【自身】的轮数预算内置默认(可被后台 >0 覆盖)。
// 均给 10 轮,保证收尾阶段有足够步数落库。未命中走 genericWrapupTurns。
var wrapupTurnDefaults = map[string]int{
	"worker":    10,
	"planner":   10,
	"mainagent": 10,
}

const genericWrapupTurns = 10

const plannerWrapUpDefault = "The step budget of this planning round is about to run out. Only [this round] ends, though: the system will wake you again as the situation changes so you can keep planning. The task itself isn't ending, so there is no need to wrap up the whole plan here. Act on the conclusions you have clearly reached this round so the round isn't wasted, but [don't make up intents just to wrap up] (zero intents in a round is a perfectly normal result). (1) If you have already judged exploration directions that [should be dispatched right now], submit them together in one add_intent call (don't sit on what you have decided; send it now). (2) Mark goals already proven met by a finding or fact with prove_goal (don't miss any). (3) If you have identified a serial exploit chain that must be split into steps, record it with TodoWrite so you can keep dispatching it when you wake next time. When you are done, end this round right away, and don't output summary text."

const mainAgentWrapUpDefault = "The step budget is about to run out and this interaction is about to end. Don't start any new exploration or operation. Summarize the current progress, key conclusions and the recommended next step for the user in Korean, **in one sentence of plain text only**."

const genericWrapUpDefault = "This run is about to end because the budget is running out. First record results you finished but haven't saved, then summarize what you did and the key conclusions in Korean, **in one sentence of plain text only** (this sentence is shown as the result of this run)."

// WrapupDefault returns the built-in default wrap-up prompt for an agent key —
// used by the admin UI as the "restore default" value and empty-field placeholder.
func WrapupDefault(agentKey string) string {
	if d, ok := wrapupDefaults[agentKey]; ok {
		return d
	}
	return genericWrapUpDefault
}

// WrapupTurnsDefault returns the built-in wrap-up turn budget for an agent key —
// used by the admin UI as the "0 = default N" hint.
func WrapupTurnsDefault(agentKey string) int {
	if n, ok := wrapupTurnDefaults[agentKey]; ok {
		return n
	}
	return genericWrapupTurns
}

// resolveWrapup returns the effective wrap-up prompt: the DB override (if set and
// non-empty) over the built-in default.
func resolveWrapup(ctx context.Context, agentKey string) string {
	if h := RuntimeHooksFrom(ctx).WrapupOverride; h != nil {
		if t, ok := h(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return WrapupDefault(agentKey)
}

// resolveWrapupTurns returns the effective wrap-up turn budget: a positive DB
// override over the built-in per-agent default.
func resolveWrapupTurns(ctx context.Context, agentKey string) int {
	if h := RuntimeHooksFrom(ctx).WrapupMaxTurnsOverride; h != nil {
		if v, ok := h(agentKey); ok && v > 0 {
			return v
		}
	}
	return WrapupTurnsDefault(agentKey)
}

// wrapupSettlement builds the settlement config for an agent's run. Prompt and the
// turn budget are admin-editable per agent; disabled tools are code-owned policy so
// a user can't edit away the "stop probing" guardrail. Resolved fresh each run
// (reads DB live), so edits apply on the next run without a restart.
func wrapupSettlement(ctx context.Context, agentKey string, disabledTools []string) *harness.Settlement {
	return &harness.Settlement{
		Prompt:        withWrapupLang(ctx, resolveWrapup(ctx, agentKey)),
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(ctx, agentKey),
	}
}

// withWrapupLang 은 마무리 프롬프트 뒤에 출력 언어 지시(langDirective)를 덧붙인다.
// 마무리 프롬프트는 시스템 프롬프트와 달리 langDirective 꼬리가 자동으로 붙지 않으므로,
// 이렇게 직접 붙여야 사용자에게 보이는 마지막 한 문장 요약이 선택한 표시 언어를 따른다.
// 마무리 산문 자체는 모델이 읽는 운영 지시문(영어)이고, 이 꼬리가 최고 우선순위로
// 사용자 노출 텍스트의 언어만 강제한다. 요약을 내지 않는 planner 계열에는 무해하다.
// 기본 언어 ko 에서는 꼬리를 붙이지 않는다. 시스템 프롬프트의 langDirective 가 이미
// 한국어 출력을 강제하고 있어, 기본 경로의 모델 입력을 이전 버전과 같게 둔다(벤치마크 드리프트 방지).
func withWrapupLang(ctx context.Context, prompt string) string {
	if strings.TrimSpace(prompt) == "" {
		return prompt
	}
	lang := resolveOutputLang(ctx)
	if lang == "ko" {
		return prompt
	}
	return prompt + langDirective(lang)
}

// ---------- 任务级超时收尾词（见 docs/任务级超时与收尾设计.md）----------
//
// 与 per-run 收尾词是【两套】：per-run 是"你这一次 run 的预算用完了"；任务超时是
// "整个任务到点、即将结束"。语义常相反（尤其 planner：per-run 说"别停继续规划"，
// 任务超时说"到点停止规划、做最后判定"）。只给 worker/planner 配置。

// WrapupTaskTimeoutOverride / …TurnsOverride：任务超时收尾词与轮数的 DB 覆盖
// （wire 到 agents.task_timeout_wrapup_prompt / _max_turns，仅 worker/planner）。
var taskTimeoutWrapupDefaults = map[string]string{
	"worker":  workerTaskTimeoutDefault,
	"planner": plannerTaskTimeoutDefault,
}

const workerTaskTimeoutDefault = "**The whole task has reached its time limit and is about to end** (this is the end of the whole exploration, not the budget of this run). This is the last chance. (1) Save [everything] you have identified but not yet recorded: new assets with insert_assets, exploration conclusions and facts with record_fact, and confirmed vulnerabilities with report_finding. (2) Don't start any new command or probe. (3) **At the very end, in one sentence of plain text only**, summarize the key conclusion of this intent in Korean."

const plannerTaskTimeoutDefault = "**The whole task has reached its time limit and is about to end** (the whole task ends, not just this round). Make the final goal judgment based on [all] facts and findings so far. Mark goals proven met by evidence with prove_goal (don't miss any). **Don't create any new intent** (intents dispatched now won't run anymore). End right after the judgment, and don't output summary text."

// TaskTimeoutWrapupDefault 返回某 agent 的任务超时内置默认收尾词（供后台占位/恢复默认）。
func TaskTimeoutWrapupDefault(agentKey string) string {
	return taskTimeoutWrapupDefaults[agentKey] // 未配置(mainagent/chat)返回空串
}

// resolveTaskTimeoutWrapup：DB 覆盖(非空) > 内置默认。空串表示该 agent 无任务超时词
// （非 worker/planner），此时调用方应回退 per-run 词。
func resolveTaskTimeoutWrapup(ctx context.Context, agentKey string) string {
	if h := RuntimeHooksFrom(ctx).WrapupTaskTimeoutOverride; h != nil {
		if t, ok := h(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return TaskTimeoutWrapupDefault(agentKey)
}

func resolveTaskTimeoutTurns(ctx context.Context, agentKey string) int {
	if h := RuntimeHooksFrom(ctx).WrapupTaskTimeoutTurnsOverride; h != nil {
		if v, ok := h(agentKey); ok && v > 0 {
			return v
		}
	}
	return resolveWrapupTurns(ctx, agentKey) // 默认沿用 per-run 轮数
}

// wrapupSettlementForTask builds settlement for a worker/planner run that is aware
// of the task deadline. See §5 of the design doc:
//   - clamped=true  → 本次 run 被任务 deadline 夹逼：因 Timeout 收尾=任务到点→任务超时词；
//     因 MaxTurns 收尾=夹逼窗口内步数先耗尽、任务还剩几分钟→回落 per-run 词。
//   - clamped=false → 任务还早：两种 reason 都用 per-run 词（即退化为 wrapupSettlement）。
//
// 交给 harness 的 PromptByReason 在收尾时按【实际】reason 现场挑，无 build 时错配。
func wrapupSettlementForTask(ctx context.Context, agentKey string, disabledTools []string, clamped bool) *harness.Settlement {
	perRun := withWrapupLang(ctx, resolveWrapup(ctx, agentKey))
	st := &harness.Settlement{
		Prompt:        perRun, // 兜底(也是非 clamped 时两种 reason 的取值)
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(ctx, agentKey),
	}
	if clamped {
		if tt := resolveTaskTimeoutWrapup(ctx, agentKey); tt != "" {
			st.PromptByReason = map[harness.TerminalReason]string{
				harness.ReasonTimeout:  withWrapupLang(ctx, tt), // 任务到点
				harness.ReasonMaxTurns: perRun,                  // 步数先耗尽、任务还剩时间
			}
			st.MaxTurns = resolveTaskTimeoutTurns(ctx, agentKey)
		}
	}
	return st
}
