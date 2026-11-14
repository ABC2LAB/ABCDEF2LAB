package diagnosticlog

import "testing"

// 진단 기록의 모델 칸은 지금 모델(3.6)과 2026-11-14 전 기록의 모델(3.5)만 받는다.
func TestModelVocabularyKeepsRecordedModel(t *testing.T) {
	for model, ok := range map[string]bool{"qwen/qwen3.6-35b-a3b": true, "qwen/qwen3.5-35b-a3b": true, "qwen/qwen3.7-35b-a3b": false} {
		if allowed(models, model, true) != ok {
			t.Fatalf("%q: allowed=%v", model, !ok)
		}
	}
}
