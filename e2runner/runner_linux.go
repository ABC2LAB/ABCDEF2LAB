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
	"strconv"
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
	// workspaceMu는 intent별 작업 공간 보관 파일의 읽기·교체·삭제를 차례로 한다.
	workspaceMu sync.Mutex
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
	// workspace는 이 호출의 작업 공간 보관 결과다(PersistWorkspace일 때). 한 번만 정한다.
	workspace string
	// outputFile은 모델에 준 글보다 긴 출력을 남긴 작업 공간 안 경로다.
	outputFile string
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
	sandboxConfig := e2sandbox.Config{
		Helper: s.owner.cfg.Helper, Workspace: workspace, ProxySocket: socket, CACert: caPath,
		Command: command, Timeout: s.owner.cfg.CommandTimeout, DrainTimeout: s.owner.cfg.DrainTimeout,
		OutputLimit: s.owner.cfg.OutputLimit,
	}
	if s.owner.cfg.PersistWorkspace {
		saved, err := s.owner.loadWorkspace(s.intentID, e2sandbox.WorkspaceLimit(sandboxConfig))
		if err != nil {
			return ErrUnavailable
		}
		sandboxConfig.WorkspaceIn, sandboxConfig.SaveWorkspace = saved, true
		// 큰 출력을 작업 공간에 남기려면 샌드박스에서 모델용 상한보다 길게 받아야 한다. 모델에는 OutputLimit까지만 준다.
		sandboxConfig.OutputLimit = maxOutputBytes
	}
	process, err := e2sandbox.Launch(s.call.Context(), sandboxConfig)
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
	var pendingWorkspace *e2sandbox.Result
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
		if s.owner.cfg.PersistWorkspace && s.workspace == "" {
			pending := processResult
			pendingWorkspace = &pending
		}
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
	if pendingWorkspace != nil {
		// 모델에 주는 글보다 긴 출력은 가린 전체를 작업 공간 파일로 남긴다(결정안 §2-3). 보관 상한을 넘으면 남기지 않는다.
		if len(text) > s.owner.cfg.OutputLimit && pendingWorkspace.WorkspaceState == "saved" {
			name := workspaceOutputDir + "/" + s.call.Ref() + ".txt"
			if archive, err := e2sandbox.AppendWorkspaceFile(pendingWorkspace.Workspace, name, text, e2sandbox.WorkspaceLimit(e2sandbox.Config{})); err == nil {
				pendingWorkspace.Workspace = archive
				s.outputFile = name
			}
		}
		s.workspace = s.owner.storeWorkspace(s.intentID, *pendingWorkspace)
		if s.workspace != "saved" {
			s.outputFile = ""
		}
	}
	if len(text) > s.owner.cfg.OutputLimit {
		text = text[:s.owner.cfg.OutputLimit]
		trusted.OutputTruncated = true
	}
	s.result = Result{ToolOutput: strings.ToValidUTF8(string(text), "�"), Trusted: trusted, Workspace: s.workspace, OutputFile: s.outputFile}
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
	// intent가 끝나면 그 작업 공간 보관본을 지운다(결정안 §2-2 수명).
	if r.cfg.PersistWorkspace && r.removeWorkspace(intentID) != nil {
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
	if err := r.Drain(ctx); err != nil {
		return err
	}
	if r.cfg.PersistWorkspace {
		return r.removeAllWorkspaces()
	}
	return nil
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

// workspacePath는 intent 하나의 작업 공간 보관 파일이다. RootDir(0700, Runner 소유) 바로 아래에 둔다.
func (r *Runner) workspacePath(intentID int64) string {
	return filepath.Join(r.cfg.RootDir, workspaceFilePrefix+strconv.FormatInt(intentID, 10)+".tar")
}

const workspaceFilePrefix = "workspace-"

// workspaceOutputDir는 큰 출력 파일을 두는 작업 공간 안 디렉터리다(/work/.outputs).
const workspaceOutputDir = ".outputs"

// loadWorkspace는 intent의 보관 파일을 읽는다. 없으면 빈 작업 공간이다. 링크·일반 파일이 아닌 것·상한 밖은 거부한다.
func (r *Runner) loadWorkspace(intentID int64, contentLimit uint64) ([]byte, error) {
	r.workspaceMu.Lock()
	defer r.workspaceMu.Unlock()
	f, err := os.OpenFile(r.workspacePath(intentID), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(f, info.Size()+1))
	if err != nil || int64(len(data)) != info.Size() || e2sandbox.ValidateWorkspace(data, contentLimit) != nil {
		return nil, ErrUnavailable
	}
	return data, nil
}

// storeWorkspace는 정상 마감한 호출의 작업 공간을 다시 검사한 뒤 원자적으로 바꿔 둔다. 저장하지 못하면 이전 보관본을
// 그대로 두고 그 사실만 돌려준다(호출 결과는 바뀌지 않는다).
func (r *Runner) storeWorkspace(intentID int64, result e2sandbox.Result) string {
	if result.WorkspaceState != "saved" {
		if result.WorkspaceState == "" {
			return "not_saved"
		}
		return result.WorkspaceState
	}
	if e2sandbox.ValidateWorkspace(result.Workspace, e2sandbox.WorkspaceLimit(e2sandbox.Config{})) != nil {
		return "invalid"
	}
	r.workspaceMu.Lock()
	defer r.workspaceMu.Unlock()
	tmp, err := os.CreateTemp(r.cfg.RootDir, workspaceFilePrefix+"tmp-")
	if err != nil {
		return "not_saved"
	}
	name := tmp.Name()
	_, writeErr := tmp.Write(result.Workspace)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil || os.Chmod(name, 0o600) != nil || os.Rename(name, r.workspacePath(intentID)) != nil {
		_ = os.Remove(name)
		return "not_saved"
	}
	return "saved"
}

// removeWorkspace는 intent의 보관 파일을 지운다. 없으면 할 일이 없다.
func (r *Runner) removeWorkspace(intentID int64) error {
	r.workspaceMu.Lock()
	defer r.workspaceMu.Unlock()
	if err := os.Remove(r.workspacePath(intentID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrIncomplete
	}
	return nil
}

// removeAllWorkspaces는 Runner가 닫힐 때 남은 보관 파일을 모두 지운다.
func (r *Runner) removeAllWorkspaces() error {
	r.workspaceMu.Lock()
	defer r.workspaceMu.Unlock()
	entries, err := os.ReadDir(r.cfg.RootDir)
	if err != nil {
		return ErrIncomplete
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), workspaceFilePrefix) {
			if err := os.Remove(filepath.Join(r.cfg.RootDir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return ErrIncomplete
			}
		}
	}
	return nil
}
