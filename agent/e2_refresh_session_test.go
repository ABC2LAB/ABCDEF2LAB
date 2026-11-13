package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

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
	result = "refresh_limit"
	if text, isErr := run(`{"account":"tester-a"}`); !isErr || !strings.Contains(text, "refresh_session_refresh_limit") || strings.Contains(text, "fresh-value") {
		t.Fatalf("limit output: %q", text)
	}
	if len(asked) != 2 || asked[0] != "tester-a" || asked[1] != "tester-a" {
		t.Fatalf("host asked for %v", asked)
	}
	if tool.Name() != "refresh_session" {
		t.Fatal(tool.Name())
	}
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
