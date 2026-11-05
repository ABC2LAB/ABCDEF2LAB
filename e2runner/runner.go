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

type Config struct {
	Execution      *e2proxy.Execution
	Helper         string
	RootDir        string
	CACert         string
	Proxy          e2proxy.ProxyConfig
	CommandTimeout time.Duration
	DrainTimeout   time.Duration
	OutputLimit    int
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
}

type Result struct {
	ToolOutput string        `json:"tool_output"`
	Trusted    TrustedResult `json:"trusted"`
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
