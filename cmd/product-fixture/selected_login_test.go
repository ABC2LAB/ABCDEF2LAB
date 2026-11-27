package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

const selectedLoginTarget = "https://synthetic.test:9443"

// 시험마다 새로 만드는 자격이다. 시험 파일에 자격 값을 적지 않고, 합성 모델이 지침의 등록 계정 줄에서만 읽는지 본다.
func selectedLoginCredential(t *testing.T) (string, string) {
	t.Helper()
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return "user-" + hex.EncodeToString(raw[:4]), "pw-" + hex.EncodeToString(raw[4:])
}

func selectedLoginHandler() *workflowLoginConfig {
	return &workflowLoginConfig{loginPath: "/login", usernameField: "username", passwordField: "password",
		tokenField: "auth_token", header: "Authorization", prefix: "Bearer "}
}

func selectedLoginSystem(lines string) []any {
	return []any{map[string]any{"role": "system", "content": "Approved target " + selectedLoginTarget + " ..." + lines},
		map[string]any{"role": "user", "content": "go"}}
}

func selectedLoginTool(messages []any, content string) []any {
	return append(append([]any{}, messages...), map[string]any{"role": "tool", "content": content})
}

func selectedLoginCommand(t *testing.T, h http.Handler, messages []any) string {
	t.Helper()
	name, args := selectedFixtureResponseArgs(t, h, messages)
	if name != "Bash" {
		t.Fatalf("Bash expected, got %q", name)
	}
	var input struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(args), &input) != nil || input.Command == "" {
		t.Fatal("Bash command missing")
	}
	return input.Command
}

// 인증 후보 시나리오 Worker: 세션 없는 읽기 → 지침의 계정 줄로 로그인 POST 한 번 → 받은 토큰으로 같은 읽기 → 그 읽기로 후보 →
// 승인 밖 요청 → 끝.
func TestSelectedAuthFixtureLogsInFromAccountLine(t *testing.T) {
	username, password := selectedLoginCredential(t)
	h := modelWithSelectionMode(selectedLoginTarget, true, true, false, selectedLoginHandler(), false, false, false)
	msgs := selectedLoginSystem("\n\nRegistered accounts for the approved target.\n- owned-a: username " + strconv.Quote(username) + ", password " + strconv.Quote(password) + "\n")
	products := selectedLoginTarget + "/rest/products/search"

	command := selectedLoginCommand(t, h, msgs)
	if !strings.HasSuffix(command, " "+products) || strings.Contains(command, "--header") || strings.Contains(command, password) {
		t.Fatalf("first command is not a session-less read: %s", command)
	}
	msgs = selectedLoginTool(msgs, "anonymous result")
	command = selectedLoginCommand(t, h, msgs)
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	if !strings.Contains(command, "--request POST") || !strings.Contains(command, shellQuote(string(body))) || !strings.HasSuffix(command, " "+selectedLoginTarget+"/login") {
		t.Fatalf("second command is not one login POST from the account line: %s", command)
	}
	token := "issued-" + hex.EncodeToString([]byte(username))
	// 로그인 응답 본문은 E② 도구 결과 JSON의 문자열 안에 실려 와 따옴표가 escape돼 있다.
	loginResult, _ := json.Marshal(map[string]string{"tool_output": `{"auth_token":"` + token + `"}`})
	msgs = selectedLoginTool(msgs, string(loginResult))
	command = selectedLoginCommand(t, h, msgs)
	if command != "curl --silent --show-error --max-time 10 --header 'Authorization: Bearer "+token+"' "+products {
		t.Fatalf("third command does not read with the issued session: %s", command)
	}
	observed, _ := json.Marshal(e2runner.Result{Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Final: true,
		Call: e2proxy.CallResult{CallRef: "session-read", Counts: e2proxy.Counts{Responses: 1}}}})
	msgs = selectedLoginTool(msgs, string(observed))
	name, args := selectedFixtureResponseArgs(t, h, msgs)
	if name != "submit_diagnostic_candidate" || !strings.Contains(args, `"call_ref":"session-read"`) {
		t.Fatalf("candidate does not use the session read: %s %s", name, args)
	}
	msgs = selectedLoginTool(msgs, "candidate saved")
	if command = selectedLoginCommand(t, h, msgs); !strings.HasSuffix(command, " "+selectedLoginTarget+"/outside-approved") {
		t.Fatalf("fifth command is not the denial probe: %s", command)
	}
	msgs = selectedLoginTool(msgs, "denied")
	if name, _ := selectedFixtureResponseArgs(t, h, msgs); name != "" {
		t.Fatalf("worker did not stop after its sequence: %s", name)
	}
}

// 계정 줄이 없으면 로그인하지 않는다(프로그램이 세션을 채우던 예전 동작으로 돌아가지 않는다). 아무 명령도 내지 않는다.
func TestSelectedAuthFixtureWithoutAccountLineDoesNotLogIn(t *testing.T) {
	h := modelWithSelectionMode(selectedLoginTarget, true, true, false, selectedLoginHandler(), false, false, false)
	msgs := selectedLoginSystem("\n\nSession for owned-a: Authorization: Bearer synthetic-session-a-1\n")
	for i := 0; i < 3; i++ {
		if name, args := selectedFixtureResponseArgs(t, h, msgs); name != "" {
			t.Fatalf("command without an account line: %s %s", name, args)
		}
		msgs = selectedLoginTool(msgs, "result")
	}
}

// 로그인이 토큰을 주지 않으면 세션 읽기도 다시 로그인도 하지 않고 멈춘다.
func TestSelectedAuthFixtureStopsWithoutToken(t *testing.T) {
	username, password := selectedLoginCredential(t)
	h := modelWithSelectionMode(selectedLoginTarget, true, true, false, selectedLoginHandler(), false, false, false)
	msgs := selectedLoginSystem("\n- owned-a: username " + strconv.Quote(username) + ", password " + strconv.Quote(password) + "\n")
	msgs = selectedLoginTool(selectedLoginTool(msgs, "anonymous result"), `{"tool_output":"HTTP/1.1 401 Unauthorized"}`)
	if name, args := selectedFixtureResponseArgs(t, h, msgs); name != "" {
		t.Fatalf("command after a login without token: %s %s", name, args)
	}
	// 짧은 값(16바이트 미만)은 토큰으로 보지 않는다.
	short, _ := json.Marshal(map[string]string{"tool_output": `{"auth_token":"short"}`})
	username, password = selectedLoginCredential(t)
	msgs = selectedLoginTool(selectedLoginTool(selectedLoginSystem("\n- owned-a: username "+strconv.Quote(username)+", password "+strconv.Quote(password)+"\n"), "anonymous result"), string(short))
	if name, args := selectedFixtureResponseArgs(t, h, msgs); name != "" {
		t.Fatalf("short value accepted as token: %s %s", name, args)
	}
}

// 워크플로 자동 모드 Planner: 초기 탐색 의도 → (결과) 라운드 끝 → 진단 의도 → (결과) 라운드 끝. 진단 의도 요약은 목표 증명이
// 찾는 앞부분으로 시작한다. Worker가 순서를 마치기 전엔 증명하지 않는다.
func TestSelectedAuthFixturePlannerStartsWithDiscovery(t *testing.T) {
	h := modelWithSelectionMode(selectedLoginTarget, true, true, false, selectedLoginHandler(), false, false, false)
	msgs := selectedLoginSystem("")
	names := []string{"add_intent", "prove_goal"}
	name, args := selectedFixtureResponse(t, h, names, msgs)
	if name != "add_intent" || !strings.Contains(args, "initial discovery") {
		t.Fatalf("first intent is not discovery: %s %s", name, args)
	}
	if name, _ = selectedFixtureResponse(t, h, names, selectedLoginTool(msgs, "intent added")); name != "" {
		t.Fatalf("round did not end after the discovery intent: %s", name)
	}
	name, args = selectedFixtureResponse(t, h, names, msgs)
	if name != "add_intent" || !strings.Contains(args, "Observe "+selectedLoginTarget+"/rest/products/search through the enforced Bash boundary") {
		t.Fatalf("second intent is not the diagnostic intent: %s %s", name, args)
	}
	if name, _ = selectedFixtureResponse(t, h, names, selectedLoginTool(msgs, "intent added")); name != "" {
		t.Fatalf("round did not end after the diagnostic intent: %s", name)
	}
	if name, _ = selectedFixtureResponse(t, h, names, msgs); name != "" {
		t.Fatalf("goal handled before the worker finished: %s", name)
	}
}

// 합성 로그인에 쓰는 코드·overlay에는 자격 값이 없다: 값이 든 등록 계정 줄도, 대상 fixture가 아는 시험 자격도 없다.
func TestSelectedLoginSourcesCarryNoCredentialValues(t *testing.T) {
	marker := "synthetic-" + "secret"
	for _, path := range []string{"selected_login.go", "login_model.go", filepath.Join("..", "..", "deploy", "product", "compose.learned-login-acceptance.yaml")} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), marker) || fixtureCredentialLine.Match(raw) {
			t.Fatalf("%s carries a credential value", path)
		}
	}
}

// selectedFixtureResponseArgs는 Worker 요청(Bash 도구) 하나의 응답이다.
func selectedFixtureResponseArgs(t *testing.T, h http.Handler, messages []any) (string, string) {
	t.Helper()
	name, args := selectedFixtureResponse(t, h, []string{"Bash", "submit_diagnostic_candidate"}, messages)
	if name == "" {
		return "", ""
	}
	return name, args
}
