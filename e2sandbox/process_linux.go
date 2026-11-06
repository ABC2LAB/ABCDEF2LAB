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
	startupLimit      = 10 * time.Second
	maxCommandBytes   = 1 << 20
	maxOutputBytes    = 4 << 20
	maxCABytes        = 1 << 20
	maxDrainTimeout   = 2 * time.Minute
	maxStatusBytes    = 4096
	helperMaxProcs    = "GOMAXPROCS=2"
)

type helperConfig struct {
	Limits                                 resourceLimits
	Workspace, ProxySocket, CACert, Helper string
	Root                                   string
	Command                                string
	DrainNanos                             int64
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
	result              Result
	output              boundedOutput
	cancel              context.CancelFunc
	stopContext         func() bool
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
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrConfig
	}
	limits, err := limitsFor(cfg)
	if err != nil {
		return nil, err
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
	runCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	p := &Process{callbackDone: make(chan struct{}), statusDone: make(chan struct{}), pidfd: -1, root: root, control: configWrite, ready: make(chan struct{}), bashDone: make(chan struct{}), done: make(chan struct{}),
		uid: uint32(os.Getuid()), gid: uint32(os.Getgid()), cancel: cancel, result: Result{ExitCode: -1},
		output: boundedOutput{limit: cfg.OutputLimit}}
	cmd := exec.Command(cfg.Helper)
	cmd.Env = []string{helperMaxProcs, "GOTELEMETRY=off"}
	cmd.ExtraFiles = []*os.File{configRead, workspace, proxy, ca, statusWrite}
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
		return nil, fmt.Errorf("%w: process_start", ErrUnavailable)
	}
	cleanupRoot = false
	_ = configRead.Close()
	_ = statusWrite.Close()
	p.pidfd, err = unix.PidfdOpen(cmd.Process.Pid, 0)
	if err == nil {
		err = p.pinNamespaces()
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		p.release()
		statusRead.Close()
		return nil, fmt.Errorf("%w: namespace_pin", err)
	}
	if json.NewEncoder(configWrite).Encode(helperConfig{Limits: limits, Root: root, Command: cfg.Command, DrainNanos: int64(cfg.DrainTimeout), Workspace: cfg.Workspace, ProxySocket: cfg.ProxySocket, CACert: cfg.CACert, Helper: cfg.Helper}) != nil {
		_ = unix.PidfdSendSignal(p.pidfd, unix.SIGKILL, nil, 0)
		_ = cmd.Wait()
		p.release()
		statusRead.Close()
		return nil, fmt.Errorf("%w: config_write", ErrUnavailable)
	}
	go p.readStatus(statusRead)
	p.stopContext = context.AfterFunc(runCtx, func() {
		defer close(p.callbackDone)
		p.mu.Lock()
		if runCtx.Err() == context.DeadlineExceeded {
			p.result.Reason = "timeout"
		} else {
			p.result.Reason = "canceled"
		}
		p.mu.Unlock()
		p.kill()
	})
	go func() {
		waitErr := cmd.Wait()
		<-p.statusDone
		p.mu.Lock()
		p.result.Drained = p.exited()
		if waitErr != nil && p.result.Reason == "" {
			p.result.Reason = "helper_failed"
		}
		if p.result.Reason == "" {
			p.result.Reason = "completed"
		}
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
		reason := p.result.Reason
		p.release()
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, reason)
	case <-runCtx.Done():
		p.kill()
		<-p.done
		reason := p.result.Reason
		p.release()
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, reason)
	case <-timer.C:
		p.kill()
		<-p.done
		p.release()
		return nil, ErrUnavailable
	}
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
			p.result.Reason = "isolation_setup_failed:" + s.Stage
		case "exec_failed":
			p.result.Reason = "bash_start_failed"
		}
		p.mu.Unlock()
	}
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
		_ = unix.PidfdSendSignal(p.pidfd, unix.SIGKILL, nil, 0)
	}
}

// Start permits one Bash invocation after the host proxy has bound this PID.
func (p *Process) Start(ctx context.Context) error {
	if p == nil || ctx == nil || ctx.Err() != nil {
		return ErrState
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started || p.exited() {
		return ErrState
	}
	if json.NewEncoder(p.control).Encode(helperControl{Action: "start"}) != nil {
		return ErrUnavailable
	}
	p.started = true
	return nil
}
func (p *Process) PID() int {
	if p == nil || p.cmd == nil {
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
		p.release()
		return nil
	default:
		return ErrState
	}
}
func (p *Process) release() {
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
		_ = os.Remove(p.root)
		p.root = ""
	}
}
