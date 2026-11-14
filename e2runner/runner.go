// Package e2runner assembles an approved execution, a protected per-call proxy
// and a Linux Bash boundary. It neither issues approvals nor creates budgets.
package e2runner

import (
	"context"
	"errors"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
)

var (
	ErrConfig      = errors.New("e2runner: invalid trusted configuration")
	ErrUnavailable = errors.New("e2runner: isolated execution unavailable")
	ErrIncomplete  = errors.New("e2runner: call finalization incomplete")
)

// 작업 공간 파일 도구의 오류다(workspace_files_linux.go).
var (
	// ErrWorkspaceBusy는 같은 intent의 호출이 보관소를 쓰는 중이라 지금 바꿀 수 없다는 뜻이다.
	ErrWorkspaceBusy = errors.New("e2runner: workspace in use by a running call")
	// ErrWorkspaceFile은 경로가 틀렸거나 그 파일·디렉터리가 없다는 뜻이다.
	ErrWorkspaceFile = errors.New("e2runner: workspace path unavailable")
	// ErrWorkspaceFull은 바꾼 결과가 작업 공간 상한을 넘는다는 뜻이다.
	ErrWorkspaceFull = errors.New("e2runner: workspace limit exceeded")
	// ErrWorkspaceEdit은 고칠 문자열이 없거나 하나로 정해지지 않는다는 뜻이다.
	ErrWorkspaceEdit = errors.New("e2runner: edit target not unique")
)

// ClassificationForwardNotAttempted marks allowed policy intent that never reached a forward attempt.
const ClassificationForwardNotAttempted = "forward_not_attempted"

// TrustedResult.Termination 값이다.
const (
	TerminationTimeout          = "timeout"
	TerminationProxyUnavailable = "proxy_unavailable"
)

type Config struct {
	Execution      *e2proxy.Execution
	Helper         string
	RootDir        string
	CACert         string
	Proxy          e2proxy.ProxyConfig
	CommandTimeout time.Duration
	DrainTimeout   time.Duration
	OutputLimit    int
	// PersistWorkspace면 같은 intent의 호출 사이에 /work를 잇는다(C29, docs/workspace-decision.md). 보관은
	// RootDir의 intent별 파일 하나이고, intent가 끝나거나(DrainIntent) Runner가 닫히면 지운다.
	PersistWorkspace bool
}

// TrustedResult is built by the host, separately from the tool's stdout. Final
// means process, proxy and audit closure have all been observed for this call.
type TrustedResult struct {
	Version           string                `json:"version"`
	Execution         e2proxy.ExecutionView `json:"execution"`
	Call              e2proxy.CallResult    `json:"call"`
	ProcessBashExited bool                  `json:"process_bash_exited"`
	ProcessDrained    bool                  `json:"process_drained"`
	ProxyDrained      bool                  `json:"proxy_drained"`
	ExitCode          int                   `json:"exit_code"`
	OutputTruncated   bool                  `json:"output_truncated"`
	Final             bool                  `json:"final"`
	Classification    string                `json:"classification"`
	// Termination은 호스트가 명령을 멈춘 원인 중 E② 결과 분류에서 나눠야 하는 것이다: timeout(명령 시간 상한)·
	// proxy_unavailable(E② 프록시 장애). 둘 다 transient_failure로 분류되지만 시간 상한은 그 명령만 끝나지 않은
	// 것이고 프록시 장애는 집행 장애다(실행 결과 상태 값 계약 열린 항목 2). 그 밖에는 비운다.
	Termination string `json:"termination,omitempty"`
}

type Result struct {
	ToolOutput string        `json:"tool_output"`
	Trusted    TrustedResult `json:"trusted"`
	// Workspace는 작업 공간 보관 결과다(PersistWorkspace일 때만): saved, too_large, not_saved, invalid.
	// 보관하지 못해도 호출 결과는 그대로이고 이전 보관본이 남는다.
	Workspace string `json:"workspace,omitempty"`
	// OutputFile은 tool_output보다 긴 출력의 가린 전체를 남긴 작업 공간 안 경로다(예: .outputs/<call_ref>.txt).
	// 다음 Bash 호출에서 읽을 수 있다. 남기지 못했으면 비어 있다.
	OutputFile string `json:"output_file,omitempty"`
}

// Executor is the Worker seam; it returns program-owned execution evidence
// alongside redacted text, never a claim parsed from Bash's own output.
type Executor interface {
	Run(context.Context, string) (Result, error)
}

func (Runner) String() string   { return "e2runner.Runner[protected]" }
func (Runner) GoString() string { return "e2runner.Runner[protected]" }
func (Config) String() string   { return "e2runner.Config[protected]" }
func (Config) GoString() string { return "e2runner.Config[protected]" }
