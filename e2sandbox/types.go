// Package e2sandbox owns a call's Linux process and filesystem boundary.
// It never receives an approval or proxy attribution secret.
package e2sandbox

import (
	"errors"
	"time"
)

// 시작 중지는 오류 문자열이 아니라 호스트의 문맥·프로세스 종료 근거로만 만든다.
type startupInterruption struct{ cause error }

func (e *startupInterruption) Error() string { return "e2sandbox: startup interrupted" }
func (e *startupInterruption) Unwrap() error { return e.cause }

// StartupInterruption은 시작 전에 관측했거나 실제로 중지시킨 초기화의 문맥 원인이다.
// 호출자는 별도로 Process의 Stop·Wait·Close와 감사 마감을 확인해야 한다.
func StartupInterruption(err error) error {
	var stopped *startupInterruption
	if errors.As(err, &stopped) {
		return stopped.cause
	}
	return nil
}

var (
	ErrConfig      = errors.New("e2sandbox: invalid configuration")
	ErrUnavailable = errors.New("e2sandbox: isolation unavailable")
	ErrState       = errors.New("e2sandbox: invalid lifecycle state")
	ErrStopped     = errors.New("e2sandbox: stopped")
)

// Config contains only public paths and tool input. Workspace must be an
// empty, private, caller-owned lifecycle directory dedicated to this call.
// Tool files live in a bounded tmpfs. They are written back only as the bounded
// WorkspaceIn/Workspace archive (regular files and directories). Helper is a
// trusted, prebuilt e2-bash-sandbox executable, never a model-selected binary.
// The caller must bind ProxySocket to this Process before calling Start.
type Config struct {
	// Zero resource values use bounded defaults. Scratch is an ephemeral tmpfs;
	// Workspace is only a private host lifecycle directory and stays empty.
	ScratchBytes, TempBytes, MaxFileBytes uint64
	// MaxProcesses caps all tasks in the isolated user namespace, including
	// the trusted helper; it is not an extra Bash-process allowance.
	MaxProcesses, CPUSeconds, OpenFiles uint64
	// MaxCommandProcesses optionally lowers the command's hard NPROC limit
	// below the helper's, preserving task headroom for the trusted relay.
	// Zero keeps the existing shared limit.
	MaxCommandProcesses uint64
	Helper              string
	Workspace           string
	ProxySocket         string
	CACert              string
	Command             string
	Timeout             time.Duration
	DrainTimeout        time.Duration
	OutputLimit         int
	// WorkspaceIn은 /work에 미리 풀 작업 공간 tar다(packWorkspace 형식, 비면 빈 작업 공간). SaveWorkspace면
	// 명령과 그 하위 프로세스가 모두 끝난 뒤 /work를 묶어 Result.Workspace로 돌려준다(docs/workspace-decision.md).
	WorkspaceIn   []byte
	SaveWorkspace bool
}

// Result is process evidence, not an HTTP policy or vulnerability decision.
// Output is untrusted tool text and must be redacted before model/UI use.
type Result struct {
	ExitCode        int
	Output          []byte
	OutputTruncated bool
	BashExited      bool
	Drained         bool
	HelperFailed    bool
	Reason          string
	// Workspace는 SaveWorkspace일 때 마감된 /work의 tar다. WorkspaceState가 "saved"일 때만 있다.
	// "too_large"는 상한을 넘어 묶지 않음, "not_saved"는 정상 마감이 아니라 묶지 않음이다.
	Workspace      []byte
	WorkspaceState string
}

// WorkspaceEntry는 작업 공간 항목 하나다(파일 도구 LS용). 이름은 작업 공간 기준 상대 경로의 마지막 조각이다.
type WorkspaceEntry struct {
	Name string
	Dir  bool
	Size int64
}
