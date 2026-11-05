//go:build linux

package e2sandbox

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if os.Getpid() == 1 && os.Getppid() == 0 {
		os.Exit(HelperMain())
	}
	os.Exit(m.Run())
}

type sandboxFixture struct {
	cfg      Config
	proxy    net.Listener
	server   *http.Server
	target   *httptest.Server
	receipts atomic.Int64
	peers    atomic.Int64
	mu       sync.Mutex
	process  *Process
}

func newSandboxFixture(t *testing.T, command string) *sandboxFixture {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("requires unprivileged host user namespace")
	}
	dir, err := os.MkdirTemp("", "e2-sb-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	workspace := filepath.Join(dir, "work")
	if os.Mkdir(workspace, 0700) != nil {
		t.Fatal("workspace create")
	}
	ca := filepath.Join(dir, "public-ca.pem")
	if os.WriteFile(ca, []byte("synthetic-public-ca"), 0600) != nil {
		t.Fatal("ca create")
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := &sandboxFixture{}
	f.target = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.receipts.Add(1)
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "owned-target-ok")
	}))
	t.Cleanup(f.target.Close)
	f.proxy, err = net.Listen("unix", filepath.Join(dir, "p.sock"))
	if err != nil {
		t.Fatal(err)
	}
	f.server = &http.Server{
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			u, ok := c.(*net.UnixConn)
			if !ok {
				return context.WithValue(ctx, "owned", false)
			}
			raw, e := u.SyscallConn()
			if e != nil {
				return context.WithValue(ctx, "owned", false)
			}
			var cred *unix.Ucred
			_ = raw.Control(func(fd uintptr) { cred, _ = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
			f.mu.Lock()
			p := f.process
			f.mu.Unlock()
			valid := cred != nil && p != nil && p.LivePeer(cred.Pid, cred.Uid, cred.Gid)
			if valid {
				f.peers.Add(1)
			}
			return context.WithValue(ctx, "owned", valid)
		},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if valid, _ := r.Context().Value("owned").(bool); !valid {
				http.Error(w, "peer_denied", 403)
				return
			}
			if !r.URL.IsAbs() || r.URL.Scheme != "http" || r.URL.Host != strings.TrimPrefix(f.target.URL, "http://") || r.Method != "GET" {
				http.Error(w, "not_allowed", 403)
				return
			}
			out := r.Clone(r.Context())
			out.RequestURI = ""
			out.Header = r.Header.Clone()
			out.Close = true
			tr := &http.Transport{Proxy: nil, DisableKeepAlives: true}
			defer tr.CloseIdleConnections()
			response, e := tr.RoundTrip(out)
			if e != nil {
				http.Error(w, "send_unknown", 502)
				return
			}
			defer response.Body.Close()
			w.WriteHeader(response.StatusCode)
			_, _ = io.Copy(w, response.Body)
		}),
	}
	go func() { _ = f.server.Serve(f.proxy) }()
	t.Cleanup(func() { _ = f.server.Close(); _ = f.proxy.Close() })
	f.cfg = Config{Helper: helper, Workspace: workspace, ProxySocket: filepath.Join(dir, "p.sock"), CACert: ca,
		Command: command, Timeout: 10 * time.Second, DrainTimeout: 2 * time.Second, OutputLimit: 65536}
	return f
}
func (f *sandboxFixture) launch(t *testing.T) *Process {
	t.Helper()
	p, err := Launch(t.Context(), f.cfg)
	if err != nil {
		t.Fatalf("real namespace launch unavailable: %v", err)
	}
	f.mu.Lock()
	f.process = p
	f.mu.Unlock()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = p.Stop(ctx)
		_ = p.Close()
	})
	return p
}
func (f *sandboxFixture) run(t *testing.T) Result {
	t.Helper()
	p := f.launch(t)
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	r, err := p.Wait(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSandboxActualHTTPRelayAndPeer(t *testing.T) {
	f := newSandboxFixture(t, "placeholder")
	f.cfg.Command = "curl -fsS --max-time 3 " + f.target.URL + "/allowed"
	p := f.launch(t)
	if p.LivePeer(int32(p.PID()), 0, 0) || p.LivePeer(0, uint32(os.Getuid()), uint32(os.Getgid())) {
		t.Fatal("peer forgery accepted")
	}
	if !p.LivePeer(int32(p.PID()), uint32(os.Getuid()), uint32(os.Getgid())) {
		t.Fatal("owned helper rejected")
	}
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p.Start(t.Context()) == nil {
		t.Fatal("second Bash Start accepted")
	}
	r, err := p.Wait(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if r.ExitCode != 0 || !r.BashExited || !r.Drained || string(r.Output) != "owned-target-ok" || f.receipts.Load() != 1 || f.peers.Load() != 1 {
		t.Fatalf("relay evidence: result=%+v receipts=%d peers=%d", r, f.receipts.Load(), f.peers.Load())
	}
	if p.LivePeer(int32(p.PID()), uint32(os.Getuid()), uint32(os.Getgid())) {
		t.Fatal("exited peer accepted")
	}
}

func TestSandboxBlocksDirectAndEnvironmentRemoval(t *testing.T) {
	for _, kind := range []string{"env_removed", "explicit_no_proxy", "shell_child", "ipv6", "udp", "unsupported_socks"} {
		t.Run(kind, func(t *testing.T) {
			f := newSandboxFixture(t, "placeholder")
			var udp *net.UDPConn
			if kind == "ipv6" {
				v6, e := net.Listen("tcp6", "[::1]:0")
				if e != nil {
					t.Skip("IPv6 loopback unavailable")
				}
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.receipts.Add(1); w.WriteHeader(200) }))
				server.Listener = v6
				server.Start()
				defer server.Close()
				resp, e := http.Get(server.URL)
				if e != nil {
					t.Fatal("IPv6 positive control unavailable")
				}
				resp.Body.Close()
				if f.receipts.Swap(0) != 1 {
					t.Fatal("IPv6 positive control missing")
				}
				f.cfg.Command = "curl -sS --max-time 1 --noproxy '*' " + server.URL + "/direct"
			}
			if kind == "udp" {
				var e error
				udp, e = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
				if e != nil {
					t.Fatal(e)
				}
				defer udp.Close()
				positive, e := net.DialUDP("udp4", nil, udp.LocalAddr().(*net.UDPAddr))
				if e != nil {
					t.Fatal(e)
				}
				if _, e = positive.Write([]byte("positive")); e != nil {
					t.Fatal(e)
				}
				positive.Close()
				_ = udp.SetReadDeadline(time.Now().Add(time.Second))
				var b [32]byte
				if n, _, e := udp.ReadFromUDP(b[:]); e != nil || n != 8 {
					t.Fatal("UDP positive control missing")
				}
			}
			direct := "curl -sS --max-time 1 --noproxy '*' " + f.target.URL + "/direct"
			switch kind {
			case "env_removed":
				f.cfg.Command = "env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY -u http_proxy -u https_proxy -u all_proxy " + direct
			case "explicit_no_proxy":
				f.cfg.Command = direct
			case "shell_child":
				f.cfg.Command = "bash -c " + fmt.Sprintf("%q", direct)
			case "ipv6":
				// Command uses the positive-controlled host IPv6 listener above.
			case "udp":
				f.cfg.Command = fmt.Sprintf("bash -c 'echo blocked >/dev/udp/127.0.0.1/%d'; exit 0", udp.LocalAddr().(*net.UDPAddr).Port)
			case "unsupported_socks":
				f.cfg.Command = "curl -sS --max-time 1 --proxy socks5h://127.0.0.1:1 " + f.target.URL + "/direct"
			}
			r := f.run(t)
			if udp != nil {
				_ = udp.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
				var b [32]byte
				if _, _, e := udp.ReadFromUDP(b[:]); e == nil {
					t.Fatal("host UDP target received isolated child datagram")
				}
			}
			if (r.ExitCode == 0 && kind != "udp") || !r.Drained || f.receipts.Load() != 0 {
				t.Fatalf("direct route result=%+v receipts=%d", r, f.receipts.Load())
			}
		})
	}
}

func TestSandboxJailSecretsAndPrivilegeEscape(t *testing.T) {
	f := newSandboxFixture(t, "placeholder")
	markerDir, err := os.MkdirTemp("", "e2-host-private-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(markerDir)
	marker := filepath.Join(markerDir, "not-tool-readable")
	if os.WriteFile(marker, []byte("host-private-marker"), 0600) != nil {
		t.Fatal("marker")
	}
	f.cfg.Command = "test ! -e " + marker + " && test ! -e /home && test ! -e /mnt && test ! -e /proc/self/fd/3 && test ! -e /proc/self/fd/7 && ! ( : </proc/1/mem ) && ! ( : </proc/1/environ ) && ! ( : </proc/1/fd/3 ) && ! unshare --user true && ! unshare --net true && ! mount -t tmpfs tmpfs /work && ! chroot /work true && grep -E '^(Cap(Inh|Prm|Eff|Bnd|Amb)|NoNewPrivs|Seccomp):' /proc/self/status"
	r := f.run(t)
	if r.ExitCode != 0 || !r.Drained || strings.Contains(string(r.Output), "host-private-marker") {
		t.Fatalf("jail result=%+v", r)
	}
	for _, field := range []string{"CapInh:", "CapPrm:", "CapEff:", "CapBnd:", "CapAmb:"} {
		if !strings.Contains(string(r.Output), field+"\t0000000000000000") {
			t.Fatalf("nonempty capability %s: %s", field, r.Output)
		}
	}
}

func TestSandboxBackgroundDrainsAfterBash(t *testing.T) {
	f := newSandboxFixture(t, "placeholder")
	f.cfg.Command = "(sleep 1; curl -sS --max-time 1 " + f.target.URL + "/late) >/work/background 2>&1 & echo bash-returned"
	r := f.run(t)
	if !r.BashExited || !r.Drained || r.ExitCode != 0 {
		t.Fatalf("drain result=%+v", r)
	}
	time.Sleep(1200 * time.Millisecond)
	if f.receipts.Load() != 0 {
		t.Fatal("background survived namespace init")
	}
}
func TestSandboxStopAndTimeout(t *testing.T) {
	for _, kind := range []string{"stop", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			f := newSandboxFixture(t, "sleep 30")
			if kind == "timeout" {
				f.cfg.Timeout = 2 * time.Second
			}
			p := f.launch(t)
			if err := p.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			if kind == "stop" {
				if err := p.Stop(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			r, err := p.Wait(t.Context())
			if err != nil || !r.Drained {
				t.Fatalf("stop evidence %+v %v", r, err)
			}
			if kind == "timeout" && r.Reason != "timeout" {
				t.Fatalf("timeout reason %q", r.Reason)
			}
		})
	}
}
func TestSandboxRejectsNonPrivateOrNonEmptyWorkspace(t *testing.T) {
	for _, kind := range []string{"public", "nonempty", "symlink", "zero_timeout", "zero_output", "socket_as_ca"} {
		t.Run(kind, func(t *testing.T) {
			f := newSandboxFixture(t, "true")
			switch kind {
			case "public":
				_ = os.Chmod(f.cfg.Workspace, 0755)
			case "nonempty":
				_ = os.WriteFile(filepath.Join(f.cfg.Workspace, "existing"), []byte("preserve"), 0600)
			case "symlink":
				link := f.cfg.Workspace + "-link"
				_ = os.Symlink(f.cfg.Workspace, link)
				f.cfg.Workspace = link
			case "zero_timeout":
				f.cfg.Timeout = 0
			case "zero_output":
				f.cfg.OutputLimit = 0
			case "socket_as_ca":
				f.cfg.CACert = f.cfg.ProxySocket
			}
			p, err := Launch(t.Context(), f.cfg)
			if err == nil || p != nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
}
func TestSandboxOutputLimit(t *testing.T) {
	f := newSandboxFixture(t, "printf '01234567890123456789'")
	f.cfg.OutputLimit = 8
	r := f.run(t)
	if string(r.Output) != "01234567" || !r.OutputTruncated || !r.Drained {
		t.Fatalf("bounded output %+v", r)
	}
}

func TestSandboxBoundedScratchAndParentLimitsUnchanged(t *testing.T) {
	f := newSandboxFixture(t, "placeholder")
	f.cfg.ScratchBytes = 4 << 20
	f.cfg.TempBytes = 2 << 20
	f.cfg.MaxFileBytes = 1 << 20
	f.cfg.Command = `if ! stat -f -c %T /work /tmp; then exit 1; fi; if truncate -s 2097152 /work/too-large; then exit 1; fi; if ulimit -n 1024; then exit 1; fi; if ulimit -u unlimited; then exit 1; fi; if ulimit -t unlimited; then exit 1; fi; printf bounded-resource-ok`
	resources := []int{unix.RLIMIT_CPU, unix.RLIMIT_NPROC, unix.RLIMIT_NOFILE, unix.RLIMIT_FSIZE}
	before := map[int]unix.Rlimit{}
	for _, resource := range resources {
		var l unix.Rlimit
		if unix.Getrlimit(resource, &l) != nil {
			t.Fatal("parent limit read")
		}
		before[resource] = l
	}
	var rootBefore, procBefore unix.Statfs_t
	if unix.Statfs("/", &rootBefore) != nil || unix.Statfs("/proc", &procBefore) != nil {
		t.Fatal("parent mount read")
	}
	namespaceBefore, err := os.Stat("/proc/self/ns/mnt")
	if err != nil {
		t.Fatal("parent namespace")
	}
	r := f.run(t)
	if r.ExitCode != 0 || !r.Drained || !strings.Contains(string(r.Output), "bounded-resource-ok") {
		t.Fatalf("bounded resource result %+v", r)
	}
	for _, resource := range resources {
		var l unix.Rlimit
		if unix.Getrlimit(resource, &l) != nil || l != before[resource] {
			t.Fatal("parent resource limit changed")
		}
	}
	var rootAfter, procAfter unix.Statfs_t
	if unix.Statfs("/", &rootAfter) != nil || unix.Statfs("/proc", &procAfter) != nil || rootBefore.Flags != rootAfter.Flags || procBefore.Flags != procAfter.Flags {
		t.Fatal("parent mount flags changed")
	}
	namespaceAfter, err := os.Stat("/proc/self/ns/mnt")
	if err != nil || !os.SameFile(namespaceBefore, namespaceAfter) {
		t.Fatal("parent mount namespace changed")
	}
	entries, err := os.ReadDir(f.cfg.Workspace)
	if err != nil || len(entries) != 0 {
		t.Fatal("tool wrote back to host workspace")
	}
}

func TestSandboxRejectsSystemSpecialFiles(t *testing.T) {
	for _, kind := range []string{"socket", "fifo", "device"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			switch kind {
			case "socket":
				listener, err := net.Listen("unix", filepath.Join(dir, "sock"))
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
			case "fifo":
				if unix.Mkfifo(filepath.Join(dir, "fifo"), 0600) != nil {
					t.Fatal("fifo fixture")
				}
			case "device":
				if rejectSystemIPC("/dev/null") == nil {
					t.Fatal("device accepted")
				}
				return
			}
			if rejectSystemIPC(dir) == nil {
				t.Fatal("host IPC accepted in runtime tree")
			}
		})
	}
}

func TestSandboxInheritedEnvironmentSecretAbsent(t *testing.T) {
	const sentinel = "host-only-environment-marker"
	t.Setenv("E2_SANDBOX_PARENT_SECRET", sentinel)
	f := newSandboxFixture(t, "env; cat /proc/1/cmdline")
	r := f.run(t)
	if r.ExitCode != 0 || !r.Drained || strings.Contains(string(r.Output), sentinel) || strings.Contains(string(r.Output), "E2_SANDBOX_PARENT_SECRET") {
		t.Fatalf("inherited environment exposed %+v", r)
	}
}

func TestSandboxConcurrentCallPeerSeparation(t *testing.T) {
	a := newSandboxFixture(t, "placeholder")
	b := newSandboxFixture(t, "placeholder")
	a.cfg.Command = "curl -fsS --max-time 2 " + a.target.URL + "/a; sleep 0.2"
	b.cfg.Command = "curl -fsS --max-time 2 " + b.target.URL + "/b; sleep 0.2"
	pa, pb := a.launch(t), b.launch(t)
	if pa.LivePeer(int32(pb.PID()), uint32(os.Getuid()), uint32(os.Getgid())) || pb.LivePeer(int32(pa.PID()), uint32(os.Getuid()), uint32(os.Getgid())) {
		t.Fatal("other call helper accepted")
	}
	if pa.Start(t.Context()) != nil || pb.Start(t.Context()) != nil {
		t.Fatal("concurrent start")
	}
	ra, ea := pa.Wait(t.Context())
	rb, eb := pb.Wait(t.Context())
	if ea != nil || eb != nil || !ra.Drained || !rb.Drained || ra.ExitCode != 0 || rb.ExitCode != 0 || a.receipts.Load() != 1 || b.receipts.Load() != 1 {
		t.Fatal("concurrent call evidence incomplete")
	}
}
