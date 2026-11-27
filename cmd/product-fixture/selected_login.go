package main

import (
	"encoding/json"
	"regexp"
	"sort"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

// selectedLoginWorkerSteps는 인증 후보 시나리오 Worker가 내는 명령 수다(세션 없는 읽기 · 로그인 · 세션 읽기 · 후보 ·
// 승인 밖 요청). Planner는 Worker가 이만큼 낸 뒤에만 목표를 증명한다.
const selectedLoginWorkerSteps = 5

// selectedLoginCommandMS는 인증 후보 Worker 명령 하나의 도구 시간 상한이다(curl --max-time 10보다 길게).
const selectedLoginCommandMS = 15000

// selectedLoginPlanner는 인증 후보 시나리오의 Planner 한 요청이다. 워크플로 자동 모드라 첫 의도는 프로그램이 맡는 초기
// 탐색이고, 다음 라운드에 진단 의도를 낸다. 진단 의도 요약은 일반 모드 후보 시나리오와 같은 말로 시작한다(selectedGoalProof가
// 그 앞부분으로 의도를 찾는다). 방금 낸 도구 호출의 결과를 받는 요청은 라운드를 끝낸다.
func selectedLoginPlanner(turn *fixtureTurns, tool func(string, string, any), messages []fixtureMessage, target string, canProve bool) {
	if turn.awaiting {
		turn.awaiting = false
		return
	}
	switch {
	case !turn.discoverySent:
		turn.discoverySent, turn.awaiting = true, true
		tool("add_intent", "owned-discovery", map[string]any{"intents": []any{map[string]any{"summary": "Perform host-controlled initial discovery at the approved start page of " + target + ".", "priority": 5}}})
	case !turn.deepSent:
		turn.deepSent, turn.awaiting = true, true
		tool("add_intent", "owned-intent", map[string]any{"intents": []any{map[string]any{"summary": "Observe " + target + "/rest/products/search through the enforced Bash boundary, preserve a manual-review candidate, and verify denial of /outside-approved. Log in yourself with a registered account first and read with the session the target issues.", "priority": 5}}})
	case turn.worker >= selectedLoginWorkerSteps && canProve:
		if goalID, evidenceID, ok := selectedGoalProof(messages, target); ok {
			turn.awaiting = true
			tool("prove_goal", "owned-goal-proof", map[string]any{"goal_id": goalID, "evidence_id": evidenceID, "reason": "The approved read was observed and its review candidate was saved."})
		}
	}
}

// selectedLoginWorker는 인증 후보 시나리오 Worker의 다음 명령을 낸다. 모델 로그인 계약 §2-1·§4처럼 세션 없는 읽기(로그인
// 판정기의 막힘 대조) → 지침의 등록 계정 줄로 대상 로그인 API에 POST 한 번 → 받은 토큰으로 같은 읽기 → 그 읽기로 후보 →
// 승인 밖 요청 순서다. 판단하지 않는 fixture라 계정 줄이 없으면 로그인하지 않고, 토큰을 못 받으면 다시 로그인하지 않고
// 멈춘다. 자격은 지침에서, 토큰은 이 대화의 도구 결과에서만 읽는다(프로그램이 세션을 채우지 않는다). false는 후보로 쓸
// 신뢰 관찰이 없다는 뜻이다.
func selectedLoginWorker(tool func(string, string, any), messages []fixtureMessage, target string, cfg workflowLoginConfig, tokenPattern *regexp.Regexp) bool {
	var accounts []fixtureAccount
	var results []json.RawMessage
	for _, m := range messages {
		if m.Role == "tool" {
			results = append(results, m.Content)
			continue
		}
		var plain string
		if json.Unmarshal(m.Content, &plain) != nil {
			plain = string(m.Content)
		}
		accounts = appendFixtureCredentials(accounts, plain)
	}
	if len(accounts) == 0 {
		return true
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].label < accounts[j].label })
	account := accounts[0]
	products := target + "/rest/products/search"
	bash := func(id, command string) {
		tool("Bash", id, map[string]any{"command": command, "timeout_ms": selectedLoginCommandMS})
	}
	switch len(results) {
	case 0:
		bash("owned-anonymous", "curl --silent --show-error --max-time 10 "+products)
	case 1:
		authLogins.Add(1)
		bash("owned-login-"+account.label, fixtureLoginCommand(target, cfg, account))
	case 2:
		match := tokenPattern.FindStringSubmatch(string(results[1]))
		if match == nil {
			return true
		}
		authenticatedReads.Add(1)
		bash("owned-allow", fixtureSessionReadCommand(products, cfg, match[1]))
	case 3:
		ref := fixtureTrustedCallRef(results[2])
		if ref == "" {
			return false
		}
		tool("submit_diagnostic_candidate", "owned-candidate", selectedNosniffCandidate(ref))
	case 4:
		bash("owned-deny", "curl --silent --show-error --max-time 10 "+selectedDeniedTarget(target))
	}
	return true
}

// fixtureTrustedCallRef는 도구 결과 하나가 E② 신뢰 결과(응답을 받고 끝난 호출)면 그 호출 참조다. 아니면 빈 문자열이다.
func fixtureTrustedCallRef(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return ""
	}
	var result e2runner.Result
	if json.Unmarshal([]byte(text), &result) != nil || result.Trusted.Version != e2proxy.Version || result.Trusted.Call.CallRef == "" ||
		result.Trusted.Call.Counts.Responses == 0 || !result.Trusted.Final {
		return ""
	}
	return result.Trusted.Call.CallRef
}

// selectedNosniffCandidate는 nosniff 후보 시나리오들이 같이 쓰는 후보 제출 인자다.
func selectedNosniffCandidate(ref string) map[string]any {
	return map[string]any{"call_ref": ref, "vulnclass": "security_headers", "name": "HTML nosniff 후보", "summary": "합성 HTML 응답의 보안 헤더 상태를 별도 요청으로 검증합니다.", "remediation": "HTML 응답에 X-Content-Type-Options: nosniff를 설정하세요."}
}

// selectedDeniedTarget은 승인 밖 요청 주소다. A/B 대상이면 다른 대상의 같은 경로, 아니면 승인 밖 경로다.
func selectedDeniedTarget(target string) string {
	switch target {
	case "https://juice-a.test:9443":
		return "https://juice-b.test:9443/rest/products/search"
	case "https://juice-b.test:9443":
		return "https://juice-a.test:9443/rest/products/search"
	}
	return target + "/outside-approved"
}
