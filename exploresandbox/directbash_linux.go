//go:build linux

package exploresandbox

import (
	"bytes"
	"context"
	"crypto/x509"
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

	"github.com/Autumn-27/artex/e2sandbox"
	"github.com/Autumn-27/artex/exploration"
)

// DirectBash는 회차 하나의 탐색 직접 Bash다. 명령은 한 번에 하나만 돈다. 정리를 확인하지 못하면(ErrIncomplete)
// 격리 실패로 보고 그 뒤 새 명령을 받지 않는다(CLAUDE.md: 격리 실패만 새 송신을 멈춘다).
type DirectBash struct {
	cfg    Config
	slot   chan struct{}
	mu     sync.Mutex
	broken bool
}

func New(cfg Config) (*DirectBash, error) {
	if !trustedPath(cfg.Helper, false) || !trustedPath(cfg.RootDir, true) || !validProxy(cfg.DirectProxy) ||
		cfg.Timeout <= 0 || cfg.Timeout > maxCommandTime || cfg.DrainTimeout <= 0 || cfg.DrainTimeout > maxDrainTime ||
		cfg.OutputLimit < 1 || cfg.OutputLimit > maxOutputBytes || !validCA(cfg.CACert) {
		return nil, ErrConfig
	}
	proxy := *cfg.DirectProxy
	cfg.DirectProxy = &proxy
	cfg.CACert = append([]byte(nil), cfg.CACert...)
	return &DirectBash{cfg: cfg, slot: make(chan struct{}, 1)}, nil
}

// CommandTimeout은 명령 하나의 실행 상한이다. 도구 입력의 시간은 이보다 짧게만 줄 수 있다.
func (b *DirectBash) CommandTimeout() time.Duration {
	if b == nil {
		return 0
	}
	return b.cfg.Timeout
}

func (b *DirectBash) String() string { return "exploresandbox.DirectBash[protected]" }

func (b *DirectBash) isBroken() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.broken
}

// Run은 명령 하나를 격리 실행한다. timeout은 0이면 설치 상한, 아니면 그보다 짧은 값만 쓴다.
func (b *DirectBash) Run(ctx context.Context, command string, timeout time.Duration) (Result, error) {
	if b == nil || ctx == nil || ctx.Err() != nil || command == "" || len(command) > maxCommandBytes || strings.IndexByte(command, 0) >= 0 ||
		timeout < 0 || timeout > b.cfg.Timeout {
		return Result{}, ErrConfig
	}
	if timeout == 0 {
		timeout = b.cfg.Timeout
	}
	select {
	case b.slot <- struct{}{}:
	case <-ctx.Done():
		return Result{}, ErrUnavailable
	}
	defer func() { <-b.slot }()
	if b.isBroken() {
		return Result{}, ErrUnavailable
	}
	call, err := b.prepare()
	if err != nil {
		if removeErr := call.remove(); removeErr != nil {
			b.fail("direct_bash_cleanup", removeErr)
			return Result{}, ErrIncomplete
		}
		return Result{}, ErrUnavailable
	}
	process, err := e2sandbox.Launch(ctx, e2sandbox.Config{Helper: b.cfg.Helper, Workspace: call.workspace, ProxySocket: call.socket,
		CACert: call.ca, Command: command, Timeout: timeout, DrainTimeout: b.cfg.DrainTimeout, OutputLimit: b.cfg.OutputLimit})
	if err != nil {
		call.listener.Close()
		if removeErr := call.remove(); removeErr != nil {
			b.fail("direct_bash_cleanup", removeErr)
			return Result{}, ErrIncomplete
		}
		return Result{}, ErrUnavailable
	}
	r := newRelay(call.listener, process, b.cfg.DirectProxy.Host)
	go r.serve()
	startErr := process.Start(ctx)
	if startErr == nil {
		waitDone := make(chan struct{})
		go func() { _, _ = process.Wait(ctx); close(waitDone) }()
		select {
		case <-process.BashDone():
		case <-waitDone:
		case <-ctx.Done():
		}
	}
	// 명령이 끝나도 namespace 안에 남은 자식이 있을 수 있어 늘 Stop으로 마감한다(진단 Bash와 같은 순서).
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), b.cfg.DrainTimeout)
	defer cancel()
	stopErr := process.Stop(drainCtx)
	processResult, waitErr := process.Wait(drainCtx)
	relayErr := r.close(drainCtx)
	closeErr := process.Close()
	removeErr := call.remove()
	accepted, refused := r.counts()
	result := Result{Output: processResult.Output, OutputTruncated: processResult.OutputTruncated, ExitCode: -1,
		BashExited: processResult.BashExited, Reason: processResult.Reason, RelayConnections: accepted, RelayRefused: refused}
	if processResult.BashExited && processResult.Drained {
		result.ExitCode = processResult.ExitCode
	}
	if cleanup := errors.Join(stopErr, waitErr, relayErr, closeErr, removeErr); cleanup != nil || !processResult.Drained {
		if cleanup == nil {
			cleanup = ErrIncomplete
		}
		b.fail("direct_bash_cleanup", cleanup)
		return result, ErrIncomplete
	}
	if startErr != nil {
		return result, ErrUnavailable
	}
	return result, nil
}

func (b *DirectBash) fail(stage string, err error) {
	b.mu.Lock()
	b.broken = true
	b.mu.Unlock()
	exploration.ReportFailure(stage, err)
}

type callDir struct {
	dir, workspace, ca, socket string
	listener                   *net.UnixListener
}

// prepare는 호출 디렉터리(0700)에 빈 작업 공간·경계 CA·relay 소켓(0600)을 만든다.
func (b *DirectBash) prepare() (*callDir, error) {
	c := &callDir{}
	dir, err := os.MkdirTemp(b.cfg.RootDir, "call-")
	if err != nil {
		return c, err
	}
	c.dir = dir
	if err = os.Chmod(dir, 0700); err != nil {
		return c, err
	}
	c.workspace = filepath.Join(dir, "work")
	if err = os.Mkdir(c.workspace, 0700); err != nil {
		return c, err
	}
	c.ca = filepath.Join(dir, "ca.pem")
	f, err := os.OpenFile(c.ca, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return c, err
	}
	n, writeErr := f.Write(b.cfg.CACert)
	if err = errors.Join(writeErr, f.Close()); err != nil || n != len(b.cfg.CACert) {
		return c, errors.Join(err, ErrUnavailable)
	}
	c.socket = filepath.Join(dir, "relay.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: c.socket, Net: "unix"})
	if err != nil {
		return c, err
	}
	listener.SetUnlinkOnClose(false)
	c.listener = listener
	if err = os.Chmod(c.socket, 0600); err != nil {
		listener.Close()
		return c, err
	}
	return c, nil
}

func (c *callDir) remove() error {
	if c == nil || c.dir == "" {
		return nil
	}
	return os.RemoveAll(c.dir)
}

// peerVerifier는 e2sandbox.Process.LivePeer다: 그 호출이 띄운 relay 프로세스(살아 있는 pidfd·같은 namespace)만 받는다.
type peerVerifier interface {
	LivePeer(pid int32, uid, gid uint32) bool
}

// relay는 호출 소켓에 붙은 샌드박스 relay 연결을 경계 직접 입구 한 주소로만 잇는다. 요청을 해석하지 않는다:
// 목적지(절대 형식·CONNECT)가 무엇이든 받는 쪽은 늘 직접 입구이고, 범위 판정은 경계가 한다.
type relay struct {
	listener *net.UnixListener
	peers    peerVerifier
	addr     string
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	closed   bool
	accepted int
	refused  int
	wg       sync.WaitGroup
	done     chan struct{}
}

func newRelay(listener *net.UnixListener, peers peerVerifier, addr string) *relay {
	return &relay{listener: listener, peers: peers, addr: addr, conns: map[net.Conn]struct{}{}, done: make(chan struct{})}
}

func (r *relay) serve() {
	defer close(r.done)
	for {
		conn, err := r.listener.AcceptUnix()
		if err != nil {
			return
		}
		credential, err := peerCredential(conn)
		if err != nil || credential.Pid <= 0 || !r.peers.LivePeer(credential.Pid, credential.Uid, credential.Gid) {
			conn.Close()
			r.mu.Lock()
			r.refused++
			r.mu.Unlock()
			continue
		}
		if !r.track(conn) {
			conn.Close()
			continue
		}
		r.mu.Lock()
		r.accepted++
		r.mu.Unlock()
		r.wg.Add(1)
		go r.forward(conn)
	}
}

func (r *relay) track(c net.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || len(r.conns) >= maxRelayConns {
		return false
	}
	r.conns[c] = struct{}{}
	return true
}

func (r *relay) forget(c net.Conn) {
	c.Close()
	r.mu.Lock()
	delete(r.conns, c)
	r.mu.Unlock()
}

func (r *relay) forward(down *net.UnixConn) {
	defer r.wg.Done()
	defer r.forget(down)
	dialer := net.Dialer{Timeout: relayDialTimeout}
	up, err := dialer.Dial("tcp", r.addr)
	if err != nil {
		exploration.ReportFailure("direct_bash_relay", err)
		return
	}
	if !r.track(up) {
		up.Close()
		return
	}
	defer r.forget(up)
	var copies sync.WaitGroup
	copies.Add(2)
	go func() {
		defer copies.Done()
		_, _ = io.Copy(up, down)
		if tcp, ok := up.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	go func() {
		defer copies.Done()
		_, _ = io.Copy(down, up)
		_ = down.CloseWrite()
	}()
	copies.Wait()
}

func (r *relay) counts() (accepted, refused int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.accepted, r.refused
}

// close는 새 연결을 막고 열린 연결을 모두 끊은 뒤 relay goroutine이 끝나기를 기다린다.
func (r *relay) close(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	for c := range r.conns {
		c.Close()
	}
	r.mu.Unlock()
	listenerErr := r.listener.Close()
	finished := make(chan struct{})
	go func() { <-r.done; r.wg.Wait(); close(finished) }()
	select {
	case <-finished:
		if listenerErr != nil && !errors.Is(listenerErr, net.ErrClosed) {
			return listenerErr
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func peerCredential(conn *net.UnixConn) (*syscall.Ucred, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil, err
	}
	var credential *syscall.Ucred
	var sockErr error
	if err = raw.Control(func(fd uintptr) {
		credential, sockErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || sockErr != nil || credential == nil {
		return nil, errors.Join(err, sockErr, ErrUnavailable)
	}
	return credential, nil
}

// validCA는 PEM 인증서 블록만 있고(하나 이상) 모두 x509로 읽히는지 본다.
func validCA(data []byte) bool {
	if len(data) == 0 || len(data) > maxCACertBytes {
		return false
	}
	rest := data
	count := 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return false
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return false
		}
		count++
	}
	return count > 0 && len(bytes.TrimSpace(rest)) == 0
}

// trustedPath는 진단 러너(e2runner)와 같은 조건이다: 절대·정규 경로, 링크 아님, 소유자는 서버 uid(실행 파일은 root도),
// 디렉터리는 정확히 0700, 파일은 그룹·남 쓰기 없음.
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
