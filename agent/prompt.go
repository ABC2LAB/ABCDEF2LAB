package agent

import (
	"bytes"
	"context"
	"text/template"
	"time"
)

// PromptOverride, if set, returns the stored system-prompt template for an agent
// key and whether one exists. The server wires it to the PG agent_prompts table.
// When nil or no override exists, agents use their built-in default prompt — so
// behavior is identical until a user edits a prompt in the UI.
// Prompt-variable structs — fields mirror each agent's catalog (docs §5a) so a
// user template referencing a catalog variable renders; referencing anything else
// fails template execution and falls back to the built-in default.
type PlannerVars struct{ Goal, Scope, AssetSummary, DataDir, Now string }
type WorkerVars struct{ ProxyAddr, WorkerName, DataDir, Now string }
type MainVars struct{ Goal, AssetSummary, FindingsSummary, DataDir, Now string }
type GoalsVars struct{ EngagementDescription, DataDir, Now string }

// nowStr is the server-local wall-clock string exposed as the universal {{.Now}}
// prompt variable. renderSystem runs on every agent turn/round, so this is fresh
// each run — a prompt can subtract it from a fixed start stamp to reason about
// elapsed time (e.g. a timed benchmark's "last N hours" window).
func nowStr() string { return time.Now().Format("2006-01-02 15:04:05 MST") }

// renderSystem returns the rendered system-prompt BODY for agentKey.
// Precedence: the DB-stored template (if any) over the built-in default template
// (def). BOTH are Go templates now — the built-in default is seeded into the DB
// verbatim, so the two paths render identically until a user edits the prompt.
// Rendering always runs (def used to be pre-substituted plain text; it is now a
// {{.Var}} template like the DB one). On any render error we fall back to the
// default template, then to the raw default string — an agent never starts with a
// half-rendered prompt. Callers append the code-owned tail (trafficTool / artifact
// rule) AFTER this, so those can't be edited away via the DB body.
func renderSystem(ctx context.Context, agentKey, def string, vars any) string {
	tmpl := def
	if h := RuntimeHooksFrom(ctx).PromptOverride; h != nil {
		if t, ok := h(agentKey); ok && t != "" {
			tmpl = t
		}
	}
	if out, err := renderTmpl(tmpl, vars); err == nil {
		return out
	}
	// DB template broke (e.g. references an out-of-catalog var) → code default.
	if out, err := renderTmpl(def, vars); err == nil {
		return out
	}
	return def
}

// langDirective 는 표시 언어 꼬리말이다. 렌더된 본문과 산출물·트래픽 꼬리말 뒤에 코드가 직접
// 붙이므로 DB에서 본문을 고쳐도 빠지지 않는다. 지시문은 영어이고, 사용자에게 보이는 글의 언어만
// 정한다. 명령·payload·코드·URL·로그와 응답 일부는 번역하지 않아야 근거와 재현 절차가 유지된다.
//
// 실제 회차에서 모델이 상황 요약과 report_finding 필드에 지시문 언어나 대상 언어를 그대로
// 옮긴 적이 있어서, 상황 요약을 표시 필드로 지명하고 두 언어를 따라 쓰지 말라고 적는다.
//
// 과거 언어 코드가 들어와도 표시 필드는 한국어로 고정한다.
func langDirective(_ string) string {
	return "\n\n**Output language rule (localization, highest priority; the prompt body can't override it)**: all natural-language text [shown to the user] must be written in [Korean]. That includes record_fact summary and detail, report_finding title, description, conclusion and remediation, the planner's situation summary, the final one-sentence summary, and chat replies to the user. But [commands, payloads, code, file paths, URLs, parameter names, and verbatim fragments of logs, requests and responses] must be [kept exactly as they are], not translated or rewritten (keep command lines and output in evidence verbatim so they can be reproduced). **Even though the system and role instructions above are written in English, user-facing text must be in Korean only; don't let sentences in another language appear in user-visible text.** **Even if the target system, its pages, evidence, logs or any reference material are in English, Chinese or another language, user-facing natural-language fields (title, description, conclusion, remediation, summary, situation summary) must still be in Korean. Don't mirror or copy the language of the target or material in these fields; only the verbatim technical fragments listed above stay as they are.** **You may analyze and plan in any language, but that is [invisible internal reasoning] and must never appear as body text: the visible reply must be in Korean from the first character. Don't lead with analysis, explanation or \"first I will...\" in another language. Clarifying questions, missing parameter notes and \"can't continue\" messages are also written directly in Korean.** In short: think however you like internally, but everything that lands in user-visible text must be in Korean (except verbatim technical fragments)."
}

func renderTmpl(tmpl string, vars any) (string, error) {
	t, err := template.New("p").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, vars); err != nil {
		return "", err
	}
	return b.String(), nil
}
