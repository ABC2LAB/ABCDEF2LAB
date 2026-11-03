//go:build linux

package budgetlocal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/transmit/budget"
)

const requestTimeout = 5 * time.Second
const maxInFlight = 16

// Server owns a local IPC endpoint. Server, operator, and model execution UIDs
// must be distinct so model-controlled processes cannot issue grants or replace
// a socket owned by the server UID.
type Server struct {
	listener    *net.UnixListener
	path        string
	socket      os.FileInfo
	operatorUID uint32
	approve     AuthorizeAndIssue
	ctx         context.Context
	cancel      context.CancelFunc
	acceptDone  chan struct{}
	sem         chan struct{}
	mu          sync.Mutex
	conns       map[*net.UnixConn]struct{}
	wg          sync.WaitGroup
	closed      bool
	timeout     time.Duration
}

// Listen requires a pre-provisioned, server-owned 0755 directory. The socket
// is locally reachable; SO_PEERCRED, not filesystem writability, authorizes
// issuance. No path is removed if one already exists.
func Listen(ctx context.Context, path string, operatorUID, modelUID uint32, approve AuthorizeAndIssue) (*Server, error) {
	return listen(ctx, path, operatorUID, modelUID, approve, false)
}

func listen(ctx context.Context, path string, operatorUID, modelUID uint32, approve AuthorizeAndIssue, allowSameUID bool) (*Server, error) {
	return listenWithTimeout(ctx, path, operatorUID, modelUID, approve, allowSameUID, requestTimeout)
}

func listenWithTimeout(ctx context.Context, path string, operatorUID, modelUID uint32, approve AuthorizeAndIssue, allowSameUID bool, timeout time.Duration) (*Server, error) {
	if ctx == nil || approve == nil || timeout <= 0 ||
		(!allowSameUID && !validAuthorityUIDs(uint32(os.Geteuid()), operatorUID, modelUID)) ||
		!filepath.IsAbs(path) {
		return nil, ErrInvalid
	}
	path = filepath.Clean(path)
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o755 || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, ErrUnavailable
	}
	if !trustedSocketDirectory(dir, uint32(os.Geteuid())) {
		return nil, ErrUnavailable
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return nil, ErrUnavailable
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, ErrUnavailable
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o666); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, ErrUnavailable
	}
	socket, err := os.Lstat(path)
	if err != nil || socket.Mode()&os.ModeSocket == 0 {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, ErrUnavailable
	}
	runCtx, cancel := context.WithCancel(ctx)
	s := &Server{listener: listener, path: path, socket: socket, operatorUID: operatorUID,
		approve: approve, ctx: runCtx, cancel: cancel,
		acceptDone: make(chan struct{}), sem: make(chan struct{}, maxInFlight), timeout: timeout,
		conns: make(map[*net.UnixConn]struct{})}
	go func() {
		<-runCtx.Done()
		_ = listener.Close()
	}()
	go s.accept()
	return s, nil
}

func validAuthorityUIDs(serverUID, operatorUID, modelUID uint32) bool {
	return serverUID != 0 && modelUID != 0 &&
		operatorUID != ^uint32(0) && modelUID != ^uint32(0) &&
		serverUID != operatorUID && serverUID != modelUID && operatorUID != modelUID
}

// trustedSocketDirectory checks every ancestor. A root-owned sticky directory
// such as /tmp is allowed only when its child entry is root/server owned; other
// group/world-writable ancestors and symlinks are refused.
func trustedSocketDirectory(dir string, serverUID uint32) bool {
	return trustedSocketDirectoryWithLstat(dir, serverUID, os.Lstat)
}

func trustedSocketDirectoryWithLstat(dir string, serverUID uint32, lstat func(string) (os.FileInfo, error)) bool {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return false
	}
	childOwner := serverUID
	for current := dir; ; current = filepath.Dir(current) {
		info, err := lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 && stat.Uid != serverUID {
			return false
		}
		if current == dir && (stat.Uid != serverUID || info.Mode().Perm() != 0o755) {
			return false
		}
		if info.Mode().Perm()&0o022 != 0 &&
			(stat.Uid != 0 || info.Mode()&os.ModeSticky == 0 ||
				childOwner != 0 && childOwner != serverUID) {
			return false
		}
		childOwner = stat.Uid
		if current == string(filepath.Separator) {
			return true
		}
	}
}

func (s *Server) accept() {
	defer close(s.acceptDone)
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			return
		}
		select {
		case s.sem <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			<-s.sem
			_ = conn.Close()
			return
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.handle(conn)
	}
}

func (s *Server) handle(conn *net.UnixConn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		_ = conn.Close()
		<-s.sem
		s.wg.Done()
	}()
	deadline := time.Now().Add(s.timeout)
	if conn.SetDeadline(deadline) != nil {
		return
	}
	uid, err := peerUID(conn)
	if err != nil || uid != s.operatorUID {
		writeWire(conn, wireResponse{Error: wireRejected})
		return
	}
	data, err := io.ReadAll(io.LimitReader(conn, maxMessageBytes+1))
	if err != nil {
		writeWire(conn, wireResponse{Error: wireRejected})
		return
	}
	request, err := ParseRequest(data)
	if err != nil {
		writeWire(conn, wireResponse{Error: wireRejected})
		return
	}
	ctx, cancel := context.WithDeadline(s.ctx, deadline)
	defer cancel()
	now := time.Now().UTC()
	issuerRef := fmt.Sprintf("uid.%d", uid)
	approved := db.BudgetApprovalRequest{
		RequestRef: request.RequestRef, IssuerRef: issuerRef,
		TaskRef:     request.TaskRef,
		Binding:     budget.Binding{PolicyRef: request.PolicyRef, ResetRef: request.ResetRef},
		MaxRequests: request.MaxRequests, ValidFrom: request.ValidFrom, ValidUntil: request.ValidUntil,
	}
	record, err := s.approve(ctx, uid, approved, now)
	if err != nil {
		code := wireUnknown
		if errors.Is(err, ErrDenied) && record == (db.BudgetApprovalRecord{}) && ctx.Err() == nil {
			code = wireRejected
		}
		writeWire(conn, wireResponse{Error: code})
		return
	}
	if ctx.Err() != nil || record.State != "ready" ||
		record.RequestRef != request.RequestRef || record.IssuerRef != issuerRef ||
		record.Grant.Key.TaskRef != request.TaskRef || !validRef(record.Grant.Key.BudgetRef) ||
		record.Grant.Binding != approved.Binding ||
		record.Grant.MaxRequests != request.MaxRequests ||
		!record.Grant.ValidFrom.Equal(request.ValidFrom.Round(0).UTC().Truncate(time.Microsecond)) ||
		!record.Grant.ValidUntil.Equal(request.ValidUntil.Round(0).UTC().Truncate(time.Microsecond)) ||
		!validRef(record.Epoch) {
		writeWire(conn, wireResponse{Error: wireUnknown})
		return
	}
	writeWire(conn, wireResponse{Response: Response{RequestRef: record.RequestRef,
		TaskRef: record.Grant.Key.TaskRef, BudgetRef: record.Grant.Key.BudgetRef,
		Epoch: record.Epoch, State: record.State,
		PolicyRef: record.Grant.Binding.PolicyRef, ResetRef: record.Grant.Binding.ResetRef,
		MaxRequests: record.Grant.MaxRequests, ValidFrom: record.Grant.ValidFrom,
		ValidUntil: record.Grant.ValidUntil}})
}

func peerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, ErrUnavailable
	}
	var uid uint32
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		var cred *syscall.Ucred
		cred, socketErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if socketErr == nil {
			uid = cred.Uid
		}
	}); err != nil || socketErr != nil {
		return 0, ErrUnavailable
	}
	return uid, nil
}

func writeWire(conn *net.UnixConn, response wireResponse) {
	_ = json.NewEncoder(conn).Encode(response)
}

// Close stops accepting, cancels in-flight work and removes only this listener's
// original socket inode. A caller timeout never leaves the listener open.
func (s *Server) Close(ctx context.Context) error {
	if s == nil || ctx == nil {
		return ErrInvalid
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
		_ = s.listener.Close()
	}
	s.mu.Unlock()
	<-s.acceptDone
	s.mu.Lock()
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	var waitErr error
	select {
	case <-done:
	case <-ctx.Done():
		waitErr = ErrUnavailable
	}
	info, err := os.Lstat(s.path)
	if err == nil && os.SameFile(s.socket, info) && info.Mode()&os.ModeSocket != 0 {
		if err := os.Remove(s.path); err != nil {
			return ErrUnavailable
		}
	} else if err == nil {
		return ErrUnavailable
	}
	return waitErr
}
