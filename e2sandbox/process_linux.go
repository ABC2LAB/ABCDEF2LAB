//go:build linux

package e2sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	helperConfigFD    = 3
	helperWorkspaceFD = 4
	helperProxyFD     = 5
	helperCAFD        = 6
	helperStatusFD    = 7
	// 작업 공간을 넣는 봉인 memfd와 빼는 전용 pipe다. helper는 명령을 시작하기 전에 넣는 쪽을 닫고 빼는 쪽에
	// close-on-exec를 건다. 빼는 쪽에는 namespace 안 프로세스가 모두 끝난 뒤에만 쓴다.
	helperWorkspaceInFD  = 8
	helperWorkspaceOutFD = 9
	startupLimit         = 10 * time.Second
	maxCommandBytes      = 1 << 20
	maxOutputBytes       = 4 << 20
	maxCABytes           = 1 << 20
	maxDrainTimeout      = 2 * time.Minute
	maxStatusBytes       = 4096
	helperMaxProcs       = "GOMAXPROCS=2"
)

type helperConfig struct {
	Limits                                 resourceLimits
	Workspace, ProxySocket, CACert, Helper string
	Root                                   string
	Command                                string
	DrainNanos                             int64
	SaveWorkspace                          bool
}
type helperControl struct{ Action string }
type helperStatus struct {
	Stage    string
	State    string
	ExitCode int
}
type namespaceAnchor struct {
	file *os.File
	path string
}

// Process owns a private PID namespace's init and pidfd. Its sole authorized
// socket peer is this helper's relay, never a PID supplied by Bash.
type Process struct {
	callbackDone        chan struct{}
	statusDone          chan struct{}
	mu                  sync.Mutex
	cmd                 *exec.Cmd
	pidfd               int
	anchors             []namespaceAnchor
	uid, gid            uint32
	control             *os.File
	root                string
	ready               chan struct{}
	bashDone            chan struct{}
	done                chan struct{}
	readyOnce, bashOnce sync.Once
	started             bool
	setupFailed         bool
	interruptCause      error
	stopSent            bool
	result              Result
	output              boundedOutput
	cancel              context.CancelFunc
	stopContext         func() bool
	saveWorkspace       bool
	workspaceDone       chan struct{}
	workspace           []byte
	workspaceOverflow   bool
	workspaceStatus     string
}

type boundedOutput struct {
	mu        sync.Mutex
	bytes     []byte
	limit     int
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	left := b.limit - len(b.bytes)
	if left > 0 {
		if left > n {
			left = n
		}
		b.bytes = append(b.bytes, p[:left]...)
	}
	if left < n {
		b.truncated = true
	}
	return n, nil
}

func trustedRegular(path string, maxSize int64) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrConfig
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrConfig
	}
	f := os.NewFile(uintptr(fd), "owned-input")
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || (maxSize > 0 && st.Size() > maxSize) {
		f.Close()
		return nil, ErrConfig
	}
	return f, nil
}

func openCapability(path string, mode uint32) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrConfig
	}
	fd, err := unix.Open(path, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrConfig
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != mode || st.Uid != uint32(os.Getuid()) {
		unix.Close(fd)
		return nil, ErrConfig
	}
	return os.NewFile(uintptr(fd), "owned-capability"), nil
}

func validateConfig(cfg Config) (workspace, proxy, ca *os.File, err error) {
	err = ErrConfig
	if cfg.Command == "" || len(cfg.Command) > maxCommandBytes || strings.IndexByte(cfg.Command, 0) >= 0 ||
		cfg.Timeout <= 0 || cfg.Timeout > maxCallTimeout || cfg.DrainTimeout <= 0 || cfg.DrainTimeout > maxDrainTimeout || cfg.OutputLimit < 1 || cfg.OutputLimit > maxOutputBytes ||
		os.Getuid() == 0 || os.Getgid() == 0 {
		return
	}
	helper, e := trustedRegular(cfg.Helper, 0)
	if e != nil {
		return
	}
	defer helper.Close()
	st, e := helper.Stat()
	if e != nil {
		return
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if e != nil || !ok || (stat.Uid != uint32(os.Getuid()) && stat.Uid != 0) || st.Mode().Perm()&0022 != 0 || st.Mode().Perm()&0111 == 0 {
		return
	}
	workspace, e = openCapability(cfg.Workspace, unix.S_IFDIR)
	if e != nil {
		return
	}
	defer func() {
		if err != nil {
			workspace.Close()
			if proxy != nil {
				proxy.Close()
			}
			if ca != nil {
				ca.Close()
			}
		}
	}()
	st, e = os.Stat(cfg.Workspace)
	entries, e2 := os.ReadDir(cfg.Workspace)
	if e != nil || e2 != nil || st.Mode().Perm() != 0700 || len(entries) != 0 {
		return
	}
	proxy, e = openCapability(cfg.ProxySocket, unix.S_IFSOCK)
	if e != nil {
		return
	}
	ca, e = trustedRegular(cfg.CACert, maxCABytes)
	if e != nil {
		return
	}
	err = nil
	return
}

// Launch establishes isolation and returns before Bash starts, so the caller
// can install the exact helper's peer binding in its call proxy first.
func Launch(ctx context.Context, cfg Config) (*Process, error) {
	p, err := Prepare(ctx, cfg)
	if err != nil && p != nil {
		if closeErr := p.Close(); closeErr != nil {
			return p, err // 미확인 자원의 소유권을 버리지 않는다.
		}
		return nil, err
	}
	return p, err
}

// Prepare는 시작 실패 때도 생성된 Process의 소유권을 돌려준다. Runner는
// 초기화의 중지와 장애를 나눈 뒤 Stop·Wait·Close가 끝날 때까지 슬롯을 보유한다.
func Prepare(ctx context.Context, cfg Config) (*Process, error) {
	if ctx == nil {
		return nil, ErrConfig
	}
	if ctx.Err() != nil {
		return nil, &startupInterruption{cause: ctx.Err()}
	}
	limits, err := limitsFor(cfg)
	if err != nil {
		return nil, err
	}
	if int64(len(cfg.WorkspaceIn)) > workspaceArchiveLimit(limits.ScratchBytes) {
		return nil, ErrConfig
	}
	workspace, proxy, ca, err := validateConfig(cfg)
	if err != nil {
		return nil, err
	}
	defer workspace.Close()
	defer proxy.Close()
	defer ca.Close()
	root, err := os.MkdirTemp("", "e2-jail-")
	if err != nil {
		return nil, ErrUnavailable
	}
	cleanupRoot := true
	defer func() {
		if cleanupRoot {
			_ = os.Remove(root)
		}
	}()
	configRead, configWrite, err := os.Pipe()
	if err != nil {
		return nil, ErrUnavailable
	}
	defer configRead.Close()
	statusRead, statusWrite, err := os.Pipe()
	if err != nil {
		configWrite.Close()
		return nil, ErrUnavailable
	}
	defer statusWrite.Close()
	workspaceIn, err := sealedWorkspaceInput(cfg.WorkspaceIn)
	if err != nil {
		configWrite.Close()
		statusRead.Close()
		return nil, ErrUnavailable
	}
	defer workspaceIn.Close()
	workspaceRead, workspaceWrite, err := os.Pipe()
	if err != nil {
		configWrite.Close()
		statusRead.Close()
		return nil, ErrUnavailable
	}
	defer workspaceWrite.Close()
	runCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	p := &Process{callbackDone: make(chan struct{}), statusDone: make(chan struct{}), pidfd: -1, root: root, control: configWrite, ready: make(chan struct{}), bashDone: make(chan struct{}), done: make(chan struct{}),
		uid: uint32(os.Getuid()), gid: uint32(os.Getgid()), cancel: cancel, result: Result{ExitCode: -1},
		output: boundedOutput{limit: cfg.OutputLimit}, saveWorkspace: cfg.SaveWorkspace, workspaceDone: make(chan struct{})}
	cmd := exec.Command(cfg.Helper)
	cmd.Env = []string{helperMaxProcs, "GOTELEMETRY=off"}
	cmd.ExtraFiles = []*os.File{configRead, workspace, proxy, ca, statusWrite, workspaceIn, workspaceWrite}
	cmd.Stdout, cmd.Stderr = &p.output, &p.output
	cmd.WaitDelay = cfg.DrainTimeout
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 unix.CLONE_NEWUSER | unix.CLONE_NEWPID | unix.CLONE_NEWNS | unix.CLONE_NEWNET | unix.CLONE_NEWIPC | unix.CLONE_NEWUTS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false, Pdeathsig: syscall.SIGKILL,
	}
	p.cmd = cmd
	if err = cmd.Start(); err != nil {
		cancel()
		configWrite.Close()
		statusRead.Close()
		workspaceRead.Close()
		cleanupRoot = false
		p.result.Drained, p.result.HelperFailed = true, true // Start 오류면 실행 중인 자식이 없다.
		p.result.Reason = "helper_failed"
		close(p.done)
		return p, fmt.Errorf("%w: process_start", ErrUnavailable)
	}
	cleanupRoot = false
	_ = configRead.Close()
	_ = statusWrite.Close()
	_ = workspaceIn.Close()
	_ = workspaceWrite.Close()
	go p.readWorkspace(workspaceRead, workspaceArchiveLimit(limits.ScratchBytes))
	// PID pin·설정 전달 실패도 시작한 자원의 소유권을 Runner에 남긴다.
	// 제거 실패를 삼킨 채 nil을 돌려주면 Runner가 슬롯을 조기 반환할 수 있다.
	abortStartup := func(cause error) (*Process, error) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = statusRead.Close()
		<-p.workspaceDone
		p.result.Drained = cmd.ProcessState != nil
		p.result.HelperFailed = true
		p.result.Reason = "helper_failed"
		p.settleWorkspace()
		cancel()
		close(p.done)
		return p, cause
	}
	p.pidfd, err = unix.PidfdOpen(cmd.Process.Pid, 0)
	if err == nil {
		err = p.pinNamespaces()
	}
	if err != nil {
		return abortStartup(fmt.Errorf("%w: namespace_pin", err))
	}
	if json.NewEncoder(configWrite).Encode(helperConfig{Limits: limits, Root: root, Command: cfg.Command, DrainNanos: int64(cfg.DrainTimeout), Workspace: cfg.Workspace, ProxySocket: cfg.ProxySocket, CACert: cfg.CACert, Helper: cfg.Helper, SaveWorkspace: cfg.SaveWorkspace}) != nil {
		return abortStartup(fmt.Errorf("%w: config_write", ErrUnavailable))
	}
	go p.readStatus(statusRead)
	p.stopContext = context.AfterFunc(runCtx, func() {
		defer close(p.callbackDone)
		p.mu.Lock()
		defer p.mu.Unlock()
		signaled := !p.exited() && unix.PidfdSendSignal(p.pidfd, unix.SIGKILL, nil, 0) == nil
		p.stopSent = p.stopSent || signaled
		if p.result.Reason == "" && signaled {
			p.interruptCause = runCtx.Err()
			if runCtx.Err() == context.DeadlineExceeded {
				p.result.Reason = "timeout"
			} else {
				p.result.Reason = "canceled"
			}
		}
	})
	go func() {
		waitErr := cmd.Wait()
		<-p.statusDone
		<-p.workspaceDone
		p.mu.Lock()
		p.result.Drained = p.exited()
		var status syscall.WaitStatus
		statusKnown := false
		if cmd.ProcessState != nil {
			status, statusKnown = cmd.ProcessState.Sys().(syscall.WaitStatus)
		}
		stoppedByHost := statusKnown && status.Signaled() && status.Signal() == syscall.SIGKILL && p.stopSent
		p.result.HelperFailed = waitErr != nil && !stoppedByHost
		// 취소와 실제 helper 장애가 경쟁해도 비정상 종료를 정상 중지로 덮지 않는다.
		if p.interruptCause != nil && (!stoppedByHost || p.setupFailed) {
			p.interruptCause = nil
			if !p.setupFailed {
				p.result.Reason = "helper_failed"
			}
		}
		if waitErr != nil && p.result.Reason == "" {
			p.result.Reason = "helper_failed"
		}
		if p.result.Reason == "" {
			p.result.Reason = "completed"
		}
		p.settleWorkspace()
		p.output.mu.Lock()
		p.result.Output = append([]byte(nil), p.output.bytes...)
		p.result.OutputTruncated = p.output.truncated
		p.output.mu.Unlock()
		p.mu.Unlock()
		if p.stopContext != nil {
			if !p.stopContext() {
				<-p.callbackDone
			}
		}
		cancel()
		close(p.done)
	}()
	timer := time.NewTimer(startupLimit)
	defer timer.Stop()
	select {
	case <-p.ready:
		return p, nil
	case <-p.done:
		return p, p.startupError()
	case <-runCtx.Done():
		<-p.done
		return p, p.startupError()
	case <-timer.C:
		p.kill()
		<-p.done
		return p, ErrUnavailable
	}
}

func (p *Process) startupError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.interruptCause != nil && !p.setupFailed && p.result.Drained {
		return &startupInterruption{cause: p.interruptCause}
	}
	return fmt.Errorf("%w: %s", ErrUnavailable, p.result.Reason)
}

func (p *Process) pinNamespaces() error {
	for _, kind := range []string{"pid", "mnt", "net", "user", "ipc", "uts"} {
		path := filepath.Join("/proc", itoa(p.cmd.Process.Pid), "ns", kind)
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("%w: open_%s", ErrUnavailable, kind)
		}
		st, err := f.Stat()
		parent, parentErr := os.Stat(filepath.Join("/proc/self/ns", kind))
		if err != nil || parentErr != nil || os.SameFile(st, parent) {
			f.Close()
			return fmt.Errorf("%w: relation_%s", ErrUnavailable, kind)
		}
		p.anchors = append(p.anchors, namespaceAnchor{file: f, path: path})
	}
	return nil
}
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte(v%10) + '0'
		v /= 10
	}
	return string(buf[i:])
}

func (p *Process) readStatus(f *os.File) {
	defer f.Close()
	defer close(p.statusDone)
	dec := json.NewDecoder(io.LimitReader(f, maxStatusBytes))
	for {
		var s helperStatus
		if dec.Decode(&s) != nil {
			return
		}
		p.mu.Lock()
		switch s.State {
		case "ready":
			p.readyOnce.Do(func() { close(p.ready) })
		case "bash_exit":
			p.result.BashExited = true
			p.result.ExitCode = s.ExitCode
			p.bashOnce.Do(func() { close(p.bashDone) })
		case "setup_failed":
			p.setupFailed = true
			p.result.Reason = "isolation_setup_failed:" + s.Stage
		case "exec_failed":
			p.result.Reason = "bash_start_failed"
		case "workspace_saved", "workspace_too_large", "workspace_failed":
			p.workspaceStatus = s.State
		}
		p.mu.Unlock()
	}
}

// sealedWorkspaceInput은 넣을 작업 공간을 봉인한 memfd다. helper는 읽기만 할 수 있다.
func sealedWorkspaceInput(data []byte) (*os.File, error) {
	fd, err := unix.MemfdCreate("e2-workspace-in", unix.MFD_ALLOW_SEALING|unix.MFD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "workspace-in")
	if _, err = f.Write(data); err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err == nil {
		_, err = unix.FcntlInt(f.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_SEAL|unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_WRITE)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// readWorkspace는 빼는 pipe를 끝까지 읽는다. 상한을 넘으면 내용을 버리고 넘었다는 사실만 남긴다(helper가 막히지
// 않게 끝까지 읽어 버린다).
func (p *Process) readWorkspace(f *os.File, limit int64) {
	defer close(p.workspaceDone)
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	overflow := int64(len(data)) > limit
	if overflow {
		data = nil
		_, _ = io.Copy(io.Discard, f)
	}
	p.mu.Lock()
	p.workspace, p.workspaceOverflow = data, overflow || err != nil
	p.mu.Unlock()
}

// settleWorkspace는 helper가 Bash 종료 뒤 저장을 마쳤다고 알렸고 빼는 통로를 끝까지 읽었을 때만 작업 공간을 결과에
// 싣는다. helper는 저장을 bash_exit보다 먼저 끝내므로, 그 뒤 호스트가 namespace를 끝내도 보관본은 온전하다. Bash가
// 끝나기 전에 중지·시간 초과로 끊겼으면 저장 알림이 없어 싣지 않는다. p.mu를 잡은 채 부른다.
func (p *Process) settleWorkspace() {
	if !p.saveWorkspace {
		return
	}
	switch {
	case p.workspaceStatus == "workspace_too_large" || p.workspaceOverflow:
		p.result.WorkspaceState = "too_large"
	case p.workspaceStatus == "workspace_saved" && p.result.BashExited:
		p.result.Workspace = append([]byte(nil), p.workspace...)
		p.result.WorkspaceState = "saved"
	default:
		p.result.WorkspaceState = "not_saved"
	}
	p.workspace = nil
}

func (p *Process) exited() bool {
	if p.pidfd < 0 {
		return false
	}
	poll := []unix.PollFd{{Fd: int32(p.pidfd), Events: unix.POLLIN}}
	_, err := unix.Poll(poll, 0)
	return err == nil && poll[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0
}
func (p *Process) kill() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pidfd >= 0 {
		live := !p.exited()
		if unix.PidfdSendSignal(p.pidfd, unix.SIGKILL, nil, 0) == nil && live {
			p.stopSent = true
		}
	}
}

// Start permits one Bash invocation after the host proxy has bound this PID.
func (p *Process) Start(ctx context.Context) error {
	if p == nil || ctx == nil || p.cmd == nil || p.cmd.Process == nil {
		return ErrState
	}
	p.mu.Lock()
	if p.exited() {
		p.mu.Unlock()
		<-p.done
		return p.startupError()
	}
	defer p.mu.Unlock()
	if p.started {
		return ErrState
	}
	if ctx.Err() != nil {
		return &startupInterruption{cause: ctx.Err()}
	}
	if json.NewEncoder(p.control).Encode(helperControl{Action: "start"}) != nil {
		return ErrUnavailable
	}
	p.started = true
	return nil
}
func (p *Process) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}
func (p *Process) BashDone() <-chan struct{} {
	if p == nil {
		return nil
	}
	return p.bashDone
}

// LivePeer accepts only the owned relay process, with a live pidfd and the
// same pinned namespaces. Direct UDS attempts by Bash/descendants are refused.
func (p *Process) LivePeer(pid int32, uid, gid uint32) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if pid <= 0 || int(pid) != p.PID() || uid != p.uid || gid != p.gid || p.exited() {
		return false
	}
	for _, a := range p.anchors {
		// Anchors were opened before handing configuration to the trusted
		// helper. After ready the non-dumpable helper denies path inspection;
		// inherited seccomp denies every namespace transition instead.
		if _, err := a.file.Stat(); err != nil {
			return false
		}
	}
	return true
}
func (p *Process) Wait(ctx context.Context) (Result, error) {
	if p == nil || ctx == nil {
		return Result{}, ErrState
	}
	select {
	case <-p.done:
		p.mu.Lock()
		r := p.result
		r.Output = append([]byte(nil), r.Output...)
		r.Workspace = append([]byte(nil), r.Workspace...)
		p.mu.Unlock()
		if !r.Drained {
			return r, ErrUnavailable
		}
		return r, nil
	case <-ctx.Done():
		return Result{Reason: "drain_incomplete"}, ctx.Err()
	}
}

// Stop kills the namespace init through its pidfd, then waits for kernel
// namespace teardown and output drain. Caller closes HTTP admission first.
func (p *Process) Stop(ctx context.Context) error {
	if p == nil || ctx == nil {
		return ErrState
	}
	p.mu.Lock()
	if p.result.Reason == "" {
		p.result.Reason = "stopped"
	}
	p.mu.Unlock()
	p.kill()
	_, err := p.Wait(ctx)
	return err
}

// Close releases host handles only after actual process drain.
func (p *Process) Close() error {
	if p == nil {
		return nil
	}
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.release()
	default:
		return ErrState
	}
}
func (p *Process) release() error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.control != nil {
		p.control.Close()
		p.control = nil
	}
	for _, a := range p.anchors {
		a.file.Close()
	}
	p.anchors = nil
	if p.pidfd >= 0 {
		unix.Close(p.pidfd)
		p.pidfd = -1
	}
	if p.root != "" {
		if err := os.Remove(p.root); err != nil && !os.IsNotExist(err) {
			return err
		}
		p.root = ""
	}
	return nil
}
