//go:build linux

package diagnosisprep

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const transportTimeout = 10 * time.Second
const maxConnections = 16

func socketLocation(path string) (string, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !reference.MatchString(filepath.Base(path)) {
		return "", "", ErrProtocol
	}
	return filepath.Dir(path), filepath.Base(path), nil
}
func fdPath(root *protectedRoot, name string) string {
	return "/proc/self/fd/" + strconv.FormatUint(uint64(root.file.Fd()), 10) + "/" + name
}
func socketInfo(root *protectedRoot, name string) (os.FileInfo, error) {
	info, err := os.Lstat(fdPath(root, name))
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return nil, ErrProtocol
	}
	return info, nil
}
func peerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credentials *syscall.Ucred
	var getErr error
	err = raw.Control(func(fd uintptr) {
		credentials, getErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil || getErr != nil || credentials == nil {
		return 0, ErrProtocol
	}
	return credentials.Uid, nil
}

type UnixClient struct {
	root   *protectedRoot
	name   string
	mu     sync.Mutex
	closed bool
}

func NewUnixClient(path string) (*UnixClient, error) {
	parent, name, err := socketLocation(path)
	if err != nil {
		return nil, err
	}
	root, err := openProtectedRoot(parent)
	if err != nil {
		return nil, ErrUnavailable
	}
	return &UnixClient{root: root, name: name}, nil
}

// Call마다 새 연결을 한 번만 쓴다. 응답 유실 뒤 변경 요청을 재전송하지 않는다.
func (c *UnixClient) Call(ctx context.Context, r Request) (Response, error) {
	if ctx == nil || ctx.Err() != nil || ValidateRequest(r) != nil {
		return Response{}, ErrProtocol
	}
	c.mu.Lock()
	if c.closed || !c.root.current() {
		c.mu.Unlock()
		return Response{}, ErrUnavailable
	}
	before, err := socketInfo(c.root, c.name)
	path := fdPath(c.root, c.name)
	if err != nil {
		c.mu.Unlock()
		return Response{}, ErrUnavailable
	}
	deadline := time.Now().Add(transportTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	dialCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "unix", path)
	after, statErr := socketInfo(c.root, c.name)
	c.mu.Unlock()
	if err != nil {
		return Response{}, ErrUnavailable
	}
	defer conn.Close()
	unix, ok := conn.(*net.UnixConn)
	if !ok || statErr != nil || !os.SameFile(before, after) {
		return Response{}, ErrUnavailable
	}
	uid, err := peerUID(unix)
	if err != nil || uid != uint32(os.Geteuid()) {
		return Response{}, ErrUnavailable
	}
	if conn.SetDeadline(deadline) != nil {
		return Response{}, ErrUnavailable
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	raw, err := json.Marshal(r)
	if err != nil || len(raw)+1 > MaxFrameBytes {
		return Response{}, ErrProtocol
	}
	raw = append(raw, '\n')
	if n, err := conn.Write(raw); err != nil || n != len(raw) {
		return Response{}, ErrUnavailable
	}
	// 쓰기 방향을 닫아 같은 연결의 추가 프레임도 확인한다.
	if unix.CloseWrite() != nil {
		return Response{}, ErrUnavailable
	}
	reader := bufio.NewReaderSize(conn, MaxFrameBytes+1)
	frame, err := reader.ReadSlice('\n')
	if err != nil || len(frame) > MaxFrameBytes || ctx.Err() != nil {
		return Response{}, ErrUnavailable
	}
	var response Response
	if strictDecode(frame, &response) != nil || ValidateResponse(r, response) != nil {
		return Response{}, ErrProtocol
	}
	if _, err := reader.ReadByte(); err != io.EOF {
		return Response{}, ErrProtocol
	}
	return response, nil
}
func (c *UnixClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.root.file.Close()
}

type UnixServer struct {
	root      *protectedRoot
	name      string
	listener  *net.UnixListener
	lock      *os.File
	socket    os.FileInfo
	endpoint  Endpoint
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	active    map[*net.UnixConn]struct{}
	closing   bool
	slots     chan struct{}
	done      chan struct{}
	workers   sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

func ListenUnix(ctx context.Context, path string, endpoint Endpoint) (*UnixServer, error) {
	if ctx == nil || ctx.Err() != nil || endpoint == nil {
		return nil, ErrProtocol
	}
	parent, name, err := socketLocation(path)
	if err != nil {
		return nil, err
	}
	root, err := openProtectedRoot(parent)
	if err != nil {
		return nil, ErrUnavailable
	}
	lifetime, cancel := context.WithCancel(ctx)
	server := &UnixServer{root: root, name: name, endpoint: endpoint, ctx: lifetime, cancel: cancel, active: map[*net.UnixConn]struct{}{}, slots: make(chan struct{}, maxConnections), done: make(chan struct{})}
	fail := func() (*UnixServer, error) {
		cancel()
		if server.lock != nil {
			_ = server.lock.Close()
		}
		_ = root.file.Close()
		return nil, ErrUnavailable
	}
	lock, _, err := root.open(name+".lock", syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL)
	if errors.Is(err, syscall.EEXIST) {
		lock, _, err = root.open(name+".lock", syscall.O_RDWR)
	}
	if err != nil {
		return fail()
	}
	server.lock = lock
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return fail()
	}
	if previous, err := socketInfo(root, name); err == nil {
		// 전용 socket lease를 가진 프로세스만 동일 UID의 남은 socket을 제거한다.
		current, e := socketInfo(root, name)
		if e != nil || !os.SameFile(previous, current) || syscall.Unlinkat(int(root.file.Fd()), name) != nil {
			return fail()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail()
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: fdPath(root, name), Net: "unix"})
	if err != nil {
		return fail()
	}
	listener.SetUnlinkOnClose(false)
	server.listener = listener
	if os.Chmod(fdPath(root, name), 0600) != nil {
		_ = listener.Close()
		return fail()
	}
	server.socket, err = socketInfo(root, name)
	if err != nil || root.file.Sync() != nil {
		_ = listener.Close()
		return fail()
	}
	go server.accept()
	go func() {
		<-lifetime.Done()
		_ = server.listener.Close()
		server.mu.Lock()
		for conn := range server.active {
			_ = conn.Close()
		}
		server.mu.Unlock()
	}()
	return server, nil
}
func (s *UnixServer) accept() {
	defer close(s.done)
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			return
		}
		uid, err := peerUID(conn)
		if err != nil || uid != uint32(os.Geteuid()) {
			_ = conn.Close()
			continue
		}
		select {
		case s.slots <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			_ = conn.Close()
			<-s.slots
			return
		}
		s.active[conn] = struct{}{}
		s.workers.Add(1)
		s.mu.Unlock()
		go s.serve(conn)
	}
}
func (s *UnixServer) serve(conn *net.UnixConn) {
	defer func() {
		_ = conn.Close()
		s.mu.Lock()
		delete(s.active, conn)
		s.mu.Unlock()
		<-s.slots
		s.workers.Done()
	}()
	ctx, cancel := context.WithTimeout(s.ctx, transportTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return
	}
	reader := bufio.NewReaderSize(conn, MaxFrameBytes+1)
	raw, err := reader.ReadSlice('\n')
	if err != nil || len(raw) > MaxFrameBytes {
		return
	}
	var r Request
	if strictDecode(raw, &r) != nil || ValidateRequest(r) != nil || !s.root.current() {
		return
	}
	// 한 연결은 한 프레임뿐이다. EOF까지 확인해 뒤에 붙은 JSON을 거부한다.
	if _, err := reader.ReadByte(); err != io.EOF {
		return
	}
	response, err := s.endpoint.Call(ctx, r)
	if err != nil || ValidateResponse(r, response) != nil {
		return
	}
	raw, err = json.Marshal(response)
	if err != nil || len(raw)+1 > MaxFrameBytes {
		return
	}
	raw = append(raw, '\n')
	_, _ = conn.Write(raw)
}
func (s *UnixServer) Close(ctx context.Context) error {
	if ctx == nil {
		return ErrProtocol
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		s.cancel()
		_ = s.listener.Close()
		<-s.done
		done := make(chan struct{})
		go func() { s.workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-ctx.Done():
			s.closeErr = ErrUnavailable
		}
		if current, err := socketInfo(s.root, s.name); err == nil && os.SameFile(current, s.socket) {
			if syscall.Unlinkat(int(s.root.file.Fd()), s.name) != nil || s.root.file.Sync() != nil {
				s.closeErr = ErrUnavailable
			}
		}
		s.closeErr = errors.Join(s.closeErr, s.lock.Close(), s.root.file.Close())
	})
	return s.closeErr
}
