package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// refresh_session은 세션 목록의 라벨만 호스트에 넘기고, 성공하면 새 세션 헤더를, 실패하면 결과 코드만 돌려준다.
func TestE2RefreshSessionTool(t *testing.T) {
	var asked []string
	result := "authenticated"
	tool := newE2RefreshSessionTool([]string{"tester-a"}, func(_ context.Context, label string) (http.Header, string) {
		asked = append(asked, label)
		if result != "authenticated" {
			return nil, result
		}
		return http.Header{"Cookie": {"session=fresh-value"}}, result
	})
	run := func(input string) (string, bool) {
		out, err := tool.Call(context.Background(), json.RawMessage(input), nil)
		if err != nil {
			t.Fatal(err)
		}
		return out.Flatten(), out.IsError
	}
	if text, isErr := run(`{"account":"tester-a"}`); isErr || !strings.Contains(text, "- tester-a: Cookie: session=fresh-value") {
		t.Fatalf("refresh output: %q err=%v", text, isErr)
	}
	for _, input := range []string{`{"account":"someone-else"}`, `{}`, `not json`} {
		if text, isErr := run(input); !isErr || !strings.Contains(text, "refresh_session_unknown_account") {
			t.Fatalf("%s: %q", input, text)
		}
	}
	for _, status := range []string{"refresh_limit", "refresh_cooldown"} {
		result = status
		if text, isErr := run(`{"account":"tester-a"}`); !isErr || !strings.Contains(text, "refresh_session_"+status) || strings.Contains(text, "fresh-value") {
			t.Fatalf("%s output: %q", status, text)
		}
	}
	if strings.Join(asked, ",") != "tester-a,tester-a,tester-a" {
		t.Fatalf("host asked for %v", asked)
	}
	if tool.Name() != "refresh_session" {
		t.Fatal(tool.Name())
	}
}

// 실제 SDK 세션에서 refresh_session 실패 결과를 읽은 뒤 다른 Bash 명령을 처리한다.
// 보호 저널·실행은 실제 구현, 모델과 Bash 실행만 fixture이며 외부 LLM은 호출하지 않는다.
func TestE2RefreshFailureOnlyFailsCommand(t *testing.T) {
	for _, status := range []string{"refresh_limit", "refresh_cooldown", "login_failed", "session_not_found", "not_sent"} {
		t.Run(status, func(t *testing.T) {
			execution, _, _ := e2WorkerFixture(t)
			provider := &e2RefreshThenBashProvider{}
			worker := NewWorker(provider, "synthetic", t.TempDir(), nil, 10000, 4)
			state := &e2HTTPToolState{}
			refreshes, commands := 0, 0
			refresh := newE2RefreshSessionTool([]string{"tester-a"}, func(context.Context, string) (http.Header, string) {
				refreshes++
				return nil, status
			})
			bash := e2WorkerBashTool(t, execution, func(context.Context, string) (e2runner.Result, error) {
				commands++
				return completedE2WorkerResult(), nil
			}, time.Second, state)
			opts := agentcore.Options{Provider: provider, Tools: []actool.CoreTool{refresh, bash}, DisableBackgroundTasks: true, PermissionMode: permission.ModeBypass, MaxTurns: 4, MaxDuration: 5 * time.Second, NonStreaming: true}
			reason, _, err := worker.runE2HTTPSession(t.Context(), "worker", &db.Node{ID: 22, Kind: db.KindIntent}, nil, opts, "assigned intent", execution, state, nil)
			if err != nil || reason != harness.ReasonCompleted || refreshes != 1 || commands != 1 || provider.calls != 3 || !strings.Contains(provider.refreshResult, "refresh_session_"+status) {
				t.Fatalf("reason=%s refreshes=%d commands=%d turns=%d result=%q err=%v", reason, refreshes, commands, provider.calls, provider.refreshResult, err)
			}
		})
	}
}

type e2RefreshThenBashProvider struct {
	e2WorkerProvider
	refreshResult string
}

func (p *e2RefreshThenBashProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls++
	if p.calls == 2 {
		for _, message := range req.Messages {
			for _, block := range message.Content {
				if block.Type == llm.BlockToolResult {
					for _, content := range block.Content {
						p.refreshResult += content.Text
					}
				}
			}
		}
	}
	name, input := "refresh_session", `{"account":"tester-a"}`
	if p.calls == 2 {
		name, input = "Bash", `{"command":"echo permitted"}`
	} else if p.calls > 2 {
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("done")}}, "end_turn", llm.Usage{}, nil
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: name, Name: name, Input: json.RawMessage(input)}}}, "tool_use", llm.Usage{}, nil
}

// 세션 줄 안내는 직접 로그인하지 말고 refresh_session을 쓰라고 말한다.
func TestE2HTTPSessionLinesPointToRefresh(t *testing.T) {
	lines := e2HTTPSessionLines(map[string]http.Header{"tester-a": {"Cookie": {"session=x"}}})
	if !strings.Contains(lines, "refresh_session") || !strings.Contains(lines, "do not try to log in") {
		t.Fatal(lines)
	}
}

// 세션과 다시 로그인 경로가 둘 다 있을 때만 도구가 붙고, 진단 Worker 프로필에서 맨 끝에 온다. 없으면 도구 목록은 그대로다.
func TestE2RefreshSessionBindingAndProfile(t *testing.T) {
	refresh := E2HTTPRefreshSession(func(context.Context, string) (http.Header, string) { return nil, "" })
	sessions := map[string]http.Header{"tester-a": {"Cookie": {"session=x"}}}
	if e2RefreshSessionBinding(E2HTTPExecution{Sessions: sessions}) != nil || e2RefreshSessionBinding(E2HTTPExecution{RefreshSession: refresh}) != nil {
		t.Fatal("refresh_session bound without sessions or a host path")
	}
	tool := e2RefreshSessionBinding(E2HTTPExecution{Sessions: sessions, RefreshSession: refresh})
	if tool == nil {
		t.Fatal("refresh_session missing for a test account run")
	}
	bash := e2WorkerFixtureTool("Bash", nil)
	with, err := diagnosisWorkerProfile.selectTools(nil, map[string]actool.CoreTool{"Bash": bash, "refresh_session": tool}, nil)
	if err != nil || with[len(with)-1].Name() != "refresh_session" {
		t.Fatalf("profile with refresh: %v", err)
	}
	without, err := diagnosisWorkerProfile.selectTools(nil, map[string]actool.CoreTool{"Bash": bash}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range without {
		if tool.Name() == "refresh_session" {
			t.Fatal("refresh_session in a run without test account sessions")
		}
	}
}
