package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 로그인 합성 모델은 Worker 지침의 등록 계정 줄에서 자격을 읽어 세션 없는 신원 GET → 계정마다 로그인 POST → 받은 토큰으로
// 신원 GET 순서로 명령을 낸다. 토큰을 못 받으면 그 대화를 끝낸다.
func TestWorkflowLoginModelSteps(t *testing.T) {
	h := workflowLoginModel(workflowLoginConfig{loginPath: "/users/v1/login", identityPath: "/me", usernameField: "username",
		passwordField: "password", tokenField: "auth_token", header: "Authorization", prefix: "Bearer "})
	system := "Approved target https://vampi.test:9443 ...\n\nRegistered accounts for the approved target.\n" +
		`- vampi-b: username "name2", password "pass2"` + "\n" + `- vampi-a: username "name1", password "pa\"ss1"` + "\n"
	tools := []map[string]any{{"type": "function", "function": map[string]any{"name": "Bash"}}}
	step := func(results ...string) map[string]any {
		msgs := []map[string]any{{"role": "system", "content": system}, {"role": "user", "content": "go"}}
		for _, r := range results {
			msgs = append(msgs, map[string]any{"role": "tool", "content": r})
		}
		raw, _ := json.Marshal(map[string]any{"model": "m", "tools": tools, "messages": msgs})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(raw)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d %s", rec.Code, rec.Body)
		}
		var out struct {
			Choices []struct {
				Message struct {
					ToolCalls []struct {
						Function struct{ Arguments string } `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if len(out.Choices[0].Message.ToolCalls) == 0 {
			return nil
		}
		var args map[string]any
		_ = json.Unmarshal([]byte(out.Choices[0].Message.ToolCalls[0].Function.Arguments), &args)
		return args
	}
	cmd := func(a map[string]any) string { s, _ := a["command"].(string); return s }
	if c := cmd(step()); !strings.Contains(c, "https://vampi.test:9443/me") || strings.Contains(c, "Authorization") {
		t.Fatalf("세션 없는 신원 GET: %s", c)
	}
	if c := cmd(step("anonymous=401")); !strings.Contains(c, `"username":"name1"`) || !strings.Contains(c, `pa\"ss1`) || !strings.Contains(c, "/users/v1/login") {
		t.Fatalf("첫 계정(라벨 순) 로그인: %s", c)
	}
	login := `{"auth_token": "eyJhbGciOiJIUzI1NiJ9.abcdefgh.ijk", "message": "ok"}`
	if c := cmd(step("anonymous=401", login)); !strings.Contains(c, "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abcdefgh.ijk") {
		t.Fatalf("토큰 신원 GET: %s", c)
	}
	if c := cmd(step("anonymous=401", login, "me")); !strings.Contains(c, `"username":"name2"`) {
		t.Fatalf("둘째 계정 로그인: %s", c)
	}
	if a := step("anonymous=401", `{"message":"bad"}`); a != nil {
		t.Fatalf("토큰 없이 계속함: %v", a)
	}
	if a := step("anonymous=401", login, "me", `{"message":"bad"}`); a != nil {
		t.Fatalf("둘째 계정 토큰 없이 계속함: %v", a)
	}
	if a := step("anonymous=401", login, "me", login, "me"); a != nil {
		t.Fatalf("계정을 다 쓴 뒤 계속함: %v", a)
	}
}

// 계정을 다 쓴 Worker는 이 의도로 사실 하나를 남기고, Planner는 의도 하나를 낸 뒤 사실이 있으면 열린 목표를 증명한다.
func TestWorkflowLoginModelFinish(t *testing.T) {
	h := workflowLoginModel(workflowLoginConfig{loginPath: "/login", identityPath: "/me", usernameField: "username",
		passwordField: "password", tokenField: "auth_token", header: "Authorization", prefix: "Bearer "})
	system := "Target https://vampi.test:9443\n" + `- vampi-a: username "name1", password "pass11"` + "\n"
	send := func(tools []string, msgs []map[string]any) (string, map[string]any) {
		var ts []map[string]any
		for _, n := range tools {
			ts = append(ts, map[string]any{"type": "function", "function": map[string]any{"name": n}})
		}
		raw, _ := json.Marshal(map[string]any{"model": "m", "tools": ts, "messages": msgs})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(raw)))
		var out struct {
			Choices []struct {
				Message struct {
					ToolCalls []struct {
						Function struct{ Name, Arguments string } `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if len(out.Choices) == 0 || len(out.Choices[0].Message.ToolCalls) == 0 {
			return "", nil
		}
		var args map[string]any
		f := out.Choices[0].Message.ToolCalls[0].Function
		_ = json.Unmarshal([]byte(f.Arguments), &args)
		return f.Name, args
	}
	call := func(name string) map[string]any {
		return map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"function": map[string]any{"name": name}}}}
	}
	planner := []string{"add_intent", "list_goals", "list_facts", "prove_goal"}
	base := []map[string]any{{"role": "system", "content": system}}
	if name, _ := send([]string{"set_goals"}, base); name != "set_goals" {
		t.Fatalf("목표 %q", name)
	}
	for _, want := range []string{"wl-discovery", "", "wl-login", ""} {
		msgs := base
		if want == "" {
			msgs = append(append([]map[string]any{}, base...), call("add_intent"), map[string]any{"role": "tool", "content": "ok"})
		}
		name, args := send(planner, msgs)
		summary := ""
		if intents, ok := args["intents"].([]any); ok {
			summary, _ = intents[0].(map[string]any)["summary"].(string)
		}
		if want == "" && name != "" || want == "wl-discovery" && !strings.Contains(summary, "initial discovery") || want == "wl-login" && !strings.Contains(summary, "Log in") {
			t.Fatalf("의도 %s: %q %q", want, name, summary)
		}
	}
	if name, _ := send(planner, base); name != "" {
		t.Fatalf("사실 전 증명 시작 %q", name)
	}
	worker := append(append([]map[string]any{}, base...), map[string]any{"role": "user", "content": "Intent id: 7"})
	login := `{"auth_token": "eyJhbGciOiJIUzI1NiJ9.abcdefgh.ijk"}`
	for _, r := range []string{"anonymous=401", login, "me"} {
		worker = append(worker, call("Bash"), map[string]any{"role": "tool", "content": r})
	}
	name, args := send([]string{"Bash", "record_fact"}, worker)
	if name != "record_fact" || args["intent_id"] != float64(7) {
		t.Fatalf("사실 %q %v", name, args)
	}
	failed := append(append([]map[string]any{}, base...), map[string]any{"role": "user", "content": "Intent id: 8"})
	for _, r := range []string{"anonymous=401", `{"message":"bad"}`} {
		failed = append(failed, call("Bash"), map[string]any{"role": "tool", "content": r})
	}
	if name, args := send([]string{"Bash", "record_fact"}, failed); name != "record_fact" || args["intent_id"] != float64(8) {
		t.Fatalf("로그인 실패 뒤 마감 사실 %q %v", name, args)
	}
	if name, _ := send(planner, base); name != "list_goals" {
		t.Fatalf("목표 조회 %q", name)
	}
	goals, _ := json.Marshal(`[{"id":3,"kind":"goal","payload":{"text":"x"},"state":"met"},{"id":5,"kind":"goal","payload":{"a":{}},"state":"open"}]`)
	p := append(append([]map[string]any{}, base...), call("list_goals"), map[string]any{"role": "tool", "content": json.RawMessage(goals)})
	if name, _ := send(planner, p); name != "list_facts" {
		t.Fatalf("사실 조회 %q", name)
	}
	p = append(p, call("list_facts"), map[string]any{"role": "tool", "content": `{"facts":[{"id":9,"state":"open"}],"total":1}`})
	if name, args := send(planner, p); name != "prove_goal" || args["goal_id"] != float64(5) || args["evidence_id"] != float64(9) {
		t.Fatalf("증명 %q %v", name, args)
	}
}
