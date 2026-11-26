package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// switch_browser_account는 등록 계정 라벨이나 빈 값(로그인 없이)만 받고, 받은 값 그대로 문맥 바꾸기를 부른다. 형식 밖·모르는
// 라벨은 부르지 않는다. 바꾸기가 실패하면 고정 오류다.
func TestExploreSwitchToolInput(t *testing.T) {
	var got []string
	fail := false
	tool := newExploreSwitchTool([]string{"tester-a", "tester-b"}, func(_ context.Context, account string) error {
		got = append(got, account)
		if fail {
			return errors.New("x")
		}
		return nil
	})
	call := func(in string) (string, bool) {
		r, err := tool.Call(t.Context(), json.RawMessage(in), nil)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(r)
		return string(b), r.IsError
	}
	if out, isErr := call(`{"account":"tester-b"}`); isErr || !strings.Contains(out, "tester-b") {
		t.Fatalf("등록 계정: %s", out)
	}
	if _, isErr := call(`{"account":""}`); isErr {
		t.Fatal("로그인 없이 바꾸기를 거부함")
	}
	for _, bad := range []string{`{"account":"admin"}`, `{"account":"tester-a","x":1}`, `{}`, `{"account":1}`} {
		if _, isErr := call(bad); !isErr {
			t.Fatalf("형식 밖 입력을 받음: %s", bad)
		}
	}
	if strings.Join(got, ",") != "tester-b," {
		t.Fatalf("바꾸기 호출: %q", got)
	}
	fail = true
	if out, isErr := call(`{"account":"tester-a"}`); !isErr || !strings.Contains(out, "browser_switch_unavailable") {
		t.Fatalf("바꾸기 실패: %s", out)
	}
}

// 등록 계정과 문맥 바꾸기가 있는 탐색만 '한 문맥 한 계정' 안내를 받는다.
func TestExplorationPromptBrowserAccountsLine(t *testing.T) {
	creds := []E2HTTPAccountCredential{{Label: "tester-a", Username: "user1", Password: "alpha-pass-1"}}
	if got := explorationSystemPrompt(false, false, creds, true); !strings.Contains(got, explorationBrowserAccounts) {
		t.Fatal("문맥 바꾸기 안내 없음")
	}
	if got := explorationSystemPrompt(false, false, creds, false); strings.Contains(got, "switch_browser_account") {
		t.Fatal("도구 없는 탐색에 문맥 바꾸기 안내")
	}
	if got := explorationSystemPrompt(false, false, nil, true); strings.Contains(got, "switch_browser_account") {
		t.Fatal("계정 없는 탐색에 문맥 바꾸기 안내")
	}
}
