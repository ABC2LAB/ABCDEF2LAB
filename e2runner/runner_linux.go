//go:build linux

package e2runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2sandbox"
)

const (
	maxCommandBytes         = 1 << 20
	maxOutputBytes          = 4 << 20
	maxRunnerCommandTimeout = time.Hour
	maxRunnerDrainTimeout   = 2 * time.Minute
	maxCACertificateBytes   = 1 << 20
)

// Linux sockaddr_un.sun_path is 108 bytes including its terminating NUL.
// Keep one extra byte of headroom for the pathname representation passed by Go.
const maxUnixSocketPathBytes = 106

type Runner struct{ *runnerState }

type runnerState struct {
	cfg           Config
	caCertificate []byte
	mu            sync.Mutex
	stopped       bool
	active        map[string]*runningCall
	callGate      chan struct{}
	stop          chan struct{}
	stopOnce      sync.Once
	draining      bool
	drainGate     chan struct{}
	intentDrains  map[int64]*intentDrainState
}

type intentDrainState struct {
	gate       chan struct{}
	active     bool
	fenced     bool
	generation uint64
}

type runningCall struct {
	owner           *Runner
	intentID        int64
	call            *e2proxy.Call
	ready           chan struct{}
	cleanupGate     chan struct{}
	dir             string
	listener        *net.UnixListener
	proxy           *e2proxy.Proxy
	process         *e2sandbox.Process
	serveDone       chan struct{}
	serveErr        error
	initFailed      bool
	termination     string
	terminationMu   sync.Mutex
	resourcesClosed bool
	result          Result
	releaseSlot     func()
}

func New(cfg Config) (*Runner, error) {
	if cfg.Execution == nil || cfg.CommandTimeout <= 0 || cfg.CommandTimeout > maxRunnerCommandTimeout ||
		cfg.DrainTimeout <= 0 || cfg.DrainTimeout > maxRunnerDrainTimeout || cfg.OutputLimit <= 0 || cfg.OutputLimit > maxOutputBytes ||
		e2proxy.ValidateProxyConfig(cfg.Proxy) != nil || !trustedPath(cfg.RootDir, true) || !trustedPath(cfg.Helper, false) ||
		!trustedPath(cfg.CACert, false) {
		return nil, ErrConfig
	}
	// No secrets enter the helper. The public trust anchor must be exactly the
	// one whose private key is retained by the host's inspection proxy.
	f, err := os.Open(cfg.CACert)
	if err != nil {
		return nil, ErrConfig
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxCACertificateBytes))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || len(data) >= maxCACertificateBytes || len(cfg.Proxy.CA.Certificate) == 0 {
		return nil, ErrConfig
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 ||
		len(bytes.TrimSpace(rest)) != 0 || !bytes.Equal(block.Bytes, cfg.Proxy.CA.Certificate[0]) {
		return nil, ErrConfig
	}
	st, err := os.Stat(cfg.Helper)
	if err != nil || st.Mode().Perm()&0111 == 0 {
		return nil, ErrConfig
	}
	if ValidateRootDirLayout(cfg.RootDir) != nil {
		return nil, ErrConfig
	}
	return &Runner{runnerState: &runnerState{cfg: cfg, caCertificate: pem.EncodeToMemory(block), active: make(map[string]*runningCall), callGate: make(chan struct{}, 1), stop: make(chan struct{}), drainGate: make(chan struct{}, 1), intentDrains: make(map[int64]*intentDrainState)}}, nil
}

func trustedPath(path string, directory bool) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory {
		return false
	}
	if !directory && !info.Mode().IsRegular() {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !(stat.Uid == uint32(os.Getuid()) || !directory && stat.Uid == 0) {
		return false
	}
	if directory {
		return info.Mode().Perm() == 0700
	}
	return info.Mode().Perm()&0022 == 0
}

func commandRef() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", ErrUnavailable
	}
	return "command_" + hex.EncodeToString(nonce[:]), nil
}

func (r *Runner) Run(ctx context.Context, command string) (Result, error) {
	if r == nil || r.runnerState == nil {
		return Result{}, ErrConfig
	}
	return r.RunForIntent(ctx, r.cfg.Execution.Snapshot().IntentID, command)
}

func (r *Runner) RunForIntent(ctx context.Context, intentID int64, command string) (Result, error) {
	if r == nil || r.runnerState == nil || ctx == nil || ctx.Err() != nil || intentID <= 0 || command == "" || len(command) > maxCommandBytes || strings.ContainsRune(command, 0) {
		return Result{}, ErrConfig
	}
	if !r.cfg.Execution.HasIntent(intentID) {
		return Result{}, ErrUnavailable
	}
	r.mu.Lock()
	admissionGeneration := r.intentGenerationLocked(intentID)
	temporarilyBlocked := r.stopped || r.draining || r.intentFencedLocked(intentID)
	r.mu.Unlock()
	if temporarilyBlocked {
		return Result{}, ErrUnavailable
	}
	// One installed Runner is shared by every Worker for this execution.
	// A queued caller acquires neither a Call nor a fresh budget. The slot is
	// retained across an incomplete drain and released only by final cleanup.
	select {
	case r.callGate <- struct{}{}:
	case <-ctx.Done():
		return Result{}, ErrUnavailable
	case <-r.stop:
		return Result{}, ErrUnavailable
	}
	var releaseOnce sync.Once
	releaseSlot := func() { releaseOnce.Do(func() { <-r.callGate }) }
	stateOwnsSlot := false
	defer func() {
		if !stateOwnsSlot {
			releaseSlot()
		}
	}()
	r.mu.Lock()
	stopped := r.stopped || r.draining || r.intentFencedLocked(intentID) || r.intentGenerationLocked(intentID) != admissionGeneration
	r.mu.Unlock()
	if stopped {
		return Result{}, ErrUnavailable
	}
	ref, err := commandRef()
	if err != nil {
		return Result{}, err
	}
	runCtx, cancel := context.WithTimeout(ctx, r.cfg.CommandTimeout)
	defer cancel()
	// Publish admission atomically with Close's snapshot, so Close cannot
	// release execution resources while an unregistered call is being created.
	r.mu.Lock()
	if r.stopped || r.draining || r.intentFencedLocked(intentID) || r.intentGenerationLocked(intentID) != admissionGeneration {
		r.mu.Unlock()
		return Result{}, ErrUnavailable
	}
	call, err := r.cfg.Execution.BeginCallForIntent(runCtx, intentID, ref)
	if err != nil {
		r.mu.Unlock()
		return Result{}, ErrUnavailable
	}
	state := &runningCall{owner: r, intentID: intentID, call: call, ready: make(chan struct{}), cleanupGate: make(chan struct{}, 1), releaseSlot: releaseSlot}
	stateOwnsSlot = true
	r.active[call.Ref()] = state
	stopped = r.stopped
	r.mu.Unlock()
	if stopped {
		state.initFailed = true
		state.setTermination("stopped")
	} else {
		if state.initialize(runCtx, command) != nil {
			state.initFailed = true
			state.setTermination("isolation_unavailable")
		}
	}
	close(state.ready)
	if !state.initFailed {
		processWait := make(chan struct{})
		go func() { _, _ = state.process.Wait(runCtx); close(processWait) }()
		select {
		case <-state.process.BashDone():
			state.setTermination("bash_exited")
		case <-state.call.Context().Done():
			state.setTermination("call_stopped")
		case <-runCtx.Done():
			state.setTermination("canceled")
			if runCtx.Err() == context.DeadlineExceeded {
				state.setTermination("timeout")
			}
		case <-processWait:
			state.setTermination("process_exited")
		case <-state.serveDone:
			state.setTermination("proxy_unavailable")
		}
	}
	drainCtx, drainCancel := context.WithTimeout(context.WithoutCancel(ctx), r.cfg.DrainTimeout)
	result, cleanupErr := state.cleanup(drainCtx)
	drainCancel()
	if cleanupErr != nil {
		return result, ErrIncomplete
	}
	if state.initFailed || !result.Trusted.ProcessBashExited || result.Trusted.ExitCode != 0 {
		return result, ErrUnavailable
	}
	return result, nil
}

func (s *runningCall) initialize(ctx context.Context, command string) error {
	dir, err := os.MkdirTemp(s.owner.cfg.RootDir, "call-")
	if err != nil {
		return ErrUnavailable
	}
	s.dir = dir
	if os.Chmod(dir, 0700) != nil {
		return ErrUnavailable
	}
	workspace := filepath.Join(dir, "work")
	if os.Mkdir(workspace, 0700) != nil {
		return ErrUnavailable
	}
	caPath := filepath.Join(dir, "ca.pem")
	caFile, err := os.OpenFile(caPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrUnavailable
	}
	written, writeErr := caFile.Write(s.owner.caCertificate)
	closeErr := caFile.Close()
	if writeErr != nil || closeErr != nil || written != len(s.owner.caCertificate) {
		return ErrUnavailable
	}
	socket := filepath.Join(dir, "proxy.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return ErrUnavailable
	}
	listener.SetUnlinkOnClose(false)
	s.listener = listener
	if os.Chmod(socket, 0600) != nil {
		return ErrUnavailable
	}
	process, err := e2sandbox.Launch(s.call.Context(), e2sandbox.Config{
		Helper: s.owner.cfg.Helper, Workspace: workspace, ProxySocket: socket, CACert: caPath,
		Command: command, Timeout: s.owner.cfg.CommandTimeout, DrainTimeout: s.owner.cfg.DrainTimeout,
		OutputLimit: s.owner.cfg.OutputLimit,
	})
	if err != nil {
		return ErrUnavailable
	}
	s.process = process
	proxy, err := e2proxy.NewProxy(s.call, s.owner.cfg.Proxy)
	if err != nil {
		return ErrUnavailable
	}
	s.proxy = proxy
	s.serveDone = make(chan struct{})
	go func() { s.serveErr = proxy.ServeUnix(listener, process); close(s.serveDone) }()
	select {
	case <-s.serveDone:
		return ErrUnavailable
	default:
	}
	if process.Start(s.call.Context()) != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *runningCall) cleanup(ctx context.Context) (Result, error) {
	select {
	case s.cleanupGate <- struct{}{}:
		defer func() { <-s.cleanupGate }()
	case <-ctx.Done():
		return Result{Trusted: TrustedResult{Version: e2proxy.Version, Execution: s.owner.cfg.Execution.SnapshotForIntent(s.call.IntentID()), Call: s.call.Result(), Classification: "result_unknown"}}, ErrIncomplete
	}
	if s.resourcesClosed {
		if s.releaseSlot != nil {
			s.releaseSlot()
		}
		return s.result, nil
	}
	s.call.Block()
	trusted := TrustedResult{Version: e2proxy.Version, Call: s.call.Result(), ExitCode: -1}
	var output []byte
	processClosed := s.process == nil
	if s.process != nil {
		_ = s.process.Stop(ctx)
		processResult, err := s.process.Wait(ctx)
		trusted.ProcessBashExited = processResult.BashExited
		trusted.ProcessDrained = err == nil && processResult.Drained
		if trusted.ProcessBashExited && trusted.ProcessDrained {
			trusted.ExitCode = processResult.ExitCode
		}
		trusted.OutputTruncated = processResult.OutputTruncated
		output = processResult.Output
		processClosed = trusted.ProcessDrained
		if processResult.Reason == "timeout" {
			s.setTermination("timeout")
		}
	}
	proxyClosed := s.proxy == nil
	if s.proxy != nil {
		proxyClosed = s.proxy.Close(ctx) == nil
		if proxyClosed {
			select {
			case <-s.serveDone:
			case <-ctx.Done():
				proxyClosed = false
			}
		}
		if proxyClosed && s.serveErr != nil {
			proxyClosed = false
			s.setTermination("proxy_unavailable")
		}
	} else if s.listener != nil {
		closeErr := s.listener.Close()
		proxyClosed = closeErr == nil || errors.Is(closeErr, net.ErrClosed)
		if !proxyClosed {
			s.setTermination("proxy_unavailable")
		}
	}
	trusted.ProxyDrained = proxyClosed && s.proxy != nil
	if processClosed && proxyClosed {
		result, err := s.call.Close(ctx)
		trusted.Call = result
		if err == nil && result.Final && result.AuditComplete {
			processReleased := s.process == nil || s.process.Close() == nil
			if processReleased && s.removeOwnedRoot() == nil {
				s.resourcesClosed = true
				s.owner.mu.Lock()
				delete(s.owner.active, s.call.Ref())
				s.owner.mu.Unlock()
			}
		}
	}
	trusted.Execution = s.owner.cfg.Execution.SnapshotForIntent(s.call.IntentID())
	trusted.Final = trusted.ProcessDrained && trusted.ProxyDrained && trusted.Call.Final && trusted.Call.AuditComplete
	trusted.Classification = s.classification(trusted)
	text := s.owner.cfg.Execution.RedactOutput(output)
	clear(output)
	if len(text) > s.owner.cfg.OutputLimit {
		text = text[:s.owner.cfg.OutputLimit]
		trusted.OutputTruncated = true
	}
	s.result = Result{ToolOutput: strings.ToValidUTF8(string(text), "�"), Trusted: trusted}
	clear(text)
	if !s.resourcesClosed {
		return s.result, ErrIncomplete
	}
	if s.releaseSlot != nil {
		s.releaseSlot()
	}
	return s.result, nil
}

func (s *runningCall) classification(trusted TrustedResult) string {
	if s.initFailed {
		return "isolation_unavailable"
	}
	if trusted.Call.Reason == e2proxy.AuditFailed || trusted.Execution.Reason == e2proxy.AuditFailed {
		if trusted.Call.Counts.ForwardAttempted == 0 && trusted.Execution.Counts.ForwardAttempted == 0 && trusted.Call.Counts.Unknown == 0 && trusted.Execution.Counts.Unknown == 0 {
			return "transient_failure"
		}
		return "result_unknown"
	}
	if !trusted.Final || trusted.Call.Counts.Unknown > 0 {
		return "result_unknown"
	}
	counts := trusted.Call.Counts
	if counts.Allowed > counts.ForwardAttempted {
		return ClassificationForwardNotAttempted
	}
	if counts.ForwardAttempted == 0 && counts.Denied > 0 {
		return "policy_not_executed"
	}
	if counts.ForwardAttempted == 0 && counts.Unsupported > 0 {
		return "unsupported"
	}
	if s.getTermination() == "proxy_unavailable" {
		return "transient_failure"
	}
	if s.getTermination() == "timeout" {
		return "transient_failure"
	}
	if !trusted.ProcessBashExited {
		return "terminated"
	}
	if trusted.ExitCode != 0 {
		return "tool_failure"
	}
	if counts.Denied > 0 {
		return "partial_policy_denial"
	}
	if counts.Unsupported > 0 {
		return "partial_unsupported"
	}
	return "completed"
}

func (s *runningCall) removeOwnedRoot() error {
	if s.dir == "" {
		return nil
	}
	relative, err := filepath.Rel(s.owner.cfg.RootDir, s.dir)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.Dir(s.dir) != s.owner.cfg.RootDir || !strings.HasPrefix(filepath.Base(s.dir), "call-") {
		return ErrIncomplete
	}
	if err = os.RemoveAll(s.dir); err != nil {
		return ErrIncomplete
	}
	s.dir = ""
	return nil
}

// Drain fences and finalizes only calls admitted before this temporary
// barrier. It preserves the execution, intent set, budget, and future Run entry.
// An incomplete drain retains both the barrier and each unfinished call slot.
func (r *Runner) Drain(ctx context.Context) error {
	if r == nil || r.runnerState == nil || ctx == nil {
		return ErrConfig
	}
	select {
	case r.drainGate <- struct{}{}:
		defer func() { <-r.drainGate }()
	case <-ctx.Done():
		return ErrIncomplete
	}
	r.mu.Lock()
	r.draining = true
	states := make([]*runningCall, 0, len(r.active))
	for _, state := range r.active {
		states = append(states, state)
	}
	r.mu.Unlock()
	for _, state := range states {
		state.call.Block()
	}
	failed := false
	for _, state := range states {
		select {
		case <-state.ready:
		case <-ctx.Done():
			return ErrIncomplete
		}
		if _, err := state.cleanup(ctx); err != nil {
			failed = true
		}
	}
	if failed {
		return ErrIncomplete
	}
	r.mu.Lock()
	r.draining = false
	for _, state := range r.intentDrains {
		if !state.active {
			state.fenced = false
		}
	}
	r.mu.Unlock()
	return nil
}

// DrainIntent fences new and already-queued calls for this intent, then joins
// its admitted work. Another intent may use the shared Bash slot independently.
func (r *Runner) DrainIntent(ctx context.Context, intentID int64) error {
	if r == nil || r.runnerState == nil || ctx == nil || intentID <= 0 || !r.cfg.Execution.HasIntent(intentID) {
		return ErrConfig
	}
	r.mu.Lock()
	scope := r.intentDrains[intentID]
	if scope == nil {
		scope = &intentDrainState{gate: make(chan struct{}, 1)}
		r.intentDrains[intentID] = scope
	}
	r.mu.Unlock()
	select {
	case scope.gate <- struct{}{}:
	case <-ctx.Done():
		return ErrIncomplete
	}
	r.mu.Lock()
	if scope.generation == ^uint64(0) {
		scope.active = true
		scope.fenced = true
		r.mu.Unlock()
		defer func() { r.mu.Lock(); scope.active = false; r.mu.Unlock(); <-scope.gate }()
		return ErrIncomplete
	}
	scope.generation++
	scope.active = true
	scope.fenced = true
	states := make([]*runningCall, 0, len(r.active))
	for _, state := range r.active {
		if state.intentID == intentID {
			states = append(states, state)
		}
	}
	r.mu.Unlock()
	defer func() { r.mu.Lock(); scope.active = false; r.mu.Unlock(); <-scope.gate }()
	for _, state := range states {
		state.call.Block()
	}
	failed := false
	for _, state := range states {
		select {
		case <-state.ready:
		case <-ctx.Done():
			return ErrIncomplete
		}
		if _, err := state.cleanup(ctx); err != nil {
			failed = true
		}
	}
	if failed {
		return ErrIncomplete
	}
	r.mu.Lock()
	scope.fenced = false
	r.mu.Unlock()
	return nil
}

func (r *Runner) intentFencedLocked(intentID int64) bool {
	state := r.intentDrains[intentID]
	return state != nil && state.fenced
}

func (r *Runner) intentGenerationLocked(intentID int64) uint64 {
	state := r.intentDrains[intentID]
	if state == nil {
		return 0
	}
	return state.generation
}

// Close permanently fences this Runner, including queued calls. It blocks
// current calls immediately even if another Drain already owns the drain gate.
func (r *Runner) Close(ctx context.Context) error {
	if r == nil || r.runnerState == nil || ctx == nil {
		return ErrConfig
	}
	r.mu.Lock()
	r.stopped = true
	r.stopOnce.Do(func() { close(r.stop) })
	states := make([]*runningCall, 0, len(r.active))
	for _, state := range r.active {
		states = append(states, state)
	}
	r.mu.Unlock()
	for _, state := range states {
		state.call.Block()
	}
	return r.Drain(ctx)
}

func (s *runningCall) setTermination(reason string) {
	s.terminationMu.Lock()
	s.termination = reason
	s.terminationMu.Unlock()
}
func (s *runningCall) getTermination() string {
	s.terminationMu.Lock()
	defer s.terminationMu.Unlock()
	return s.termination
}

func (r *Runner) Execution() *e2proxy.Execution {
	if r == nil || r.runnerState == nil {
		return nil
	}
	return r.cfg.Execution
}

// ValidateRootDirLayout은 아직 생성하지 않은 호출 디렉터리의 정적 OS 경로 제약만 검사한다.
// 파일 접근·실행 생성·승인 발급은 하지 않으며 New의 실제 자원 검사를 대신하지 않는다.
func ValidateRootDirLayout(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.ContainsRune(root, 0) || len(root)+len("/call-0123456789/proxy.sock") > maxUnixSocketPathBytes {
		return ErrConfig
	}
	return nil
}
