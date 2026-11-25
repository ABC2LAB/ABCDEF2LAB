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
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var calls, goals, planner, worker, allowed, denied, authLogins, authenticatedReads, structuredReads, exploreTurns, exploreBash atomic.Int64

// exploreBashSeen은 탐색 직접 Bash 결과에서 모델이 본 상태 코드 줄이다(예: "approved=200 outside=403"). 값은 소유 fixture의 상태 코드뿐이다.
var exploreBashSeen atomic.Value

func main() {
	mode := flag.String("mode", "model", "acceptance role")
	target := flag.String("target", "https://juice.test:9443", "owned target")
	upstream := flag.String("upstream", "http://juice-shop:3000", "owned target upstream")
	pidsForks := flag.Int("pids-forks", -1, "owned PID fixture children; -1 fills the namespace limit")
	// 인증서 이름과 DNS 응답은 대상 이름 목록에서 만든다. 기본값은 Juice Shop 수용시험의 이름이고, 연습 대상 회차는
	// overlay가 바꾼다. 대상 이름이 제품 목표 문장으로 모델에 가므로 대상마다 맞춘다.
	hostsFlag := flag.String("hosts", defaultFixtureHosts, "owned target names: name=ipv4,...")
	flag.Parse()
	hosts, e := parseFixtureHosts(*hostsFlag)
	must(e)
	if *mode == "init" {
		must(fixtureInit(hosts))
		return
	}
	if *mode == "dns" {
		must(fixtureDNS(hosts))
		return
	}
	if *mode == "pids-model" {
		must(pidsModelHandler(*target, *pidsForks))
		return
	}
	if *mode == "pids-target" {
		must(servePidsTarget())
		return
	}
	var u *url.URL
	readyAddress := ""
	if *mode != "selected-target-nosniff" && *mode != "selected-target-auth-nosniff" {
		u, e = url.Parse(*upstream)
		must(e)
		readyAddress, e = upstreamDialAddress(u)
		must(e)
	}
	stats := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ready := true
		if *mode == "target" || *mode == "workflow-target" {
			c, e := net.DialTimeout("tcp", readyAddress, time.Second)
			ready = e == nil
			if c != nil {
				c.Close()
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"fixture": true, "ready": ready, "model_calls": calls.Load(), "goal_calls": goals.Load(), "planner_calls": planner.Load(), "worker_calls": worker.Load(), "explore_calls": exploreTurns.Load(), "explore_bash_calls": exploreBash.Load(), "explore_bash_seen": exploreBashSeen.Load(), "allowed_received": allowed.Load(), "outside_received": denied.Load(), "auth_logins": authLogins.Load(), "authenticated_reads": authenticatedReads.Load(), "structured_reads": structuredReads.Load()})
	})
	if *mode == "model" || *mode == "workflow-model" || *mode == "selected-model" || *mode == "selected-model-nosniff" || *mode == "selected-model-stored-nosniff" || *mode == "selected-model-auth-nosniff" || *mode == "selected-model-structured-nosniff" || *mode == "selected-model-overconfident-nosniff" {
		mux := http.NewServeMux()
		mux.Handle("GET /stats", stats)
		handler := model(*target)
		if *mode == "workflow-model" {
			handler = workflowModel()
		} else if *mode == "selected-model" {
			handler = selectedModel(*target)
		} else if *mode == "selected-model-nosniff" {
			handler = selectedNosniffModel(*target)
		} else if *mode == "selected-model-stored-nosniff" {
			handler = modelWithSelectionMode(*target, true, true, true, false, false, false)
		} else if *mode == "selected-model-auth-nosniff" {
			handler = modelWithSelectionMode(*target, true, true, false, true, false, false)
		} else if *mode == "selected-model-structured-nosniff" {
			handler = modelWithSelectionMode(*target, true, true, false, false, true, false)
		} else if *mode == "selected-model-overconfident-nosniff" {
			handler = modelWithSelectionMode(*target, true, true, false, false, false, true)
		}
		mux.Handle("POST /v1/chat/completions", handler)
		must(http.ListenAndServe(":8080", mux))
		return
	}
	var proxy *httputil.ReverseProxy
	if u != nil {
		proxy = httputil.NewSingleHostReverseProxy(u)
		proxy.Transport = &http.Transport{Proxy: nil}
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) { http.Error(w, "owned_target_unavailable", 503) }
	}
	go func() {
		mux := http.NewServeMux()
		mux.Handle("GET /stats", stats)
		if *mode == "workflow-target" {
			mux.Handle("GET /workflow-stats", workflowStats())
		}
		must(http.ListenAndServe(":9081", mux))
	}()
	server := &http.Server{Addr: ":9443", TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if *mode == "selected-target-auth-nosniff" && r.Method == "POST" && r.URL.Path == "/login" {
			var input struct {
				Username string `json:"username"`
				Password string `json:"password"`
			}
			if json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&input) != nil ||
				(input.Username != "owned-a" || input.Password != "synthetic-secret-a") &&
					(input.Username != "owned-b" || input.Password != "synthetic-secret-b") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			authLogins.Add(1)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"auth_token": fmt.Sprintf("synthetic-session-%s-%d", strings.TrimPrefix(input.Username, "owned-"), authLogins.Load())})
			return
		}
		if r.Method == "GET" && r.URL.Path == "/rest/products/search" {
			allowed.Add(1)
			if r.URL.RawQuery == "view=summary" && r.Header.Get("Accept-Language") == "en-US" {
				structuredReads.Add(1)
			}
		} else {
			denied.Add(1)
		}
		if (*mode == "selected-target-nosniff" || *mode == "selected-target-auth-nosniff") && r.Method == "GET" && r.URL.Path == "/rest/products/search" {
			if *mode == "selected-target-auth-nosniff" {
				if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer synthetic-session-a-") &&
					!strings.HasPrefix(r.Header.Get("Authorization"), "Bearer synthetic-session-b-") {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				authenticatedReads.Add(1)
			}
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html><body>Owned synthetic product listing</body></html>")
			return
		}
		if *mode == "workflow-target" {
			workflowReceive(r)
			if r.Method == "GET" && r.URL.Path == "/workflow" {
				w.Header().Set("Content-Type", "text/html")
				io.WriteString(w, `<html><body><h1>Owned acceptance navigation fixture</h1><a href="/rest/products/search">Actual Juice Shop products</a><a href="/rest/admin/application-version">Actual Juice Shop version</a><a href="/excluded">Excluded fixture path</a><form method="post" action="/api/Users"></form></body></html>`)
				return
			}
		}
		if proxy == nil {
			http.NotFound(w, r)
			return
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

type fixtureMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type selectedFixtureObservation struct {
	Source struct {
		CallRef     string `json:"call_ref"`
		Digest      string `json:"digest"`
		Observation struct {
			AttemptRef    string `json:"attempt_ref"`
			Method        string `json:"method"`
			Endpoint      string `json:"endpoint"`
			StatusCode    int    `json:"status_code"`
			ExactFunction bool   `json:"exact_function"`
			HTMLSecurity  string `json:"html_security"`
		} `json:"observation"`
	} `json:"source"`
}

// This owned fixture models a careful reviewer. It cannot report confirmed
// from the candidate text, one response, or two copies of the same attempt.
func selectedNosniffVerdict(candidateID int64, messages []fixtureMessage) any {
	result := map[string]any{"version": "selected/final-v1", "candidate_id": candidateID, "verdict": "insufficient",
		"claim":                 "An approved HTML response may be missing the nosniff header.",
		"normal_explanation":    "The response might not be HTML or the first observation might be incomplete.",
		"reason":                "Two independent comparable HTML observations were not established.",
		"limitations":           "Only the approved exact path is checked.",
		"remediation":           "Set X-Content-Type-Options: nosniff on HTML responses.",
		"verification_criteria": "Repeat two separate approved HTML reads and check both response headers.",
		"sources":               []any{}}
	var input struct {
		CandidateID int64                        `json:"candidate_id"`
		Original    *selectedFixtureObservation  `json:"original_observation"`
		Followup    []selectedFixtureObservation `json:"followup_observations"`
	}
	for _, message := range messages {
		var text string
		if message.Role == "user" && json.Unmarshal(message.Content, &text) == nil {
			_ = json.Unmarshal([]byte(text), &input)
		}
	}
	if input.CandidateID != candidateID {
		result["reason"] = "synthetic_candidate_mismatch"
		return result
	}
	if input.Original == nil {
		result["reason"] = "synthetic_original_unavailable"
		return result
	}
	if len(input.Followup) < 1 || len(input.Followup) > 3 {
		result["reason"] = "synthetic_followup_unavailable"
		return result
	}
	a, b := input.Original.Source, input.Followup[0].Source
	good := func(o selectedFixtureObservation) bool {
		s := o.Source
		return s.CallRef != "" && s.Digest != "" && s.Observation.AttemptRef != "" &&
			s.Observation.Method == "GET" && s.Observation.Endpoint == "/rest/products/search" &&
			s.Observation.ExactFunction && s.Observation.StatusCode == http.StatusOK &&
			s.Observation.HTMLSecurity == "nosniff_missing"
	}
	if !good(*input.Original) {
		result["reason"] = "synthetic_original_not_comparable_html"
		return result
	}
	if !good(input.Followup[0]) {
		result["reason"] = "synthetic_followup_not_comparable_html"
		return result
	}
	if a.CallRef == b.CallRef || a.Observation.AttemptRef == b.Observation.AttemptRef || a.Digest == b.Digest {
		result["reason"] = "synthetic_sources_not_independent"
		return result
	}
	result["verdict"] = "confirmed"
	result["reason"] = "Two independent audited HTML reads at the same approved function lack nosniff."
	result["sources"] = []any{
		map[string]string{"call_ref": a.CallRef, "attempt_ref": a.Observation.AttemptRef, "observation_digest": a.Digest},
		map[string]string{"call_ref": b.CallRef, "attempt_ref": b.Observation.AttemptRef, "observation_digest": b.Digest},
	}
	return result
}

func model(target string) http.Handler {
	return modelWithSelection(target, false)
}

// selectedModel is a disposable, no-cost fixture for the candidate API E2E.
// Its initial diagnosis has one goal and one observed fact in a fresh DB.
func selectedModel(target string) http.Handler {
	return modelWithSelectionMode(target, true, false, false, false, false, false)
}

func selectedNosniffModel(target string) http.Handler {
	return modelWithSelectionMode(target, true, true, false, false, false, false)
}

func modelWithSelection(target string, selected bool) http.Handler {
	return modelWithSelectionMode(target, selected, false, false, false, false, false)
}

func modelWithSelectionMode(target string, selected, nosniff, stored, authenticated, structured, overconfident bool) http.Handler {
	var mu sync.Mutex
	turns := map[string]*fixtureTurns{}
	originPattern := regexp.MustCompile(`https://(?:juice-a|juice-b|juice|synthetic)\.test:9443`)
	exactGoal := regexp.MustCompile(`Inspect the approved (?:read-only )?HTTPS target (https://(?:juice-a|juice-b|juice|synthetic)\.test:9443)`)
	syntheticSession := regexp.MustCompile(`synthetic-session-a-[0-9]+`)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []fixtureMessage `json:"messages"`
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
		if selected && len(names) == 0 {
			var candidateID int64
			var text string
			for _, m := range req.Messages {
				if m.Role != "user" || json.Unmarshal(m.Content, &text) != nil {
					continue
				}
				var input struct {
					CandidateID int64 `json:"candidate_id"`
				}
				if json.Unmarshal([]byte(text), &input) == nil && input.CandidateID > 0 {
					candidateID = input.CandidateID
				}
			}
			if candidateID <= 0 {
				http.Error(w, "selected_candidate_missing", 400)
				return
			}
			var result any
			all := string(req.Messages[0].Content)
			if strings.Contains(all, "selected/analysis-v1") {
				result = map[string]any{"version": "selected/analysis-v1", "candidate_id": candidateID,
					"claim":              "A product listing may reveal information outside its intended audience.",
					"normal_explanation": "The listing may be intentionally public.",
					"existing_call_refs": []string{}, "business_questions": []string{"Is this listing intended to be public?"},
					"actions":     []any{map[string]any{"purpose": "Compare the current public listing response with the original observation", "method": "GET", "path_hint": "/rest/products/search"}},
					"limitations": "The target fixture does not define a private listing policy."}
				if nosniff {
					result = map[string]any{"version": "selected/analysis-v1", "candidate_id": candidateID,
						"claim":              "The observed HTML response may be missing X-Content-Type-Options: nosniff.",
						"normal_explanation": "The original read might have been incomplete or the header may vary by response.",
						"existing_call_refs": []string{}, "business_questions": []string{},
						"actions":     []any{map[string]any{"purpose": "Independently inspect the same approved HTML response headers", "method": "GET", "path_hint": "/rest/products/search"}},
						"limitations": "Only the approved exact HTML path is checked."}
					if structured {
						result.(map[string]any)["actions"] = []any{
							map[string]any{"purpose": "Repeat the approved HTML read", "method": "GET", "path_hint": "/rest/products/search"},
							map[string]any{"purpose": "Compare the public summary representation", "kind": "normal_control", "method": "GET",
								"path_hint": "/rest/products/search", "query": map[string]string{"view": "summary"},
								"headers": map[string]string{"Accept-Language": "en-US"}},
						}
					}
				}
			} else if strings.Contains(all, "selected/final-v1") {
				result = map[string]any{"version": "selected/final-v1", "candidate_id": candidateID, "verdict": "insufficient",
					"claim":                 "A product listing may reveal information outside its intended audience.",
					"normal_explanation":    "The listing may be intentionally public.",
					"reason":                "The separate audited read does not establish an unauthorized audience.",
					"limitations":           "No approved business rule distinguishes a public from a private listing.",
					"remediation":           "Define the intended audience and restrict the listing only if needed.",
					"verification_criteria": "Confirm the intended audience, then repeat the same approved read.", "sources": []any{}}
				if nosniff {
					result = selectedNosniffVerdict(candidateID, req.Messages)
					if overconfident {
						claim := result.(map[string]any)
						claim["verdict"] = "confirmed"
						claim["reason"] = "Synthetic model asserted a defect using an unrelated source."
						claim["sources"] = []any{map[string]string{
							"call_ref": "unrelated-call", "attempt_ref": "unrelated-attempt",
							"observation_digest": strings.Repeat("a", 64),
						}}
					}
				}
			} else {
				http.Error(w, "unexpected_selected_prompt", 400)
				return
			}
			raw, _ := json.Marshal(result)
			message["content"] = string(raw)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": "owned-synthetic", "object": "chat.completion", "model": req.Model,
				"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
				"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 10, "total_tokens": 20}})
			return
		}
		tool := func(name, id string, args any) {
			raw, _ := json.Marshal(args)
			message = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}}}
			finish = "tool_calls"
		}
		switch {
		case names["set_goals"] && !names["add_intent"] && !names["Bash"]:
			// 목표 분해의 첫 요청(도구 결과가 아직 없는 대화)은 작업(점검)마다 한 번 온다. 그때 턴을 새로 센다. 프로세스
			// 전체로 세면 같은 설치·같은 대상의 두 번째 점검에서 의도를 내지 않아 E② 실행이 열리지 않는다(리허설 F11,
			// 2026-11-21). 앞 점검이 목표만 받고 끝났어도 새 점검은 새 대화로 시작하므로 같은 경계로 구분된다. 같은
			// 작업의 이어지는 요청은 앞 도구 결과를 싣고 와서 턴을 바꾸지 않는다(흐름 모드 합성 모델과 같은 경계).
			fresh := true
			for _, m := range req.Messages {
				if m.Role == "tool" {
					fresh = false
				}
			}
			if fresh {
				*turn = fixtureTurns{}
			}
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
			} else if selected && turn.worker >= 3 && (!stored || turn.worker >= 4) && names["prove_goal"] {
				// The dedicated E2E starts with an empty product DB: origin fact 1,
				// generated goal 2, and the observed read fact 4. Never use this
				// fixture mode against a persisted or shared installation.
				tool("prove_goal", "owned-goal-proof", map[string]any{"goal_id": 2, "evidence_id": 4, "reason": "The approved read was observed and its review candidate was saved."})
			}
		case names["Bash"]:
			worker.Add(1)
			turn.worker++
			switch turn.worker {
			case 1:
				command := "curl --silent --show-error --max-time 5 " + target + "/rest/products/search"
				if authenticated {
					var session string
					for _, m := range req.Messages {
						if value := syntheticSession.FindString(string(m.Content)); value != "" {
							session = value
							break
						}
					}
					if session == "" {
						http.Error(w, "owned_session_missing", 400)
						return
					}
					command = "curl --silent --show-error --max-time 5 --header 'Authorization: Bearer " + session + "' " + target + "/rest/products/search"
				}
				tool("Bash", "owned-allow", map[string]any{"command": command, "timeout_ms": 7000})
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
				if nosniff {
					tool("submit_diagnostic_candidate", "owned-candidate", map[string]any{"call_ref": ref, "vulnclass": "security_headers", "name": "HTML nosniff 후보", "summary": "합성 HTML 응답의 보안 헤더 상태를 별도 요청으로 검증합니다.", "remediation": "HTML 응답에 X-Content-Type-Options: nosniff를 설정하세요."})
				} else {
					tool("submit_diagnostic_candidate", "owned-candidate", map[string]any{"call_ref": ref, "vulnclass": "access_control", "name": "소유 Juice Shop 관찰 후보", "summary": "실제 HTTPS 상품 응답을 관찰했습니다. 확정 취약점이 아닌 수동 검토 후보입니다.", "remediation": "실제 접근 권한을 별도로 검증하세요."})
				}
			case 3:
				if stored {
					tool("Bash", "owned-independent", map[string]any{"command": "curl --silent --show-error --max-time 5 " + target + "/rest/products/search", "timeout_ms": 7000})
					break
				}
				fallthrough
			case 4:
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
