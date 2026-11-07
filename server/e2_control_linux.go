//go:build linux

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type e2ControlUnix struct {
	service     *E2ControlService
	directory   *os.File
	listener    *net.UnixListener
	socket      os.FileInfo
	acceptDone  chan struct{}
	mu          sync.Mutex
	closing     bool
	active      map[*net.UnixConn]struct{}
	slots       chan struct{}
	workers     sync.WaitGroup
	joinOnce    sync.Once
	workersDone chan struct{}
	closed      bool
	closeErr    error
}

func startE2ControlTransport(service *E2ControlService) (e2ControlTransport, error) {
	opts := service.opts
	if uint32(os.Geteuid()) != opts.RunnerUID || !filepath.IsAbs(opts.ControlDirectory) || filepath.Clean(opts.ControlDirectory) != opts.ControlDirectory {
		return nil, ErrE2Control
	}
	resolved, err := filepath.EvalSymlinks(opts.ControlDirectory)
	if err != nil || resolved != opts.ControlDirectory {
		return nil, ErrE2Control
	}
	directory, err := os.OpenFile(opts.ControlDirectory, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrE2Control
	}
	info, err := directory.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		directory.Close()
		return nil, ErrE2Control
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != opts.RunnerUID {
		directory.Close()
		return nil, ErrE2Control
	}
	// 열린 디렉터리 FD 아래의 단일 이름만 사용해 경로 교체와 sun_path 상한을 피한다.
	path := "/proc/self/fd/" + strconv.FormatUint(uint64(directory.Fd()), 10) + "/" + E2ControlSocket
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		directory.Close()
		return nil, ErrE2Control
	}
	listener.SetUnlinkOnClose(false)
	if os.Chmod(path, 0600) != nil {
		listener.Close()
		os.Remove(path)
		directory.Close()
		return nil, ErrE2Control
	}
	socket, err := os.Lstat(path)
	if err != nil {
		listener.Close()
		os.Remove(path)
		directory.Close()
		return nil, ErrE2Control
	}
	transport := &e2ControlUnix{service: service, directory: directory, listener: listener, socket: socket, acceptDone: make(chan struct{}), active: map[*net.UnixConn]struct{}{}, slots: make(chan struct{}, opts.MaxConnections), workersDone: make(chan struct{})}
	go transport.accept()
	go func() { <-service.ctx.Done(); _ = listener.Close() }()
	return transport, nil
}
func (t *e2ControlUnix) accept() {
	defer close(t.acceptDone)
	for {
		conn, err := t.listener.AcceptUnix()
		if err != nil {
			return
		}
		uid, err := e2ControlPeerUID(conn)
		if err != nil || uid != t.service.opts.RunnerUID {
			_ = conn.Close()
			continue
		}
		select {
		case t.slots <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		t.mu.Lock()
		if t.closing {
			t.mu.Unlock()
			<-t.slots
			_ = conn.Close()
			return
		}
		t.active[conn] = struct{}{}
		t.workers.Add(1)
		t.mu.Unlock()
		go t.serve(conn)
	}
}
func e2ControlPeerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var getErr error
	if err = raw.Control(func(fd uintptr) {
		cred, getErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if getErr != nil || cred == nil {
		return 0, ErrE2Control
	}
	return cred.Uid, nil
}
func (t *e2ControlUnix) serve(conn *net.UnixConn) {
	defer func() {
		t.mu.Lock()
		delete(t.active, conn)
		t.mu.Unlock()
		_ = conn.Close()
		<-t.slots
		t.workers.Done()
	}()
	deadline := time.Now().Add(t.service.opts.IOTimeout)
	if conn.SetDeadline(deadline) != nil {
		return
	}
	ctx, cancel := context.WithDeadline(t.service.ctx, deadline)
	defer cancel()
	reader := bufio.NewReaderSize(conn, e2ControlMaxMessage+1)
	line, err := reader.ReadSlice('\n')
	response := t.service.refused("bad_request")
	if err == nil && len(line) <= e2ControlMaxMessage && reader.Buffered() == 0 {
		response = t.service.handle(ctx, line[:len(line)-1])
	}
	data, err := json.Marshal(response)
	if err != nil || len(data)+1 > e2ControlMaxMessage {
		return
	}
	data = append(data, '\n')
	_, _ = conn.Write(data)
}
func (t *e2ControlUnix) Close(ctx context.Context) error {
	if t == nil || ctx == nil {
		return ErrE2Control
	}
	t.mu.Lock()
	if t.closed {
		err := t.closeErr
		t.mu.Unlock()
		return err
	}
	t.closing = true
	t.mu.Unlock()
	_ = t.listener.Close()
	select {
	case <-t.acceptDone:
	case <-ctx.Done():
		return ErrE2Control
	}
	t.mu.Lock()
	for conn := range t.active {
		_ = conn.Close()
	}
	t.mu.Unlock()
	t.joinOnce.Do(func() { go func() { t.workers.Wait(); close(t.workersDone) }() })
	select {
	case <-t.workersDone:
	case <-ctx.Done():
		return ErrE2Control
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return t.closeErr
	}
	t.closed = true
	path := "/proc/self/fd/" + strconv.FormatUint(uint64(t.directory.Fd()), 10) + "/" + E2ControlSocket
	current, err := os.Lstat(path)
	if err == nil && os.SameFile(current, t.socket) {
		if os.Remove(path) != nil {
			t.closeErr = ErrE2Control
			return t.closeErr
		}
	}
	if t.directory.Close() != nil {
		t.closeErr = ErrE2Control
	}
	return t.closeErr
}
