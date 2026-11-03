//go:build linux

// Package auditpeer observes per-packet kernel-accepted Unix credentials.
// Observations are not authentication, effective-UID attestations, or authority.
package auditpeer

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	MaxPacketBytes    = 64 << 10
	MaxReceiveTimeout = 30 * time.Second
)

var (
	ErrInput    = errors.New("audit_peer_input_invalid")
	ErrSocket   = errors.New("audit_peer_socket_rejected")
	ErrControl  = errors.New("audit_peer_control_rejected")
	ErrPacket   = errors.New("audit_peer_packet_rejected")
	ErrRead     = errors.New("audit_peer_receive_unavailable")
	ErrCanceled = errors.New("audit_peer_receive_canceled")
	ErrTimeout  = errors.New("audit_peer_receive_timeout")
	ErrClosed   = errors.New("audit_peer_closed")
)

type Limits struct {
	MaxPacketBytes int
	Timeout        time.Duration
}

// Credentials are kernel-accepted SCM_CREDENTIALS values, not proof of current
// effective IDs. Namespace, privilege, live process identity and roles remain
// the host's responsibility. Do not expose these as an authority DTO.
type Credentials struct {
	PID int32  `json:"-"`
	UID uint32 `json:"-"`
	GID uint32 `json:"-"`
}

func (Credentials) String() string   { return "auditpeer.Credentials{observation}" }
func (Credentials) GoString() string { return "auditpeer.Credentials{observation}" }

type Packet struct {
	Payload     []byte      `json:"-"`
	Credentials Credentials `json:"-"`
}

func (Packet) String() string   { return "auditpeer.Packet{[REDACTED]}" }
func (Packet) GoString() string { return "auditpeer.Packet{[REDACTED]}" }

type Receiver struct {
	conn      *net.UnixConn
	raw       syscall.RawConn
	limits    Limits
	gate      chan struct{}
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

// NewReceiver consumes every non-nil conn, including on failure (which closes
// it). On success it owns read/deadline/Close exclusively. The caller must not
// retain readers, duplicate descriptors or change socket options underneath it.
// This API creates no listener and has no default service/auth policy.
func NewReceiver(conn *net.UnixConn, limits Limits) (receiver *Receiver, err error) {
	if conn == nil {
		return nil, ErrInput
	}
	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()
	if limits.MaxPacketBytes < 1 || limits.MaxPacketBytes > MaxPacketBytes || limits.Timeout <= 0 || limits.Timeout > MaxReceiveTimeout {
		return nil, ErrInput
	}
	raw, e := conn.SyscallConn()
	if e != nil {
		return nil, ErrSocket
	}
	if e = checkSocket(raw, true); e != nil {
		return nil, e
	}
	if conn.SetReadDeadline(time.Time{}) != nil {
		return nil, ErrSocket
	}
	return &Receiver{conn: conn, raw: raw, limits: limits, gate: make(chan struct{}, 1)}, nil
}
func checkSocket(raw syscall.RawConn, enable bool) error {
	var result error
	controlErr := raw.Control(func(fd uintptr) {
		kind, e := syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_TYPE)
		if e != nil || kind != syscall.SOCK_SEQPACKET {
			result = ErrSocket
			return
		}
		address, e := syscall.Getsockname(int(fd))
		if e != nil {
			result = ErrSocket
			return
		}
		if _, ok := address.(*syscall.SockaddrUnix); !ok {
			result = ErrSocket
			return
		}
		pidfd, e := syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, unix.SO_PASSPIDFD)
		if !pidfdOptionAccepted(pidfd, e) {
			result = ErrSocket
			return
		}
		if enable && syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_PASSCRED, 1) != nil {
			result = ErrSocket
			return
		}
		enabled, e := syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_PASSCRED)
		if e != nil || enabled != 1 {
			result = ErrSocket
		}
	})
	if controlErr != nil || result != nil {
		return ErrSocket
	}
	return nil
}
func pidfdOptionAccepted(value int, err error) bool {
	return err == syscall.ENOPROTOOPT || err == nil && value == 0
}

// Receive consumes exactly one bounded packet, never a partial success. A
// rejected packet is discarded; timeout/cancellation is not a replay protocol.
// Concurrent callers serialize within their own context/timeout bounds.
func (r *Receiver) Receive(ctx context.Context) (packet Packet, err error) {
	if r == nil || r.conn == nil || ctx == nil {
		return Packet{}, ErrInput
	}
	op, cancel := context.WithTimeout(ctx, r.limits.Timeout)
	defer cancel()
	select {
	case r.gate <- struct{}{}:
		defer func() { <-r.gate }()
	case <-op.Done():
		return Packet{}, contextError(op)
	}
	if r.closed.Load() {
		return Packet{}, ErrClosed
	}
	if op.Err() != nil {
		return Packet{}, contextError(op)
	}
	if e := checkSocket(r.raw, false); e != nil {
		return Packet{}, e
	}
	deadline, _ := op.Deadline()
	if e := r.conn.SetReadDeadline(deadline); e != nil {
		return Packet{}, r.readError(op, e)
	}
	callbackDone := make(chan struct{})
	stop := context.AfterFunc(op, func() {
		defer close(callbackDone)
		_ = r.conn.SetReadDeadline(time.Now())
	})
	defer func() {
		// Join a callback that already began before clearing the shared deadline.
		// This runs before releasing gate, so it cannot poison the next Receive.
		if !stop() {
			<-callbackDone
		}
		clearErr := r.conn.SetReadDeadline(time.Time{})
		if r.closed.Load() {
			packet, err = Packet{}, ErrClosed
		} else if op.Err() != nil {
			packet, err = Packet{}, contextError(op)
		} else if clearErr != nil {
			packet, err = Packet{}, ErrRead
		}
	}()
	data := make([]byte, r.limits.MaxPacketBytes+1)
	control := make([]byte, controlBytes)
	n, oobn, flags, readErr := r.recvRaw(op, data, control)
	// Always drain installed rights, including truncated/failed packets.
	if oobn < 0 || oobn > len(control) {
		return Packet{}, ErrControl
	}
	credentials, controlErr := decodeControl(control[:oobn])
	if readErr != nil {
		return Packet{}, r.readError(op, readErr)
	}
	if flags&(syscall.MSG_TRUNC|syscall.MSG_CTRUNC) != 0 {
		return Packet{}, ErrPacket
	}
	if flags & ^(syscall.MSG_EOR|syscall.MSG_CMSG_CLOEXEC) != 0 {
		return Packet{}, ErrControl
	}
	if n < 1 || n > r.limits.MaxPacketBytes {
		return Packet{}, ErrPacket
	}
	if controlErr != nil {
		return Packet{}, controlErr
	}
	return Packet{Payload: append([]byte(nil), data[:n]...), Credentials: credentials}, nil
}

type recvmsgCall func(int, []byte, []byte, int) (int, int, int, syscall.Sockaddr, error)

func (r *Receiver) recvAttempt(ctx context.Context, fd int, data, control []byte, receive recvmsgCall) (n, oobn, flags int, err error) {
	for {
		if r.closed.Load() {
			return 0, 0, 0, ErrClosed
		}
		if ctx.Err() != nil {
			return 0, 0, 0, ctx.Err()
		}
		n, oobn, flags, _, err = receive(fd, data, control, syscall.MSG_CMSG_CLOEXEC|syscall.MSG_DONTWAIT)
		if err != syscall.EINTR {
			return n, oobn, flags, err
		}
	}
}
func (r *Receiver) recvRaw(ctx context.Context, data, control []byte) (n, oobn, flags int, err error) {
	var recvErr error
	pollErr := r.raw.Read(func(fd uintptr) bool {
		n, oobn, flags, recvErr = r.recvAttempt(ctx, int(fd), data, control, syscall.Recvmsg)
		return recvErr != syscall.EAGAIN && recvErr != syscall.EWOULDBLOCK
	})
	if pollErr != nil {
		return n, oobn, flags, pollErr
	}
	return n, oobn, flags, recvErr
}
func contextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrTimeout
	}
	return ErrCanceled
}
func (r *Receiver) readError(ctx context.Context, cause error) error {
	if r.closed.Load() {
		return ErrClosed
	}
	if ctx.Err() != nil {
		return contextError(ctx)
	}
	// A socket deadline can become visible just before the timer context fires.
	if os.IsTimeout(cause) {
		return ErrTimeout
	}
	return ErrRead
}
func (r *Receiver) Close() error {
	if r == nil || r.conn == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		if e := r.conn.Close(); e != nil && !errors.Is(e, os.ErrClosed) {
			r.closeErr = ErrRead
		}
	})
	return r.closeErr
}
