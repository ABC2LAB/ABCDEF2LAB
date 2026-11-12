package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/exploresandbox"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// newExploreBashTool은 탐색 직접 Bash 도구다. 원본 Bash처럼 모델이 명령을 쓰고, 실행은 회차에 결속된 격리 실행
// (exploresandbox)이 한다. 대상 요청의 범위·쓰기·예산·기록은 경계가 정하고 이 도구는 판정하지 않는다. 출력 가림과
// 세션 확인은 바깥 explorationTool이 한다.
func newExploreBashTool(shell *exploresandbox.DirectBash) actool.CoreTool {
	originalBash := actool.NewBash()
	limit := shell.CommandTimeout()
	return actool.Build(actool.Spec{
		Name:        "Bash",
		Description: "Run a shell command in the isolated exploration sandbox. Its only network path is the exploration boundary.",
		Prompt:      "Use command and optional timeout_ms. Timeout can only reduce the host limit. Background mode, a browser and the host filesystem are unavailable.",
		Schema: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"command":    map[string]any{"type": "string", "minLength": 1, "maxLength": e2HTTPMaxCommandBytes},
				"timeout_ms": map[string]any{"type": "integer", "minimum": 1, "maximum": limit.Milliseconds()},
			}, "required": []any{"command"},
		},
		Concurrent: func(json.RawMessage) bool { return false },
		Permissions: func(ctx context.Context, input json.RawMessage, pc permission.Context) permission.Decision {
			if originalBash.CheckPermissions(ctx, input, pc).Behavior == permission.Deny {
				return permission.Denied("exploration_bash_safety_floor")
			}
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			// 입력 형식은 진단 Bash와 같다(command, timeout_ms ≤ 설치 상한).
			command, timeout, err := parseE2HTTPBash(input, limit)
			if err != nil {
				return actool.Errorf("exploration_bash_input_invalid"), nil
			}
			if originalBash.CheckPermissions(ctx, input, permission.Context{}).Behavior == permission.Deny {
				return actool.Errorf("exploration_bash_safety_floor"), nil
			}
			result, err := shell.Run(ctx, command, timeout)
			if err != nil {
				// 원문 오류는 모델에 주지 않는다. 정리 실패(격리 실패)면 이 회차의 Bash는 더 열리지 않는다.
				exploration.ReportFailure("tool_call", err)
				if errors.Is(err, exploresandbox.ErrIncomplete) {
					return actool.Errorf("exploration_bash_closed"), nil
				}
				return actool.Errorf("exploration_bash_unavailable"), nil
			}
			return actool.Text(exploreBashOutput(result)), nil
		},
	})
}

func exploreBashOutput(r exploresandbox.Result) string {
	var b strings.Builder
	if r.BashExited {
		fmt.Fprintf(&b, "exit_code=%d\n", r.ExitCode)
	} else {
		reason := r.Reason
		if reason == "" {
			reason = "stopped"
		}
		fmt.Fprintf(&b, "command_not_finished=%s\n", reason)
	}
	b.Write(r.Output)
	if r.OutputTruncated {
		b.WriteString("\n[output truncated at the host limit]")
	}
	return b.String()
}
