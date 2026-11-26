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
	TerminationCanceled         = "canceled"
)

type Config struct {
	// BashSlots는 신뢰 호스트가 Worker 수와 승인 HTTP 동시성의 최솟값으로 정한다. 0은 기존 1개다.
	BashSlots int

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
	// SelectedProof is populated only after the E² Host authenticates this
	// ordinary result against its current journal head. It is never copied to
	// the generic tool/model result JSON.
	SelectedProof *SelectedAuditProof `json:"-"`
	// AdmissionNotSent is the acknowledged control receipt for a command that
	// never acquired a Bash slot or started a Call/process/proxy.
	AdmissionNotSent  *e2proxy.Receipt      `json:"admission_not_sent,omitempty"`
	ProcessLimits     ProcessLimits         `json:"process_limits"`
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
	// proxy_unavailable(E② 프록시 장애)·canceled(확인된 초기화 중지). 시간 상한과 프록시 장애는 transient_failure로 분류되지만 시간 상한은 그 명령만 끝나지 않은
	// 것이고 프록시 장애는 집행 장애다(실행 결과 상태 값 계약 열린 항목 2). 그 밖에는 비운다.
	Termination string `json:"termination,omitempty"`
}

type SelectedAuditProof struct {
	Event      e2proxy.Event             `json:"event"`
	Checkpoint e2proxy.JournalCheckpoint `json:"checkpoint"`
}

// ProcessLimits are host-selected run conditions, never model input. The
// product preparation service has 256 tasks; reserve 64 for the engine (measured
// peak 18, plus headroom; docs/workspace-decision.md §7). A single call retains
// the sandbox's existing 128-task ceiling. Tasks include helper/child threads.
type ProcessLimits struct {
	BashSlots          int    `json:"bash_slots"`
	ContainerTasks     uint64 `json:"container_tasks"`
	EngineReserveTasks uint64 `json:"engine_reserve_tasks"`
	CallTasks          uint64 `json:"call_tasks"`
	CommandTasks       uint64 `json:"command_tasks"`
	HelperReserveTasks uint64 `json:"helper_reserve_tasks"`
}

func processLimits(slots int) (ProcessLimits, error) {
	const container, reserve, maximum, minimum, helperReserve = 256, 64, 128, 8, 16
	if slots < 1 || slots > (container-reserve)/(minimum+helperReserve) {
		return ProcessLimits{}, ErrConfig
	}
	l := ProcessLimits{BashSlots: slots, ContainerTasks: container, EngineReserveTasks: reserve, CallTasks: min(maximum, uint64((container-reserve)/slots))}
	// 한 슬롯에서도 명령이 relay·마감 스레드의 몫을 소비하면 안 된다.
	l.HelperReserveTasks = helperReserve
	l.CommandTasks = l.CallTasks - l.HelperReserveTasks
	return l, nil
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

func AdmissionNotSent(result Result) bool {
	t := result.Trusted
	r := t.AdmissionNotSent
	return r != nil && t.Version == e2proxy.Version && t.Classification == "not_sent" &&
		t.Final && !t.ProcessBashExited && t.ProcessDrained && t.ProxyDrained &&
		t.Execution.AuditComplete && e2proxy.EqualCallResults(t.Call, e2proxy.CallResult{}) && r.Version == e2proxy.Version &&
		r.Program == "" && r.Scope == t.Execution.Scope && r.IntentID == t.Execution.IntentID && r.PolicyRevision == t.Execution.PolicyRevision &&
		r.NotSent && r.AuditComplete && !r.ForwardAttempted && !r.RequestWritten && !r.ResponseReceived &&
		!r.ResponseComplete && r.CallRef == "" && r.CommandRef != "" && r.AttemptID == "" && r.Sequence == 0 && !r.At.IsZero()
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
