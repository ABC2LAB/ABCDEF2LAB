package agent

import "testing"

// TestBuiltinPromptsHaveNoHan 은 모델이 읽는 기본 프롬프트와 코드 꼬리말에 한자가 다시 들어오지
// 않게 막는다. 표시 언어를 고르는 꼬리말은 언어마다 확인한다.
func TestBuiltinPromptsHaveNoHan(t *testing.T) {
	prompts := map[string]string{
		"goals_scope_tail": goalsScopeTail,
		"assistant":        DefaultAssistantPrompt,
		"reporter":         ReporterDefaultPrompt,
		"artifact":         artifactSpec("/data"),
		"worker_artifact":  workerArtifactSpec("/data"),
		"chat_workdir":     chatWorkDirSpec("/data"),
	}
	for key, body := range BuiltinPromptSeeds() {
		prompts[key] = body
	}
	for _, code := range SupportedOutputLangs {
		prompts["lang_"+code] = langDirective(code)
	}
	for name, body := range prompts {
		if body == "" {
			t.Fatalf("%s: empty prompt", name)
		}
		if hasHan(body) {
			t.Fatalf("%s: prompt contains Han characters: %q", name, body)
		}
	}
}
