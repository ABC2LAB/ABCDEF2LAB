// 소유 Docker 수용 전용 합성 모델·HTTPS 프런트다. 제품 엔진을 대체하지 않는다.
package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
)

var calls, goals, planner, worker, allowed, denied, exploreTurns, exploreBash atomic.Int64

// exploreBashSeen은 탐색 직접 Bash 결과에서 모델이 본 상태 코드 줄이다(예: "approved=200 outside=403"). 값은 소유 fixture의 상태 코드뿐이다.
var exploreBashSeen atomic.Value

func main() {
	mode := flag.String("mode", "model", "acceptance role")
	target := flag.String("target", "https://juice.test:9443", "owned target")
	upstream := flag.String("upstream", "http://juice-shop:3000", "owned Juice Shop")
	flag.Parse()
	if *mode == "init" {
		must(fixtureInit())
		return
	}
	if *mode == "dns" {
		must(fixtureDNS())
		return
	}
	stats := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ready := true
		if *mode == "target" || *mode == "workflow-target" {
			c, e := net.DialTimeout("tcp", "juice-shop:3000", time.Second)
			ready = e == nil
			if c != nil {
				c.Close()
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"fixture": true, "ready": ready, "model_calls": calls.Load(), "goal_calls": goals.Load(), "planner_calls": planner.Load(), "worker_calls": worker.Load(), "explore_calls": exploreTurns.Load(), "explore_bash_calls": exploreBash.Load(), "explore_bash_seen": exploreBashSeen.Load(), "allowed_received": allowed.Load(), "outside_received": denied.Load()})
	})
	if *mode == "model" || *mode == "workflow-model" {
		mux := http.NewServeMux()
		mux.Handle("GET /stats", stats)
		handler := model(*target)
		if *mode == "workflow-model" {
			handler = workflowModel()
		}
		mux.Handle("POST /v1/chat/completions", handler)
		must(http.ListenAndServe(":8080", mux))
		return
	}
	u, e := url.Parse(*upstream)
	must(e)
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.Transport = &http.Transport{Proxy: nil}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) { http.Error(w, "owned_target_unavailable", 503) }
	go func() {
		mux := http.NewServeMux()
		mux.Handle("GET /stats", stats)
		if *mode == "workflow-target" {
			mux.Handle("GET /workflow-stats", workflowStats())
		}
		must(http.ListenAndServe(":9081", mux))
	}()
	server := &http.Server{Addr: ":9443", TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/rest/products/search" {
			allowed.Add(1)
		} else {
			denied.Add(1)
		}
		if *mode == "workflow-target" {
			workflowReceive(r)
			if r.Method == "GET" && r.URL.Path == "/workflow" {
				w.Header().Set("Content-Type", "text/html")
				io.WriteString(w, `<html><body><h1>Owned acceptance navigation fixture</h1><a href="/rest/products/search">Actual Juice Shop products</a><a href="/rest/admin/application-version">Actual Juice Shop version</a><a href="/excluded">Excluded fixture path</a><form method="post" action="/api/Users"></form></body></html>`)
				return
			}
		}
		proxy.ServeHTTP(w, r)
	})}
	must(server.ListenAndServeTLS("/target-tls/leaf.pem", "/target-tls/key.pem"))
}
func must(e error) {
	if e != nil {
		fmt.Fprintln(os.Stderr, "acceptance_fixture_failed")
		os.Exit(1)
	}
}

type fixtureTurns struct{ goals, planner, worker, explore int }

func model(target string) http.Handler {
	var mu sync.Mutex
	turns := map[string]*fixtureTurns{}
	originPattern := regexp.MustCompile(`https://(?:juice-a|juice-b|juice)\.test:9443`)
	exactGoal := regexp.MustCompile(`Inspect the approved (?:read-only )?HTTPS target (https://(?:juice-a|juice-b|juice)\.test:9443)`)

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
			w.WriteHeader(400)
			return
		}
		chosen := target
		for _, message := range req.Messages {
			if match := exactGoal.FindStringSubmatch(string(message.Content)); len(match) > 1 {
				chosen = match[1]
				break
			}
		}
		if chosen == target {
			for _, message := range req.Messages {
				if match := originPattern.FindString(string(message.Content)); match != "" {
					chosen = match
					break
				}
			}
		}
		target := chosen
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
		message := map[string]any{"role": "assistant", "content": "No further approved action."}
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
				tool("set_goals", "owned-goal", map[string]any{"goals": []any{map[string]any{"text": "Observe approved products at " + target + " and save a manual-review candidate.", "vulnclass": "access_control"}}})
			}
		case names["add_intent"] && !names["Bash"]:
			planner.Add(1)
			turn.planner++
			if turn.planner == 1 {
				tool("add_intent", "owned-intent", map[string]any{"intents": []any{map[string]any{"summary": "Observe " + target + "/rest/products/search through the enforced Bash boundary, preserve a manual-review candidate, and verify denial of /outside-approved.", "priority": 5}}})
			}
		case names["Bash"]:
			worker.Add(1)
			turn.worker++
			switch turn.worker {
			case 1:
				tool("Bash", "owned-allow", map[string]any{"command": "curl --silent --show-error --max-time 5 " + target + "/rest/products/search", "timeout_ms": 7000})
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
					if json.Unmarshal([]byte(text), &result) == nil && result.Trusted.Version == e2proxy.Version && result.Trusted.Call.CallRef != "" && result.Trusted.Call.Counts.Responses > 0 && result.Trusted.Final {
						ref = result.Trusted.Call.CallRef
					}
				}
				if ref == "" {
					http.Error(w, "trusted_observation_missing", 500)
					return
				}
				tool("submit_diagnostic_candidate", "owned-candidate", map[string]any{"call_ref": ref, "vulnclass": "access_control", "name": "소유 Juice Shop 관찰 후보", "summary": "실제 HTTPS 상품 응답을 관찰했습니다. 확정 취약점이 아닌 수동 검토 후보입니다.", "remediation": "실제 접근 권한을 별도로 검증하세요."})
			case 3:
				deniedTarget := target + "/outside-approved"
				if target == "https://juice-a.test:9443" {
					deniedTarget = "https://juice-b.test:9443/rest/products/search"
				}
				if target == "https://juice-b.test:9443" {
					deniedTarget = "https://juice-a.test:9443/rest/products/search"
				}
				tool("Bash", "owned-deny", map[string]any{"command": "curl --silent --show-error --max-time 5 " + deniedTarget, "timeout_ms": 7000})
			}
		default:
			http.Error(w, "unexpected_role", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "owned-synthetic", "object": "chat.completion", "model": req.Model, "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 10, "total_tokens": 20}})
	})
}
