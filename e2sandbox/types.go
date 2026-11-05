// Package e2sandbox owns a call's Linux process and filesystem boundary.
// It never receives an approval or proxy attribution secret.
package e2sandbox

import (
	"errors"
	"time"
)

var (
	ErrConfig      = errors.New("e2sandbox: invalid configuration")
	ErrUnavailable = errors.New("e2sandbox: isolation unavailable")
	ErrState       = errors.New("e2sandbox: invalid lifecycle state")
	ErrStopped     = errors.New("e2sandbox: stopped")
)

// Config contains only public paths and tool input. Workspace must be an
// empty, private, caller-owned lifecycle directory dedicated to this call.
// Tool files live in a bounded tmpfs and are not written back. Helper is a
// trusted, prebuilt e2-bash-sandbox executable, never a model-selected binary.
// The caller must bind ProxySocket to this Process before calling Start.
type Config struct {
	// Zero resource values use bounded defaults. Scratch is an ephemeral tmpfs;
	// Workspace is only a private host lifecycle directory and stays empty.
	ScratchBytes, TempBytes, MaxFileBytes uint64
	// MaxProcesses caps all tasks in the isolated user namespace, including
	// the trusted helper; it is not an extra Bash-process allowance.
	MaxProcesses, CPUSeconds, OpenFiles uint64
	Helper                              string
	Workspace                           string
	ProxySocket                         string
	CACert                              string
	Command                             string
	Timeout                             time.Duration
	DrainTimeout                        time.Duration
	OutputLimit                         int
}

// Result is process evidence, not an HTTP policy or vulnerability decision.
// Output is untrusted tool text and must be redacted before model/UI use.
type Result struct {
	ExitCode        int
	Output          []byte
	OutputTruncated bool
	BashExited      bool
	Drained         bool
	Reason          string
}
