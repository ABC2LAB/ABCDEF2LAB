package exploration

import (
	"context"
	"errors"
	"log"
)

var ErrToolResult = errors.New("exploration_tool_result_error")

// Failure는 값·원문 오류·세션 참조 없이 단계와 고정 분류만 보존한다.
type Failure struct {
	Stage string
	Kind  string
}

func (f Failure) Error() string        { return "exploration_" + f.Stage + "_" + f.Kind }
func (f Failure) Is(target error) bool { return target == ErrUnavailable }
func ReportFailure(stage string, err error) Failure {
	switch stage {
	case "boundary_open", "browser_start", "mcp_connect", "catalog", "status", "watch_close", "open_cleanup", "worker_run", "tool_admission", "tool_call", "browser_close", "finalize", "mcp_delete", "browser_lease", "boundary_close":
	default:
		stage = "operation"
	}
	kind := "operation_failed"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		kind = "deadline"
	case errors.Is(err, context.Canceled):
		kind = "cancelled"
	case errors.Is(err, ErrBinding):
		kind = "binding_invalid"
	case errors.Is(err, ErrClosed):
		kind = "session_closed"
	case errors.Is(err, ErrToolResult):
		kind = "tool_error"
	}
	failure := Failure{Stage: stage, Kind: kind}
	log.Printf("[exploration] 탐색 단계 실패: 단계=%s 분류=%s", failure.Stage, failure.Kind)
	return failure
}
