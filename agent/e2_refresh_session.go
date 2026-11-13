package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// E2HTTPRefreshSession은 시험 계정 하나를 호스트의 인증 준비 경로로 다시 로그인시킨다(C14 결정 2·6). 라벨만 받고
// 자격 값은 모델에 오지 않는다. 결과는 새 세션 헤더와 인증 준비 결과 값(authenticated·login_failed·session_not_found·
// result_unknown·not_sent·refresh_limit)이다. 상한은 실행 전체에서 계정당이고 실패한 로그인도 센다.
type E2HTTPRefreshSession func(ctx context.Context, label string) (http.Header, string)

const e2RefreshSessionDescription = "Logs one owned test account in again through the host and returns its new session headers. " +
	"Use it only when that account's session has stopped working (the target treats requests with its headers as logged out). " +
	"Give only the account label from the test account sessions list; the host holds the credentials. Attempts are limited per account " +
	"and failed logins count. After it succeeds, send the returned headers instead of the old ones."

// newE2RefreshSessionTool은 시험 계정 세션이 있는 Worker에만 붙인다. 모르는 라벨은 호스트에 넘기지 않는다.
func newE2RefreshSessionTool(labels []string, refresh E2HTTPRefreshSession) actool.CoreTool {
	known := map[string]bool{}
	for _, label := range labels {
		known[label] = true
	}
	return actool.Build(actool.Spec{
		Name:        "refresh_session",
		Description: e2RefreshSessionDescription,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account": map[string]any{"type": "string", "description": "The test account label whose session expired."},
			},
			"required": []any{"account"},
		},
		// 로그인은 E② 요청 하나이므로 Bash처럼 하나씩 한다.
		Concurrent: func(json.RawMessage) bool { return false },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var in struct {
				Account string `json:"account"`
			}
			if json.Unmarshal(input, &in) != nil || !known[in.Account] {
				return actool.Errorf("refresh_session_unknown_account"), nil
			}
			header, result := refresh(ctx, in.Account)
			if result != "authenticated" || len(header) == 0 {
				if result == "" {
					result = "not_sent"
				}
				return actool.Errorf("refresh_session_" + result), nil
			}
			return actool.Text(e2RefreshedSessionText(in.Account, header)), nil
		},
	})
}

// e2RefreshSessionBinding은 시험 계정 세션과 다시 로그인 경로가 둘 다 있는 Worker에만 refresh_session을 준다.
// 라벨은 세션 목록의 것만 받는다.
func e2RefreshSessionBinding(binding E2HTTPExecution) actool.CoreTool {
	if binding.RefreshSession == nil || len(binding.Sessions) == 0 {
		return nil
	}
	labels := make([]string, 0, len(binding.Sessions))
	for label := range binding.Sessions {
		labels = append(labels, label)
	}
	return newE2RefreshSessionTool(labels, binding.RefreshSession)
}

func e2RefreshedSessionText(label string, header http.Header) string {
	names := make([]string, 0, len(header))
	for name := range header {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("New session for " + label + " (send these headers for this account from now on, only to the approved origin):\n")
	for _, name := range names {
		for _, value := range header[name] {
			b.WriteString("- " + label + ": " + name + ": " + value + "\n")
		}
	}
	return b.String()
}
