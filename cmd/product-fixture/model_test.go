package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fixtureCall은 합성 모델에 도구 하나만 가진 요청을 보내고, 돌려준 도구 호출 이름을 돌려준다(없으면 "").
func fixtureCall(t *testing.T, h http.Handler, tool string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"model":    "fixture-model",
		"tools":    []any{map[string]any{"function": map[string]any{"name": tool}}},
		"messages": []any{map[string]any{"role": "user", "content": "Inspect the approved HTTPS target https://juice.test:9443"}},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s 요청: %d %s", tool, rec.Code, rec.Body.String())
	}
	var out struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Choices) != 1 {
		t.Fatalf("응답 모양: %v %s", err, rec.Body.String())
	}
	if calls := out.Choices[0].Message.ToolCalls; len(calls) == 1 {
		return calls[0].Function.Name
	}
	return ""
}

// 같은 모델 프로세스·같은 대상으로 점검(작업)을 두 번 하면, 두 번째 작업에서도 목표·의도·Worker 명령을 처음부터 낸다
// (리허설 F11: 프로세스 전체로 세어 두 번째 점검의 의도가 0이었다). 한 작업 안에서는 의도를 한 번만 낸다.
func TestFixtureModelIntentsPerTask(t *testing.T) {
	h := model("https://juice.test:9443")
	for task := 1; task <= 2; task++ {
		if got := fixtureCall(t, h, "set_goals"); got != "set_goals" {
			t.Fatalf("작업 %d 목표 분해: %q", task, got)
		}
		// 계획 전 목표 분해 재요청은 같은 작업이다(목표를 두 번 내지 않음).
		if got := fixtureCall(t, h, "set_goals"); got != "" {
			t.Fatalf("작업 %d 목표 분해 재요청이 목표를 또 냄: %q", task, got)
		}
		if got := fixtureCall(t, h, "add_intent"); got != "add_intent" {
			t.Fatalf("작업 %d 의도: %q", task, got)
		}
		if got := fixtureCall(t, h, "add_intent"); got != "" {
			t.Fatalf("작업 %d 같은 작업에서 의도를 또 냄: %q", task, got)
		}
		if got := fixtureCall(t, h, "Bash"); got != "Bash" {
			t.Fatalf("작업 %d Worker 첫 명령: %q", task, got)
		}
	}
}
