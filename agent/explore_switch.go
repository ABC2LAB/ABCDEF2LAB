package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	actool "github.com/Autumn-27/norma/tool"
)

// exploreSwitchToolName은 탐색 브라우저 문맥을 바꾸는 도구다(사용자 결정 2026-11-26 (나): 한 번에 한 계정 문맥).
const exploreSwitchToolName = "switch_browser_account"

const exploreSwitchInputLimit = 256

// explorationBrowserAccounts는 등록 계정과 문맥 바꾸기 도구가 있는 세션에만 붙는 안내다.
const explorationBrowserAccounts = `Use one registered account per browser context. The browser starts without any login. To use a different account, or to browse without login again, call switch_browser_account first: it closes the current browser context and opens a fresh one without cookies, so log in again as the chosen account. Logging in as another account in the same browser context is refused (browser_account_in_use).`

// newExploreSwitchTool은 브라우저 문맥을 닫고 새 문맥을 여는 도구다. account는 등록 계정 라벨이거나 빈 값(로그인 없이)이다.
// 이 도구는 대상에 요청을 보내지 않는다. 새 문맥에는 쿠키가 없으므로 모델이 그 계정으로 다시 로그인한다.
func newExploreSwitchTool(labels []string, switchBrowser func(context.Context, string) error) actool.CoreTool {
	enum := append([]string{""}, labels...)
	return actool.Build(actool.Spec{
		Name: exploreSwitchToolName,
		Description: "Close the current browser context and open a fresh one without cookies, to use another registered account (or none). " +
			"Give the account label to use next, or an empty string to browse without login. This tool sends no request to the target. " +
			"After switching, log in again in the new context if you need the account.",
		Schema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"account": map[string]any{"type": "string", "enum": enum},
		}, "required": []string{"account"}},
		Concurrent: func(json.RawMessage) bool { return false },
		Run: func(ctx context.Context, input json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var in struct {
				Account *string `json:"account"`
			}
			decoder := json.NewDecoder(bytes.NewReader(input))
			decoder.DisallowUnknownFields()
			if len(input) > exploreSwitchInputLimit || decoder.Decode(&in) != nil || in.Account == nil {
				return actool.Errorf("browser_switch_input_invalid"), nil
			}
			if _, err := decoder.Token(); err != io.EOF {
				return actool.Errorf("browser_switch_input_invalid"), nil
			}
			known := false
			for _, label := range enum {
				known = known || label == *in.Account
			}
			if !known {
				return actool.Errorf("browser_switch_account_unknown"), nil
			}
			if err := switchBrowser(ctx, *in.Account); err != nil {
				return actool.Errorf("browser_switch_unavailable"), nil
			}
			if *in.Account == "" {
				return actool.Text("Opened a fresh browser context without login."), nil
			}
			return actool.Text("Opened a fresh browser context for account " + *in.Account + ". It has no cookies: log in as " + *in.Account + " on the target's login page."), nil
		},
	})
}
