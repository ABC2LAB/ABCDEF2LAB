package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// workflowLoginConfig는 로그인 합성 모델이 쓰는 대상 로그인 모양이다. 앱별 경로·칸 이름은 코드에 두지 않고 시험 overlay의
// 플래그로 받는다(소유 수용 전용, 제품 경로 아님).
type workflowLoginConfig struct {
	loginPath, identityPath, usernameField, passwordField, tokenField, header, prefix string
	exploreOnly                                                                       bool
}

var (
	// fixtureTestOrigin은 소유 시험 대상 이름(*.test:9443)이다. 제품 목표 문장에서 대상 origin을 찾는다.
	fixtureTestOrigin = regexp.MustCompile(`https://[a-z0-9-]+(?:\.[a-z0-9-]+)*\.test:9443`)
	// fixtureCredentialLine은 진단 Worker 지침의 등록 계정 줄이다(agent e2HTTPCredentialLines: `- 라벨: username "…", password "…"`).
	fixtureCredentialLine = regexp.MustCompile(`- ([a-z0-9][a-z0-9_-]{0,31}): username ("(?:[^"\\]|\\.)*"), password ("(?:[^"\\]|\\.)*")`)
)

type fixtureAccount struct{ label, username, password string }

// loginFixtureMessage는 도구 호출 이름까지 읽는 대화 메시지다(Planner 마무리 순서를 앞 호출로 정한다).
type loginFixtureMessage struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	ToolCalls []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tool_calls"`
}

var (
	fixtureIntentID = regexp.MustCompile(`Intent id: ([1-9][0-9]{0,18})\b`)
)

// workflowLoginModel은 워크플로 자동 모드의 소유 수용 전용 합성 모델이다(모델 로그인 계약 §2-1·§4, E③ 학습 로그인 재사용
// §6-2 종단 시험). 목표·의도 흐름은 workflowModel과 같고, 진단 Worker만 다르다: 신원 경로를 세션 없이 한 번 읽고(막힘 대조),
// Worker 지침의 등록 계정마다 대상 로그인 API로 직접 로그인한 뒤 받은 토큰으로 신원 경로를 읽는다. 판단하는 모델이 아니며
// 후보·목표 달성은 만들지 않는다. 대상이 준 토큰은 그 대화의 도구 결과에서만 읽는다.
func workflowLoginModel(cfg workflowLoginConfig) http.Handler {
	var mu sync.Mutex
	turns := map[string]*loginFixtureTurns{}
	tokenPattern := fixtureTokenPattern(cfg.tokenField)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []loginFixtureMessage `json:"messages"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req) != nil {
			http.Error(w, "invalid_fixture_input", 400)
			return
		}
		target := ""
		var accounts []fixtureAccount
		toolResults := []string{}
		lastCall, factCalled, intentID := "", false, int64(0)
		for _, m := range req.Messages {
			text := string(m.Content)
			if target == "" {
				target = fixtureTestOrigin.FindString(text)
			}
			var plain string
			if json.Unmarshal(m.Content, &plain) != nil {
				plain = text
			}
			if m.Role == "tool" {
				toolResults = append(toolResults, text)
				continue
			}
			for _, call := range m.ToolCalls {
				lastCall = call.Function.Name
				if lastCall == "record_fact" {
					factCalled = true
				}
			}
			if m.Role == "system" || m.Role == "user" {
				if match := fixtureIntentID.FindStringSubmatch(plain); match != nil {
					intentID, _ = strconv.ParseInt(match[1], 10, 64)
				}
			}
			accounts = appendFixtureCredentials(accounts, plain)
		}
		if target == "" {
			http.Error(w, "owned_target_required", 400)
			return
		}
		sort.Slice(accounts, func(i, j int) bool { return accounts[i].label < accounts[j].label })
		names := map[string]bool{}
		for _, t := range req.Tools {
			names[t.Function.Name] = true
		}
		mu.Lock()
		defer mu.Unlock()
		if names["set_goals"] && !names["add_intent"] && !names["Bash"] && len(toolResults) == 0 {
			turns[target] = &loginFixtureTurns{}
		}
		turn := turns[target]
		if turn == nil {
			turn = &loginFixtureTurns{}
			turns[target] = turn
		}
		calls.Add(1)
		message := map[string]any{"role": "assistant", "content": "Owned synthetic login plan complete. Observations are not confirmed vulnerabilities."}
		finish := "stop"
		tool := func(name, id string, args any) {
			raw, _ := json.Marshal(args)
			message = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}}}
			finish = "tool_calls"
		}
		bash := func(id, command string) {
			tool("Bash", id, map[string]any{"command": command, "timeout_ms": 60000})
		}
		switch {
		case names["mcp__explore_browser__browser_evaluate"] && cfg.exploreOnly:
			// The owned fixture drives the real browser MCP and exploration
			// boundary. The evaluated script returns status codes only; the
			// registered synthetic credentials and issued session stay in the
			// browser context.
			turn.explore++
			switch turn.explore {
			case 1, 4:
				// VAmPI's /ui redirects to its internal HTTP origin. Keep the
				// browser on the approved HTTPS origin before relative fetches.
				tool("mcp__explore_browser__browser_navigate", "wl-explore-page", map[string]any{"url": target + "/openapi.json"})
			case 2, 5:
				index := 0
				if turn.explore == 5 {
					index = 1
				}
				if index < len(accounts) {
					tool("mcp__explore_browser__browser_evaluate", "wl-explore-login", map[string]any{"function": browserLoginScript(cfg, accounts[index])})
				}
			case 3:
				if len(accounts) > 1 && names["switch_browser_account"] {
					tool("switch_browser_account", "wl-explore-switch", map[string]any{"account": accounts[1].label})
				}
			}
		case names["set_goals"] && !names["add_intent"] && !names["Bash"]:
			goals.Add(1)
			turn.goals++
			if turn.goals == 1 {
				tool("set_goals", "wl-goal", map[string]any{"goals": []any{map[string]any{"text": "Log in to the owned target with each registered account and read the identity endpoint through E2.", "vulnclass": "access_control"}}})
			}
		case names["add_intent"] && !names["Bash"]:
			planner.Add(1)
			turn.planner++
			last := ""
			if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == "tool" {
				last = lastCall
			}
			switch {
			case last == "add_intent":
				// 방금 낸 의도의 결과다. 이번 라운드를 끝낸다(다음 라운드에서 다음 의도를 낸다).
			case !turn.discoverySent:
				// 워크플로 회차의 첫 의도는 프로그램이 맡는 초기 탐색이다(workflowModel과 같은 순서).
				turn.discoverySent = true
				tool("add_intent", "wl-discovery", map[string]any{"intents": []any{map[string]any{"summary": "Perform host-controlled initial discovery at " + target + cfg.identityPath + ".", "priority": 5}}})
			case !turn.intentSent:
				turn.intentSent = true
				tool("add_intent", "wl-login", map[string]any{"intents": []any{map[string]any{"summary": "Log in to " + target + " with every registered account and read " + cfg.identityPath + " with each issued session.", "priority": 5}}})
			case !turn.factRecorded || turn.proved || !names["list_goals"] || !names["list_facts"] || !names["prove_goal"]:
				// Worker가 사실을 남기기 전이거나 이미 증명했다: 이번 라운드를 끝낸다.
			case last == "" || last == "prove_goal":
				tool("list_goals", "wl-goals", map[string]any{})
			case last == "list_goals":
				if goal := fixtureNodeID(toolResults[len(toolResults)-1], "goals", "open"); goal > 0 {
					turn.goalID = goal
					tool("list_facts", "wl-facts", map[string]any{})
				}
			case last == "list_facts":
				if fact := fixtureNodeID(toolResults[len(toolResults)-1], "facts", ""); fact > 0 && turn.goalID > 0 {
					turn.proved = true
					tool("prove_goal", "wl-prove", map[string]any{"goal_id": turn.goalID, "evidence_id": fact, "reason": "Owned synthetic fixture finished its login sequence; this is not a login success or vulnerability claim."})
				}
			}
		case names["Bash"]:
			worker.Add(1)
			if cfg.exploreOnly {
				if names["record_fact"] && !factCalled && intentID > 0 {
					turn.factRecorded = true
					tool("record_fact", "wl-explore-fact", map[string]any{"intent_id": intentID,
						"summary": "Owned browser exploration fixture finished; login success is determined by recorded observations.",
						"detail":  "synthetic fixture (not a model judgment)", "evidence": "synthetic fixture", "confidence": "observed"})
				}
				break
			}
			// Worker 대화의 도구 결과 수가 이 대화에서 낸 명령 수다. 단계: 0 세션 없는 신원 GET, 계정마다 로그인 POST·토큰 신원 GET.
			step := len(toolResults)
			if len(accounts) == 0 {
				break
			}
			if step == 0 {
				bash("wl-anonymous", "curl --silent --show-error --max-time 10 --output /dev/null --write-out 'anonymous=%{http_code}\\n' "+target+cfg.identityPath)
				break
			}
			index, read := (step-1)/2, (step-1)%2 == 1
			// 로그인이 토큰을 주지 않았으면 다음 계정으로 넘어가지 않는다(판단하지 않는 fixture).
			loginFailed := read && index < len(accounts) && tokenPattern.FindStringSubmatch(toolResults[len(toolResults)-1]) == nil
			if index >= len(accounts) || loginFailed {
				// 계정 순서를 마쳤다(실패로 멈춘 경우 포함). 이 의도로 고정 관찰 사실 하나를 남기고(대화마다 한 번) 끝내
				// 회차가 시간 상한까지 기다리지 않게 한다. 사실은 "fixture가 순서를 마침"이지 로그인 성공 주장이 아니다.
				if names["record_fact"] && !factCalled && intentID > 0 {
					turn.factRecorded = true
					tool("record_fact", "wl-fact", map[string]any{"intent_id": intentID, "summary": "Owned synthetic fixture finished its login sequence (not a login success claim).",
						"detail": "synthetic fixture (not a model judgment)", "evidence": "synthetic fixture", "confidence": "observed"})
				}
				break
			}
			account := accounts[index]
			if !read {
				authLogins.Add(1)
				bash("wl-login-"+account.label, fixtureLoginCommand(target, cfg, account))
				break
			}
			match := tokenPattern.FindStringSubmatch(toolResults[len(toolResults)-1])
			authenticatedReads.Add(1)
			bash("wl-read-"+account.label, fixtureSessionReadCommand(target+cfg.identityPath, cfg, match[1]))
		default:
			// 요약·마무리 같은 다른 역할은 도구 없이 끝낸다.
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "owned-workflow-login-synthetic", "object": "chat.completion", "model": req.Model, "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 10, "total_tokens": 20}})
	})
}

func browserLoginScript(cfg workflowLoginConfig, account fixtureAccount) string {
	literal := func(value string) string {
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
	return fmt.Sprintf(`async () => {
  const anonymous = await fetch(%s, {credentials: 'omit'});
  const login = await fetch(%s, {method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({[%s]: %s, [%s]: %s})});
  const body = await login.json();
  const identity = await fetch(%s, {headers: {[%s]: %s + body[%s]}});
  return {anonymous: anonymous.status, login: login.status, identity: identity.status};
}`, literal(cfg.identityPath), literal(cfg.loginPath), literal(cfg.usernameField), literal(account.username),
		literal(cfg.passwordField), literal(account.password), literal(cfg.identityPath), literal(cfg.header),
		literal(cfg.prefix), literal(cfg.tokenField))
}

// fixtureNodeID는 그래프 도구 결과(노드 배열 또는 {key: 노드 배열})에서 상태가 맞는 첫 노드 번호다. 상태가 비면 첫 노드다.
// 도구 결과는 JSON 문자열로 실려 오므로 한 번 풀고 읽는다. 없으면 0이다.
func fixtureNodeID(raw, key, state string) int64 {
	text := raw
	var inner string
	if json.Unmarshal([]byte(raw), &inner) == nil {
		text = inner
	}
	type node struct {
		ID    int64  `json:"id"`
		State string `json:"state"`
	}
	var nodes []node
	if json.Unmarshal([]byte(text), &nodes) != nil {
		var wrapped map[string]json.RawMessage
		if json.Unmarshal([]byte(text), &wrapped) != nil || json.Unmarshal(wrapped[key], &nodes) != nil {
			return 0
		}
	}
	for _, n := range nodes {
		if n.ID > 0 && (state == "" || n.State == state) {
			return n.ID
		}
	}
	return 0
}

// loginFixtureTurns는 대상별 진행 상태다(작업마다 목표 분해 첫 요청에서 새로 센다).
type loginFixtureTurns struct {
	goals, planner                                  int
	explore                                         int
	discoverySent, intentSent, factRecorded, proved bool
	goalID                                          int64
}

// appendFixtureCredentials는 글 하나에서 진단 Worker 지침의 등록 계정 줄을 읽어 붙인다. 합성 모델의 자격은 이 줄에서만
// 온다(모델 코드·overlay에 자격 값을 두지 않는다). 같은 라벨은 처음 것만 둔다.
func appendFixtureCredentials(out []fixtureAccount, text string) []fixtureAccount {
	for _, match := range fixtureCredentialLine.FindAllStringSubmatch(text, -1) {
		username, e1 := strconv.Unquote(match[2])
		password, e2 := strconv.Unquote(match[3])
		if e1 == nil && e2 == nil {
			out = appendFixtureAccount(out, fixtureAccount{label: match[1], username: username, password: password})
		}
	}
	return out
}

// fixtureTokenPattern은 로그인 응답에서 토큰 칸 값을 꺼내는 식이다. 응답은 도구 결과 JSON 문자열 안에 실려 오므로 따옴표가
// 한 번 이상 escape돼 있을 수 있다. 짧은 값(16바이트 미만)은 토큰으로 보지 않는다.
func fixtureTokenPattern(field string) *regexp.Regexp {
	return regexp.MustCompile(regexp.QuoteMeta(field) + `\\*"\s*:\s*\\*"([A-Za-z0-9._~+/=-]{16,})`)
}

// fixtureLoginCommand는 등록 계정 하나로 대상 로그인 API에 JSON POST를 한 번 보내는 Bash 명령이다. 실패해도 다시 보내지
// 않는다(부르는 쪽이 한 번만 낸다).
func fixtureLoginCommand(target string, cfg workflowLoginConfig, account fixtureAccount) string {
	body, _ := json.Marshal(map[string]string{cfg.usernameField: account.username, cfg.passwordField: account.password})
	return "curl --silent --show-error --max-time 10 --request POST --header 'Content-Type: application/json' --data " +
		shellQuote(string(body)) + " " + target + cfg.loginPath
}

// fixtureSessionReadCommand는 대상이 준 토큰을 세션 헤더에 실어 주소 하나를 GET하는 Bash 명령이다.
func fixtureSessionReadCommand(address string, cfg workflowLoginConfig, token string) string {
	return "curl --silent --show-error --max-time 10 --header " + shellQuote(cfg.header+": "+cfg.prefix+token) + " " + address
}

func appendFixtureAccount(out []fixtureAccount, a fixtureAccount) []fixtureAccount {
	for _, have := range out {
		if have.label == a.label {
			return out
		}
	}
	return append(out, a)
}

// shellQuote는 Bash 한 인자로 감싼다(작은따옴표 안의 작은따옴표만 바꾼다).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
