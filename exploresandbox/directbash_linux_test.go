//go:build linux

package exploresandbox

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "xsb-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error("fixture directory cleanup failed")
		}
	})
	return dir
}

// testHelper는 진단 러너 시험과 같은 조건으로 실제 샌드박스 helper를 소스에서 만든다. 비특권 namespace를
// OS가 막으면 환경 SKIP이고, 그 뒤 실패는 시험 실패다.
func testHelper(t *testing.T, dir string) string {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("requires an unprivileged Linux host identity")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	probe := exec.CommandContext(ctx, "unshare", "--user", "--map-root-user", "--pid", "--fork", "--mount", "--net", "--ipc", "--uts", "true")
	out, err := probe.CombinedOutput()
	if err != nil {
		if bytes.Contains(out, []byte("Operation not permitted")) {
			t.Skip("OS denied required unprivileged namespaces")
		}
		t.Fatal("namespace preflight failed")
	}
	helper := filepath.Join(dir, "e2-bash-sandbox")
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	build := exec.CommandContext(ctx, goBinary, "build", "-p", "2", "-buildvcs=false", "-o", helper, "../cmd/e2-bash-sandbox")
	build.Env = append(os.Environ(), "GOMAXPROCS=2", "GOTOOLCHAIN=local", "GOTELEMETRY=off", "GOPROXY=off", "GOSUMDB=off")
	if out, err = build.CombinedOutput(); err != nil {
		t.Fatalf("helper source build failed: %s", string(out))
	}
	if err = os.Chmod(helper, 0700); err != nil {
		t.Fatal(err)
	}
	return helper
}

func testCA(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "exploration boundary test root"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// fakeEntrance는 경계 직접 입구 역할의 최소 HTTP 프록시다. 받은 절대 형식 요청 줄을 기록하고 표식 본문으로 답한다.
type fakeEntrance struct {
	listener net.Listener
	mu       sync.Mutex
	targets  []string
	conns    atomic.Int64
}

func newFakeEntrance(t *testing.T) *fakeEntrance {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeEntrance{listener: l}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			f.conns.Add(1)
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeEntrance) serve(c net.Conn) {
	defer c.Close()
	req, err := http.ReadRequest(bufio.NewReader(c))
	if err != nil {
		return
	}
	f.mu.Lock()
	f.targets = append(f.targets, req.Method+" "+req.RequestURI)
	f.mu.Unlock()
	body := "direct-entrance-saw " + req.RequestURI
	fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
}

func (f *fakeEntrance) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.targets...)
}

// otherListener는 직접 입구가 아닌 호스트 쪽 주소다(브라우저 입구·CDP·엔진 망 역할). 연결 수만 센다.
func otherListener(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	var n atomic.Int64
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			c.Close()
		}
	}()
	return l.Addr().String(), &n
}

func testConfig(t *testing.T, dir, helper, entrance string) Config {
	t.Helper()
	root := filepath.Join(dir, "calls")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	return Config{Helper: helper, RootDir: root, DirectProxy: &url.URL{Scheme: "http", Host: entrance}, CACert: testCA(t),
		Timeout: 20 * time.Second, DrainTimeout: 10 * time.Second, OutputLimit: 64 << 10}
}

// 명령은 프록시 환경 변수로 직접 입구에만 닿는다. 프록시를 끄고 호스트 주소로 직접 가면 빈 namespace라 닿지 않고,
// 프록시로 다른 호스트를 가리켜도 relay는 직접 입구로만 잇는다(목적지 판정은 경계 몫).
func TestDirectBashReachesOnlyTheDirectEntrance(t *testing.T) {
	dir := testDir(t)
	helper := testHelper(t, dir)
	entrance := newFakeEntrance(t)
	other, otherConns := otherListener(t)
	bash, err := New(testConfig(t, dir, helper, entrance.listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	command := strings.Join([]string{
		"curl -s -m 5 http://approved.test/read/a; echo",
		"curl -s -m 5 http://" + other + "/via-proxy; echo",
		"curl -s -m 5 --noproxy '*' http://" + other + "/direct; echo direct_exit=$?",
	}, "\n")
	result, err := bash.Run(t.Context(), command, 0)
	if err != nil {
		t.Fatalf("run: %v reason=%s", err, result.Reason)
	}
	out := string(result.Output)
	if !strings.Contains(out, "direct-entrance-saw http://approved.test/read/a") || !strings.Contains(out, "direct-entrance-saw http://"+other+"/via-proxy") {
		t.Fatalf("proxied requests did not reach the direct entrance: %q", out)
	}
	if strings.Contains(out, "direct_exit=0") || otherConns.Load() != 0 {
		t.Fatalf("command reached a non-entrance address: other=%d out=%q", otherConns.Load(), out)
	}
	seen := entrance.seen()
	if len(seen) != 2 || seen[0] != "GET http://approved.test/read/a" || seen[1] != "GET http://"+other+"/via-proxy" {
		t.Fatalf("entrance saw %v", seen)
	}
	if result.RelayConnections != 2 || result.RelayRefused != 0 || !result.BashExited || result.ExitCode != 0 {
		t.Fatalf("result %+v", result)
	}
	if entries, err := os.ReadDir(bash.cfg.RootDir); err != nil || len(entries) != 0 {
		t.Fatalf("call directory not removed: %v %d", err, len(entries))
	}
}

// 호출 소켓에는 그 호출이 띄운 샌드박스 relay만 붙을 수 있다. 같은 uid의 다른 프로세스가 붙으면 끊고 직접 입구로 잇지 않는다.
func TestDirectBashRefusesOtherPeers(t *testing.T) {
	dir := testDir(t)
	helper := testHelper(t, dir)
	entrance := newFakeEntrance(t)
	bash, err := New(testConfig(t, dir, helper, entrance.listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := bash.Run(t.Context(), "sleep 3", 0)
		done <- outcome{r, err}
	}()
	var socket string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && socket == ""; time.Sleep(20 * time.Millisecond) {
		matches, _ := filepath.Glob(filepath.Join(bash.cfg.RootDir, "call-*", "relay.sock"))
		if len(matches) == 1 {
			socket = matches[0]
		}
	}
	if socket == "" {
		t.Fatal("call socket not found")
	}
	conn, err := net.Dial("unix", socket)
	if err == nil {
		fmt.Fprintf(conn, "GET http://approved.test/host-peer HTTP/1.1\r\nHost: approved.test\r\n\r\n")
		buf := make([]byte, 64)
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if n, readErr := conn.Read(buf); n != 0 || readErr == nil {
			t.Fatalf("host peer got a relay response: %q", buf[:n])
		}
		conn.Close()
	}
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.result.RelayRefused < 1 || got.result.RelayConnections != 0 || entrance.conns.Load() != 0 {
		t.Fatalf("host peer reached the entrance: %+v entrance=%d", got.result, entrance.conns.Load())
	}
}

// 시간 상한에 닿으면 명령을 멈추고 정리까지 끝낸 뒤 돌려준다. 호출 디렉터리는 남지 않는다.
func TestDirectBashTimeoutStopsAndCleansUp(t *testing.T) {
	dir := testDir(t)
	helper := testHelper(t, dir)
	entrance := newFakeEntrance(t)
	bash, err := New(testConfig(t, dir, helper, entrance.listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	result, err := bash.Run(t.Context(), "sleep 30 & sleep 30", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 15*time.Second || result.ExitCode == 0 {
		t.Fatalf("timeout not enforced: %v %+v", time.Since(start), result)
	}
	if entries, err := os.ReadDir(bash.cfg.RootDir); err != nil || len(entries) != 0 {
		t.Fatalf("call directory not removed: %v %d", err, len(entries))
	}
	// 정리가 끝났으니 다음 명령도 받는다.
	if r, err := bash.Run(t.Context(), "echo next", 0); err != nil || !strings.Contains(string(r.Output), "next") {
		t.Fatalf("next run: %v %q", err, r.Output)
	}
}

// 명령은 한 번에 하나만 돈다. 앞 명령을 기다리는 동안 호출 문맥이 끝나면 실행하지 않는다.
func TestDirectBashRunsOneCommandAtATime(t *testing.T) {
	dir := testDir(t)
	helper := testHelper(t, dir)
	entrance := newFakeEntrance(t)
	bash, err := New(testConfig(t, dir, helper, entrance.listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { _, err := bash.Run(t.Context(), "sleep 2", 0); first <- err }()
	time.Sleep(300 * time.Millisecond)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, err := bash.Run(ctx, "echo second", 0); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("second command ran while the first held the slot: %v", err)
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestDirectBashConfigIsChecked(t *testing.T) {
	dir := testDir(t)
	helper := filepath.Join(dir, "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	good := func() Config {
		root := filepath.Join(dir, "root")
		_ = os.Mkdir(root, 0700)
		return Config{Helper: helper, RootDir: root, DirectProxy: &url.URL{Scheme: "http", Host: "explore-direct:3129"}, CACert: testCA(t),
			Timeout: time.Minute, DrainTimeout: 10 * time.Second, OutputLimit: 4096}
	}
	if _, err := New(good()); err != nil {
		t.Fatal("valid configuration rejected:", err)
	}
	open := filepath.Join(dir, "open")
	if err := os.Mkdir(open, 0755); err != nil {
		t.Fatal(err)
	}
	// 다른 종류 블록에 인증서 바이트를 넣어도 받지 않는다(블록 종류를 본다).
	certBlock, _ := pem.Decode(testCA(t))
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: certBlock.Bytes})
	for name, change := range map[string]func(*Config){
		"https proxy":        func(c *Config) { c.DirectProxy = &url.URL{Scheme: "https", Host: "explore-direct:3129"} },
		"proxy without port": func(c *Config) { c.DirectProxy = &url.URL{Scheme: "http", Host: "explore-direct"} },
		"proxy with path":    func(c *Config) { c.DirectProxy = &url.URL{Scheme: "http", Host: "explore-direct:3129", Path: "/x"} },
		"proxy with user":    func(c *Config) { c.DirectProxy.User = url.User("u") },
		"no proxy":           func(c *Config) { c.DirectProxy = nil },
		"root not private":   func(c *Config) { c.RootDir = open },
		"root relative":      func(c *Config) { c.RootDir = "root" },
		"helper missing":     func(c *Config) { c.Helper = filepath.Join(dir, "missing") },
		"ca empty":           func(c *Config) { c.CACert = nil },
		"ca garbage":         func(c *Config) { c.CACert = []byte("not a certificate") },
		"ca with key":        func(c *Config) { c.CACert = append(c.CACert, keyPEM...) },
		"ca trailing text":   func(c *Config) { c.CACert = append(c.CACert, []byte("tail")...) },
		"timeout zero":       func(c *Config) { c.Timeout = 0 },
		"timeout too long":   func(c *Config) { c.Timeout = maxCommandTime + time.Second },
		"drain zero":         func(c *Config) { c.DrainTimeout = 0 },
		"drain too long":     func(c *Config) { c.DrainTimeout = maxDrainTime + time.Second },
		"output zero":        func(c *Config) { c.OutputLimit = 0 },
		"output too large":   func(c *Config) { c.OutputLimit = maxOutputBytes + 1 },
	} {
		c := good()
		change(&c)
		if _, err := New(c); !errors.Is(err, ErrConfig) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	bash, _ := New(good())
	for name, run := range map[string]func() error{
		"empty command":   func() error { _, err := bash.Run(t.Context(), "", 0); return err },
		"nul command":     func() error { _, err := bash.Run(t.Context(), "echo \x00", 0); return err },
		"timeout over":    func() error { _, err := bash.Run(t.Context(), "true", 2*time.Minute); return err },
		"negative":        func() error { _, err := bash.Run(t.Context(), "true", -time.Second); return err },
		"command too big": func() error { _, err := bash.Run(t.Context(), strings.Repeat("a", maxCommandBytes+1), 0); return err },
	} {
		if err := run(); !errors.Is(err, ErrConfig) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}

// 정리를 확인하지 못하면(여기서는 호출 디렉터리를 지우지 못함) 격리 실패로 보고 그 뒤 명령을 받지 않는다.
func TestDirectBashLatchesClosedAfterIncompleteCleanup(t *testing.T) {
	dir := testDir(t)
	helper := testHelper(t, dir)
	entrance := newFakeEntrance(t)
	bash, err := New(testConfig(t, dir, helper, entrance.listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	root := bash.cfg.RootDir
	t.Cleanup(func() { _ = os.Chmod(root, 0700) })
	done := make(chan error, 1)
	go func() { _, err := bash.Run(t.Context(), "sleep 1", 0); done <- err }()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if matches, _ := filepath.Glob(filepath.Join(root, "call-*", "relay.sock")); len(matches) == 1 {
			break
		}
	}
	// 루트에서 쓰기 권한을 빼면 호출 디렉터리를 지울 수 없다.
	if err := os.Chmod(root, 0500); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrIncomplete) {
		t.Fatalf("cleanup failure not reported: %v", err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := bash.Run(t.Context(), "echo after", 0); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed DirectBash ran a new command: %v", err)
	}
}

// 명령(bash)이 끝나도 namespace에 자식이 남으면 Stop으로 정리하고 바로 돌려준다(남은 자식을 기다리며 정리 상한을 쓰지 않는다).
func TestDirectBashStopsLeftoverChildren(t *testing.T) {
	dir := testDir(t)
	helper := testHelper(t, dir)
	entrance := newFakeEntrance(t)
	cfg := testConfig(t, dir, helper, entrance.listener.Addr().String())
	cfg.DrainTimeout = 5 * time.Second
	bash, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	result, err := bash.Run(t.Context(), "sleep 60 >/dev/null 2>&1 & echo started", 0)
	if err != nil || !strings.Contains(string(result.Output), "started") || !result.BashExited || result.ExitCode != 0 {
		t.Fatalf("leftover child run: %v %+v", err, result)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("waited for the leftover child: %v", elapsed)
	}
}

// 호출 문맥이 끝나면 명령을 멈추고 정리한 뒤 돌려준다(시간 상한까지 기다리지 않는다).
func TestDirectBashCancelStopsCommand(t *testing.T) {
	dir := testDir(t)
	helper := testHelper(t, dir)
	entrance := newFakeEntrance(t)
	bash, err := New(testConfig(t, dir, helper, entrance.listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(500*time.Millisecond, cancel)
	start := time.Now()
	result, err := bash.Run(ctx, "sleep 30", 0)
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("cancel did not stop the command: %v", elapsed)
	}
	if result.BashExited && result.ExitCode == 0 {
		t.Fatalf("cancelled command reported success: %v %+v", err, result)
	}
	if entries, readErr := os.ReadDir(bash.cfg.RootDir); readErr != nil || len(entries) != 0 {
		t.Fatalf("call directory not removed: %v %d", readErr, len(entries))
	}
	if _, err := bash.Run(t.Context(), "echo next", 0); err != nil {
		t.Fatalf("next run after cancel: %v", err)
	}
}
