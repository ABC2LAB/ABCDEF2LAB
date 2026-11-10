//go:build linux

package e2sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	helperListenerFD     = 100
	helperSealedConfigFD = 101
	configEnvelopeBytes  = 4096
	rootTmpfsOptions     = "mode=0755,size=32m"
	maxRelayConnections  = 128
	relayDialTimeout     = 3 * time.Second
)

// HelperMain is the trusted executable entry. It refuses execution outside
// the fresh namespace init. No credential or approval is read from env/argv.
func HelperMain() int {
	if os.Getpid() != 1 || os.Getppid() != 0 || os.Getuid() != 0 || os.Getgid() != 0 {
		return 2
	}
	input := os.NewFile(helperConfigFD, "host-control")
	status := os.NewFile(helperStatusFD, "host-status")
	if input == nil || status == nil {
		return 2
	}
	defer input.Close()
	defer status.Close()
	enc := json.NewEncoder(status)
	stage := "config"
	fail := func() int { _ = enc.Encode(helperStatus{State: "setup_failed", Stage: stage}); return 2 }
	dec := json.NewDecoder(io.LimitReader(input, maxCommandBytes+configEnvelopeBytes))
	var cfg helperConfig
	var listener net.Listener
	if len(os.Args) == 1 {
		if dec.Decode(&cfg) != nil || cfg.Root == "" || cfg.Command == "" || cfg.DrainNanos <= 0 {
			return fail()
		}
		stage = "jail"
		if err := setupJail(cfg); err != nil {
			var detail *jailFailure
			if errors.As(err, &detail) {
				stage = "jail_" + detail.step
			}
			return fail()
		}
		stage = "loopback"
		if bringLoopbackUp() != nil {
			return fail()
		}
		stage = "listener"
		rawListener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return fail()
		}
		// Re-exec from the thread whose capability sets were cleared: Go's other
		// bootstrap threads would otherwise keep their per-thread capabilities.
		// After exec every new runtime thread inherits empty sets and the filter.
		tcp, ok := rawListener.(*net.TCPListener)
		if !ok {
			return fail()
		}
		listenerFile, err := tcp.File()
		if err != nil {
			return fail()
		}
		cfg.Root = ""
		cfg.Workspace = ""
		cfg.ProxySocket = ""
		cfg.CACert = ""
		cfg.Helper = ""
		stage = "config_memfd"
		memfd, err := unix.MemfdCreate("e2-call-config", unix.MFD_ALLOW_SEALING|unix.MFD_CLOEXEC)
		if err != nil {
			return fail()
		}
		configFile := os.NewFile(uintptr(memfd), "call-config")
		if json.NewEncoder(configFile).Encode(cfg) != nil {
			return fail()
		}
		if _, err = configFile.Seek(0, io.SeekStart); err != nil {
			return fail()
		}
		if _, err = unix.FcntlInt(configFile.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_SEAL|unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_WRITE); err != nil {
			return fail()
		}
		if unix.Dup3(int(listenerFile.Fd()), helperListenerFD, 0) != nil || unix.Dup3(memfd, helperSealedConfigFD, 0) != nil {
			return fail()
		}
		listenerFile.Close()
		rawListener.Close()
		configFile.Close()
		stage = "resource_limits"
		if applyLimits(cfg.Limits) != nil {
			return fail()
		}
		stage = "privilege"
		runtime.LockOSThread()
		if lockPrivilege() != nil {
			return fail()
		}
		stage = "reexec"
		if unix.Exec("/run/e2/helper", []string{"e2-bash-sandbox", "--isolated-stage"}, []string{helperMaxProcs, "GOTELEMETRY=off"}) != nil {
			return fail()
		}
		return 2
	}
	stage = "isolated_verification"
	if len(os.Args) != 2 || os.Args[1] != "--isolated-stage" || !emptyCapabilities() {
		return fail()
	}
	// Dumpability is reset by exec. Restrict the relay's memory and descriptors
	// before any untrusted child exists.
	if unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0) != nil {
		return fail()
	}
	inheritedConfig := os.NewFile(helperSealedConfigFD, "isolated-config")
	if json.NewDecoder(io.LimitReader(inheritedConfig, maxCommandBytes+configEnvelopeBytes)).Decode(&cfg) != nil {
		return fail()
	}
	inheritedConfig.Close()
	inheritedListener := os.NewFile(helperListenerFD, "isolated-listener")
	var err error
	listener, err = net.FileListener(inheritedListener)
	inheritedListener.Close()
	if err != nil {
		return fail()
	}
	relay := newRelay(listener)
	defer relay.close()
	// These host-only status/control descriptors crossed the trusted re-exec.
	// They must not cross exec into arbitrary Bash.
	unix.CloseOnExec(helperConfigFD)
	unix.CloseOnExec(helperStatusFD)
	if enc.Encode(helperStatus{State: "ready"}) != nil {
		return 2
	}
	var control helperControl
	if dec.Decode(&control) != nil || control.Action != "start" {
		return 2
	}
	relay.start()
	cmd := exec.Command("/bin/bash", "--noprofile", "--norc", "-c", cfg.Command)
	cmd.Dir = "/work"
	proxy := "http://" + listener.Addr().String()
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/sbin:/usr/bin:/bin", "HOME=/work", "TMPDIR=/tmp", "LANG=C", "LC_ALL=C",
		"HTTP_PROXY=" + proxy, "HTTPS_PROXY=" + proxy, "ALL_PROXY=" + proxy,
		"http_proxy=" + proxy, "https_proxy=" + proxy, "all_proxy=" + proxy,
		"NO_PROXY=", "no_proxy=", "SSL_CERT_FILE=/run/e2/ca.pem", "CURL_CA_BUNDLE=/run/e2/ca.pem",
	}
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if cmd.Start() != nil {
		_ = enc.Encode(helperStatus{State: "exec_failed"})
		return 2
	}
	waitErr := cmd.Wait()
	exitCode := 0
	if waitErr != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	relay.close()
	if enc.Encode(helperStatus{State: "bash_exit", ExitCode: exitCode}) != nil {
		return 2
	}
	drained := make(chan struct{})
	go func() { relay.wait(); close(drained) }()
	select {
	case <-drained:
		return 0
	case <-time.After(time.Duration(cfg.DrainNanos)):
		return 3
	}
}

func emptyCapabilities() bool {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		pair := strings.Fields(line)
		if len(pair) == 2 {
			fields[pair[0]] = pair[1]
		}
	}
	for _, name := range []string{"CapInh:", "CapPrm:", "CapEff:", "CapBnd:", "CapAmb:"} {
		if fields[name] != "0000000000000000" {
			return false
		}
	}
	return fields["NoNewPrivs:"] == "1" && fields["Seccomp:"] == "2"
}

// 평가에서 원본 엔진과 같은 도구로 비교하려면 이미지의 도구 경로가 샌드박스에도 보여야 한다.
// /usr/local/lib 전체는 넣지 않는다. 호스트에 따라 매우 커서 시작마다 하는 특수 파일 검사가 느려진다.
func sandboxRuntimePaths(triplet string, glob func(string) ([]string, error), isDir func(string) bool) ([]string, error) {
	paths := []string{
		"/usr/bin", "/usr/sbin", "/bin", "/usr/local/bin", "/usr/lib/" + triplet, "/lib/" + triplet, "/usr/lib64", "/lib64",
		"/usr/lib/cargo/bin/coreutils", "/etc/alternatives", "/etc/ld.so.cache",
	}
	for _, pattern := range []string{"/usr/lib/python3*", "/usr/local/lib/python3*"} {
		found, err := glob(pattern)
		if err != nil {
			return nil, err
		}
		for _, path := range found {
			if isDir(path) {
				paths = append(paths, path)
			}
		}
	}
	shared, err := glob("/usr/local/lib/libpython3*")
	if err != nil {
		return nil, err
	}
	return append(paths, shared...), nil
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// /etc에서는 도구 실행에 필요한 두 항목만 연다. 계정 파일 등 나머지는 계속 가린다.
func allowedSystemSource(resolved string) bool {
	return strings.HasPrefix(resolved, "/usr/") || resolved == "/etc/alternatives" || resolved == "/etc/ld.so.cache"
}

func prepareMountTarget(target string, directory bool) error {
	if directory {
		return os.MkdirAll(target, 0755)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0444)
	if err != nil {
		return err
	}
	return f.Close()
}

func mountReadOnly(source, target string) error {
	if err := unix.Mount(source, target, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return err
	}
	return unix.MountSetattr(unix.AT_FDCWD, target, unix.AT_RECURSIVE, &unix.MountAttr{
		Attr_set: unix.MOUNT_ATTR_RDONLY | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV,
	})
}

// An inherited O_PATH refers to the parent's mount object. Reopen the
// public source inside the new namespace, retaining exact inode identity.
func reopenInNamespace(path string, fd int) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrConfig
	}
	current, err := unix.Open(path, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(current)
	var inherited, local unix.Stat_t
	if err := unix.Fstat(fd, &inherited); err != nil {
		return jailError("source_reopen_inherited_stat", err)
	}
	if err := unix.Fstat(current, &local); err != nil {
		return jailError("source_reopen_local_stat", err)
	}
	// Keep every identity guard; expose only the fixed failed component so
	// namespace/runner differences can be diagnosed without source metadata.
	for _, field := range []struct {
		matches bool
		name    string
	}{
		{inherited.Dev == local.Dev, "device"},
		{inherited.Ino == local.Ino, "inode"},
		{inherited.Mode == local.Mode, "mode"},
		{inherited.Uid == local.Uid, "uid"},
		{inherited.Gid == local.Gid, "gid"},
	} {
		if !field.matches {
			return &jailFailure{step: "source_reopen_" + field.name}
		}
	}
	return unix.Dup3(current, fd, unix.O_CLOEXEC)
}

type jailFailure struct{ step string }

func (e *jailFailure) Error() string { return "e2sandbox: jail_" + e.step }
func jailError(step string, err error) error {
	if err == nil {
		return nil
	}
	var detail *jailFailure
	if errors.As(err, &detail) {
		return detail
	}
	for _, cause := range []struct {
		errno error
		code  string
	}{{unix.EPERM, "permission"}, {unix.EACCES, "access"}, {unix.ENOENT, "notfound"}, {unix.EINVAL, "invalid"}, {unix.ENOTDIR, "notdirectory"}, {unix.EBADF, "descriptor"}, {unix.ENOSYS, "unsupported"}} {
		if errors.Is(err, cause.errno) {
			return &jailFailure{step: step + "_" + cause.code}
		}
	}
	return &jailFailure{step: step}
}
func setupJail(cfg helperConfig) error {
	root := cfg.Root
	for _, source := range []struct {
		path string
		fd   int
	}{{cfg.Workspace, helperWorkspaceFD}, {cfg.ProxySocket, helperProxyFD}, {cfg.CACert, helperCAFD}} {
		if err := reopenInNamespace(source.path, source.fd); err != nil {
			return jailError("source_reopen", err)
		}
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return jailError("private_mounts", err)
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return jailError("root_path", ErrConfig)
	}
	if err := unix.Mount("tmpfs", root, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, rootTmpfsOptions); err != nil {
		return jailError("root_tmpfs", err)
	}
	for _, path := range []string{"usr/bin", "usr/lib", "usr/lib64", "bin", "lib", "lib64", "etc", "dev", "proc", "tmp", "work", "run/e2", ".oldroot"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0755); err != nil {
			return jailError("directories", err)
		}
	}
	// Mount only user-space runtime libraries. Kernel module trees and WSL
	// host-integration mounts are not tool runtime inputs and remain absent.
	triplet := ""
	switch runtime.GOARCH {
	case "amd64":
		triplet = "x86_64-linux-gnu"
	case "arm64":
		triplet = "aarch64-linux-gnu"
	default:
		return jailError("architecture", ErrUnavailable)
	}
	paths, err := sandboxRuntimePaths(triplet, filepath.Glob, isDirectory)
	if err != nil {
		return jailError("runtime_glob", ErrConfig)
	}
	seen := map[string]bool{}
	for _, path := range paths {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return jailError("system_source", ErrConfig)
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || !allowedSystemSource(resolved) {
			return jailError("system_source", ErrConfig)
		}
		if !seen[resolved] {
			if err := rejectSystemIPC(resolved); err != nil {
				return jailError("system_special_file", err)
			}
			seen[resolved] = true
		}
		target := filepath.Join(root, path)
		if err := prepareMountTarget(target, info.IsDir()); err != nil {
			return jailError("runtime_directory", err)
		}
		if err := mountReadOnly(resolved, target); err != nil {
			return jailError("system_readonly", err)
		}
	}
	// No host-writable filesystem is exposed to arbitrary commands. The empty
	// host workspace remains a lifecycle anchor; all tool files are ephemeral.
	if err := unix.Mount("tmpfs", filepath.Join(root, "work"), "tmpfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC,
		fmt.Sprintf("mode=0700,size=%d", cfg.Limits.ScratchBytes)); err != nil {
		return jailError("workspace_tmpfs", err)
	}
	for _, binding := range []struct {
		fd   int
		dest string
	}{{helperProxyFD, "run/e2/proxy.sock"}, {helperCAFD, "run/e2/ca.pem"}} {
		f, err := os.OpenFile(filepath.Join(root, binding.dest), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return jailError("endpoint_file", err)
		}
		if err = f.Close(); err != nil {
			return jailError("endpoint_close", err)
		}
		if err := mountReadOnly("/proc/self/fd/"+strconv.Itoa(binding.fd), filepath.Join(root, binding.dest)); err != nil {
			return jailError("endpoint_bind_readonly", err)
		}
	}
	f, err := os.OpenFile(filepath.Join(root, "run/e2/helper"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0500)
	if err != nil {
		return jailError("helper_file", err)
	}
	if err = f.Close(); err != nil {
		return jailError("helper_close", err)
	}
	selfFile, err := os.Open("/proc/self/exe")
	if err != nil {
		return jailError("helper_identity", err)
	}
	childHelper, err := os.Open(cfg.Helper)
	if err != nil {
		selfFile.Close()
		return jailError("helper_reopen", err)
	}
	a, ae := selfFile.Stat()
	b, be := childHelper.Stat()
	selfFile.Close()
	if ae != nil || be != nil || !os.SameFile(a, b) {
		childHelper.Close()
		return jailError("helper_identity", ErrConfig)
	}
	if err := mountReadOnly("/proc/self/fd/"+strconv.Itoa(int(childHelper.Fd())), filepath.Join(root, "run/e2/helper")); err != nil {
		childHelper.Close()
		return jailError("helper_readonly", err)
	}
	childHelper.Close()
	for _, name := range []string{"null", "zero", "random", "urandom"} {
		target := filepath.Join(root, "dev", name)
		f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return jailError("device_file", err)
		}
		if err = f.Close(); err != nil {
			return jailError("device_close", err)
		}
		if err := unix.Mount("/dev/"+name, target, "", unix.MS_BIND, ""); err != nil {
			return jailError("device_bind", err)
		}
	}
	for name, target := range map[string]string{"fd": "/proc/self/fd", "stdin": "/proc/self/fd/0", "stdout": "/proc/self/fd/1", "stderr": "/proc/self/fd/2"} {
		if err := os.Symlink(target, filepath.Join(root, "dev", name)); err != nil {
			return jailError("device_links", err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "etc/hosts"), []byte("127.0.0.1 localhost\n::1 localhost\n"), 0444); err != nil {
		return jailError("hosts_file", err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc/nsswitch.conf"), []byte("hosts: files\n"), 0444); err != nil {
		return jailError("nsswitch_file", err)
	}
	if err := unix.Mount("tmpfs", filepath.Join(root, "tmp"), "tmpfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, fmt.Sprintf("mode=1777,size=%d", cfg.Limits.TempBytes)); err != nil {
		return jailError("tmp_tmpfs", err)
	}
	if err := unix.Mount("proc", filepath.Join(root, "proc"), "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC|unix.MS_RDONLY, ""); err != nil {
		return jailError("private_proc", err)
	}
	for _, fd := range []int{helperWorkspaceFD, helperProxyFD, helperCAFD} {
		if err := unix.Close(fd); err != nil {
			return jailError("oldroot_fds", err)
		}
	}
	if err := unix.Chdir(root); err != nil {
		return jailError("root_chdir", err)
	}
	if err := unix.PivotRoot(".", ".oldroot"); err != nil {
		return jailError("pivot_root", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return jailError("jail_chdir", err)
	}
	if err := unix.Unmount("/.oldroot", unix.MNT_DETACH); err != nil {
		return jailError("oldroot_detach", err)
	}
	if err := os.Remove("/.oldroot"); err != nil {
		return jailError("oldroot_remove", err)
	}
	return jailError("root_readonly", unix.Mount("", "/", "", unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, ""))
}

// Symlinks resolve inside the final jail; special filesystem objects must not
// expose a pre-existing host IPC endpoint or device through a library tree.
func rejectSystemIPC(root string) error {
	return filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return ErrConfig
		}
		st, err := d.Info()
		if err != nil {
			return ErrConfig
		}
		if st.Mode()&(os.ModeSocket|os.ModeNamedPipe|os.ModeDevice) != 0 {
			return ErrConfig
		}
		return nil
	})
}

func bringLoopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	req, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	req.SetUint16(unix.IFF_UP | unix.IFF_LOOPBACK)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, req)
}

type socketRelay struct {
	listener net.Listener
	mu       sync.Mutex
	closed   bool
	conns    map[net.Conn]bool
	pending  sync.WaitGroup
	done     chan struct{}
	once     sync.Once
}

func newRelay(listener net.Listener) *socketRelay {
	return &socketRelay{listener: listener, conns: map[net.Conn]bool{}, done: make(chan struct{})}
}
func (r *socketRelay) start() {
	go func() {
		defer close(r.done)
		for {
			c, err := r.listener.Accept()
			if err != nil {
				return
			}
			r.mu.Lock()
			if r.closed || len(r.conns) >= maxRelayConnections {
				r.mu.Unlock()
				c.Close()
				continue
			}
			r.conns[c] = true
			r.pending.Add(1)
			r.mu.Unlock()
			go r.forward(c)
		}
	}()
}
func (r *socketRelay) forward(c net.Conn) {
	defer r.pending.Done()
	defer r.forget(c)
	upstream, err := net.DialTimeout("unix", "/run/e2/proxy.sock", relayDialTimeout)
	if err != nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		upstream.Close()
		return
	}
	r.conns[upstream] = true
	r.mu.Unlock()
	defer r.forget(upstream)
	finished := make(chan struct{}, 2)
	copyBytes := func(dst, src net.Conn) { _, _ = io.Copy(dst, src); finished <- struct{}{} }
	go copyBytes(upstream, c)
	go copyBytes(c, upstream)
	<-finished
	c.Close()
	upstream.Close()
	<-finished
}
func (r *socketRelay) forget(c net.Conn) { c.Close(); r.mu.Lock(); delete(r.conns, c); r.mu.Unlock() }
func (r *socketRelay) close() {
	r.once.Do(func() {
		r.mu.Lock()
		r.closed = true
		_ = r.listener.Close()
		for c := range r.conns {
			_ = c.Close()
		}
		r.mu.Unlock()
	})
}
func (r *socketRelay) wait() { <-r.done; r.pending.Wait() }
