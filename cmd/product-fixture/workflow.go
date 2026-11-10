package main

// Explicit acceptance roles only. These handlers are never used by the product services.
import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

var workflowReceived = struct {
	sync.Mutex
	Counts map[string]int
}{Counts: map[string]int{}}

func workflowReceive(r *http.Request) {
	workflowReceived.Lock()
	defer workflowReceived.Unlock()
	workflowReceived.Counts[r.Method+" "+r.URL.Path]++
}
func workflowStats() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		workflowReceived.Lock()
		defer workflowReceived.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"fixture": true, "received": workflowReceived.Counts})
	})
}

func workflowModel() http.Handler {
	var mu sync.Mutex
	turns := map[string]*fixtureTurns{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req) != nil {
			http.Error(w, "invalid_fixture_input", 400)
			return
		}
		manual := false
		target := ""
		for _, m := range req.Messages {
			text := string(m.Content)
			if strings.Contains(text, "user_approval_v1") {
				manual = true
			}
			for _, origin := range []string{"https://juice-a.test:9443", "https://juice-b.test:9443"} {
				if strings.Contains(text, origin) {
					target = origin
				}
			}
		}
		if target == "" {
			http.Error(w, "owned_target_required", 400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		turn := turns[target]
		if turn == nil {
			turn = &fixtureTurns{}
			turns[target] = turn
		}
		calls.Add(1)
		names := map[string]bool{}
		for _, t := range req.Tools {
			names[t.Function.Name] = true
		}
		message := map[string]any{"role": "assistant", "content": "Owned synthetic plan complete. Observations are not confirmed vulnerabilities."}
		finish := "stop"
		tool := func(name, id string, args any) {
			raw, _ := json.Marshal(args)
			message = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}}}
			finish = "tool_calls"
		}
		switch {
		case names["set_goals"] && !names["add_intent"] && !names["Bash"]:
			goals.Add(1)
			turn.goals++
			if turn.goals == 1 {
				tool("set_goals", "wf-goal", map[string]any{"goals": []any{map[string]any{"text": "Observe the owned workflow endpoints through E2 and preserve an unconfirmed diagnostic candidate.", "vulnclass": "access_control"}}})
			}
		case names["add_intent"] && !names["Bash"]:
			planner.Add(1)
			turn.planner++
			if turn.planner == 1 {
				tool("add_intent", "wf-discovery", map[string]any{"intents": []any{map[string]any{"summary": "Perform host-controlled initial discovery at " + target + "/workflow.", "priority": 5}}})
			}
			if turn.planner == 3 {
				tool("add_intent", "wf-deep", map[string]any{"intents": []any{map[string]any{"summary": "Inspect only the server-selected endpoints at " + target + ". Observe products and save a manual-review candidate. In user approval mode, propose two reversible owned account registrations separately, then verify denial of an unselected path.", "priority": 5}}})
			}
		case names["Bash"]:
			worker.Add(1)
			turn.worker++
			switch turn.worker {
			case 1:
				tool("Bash", "wf-read", map[string]any{"command": "curl --silent --show-error --max-time 10 " + target + "/rest/products/search", "timeout_ms": 120000})
			case 2:
				ref := ""
				for _, m := range req.Messages {
					if m.Role != "tool" {
						continue
					}
					var text string
					if json.Unmarshal(m.Content, &text) != nil {
						continue
					}
					var result e2runner.Result
					if json.Unmarshal([]byte(text), &result) == nil && result.Trusted.Version == e2proxy.Version && result.Trusted.Call.Counts.Responses > 0 && result.Trusted.Final {
						ref = result.Trusted.Call.CallRef
					}
				}
				if ref == "" {
					http.Error(w, "owned_observation_missing", 500)
					return
				}
				tool("submit_diagnostic_candidate", "wf-candidate", map[string]any{"call_ref": ref, "vulnclass": "access_control", "name": "소유 Juice Shop 실제 응답 관찰", "summary": "실제 상품 응답을 관찰한 수동 검토 후보이며 취약점 확정이 아닙니다.", "remediation": "실제 계정의 권한 경계는 별도 검증해야 합니다."})
			case 3, 4:
				if manual {
					label := "allowed"
					if turn.worker == 4 {
						label = "denied"
					}
					body, _ := json.Marshal(map[string]string{"email": "workflow-" + label + "@owned.invalid", "password": "OwnedFixture-A8!", "passwordRepeat": "OwnedFixture-A8!"})
					tool("Bash", "wf-post-"+label, map[string]any{"command": "curl --silent --show-error --max-time 110 -H 'Content-Type: application/json' --data '" + string(body) + "' " + target + "/api/Users", "timeout_ms": 120000})
				} else if turn.worker == 3 {
					tool("Bash", "wf-unselected", map[string]any{"command": "curl --silent --show-error --max-time 10 " + target + "/not-discovered", "timeout_ms": 120000})
				}
			case 5:
				if manual {
					tool("Bash", "wf-unselected", map[string]any{"command": "curl --silent --show-error --max-time 10 " + target + "/not-discovered", "timeout_ms": 120000})
				}
			}
		default:
			http.Error(w, "unexpected_fixture_role", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "owned-workflow-synthetic", "object": "chat.completion", "model": req.Model, "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 10, "total_tokens": 20}})
	})
}
