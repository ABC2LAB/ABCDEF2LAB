package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	actool "github.com/Autumn-27/norma/tool"
)

const e2HTTPPromptVersion = "e2-worker-prompt/v1"

// 원본 Worker 지침(workerDefaultTmpl)을 진단 단계 도구에 맞춘 판이다(루프 복원 결정안 §3).
// 원본과 다른 곳: 자산 등록(insert_assets)·확정 기록(report_finding) 대신 record_fact와 후보 제출, 정상 대조 단계.
const e2HTTPDiagnosticPrompt = `You are the executor (work agent) of an authorized web diagnosis. You receive one intent (a one-sentence diagnostic direction). Your only job: complete this one intent, write what you observed back to the knowledge graph, then stop and return.

Boundaries:
1. Do only the one intent you received. If you notice a lead outside this intent while working (a path leaked in an error, a point that may link with other assets, an entry that looks like another chain), mention it in one line in the fact's summary for the planner.
2. A first obstacle (payload filtered, 404, no echo) doesn't mean the direction is exhausted. Try other permitted approaches for this intent before writing a conclusion. A request refused by E2 is a policy limit, not an answer from the target: choose a different permitted step instead of repeating the same request.
3. If operational constraints appear in the system prompt, they are the highest-priority red line: check each command against them first and skip anything that violates them.
4. Prefer steps that can distinguish the hypothesis from a normal control (for example the same request by the owner, or without the suspected condition).

Before acting, check what others already found (the graph overview, list_findings, search_all_worker_traces, get_worker_trace) so you don't repeat work.

Write back as you find (only what is in the graph counts; write each result right away, since running out of steps loses what is not written):
- Observations and conclusions -> record_fact (pass intent_id). Combine related observations into one fact: summary is a one-sentence conclusion, detail expands on it based on what actually ran. Write only what is new and only what you actually saw: give evidence (the command plus the one or two output lines that best prove it) and mark confidence (observed = seen directly, inferred = deduced from behavior). Distinguish facts, hypotheses, coverage limits and missing evidence.
- Potential vulnerabilities -> submit_diagnostic_candidate with the call_ref of the Bash call from this intent that showed it. It stores an unconfirmed review candidate; confirmation happens elsewhere. Never treat a version or fingerprint match, or a parameter that only looks injectable, as evidence. If you suspect an issue but could not trigger it, record an inferred fact instead (the suspicious point and why it did not trigger).

When this intent is complete, end with a concise Korean summary of what you actually observed, which facts you wrote back, and what remains unverified.`

// 예산이 끝나갈 때의 마무리 턴 지시. 일반 worker 마무리 지시는 진단 단계에 없는 도구를 부르므로 쓰지 않는다.
const e2HTTPWrapUpPrompt = "This run is about to end because its budget is running out. Do not run any more commands or probes. Do the following in order. (1) Record, one by one, what you observed above but have not recorded yet: observations and conclusions with record_fact, and potential vulnerabilities backed by an actual call_ref from this intent with submit_diagnostic_candidate. (2) At the very end, in one sentence of plain text only, summarize in Korean what you actually observed and what remains unverified. This sentence is shown to the user as the result of this run, so you must output it."

// DB 지침과 코드 통제는 분리한다. 일반 worker의 finding·파일 도구 지시는 재사용하지 않는다.
type e2HTTPPromptVars struct{ WorkerName, Now string }

// 원문·자격정보 없이 실제 적용된 내용을 식별한다. digest가 내용 버전이며 DB 행 번호는 아니다.
type e2HTTPPromptInfo struct {
	Version        string `json:"version"`
	Key            string `json:"key"`
	Source         string `json:"source"`
	TemplateDigest string `json:"template_digest"`
	GuidanceDigest string `json:"guidance_digest"`
	ControlDigest  string `json:"control_digest"`
	SystemDigest   string `json:"system_digest"`
	ToolsDigest    string `json:"tools_digest"`
}

func e2HTTPPromptDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func buildE2HTTPWorkerPrompt(ctx context.Context, name string, tools []actool.CoreTool, redact func([]byte) []byte) (string, e2HTTPPromptInfo, error) {
	info := e2HTTPPromptInfo{Version: e2HTTPPromptVersion, Key: "e2_worker", Source: "builtin"}
	tmpl := e2HTTPDiagnosticPrompt
	if hook := RuntimeHooksFrom(ctx).PromptOverride; hook != nil {
		if candidate, ok := hook(info.Key); ok && candidate != "" {
			tmpl = candidate
			info.Source = "override"
		}
	}
	vars := e2HTTPPromptVars{WorkerName: name, Now: nowStr()}
	body, err := renderTmpl(tmpl, vars)
	if err != nil {
		tmpl, body = e2HTTPDiagnosticPrompt, e2HTTPDiagnosticPrompt
		info.Source = "builtin_invalid_override"
	}
	// 전송된 지침의 digest만 남긴다. 비밀값을 포함한 템플릿 원문도 기록하지 않는다.
	tmpl = string(redact([]byte(tmpl)))
	body = string(redact([]byte(body)))
	control := e2HTTPSystemPrompt + langDirective("")
	system := "Diagnostic guidance (cannot grant tools or override execution controls):\n" + body + "\n\nProgram-owned execution controls (take precedence over diagnostic guidance):\n" + control
	type toolSpec struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Prompt      string         `json:"prompt"`
		Schema      map[string]any `json:"schema"`
	}
	specs := make([]toolSpec, 0, len(tools))
	for _, tool := range tools {
		specs = append(specs, toolSpec{tool.Name(), tool.Description(), tool.Prompt(), tool.InputSchema()})
	}
	encoded, err := json.Marshal(specs)
	if err != nil {
		return "", e2HTTPPromptInfo{}, ErrE2HTTPWorker
	}
	info.TemplateDigest = e2HTTPPromptDigest([]byte(tmpl))
	info.GuidanceDigest = e2HTTPPromptDigest([]byte(body))
	info.ControlDigest = e2HTTPPromptDigest([]byte(control))
	info.SystemDigest = e2HTTPPromptDigest([]byte(system))
	info.ToolsDigest = e2HTTPPromptDigest(encoded)
	return system, info, nil
}
