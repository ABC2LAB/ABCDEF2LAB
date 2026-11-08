package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	actool "github.com/Autumn-27/norma/tool"
)

const e2HTTPPromptVersion = "e2-worker-prompt/v1"
const e2HTTPDiagnosticPrompt = `Work on the assigned intent only. First inspect available facts and select a
bounded diagnostic step that can distinguish the hypothesis from a normal control.
Use observed responses and trusted execution status to choose the next permitted
step. Record observations through record_fact when available; distinguish facts,
hypotheses, coverage limits and missing evidence. End with a concise Korean
summary of what was actually observed and what remains unverified.`

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
