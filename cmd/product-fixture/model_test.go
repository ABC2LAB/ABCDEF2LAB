package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fixtureCall은 합성 모델에 도구 하나만 가진 요청을 보내고, 돌려준 도구 호출 이름을 돌려준다(없으면 "").
// followUp이면 같은 작업의 이어지는 요청처럼 앞 도구 결과(tool 메시지)를 대화에 싣는다.
func fixtureCall(t *testing.T, h http.Handler, tool string, followUp bool) string {
	t.Helper()
	messages := []any{map[string]any{"role": "user", "content": "Inspect the approved HTTPS target https://juice.test:9443"}}
	if followUp {
		messages = append(messages, map[string]any{"role": "tool", "content": "ok"})
	}
	body, _ := json.Marshal(map[string]any{
		"model":    "fixture-model",
		"tools":    []any{map[string]any{"function": map[string]any{"name": tool}}},
		"messages": messages,
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
		if got := fixtureCall(t, h, "set_goals", false); got != "set_goals" {
			t.Fatalf("작업 %d 목표 분해: %q", task, got)
		}
		// 같은 작업의 이어지는 목표 분해 요청(앞 도구 결과를 실음)은 목표를 두 번 내지 않는다.
		if got := fixtureCall(t, h, "set_goals", true); got != "" {
			t.Fatalf("작업 %d 이어지는 목표 분해 요청이 목표를 또 냄: %q", task, got)
		}
		if got := fixtureCall(t, h, "add_intent", false); got != "add_intent" {
			t.Fatalf("작업 %d 의도: %q", task, got)
		}
		if got := fixtureCall(t, h, "add_intent", true); got != "" {
			t.Fatalf("작업 %d 같은 작업에서 의도를 또 냄: %q", task, got)
		}
		if got := fixtureCall(t, h, "Bash", true); got != "Bash" {
			t.Fatalf("작업 %d Worker 첫 명령: %q", task, got)
		}
	}
}

// 첫 점검이 목표만 받고(Planner·Worker 전에) 끝나도, 다음 점검은 목표·의도를 처음부터 낸다. 새 점검의 목표 분해는
// 도구 결과가 없는 새 대화로 오므로 앞 점검의 진행 정도와 관계없이 구분된다.
func TestFixtureModelNewTaskAfterGoalsOnly(t *testing.T) {
	h := model("https://juice.test:9443")
	if got := fixtureCall(t, h, "set_goals", false); got != "set_goals" {
		t.Fatalf("첫 점검 목표 분해: %q", got)
	}
	if got := fixtureCall(t, h, "set_goals", false); got != "set_goals" {
		t.Fatalf("두 번째 점검 목표 분해: %q", got)
	}
	if got := fixtureCall(t, h, "add_intent", false); got != "add_intent" {
		t.Fatalf("두 번째 점검 의도: %q", got)
	}
}
