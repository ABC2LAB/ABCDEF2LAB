//go:build linux

package auditpeer

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	switch os.Getenv("ARTEX_AUDITPEER_TEST_HELPER") {
	case "sender":
		if syscall.Sendmsg(3, []byte("child-packet"), nil, nil, 0) != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "fd-count":
		device, e1 := strconv.ParseUint(os.Getenv("ARTEX_AUDITPEER_TEST_DEVICE"), 10, 64)
		inode, e2 := strconv.ParseUint(os.Getenv("ARTEX_AUDITPEER_TEST_INODE"), 10, 64)
		if e1 != nil || e2 != nil {
			os.Exit(2)
		}
		count, err := countIdentity(device, inode)
		if err != nil {
			os.Exit(2)
		}
		fmt.Print(count)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func socketPair(t *testing.T, kind int) (*net.UnixConn, *os.File) {
	t.Helper()
	descriptors, err := syscall.Socketpair(syscall.AF_UNIX, kind|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	first := os.NewFile(uintptr(descriptors[0]), "owned-receiver")
	second := os.NewFile(uintptr(descriptors[1]), "owned-sender")
	t.Cleanup(func() { _ = second.Close() })
	conn, err := net.FileConn(first)
	_ = first.Close()
	if err != nil {
		_ = second.Close()
		t.Fatal(err)
	}
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		t.Fatal("Unix connection required")
	}
	t.Cleanup(func() { _ = unix.Close() })
	return unix, second
}
func fixture(t *testing.T, size int, timeout time.Duration) (*Receiver, *os.File) {
	t.Helper()
	conn, sender := socketPair(t, syscall.SOCK_SEQPACKET)
	receiver, err := NewReceiver(conn, Limits{size, timeout})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receiver.Close() })
	return receiver, sender
}
func send(t *testing.T, sender *os.File, payload, control []byte) {
	t.Helper()
	if err := syscall.Sendmsg(int(sender.Fd()), payload, control, nil, 0); err != nil {
		t.Fatal(err)
	}
}
func currentCredentialBytes() []byte {
	return syscall.UnixCredentials(&syscall.Ucred{Pid: int32(os.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())})
}
func requireEmpty(t *testing.T, packet Packet) {
	t.Helper()
	if packet.Payload != nil || packet.Credentials != (Credentials{}) {
		t.Fatal("failure returned a partial packet or credentials")
	}
}

func TestReceiveActualKernelCredentialsAndBoundaries(t *testing.T) {
	r, sender := fixture(t, 32, time.Second)
	send(t, sender, []byte("first"), nil)
	send(t, sender, []byte("second"), nil)
	for _, value := range []string{"first", "second"} {
		packet, err := r.Receive(context.Background())
		if err != nil || string(packet.Payload) != value || packet.Credentials != (Credentials{int32(os.Getpid()), uint32(os.Getuid()), uint32(os.Getgid())}) {
			t.Fatalf("actual packet observation failed: %v", err)
		}
	}
}
func TestInheritedEndpointReportsMessageSenderNotConnectPeer(t *testing.T) {
	conn, sender := socketPair(t, syscall.SOCK_SEQPACKET)
	raw, err := conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var connected *syscall.Ucred
	var inner error
	if err := raw.Control(func(fd uintptr) {
		connected, inner = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || inner != nil {
		t.Fatal("connection credential unavailable")
	}
	if connected.Pid != int32(os.Getpid()) {
		t.Fatal("socketpair did not record creating parent")
	}
	r, err := NewReceiver(conn, Limits{128, time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	command.Env = append(os.Environ(), "ARTEX_AUDITPEER_TEST_HELPER=sender")
	command.ExtraFiles = []*os.File{sender}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	packet, readErr := r.Receive(ctx)
	waitErr := command.Wait()
	if readErr != nil || waitErr != nil || string(packet.Payload) != "child-packet" || packet.Credentials.PID != int32(command.Process.Pid) || packet.Credentials.PID == connected.Pid {
		t.Fatalf("message sender observation mismatch: %v / %v", readErr, waitErr)
	}
	t.Logf("connection_peer_pid=%d message_sender_pid=%d inherited_child_pid=%d", connected.Pid, packet.Credentials.PID, command.Process.Pid)
}
func TestPayloadLimitAndEmptyPackets(t *testing.T) {
	for _, size := range []int{0, 9, 100} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			r, sender := fixture(t, 8, time.Second)
			send(t, sender, bytes.Repeat([]byte{'x'}, size), nil)
			packet, err := r.Receive(context.Background())
			if !errors.Is(err, ErrPacket) {
				t.Fatalf("packet size %d error %v", size, err)
			}
			requireEmpty(t, packet)
			send(t, sender, []byte("next"), nil)
			packet, err = r.Receive(context.Background())
			if err != nil || string(packet.Payload) != "next" {
				t.Fatal("rejected packet leaked a tail into the next frame")
			}
		})
	}
	r, sender := fixture(t, 8, time.Second)
	send(t, sender, bytes.Repeat([]byte{'x'}, 8), nil)
	if packet, err := r.Receive(context.Background()); err != nil || len(packet.Payload) != 8 {
		t.Fatal("exact payload limit rejected")
	}
}
func TestControlStrictValidation(t *testing.T) {
	good := currentCredentialBytes()
	unknown := append([]byte(nil), good...)
	offset := syscall.SizeofCmsghdr - 4
	binary.NativeEndian.PutUint32(unknown[offset:offset+4], 0x7fff)
	zero := syscall.UnixCredentials(&syscall.Ucred{Uid: 1, Gid: 1})
	short := good[:syscall.CmsgLen(0)+4]
	cases := map[string][]byte{
		"missing": nil, "duplicate": append(append([]byte(nil), good...), good...),
		"extra":    append(append([]byte(nil), good...), unknown...),
		"zero-pid": zero, "short": short, "trailing": append(append([]byte(nil), good...), 1),
		"trailing-zero": append(append([]byte(nil), good...), 0),
	}
	for name, control := range cases {
		t.Run(name, func(t *testing.T) {
			credentials, err := decodeControl(control)
			if !errors.Is(err, ErrControl) || credentials != (Credentials{}) {
				t.Fatal("invalid control returned credentials")
			}
		})
	}
}
func TestControlRightsDisposedBeforeLaterMalformedHeader(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "owned-right")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	fd, _, errno := syscall.Syscall(syscall.SYS_FCNTL, file.Fd(), syscall.F_DUPFD_CLOEXEC, 0)
	if errno != 0 {
		t.Fatal(errno)
	}
	malformed := make([]byte, syscall.CmsgLen(0))
	setControlLength(malformed, 1)
	control := append(syscall.UnixRights(int(fd)), malformed...)
	if _, err := decodeControl(control); !errors.Is(err, ErrControl) {
		t.Fatal("malformed control accepted")
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(fd), &stat); err != syscall.EBADF {
		_ = syscall.Close(int(fd))
		t.Fatal("right preceding malformed header was not closed")
	}
}
func setControlLength(control []byte, length uint64) {
	if syscall.SizeofCmsghdr-8 == 8 {
		binary.NativeEndian.PutUint64(control[:8], length)
	} else {
		binary.NativeEndian.PutUint32(control[:4], uint32(length))
	}
}
func TestRightsAndTruncatedRightsLeaveNoInstalledDescriptors(t *testing.T) {
	for _, number := range []int{1, 64} {
		t.Run(strconv.Itoa(number), func(t *testing.T) {
			r, sender := fixture(t, 8, time.Second)
			file, err := os.CreateTemp(t.TempDir(), "owned-fd")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			var identity syscall.Stat_t
			if syscall.Fstat(int(file.Fd()), &identity) != nil {
				t.Fatal("file identity unavailable")
			}
			before, err := countIdentity(uint64(identity.Dev), identity.Ino)
			if err != nil {
				t.Fatal(err)
			}
			rights := make([]int, number)
			for i := range rights {
				rights[i] = int(file.Fd())
			}
			send(t, sender, []byte("x"), syscall.UnixRights(rights...))
			packet, err := r.Receive(context.Background())
			if err == nil {
				t.Fatal("SCM_RIGHTS packet accepted")
			}
			requireEmpty(t, packet)
			after, err := countIdentity(uint64(identity.Dev), identity.Ino)
			if err != nil || before != after {
				t.Fatalf("installed descriptor leak: %d -> %d", before, after)
			}
		})
	}
}
func countIdentity(device, inode uint64) (int, error) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		var identity syscall.Stat_t
		if syscall.Stat("/proc/self/fd/"+entry.Name(), &identity) == nil && uint64(identity.Dev) == device && identity.Ino == inode {
			count++
		}
	}
	return count, nil
}
func TestReceivedRightsAreCloseOnExecBeforeDisposal(t *testing.T) {
	r, sender := fixture(t, 8, time.Second)
	file, err := os.CreateTemp(t.TempDir(), "exec-right")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var identity syscall.Stat_t
	if syscall.Fstat(int(file.Fd()), &identity) != nil {
		t.Fatal("file identity unavailable")
	}
	send(t, sender, []byte("x"), syscall.UnixRights(int(file.Fd())))
	data, control := make([]byte, 9), make([]byte, controlBytes)
	if r.conn.SetReadDeadline(time.Now().Add(time.Second)) != nil {
		t.Fatal("read deadline unavailable")
	}
	_, size, _, err := r.recvRaw(context.Background(), data, control)
	if err != nil {
		t.Fatal(err)
	}
	control = control[:size]
	defer func() { _, _ = decodeControl(control) }()
	messages, err := syscall.ParseSocketControlMessage(control)
	if err != nil {
		t.Fatal(err)
	}
	received := -1
	for _, message := range messages {
		if message.Header.Level == syscall.SOL_SOCKET && message.Header.Type == syscall.SCM_RIGHTS {
			rights, err := syscall.ParseUnixRights(&message)
			if err != nil || len(rights) != 1 {
				t.Fatal("received rights unavailable")
			}
			received = rights[0]
		}
	}
	if received < 0 {
		t.Fatal("received right missing")
	}
	bits, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(received), syscall.F_GETFD, 0)
	if errno != 0 || bits&syscall.FD_CLOEXEC == 0 {
		t.Fatal("kernel-installed right is not atomically close-on-exec")
	}
	if count := childIdentityCount(t, identity); count != 0 {
		t.Fatal("received right leaked through actual exec")
	}
	// Positive control: removing this bit from our own test descriptor really
	// does make the same exec observe it. Restore it before disposal.
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(received), syscall.F_SETFD, 0); errno != 0 {
		t.Fatal(errno)
	}
	defer syscall.CloseOnExec(received)
	if count := childIdentityCount(t, identity); count != 1 {
		t.Fatal("exec inheritance negative test lacked a working positive control")
	}
}
func childIdentityCount(t *testing.T, identity syscall.Stat_t) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	command.Env = append(os.Environ(), "ARTEX_AUDITPEER_TEST_HELPER=fd-count",
		"ARTEX_AUDITPEER_TEST_DEVICE="+strconv.FormatUint(uint64(identity.Dev), 10),
		"ARTEX_AUDITPEER_TEST_INODE="+strconv.FormatUint(identity.Ino, 10))
	output, err := command.Output()
	if err != nil {
		t.Fatal("exec observation failed")
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatal("exec observation invalid")
	}
	return count
}
func TestPasscredDisabledFailsClosed(t *testing.T) {
	r, _ := fixture(t, 8, time.Second)
	var inner error
	if err := r.raw.Control(func(fd uintptr) { inner = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_PASSCRED, 0) }); err != nil || inner != nil {
		t.Fatal("test option change unavailable")
	}
	packet, err := r.Receive(context.Background())
	if !errors.Is(err, ErrSocket) {
		t.Fatal("disabled SO_PASSCRED accepted")
	}
	requireEmpty(t, packet)
}
func TestConstructorFailureConsumesConnection(t *testing.T) {
	for _, mode := range []string{"stream", "limits"} {
		t.Run(mode, func(t *testing.T) {
			kind := syscall.SOCK_SEQPACKET
			limits := Limits{8, time.Second}
			if mode == "stream" {
				kind = syscall.SOCK_STREAM
			} else {
				limits.MaxPacketBytes = 0
			}
			conn, _ := socketPair(t, kind)
			if r, err := NewReceiver(conn, limits); err == nil || r != nil {
				t.Fatal("invalid constructor accepted")
			}
			if _, err := conn.Write([]byte("x")); !errors.Is(err, net.ErrClosed) {
				t.Fatal("failed constructor did not consume connection")
			}
		})
	}
	if _, err := NewReceiver(nil, Limits{8, time.Second}); !errors.Is(err, ErrInput) {
		t.Fatal("nil connection accepted")
	}
}
func TestCancellationDeadlineCleanupAndClose(t *testing.T) {
	r, sender := fixture(t, 16, time.Second)
	original := r.raw
	for i := 0; i < 40; i++ {
		observed := &observedRead{RawConn: original, entered: make(chan struct{})}
		r.raw = observed
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			packet, err := r.Receive(ctx)
			if packet.Payload != nil {
				result <- ErrPacket
			} else {
				result <- err
			}
		}()
		<-observed.entered // recvmsg really reached EAGAIN after AfterFunc install.
		cancel()
		select {
		case err := <-result:
			if !errors.Is(err, ErrCanceled) {
				t.Fatalf("cancellation error %v", err)
			}
		case <-time.After(300 * time.Millisecond):
			t.Fatal("cancellation did not wake the actual poll wait")
		}
		r.raw = original
		send(t, sender, []byte("after-cancel"), nil)
		if packet, err := r.Receive(context.Background()); err != nil || string(packet.Payload) != "after-cancel" {
			t.Fatalf("stale cancellation deadline poisoned next read: %v", err)
		}
	}
	observed := &observedRead{RawConn: original, entered: make(chan struct{})}
	r.raw = observed
	blocked := make(chan error, 1)
	go func() { _, err := r.Receive(context.Background()); blocked <- err }()
	<-observed.entered
	start := time.Now()
	if r.Close() != nil {
		t.Fatal("close failed")
	}
	select {
	case err := <-blocked:
		if !errors.Is(err, ErrClosed) {
			t.Fatal("close did not reject pending read")
		}
	case <-time.After(time.Second):
		t.Fatal("close did not wake receiver")
	}
	if time.Since(start) > time.Second || r.Close() != nil {
		t.Fatal("close not promptly idempotent")
	}
}
func TestTimeoutAndGateWaitAreBounded(t *testing.T) {
	r, _ := fixture(t, 8, 20*time.Millisecond)
	packet, err := r.Receive(context.Background())
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("configured timeout error %v", err)
	}
	requireEmpty(t, packet)
	r2, _ := fixture(t, 8, time.Second)
	observed := &observedRead{RawConn: r2.raw, entered: make(chan struct{})}
	r2.raw = observed
	active := make(chan error, 1)
	go func() { _, err := r2.Receive(context.Background()); active <- err }()
	<-observed.entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := r2.Receive(ctx); !errors.Is(err, ErrTimeout) {
		t.Fatalf("waiting receiver timeout error %v", err)
	}
	if err := r2.Close(); err != nil {
		t.Fatal("close active receiver failed")
	}
	if err := <-active; !errors.Is(err, ErrClosed) {
		t.Fatalf("active receive did not remain blocked until close: %v", err)
	}
}

type observedRead struct {
	syscall.RawConn
	entered chan struct{}
	once    sync.Once
}

func (o *observedRead) Read(callback func(uintptr) bool) error {
	return o.RawConn.Read(func(fd uintptr) bool {
		ready := callback(fd)
		if !ready {
			o.once.Do(func() { close(o.entered) })
		}
		return ready
	})
}

func TestInterruptedReceiveRetriesAndChecksCancellation(t *testing.T) {
	t.Run("actual-success", func(t *testing.T) {
		r, sender := fixture(t, 8, time.Second)
		send(t, sender, []byte("x"), nil)
		data, control := make([]byte, 9), make([]byte, controlBytes)
		calls := 0
		var size, n int
		var inner error
		err := r.raw.Control(func(fd uintptr) {
			n, size, _, inner = r.recvAttempt(context.Background(), int(fd), data, control, func(fd int, b, oob []byte, flags int) (int, int, int, syscall.Sockaddr, error) {
				calls++
				if calls == 1 {
					return 0, 0, 0, nil, syscall.EINTR
				}
				return syscall.Recvmsg(fd, b, oob, flags)
			})
		})
		credentials, parseErr := decodeControl(control[:size])
		if err != nil || inner != nil || parseErr != nil || calls != 2 || n != 1 || credentials.PID != int32(os.Getpid()) {
			t.Fatal("EINTR did not retry the actual readable recvmsg immediately")
		}
	})
	for _, mode := range []string{"canceled", "closed"} {
		t.Run(mode, func(t *testing.T) {
			r, _ := fixture(t, 8, time.Second)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			_, _, _, err := r.recvAttempt(ctx, -1, nil, nil, func(int, []byte, []byte, int) (int, int, int, syscall.Sockaddr, error) {
				calls++
				if mode == "canceled" {
					cancel()
				} else {
					r.closed.Store(true)
				}
				return 0, 0, 0, nil, syscall.EINTR
			})
			if calls != 1 || mode == "canceled" && !errors.Is(err, context.Canceled) || mode == "closed" && !errors.Is(err, ErrClosed) {
				t.Fatal("EINTR loop ignored cancellation/closed state")
			}
		})
	}
}

func TestPassPIDFDOptionPolicy(t *testing.T) {
	for _, item := range []struct {
		value   int
		err     error
		allowed bool
	}{{0, nil, true}, {1, nil, false}, {0, syscall.ENOPROTOOPT, true}, {0, syscall.EPERM, false}, {0, syscall.EBADF, false}} {
		if pidfdOptionAccepted(item.value, item.err) != item.allowed {
			t.Fatal("PIDFD option failure was treated as unsupported")
		}
	}
}

func enablePIDFD(t *testing.T, conn *net.UnixConn) {
	t.Helper()
	raw, err := conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var inner error
	if err := raw.Control(func(fd uintptr) { inner = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, unix.SO_PASSPIDFD, 1) }); err != nil {
		t.Fatal(err)
	}
	if inner == syscall.ENOPROTOOPT {
		t.Skip("kernel lacks SO_PASSPIDFD")
	}
	if inner != nil {
		t.Fatal("actual SO_PASSPIDFD setup failed")
	}
}
func countPIDFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		target, _ := os.Readlink("/proc/self/fd/" + entry.Name())
		if target == "anon_inode:[pidfd]" {
			count++
		}
	}
	return count
}
func TestPassPIDFDRejectedAndActualInstalledPIDFDDisposed(t *testing.T) {
	t.Run("constructor", func(t *testing.T) {
		conn, _ := socketPair(t, syscall.SOCK_SEQPACKET)
		enablePIDFD(t, conn)
		if receiver, err := NewReceiver(conn, Limits{8, time.Second}); !errors.Is(err, ErrSocket) || receiver != nil {
			t.Fatal("PIDFD-producing connection accepted")
		}
		if _, err := conn.Write([]byte("x")); !errors.Is(err, net.ErrClosed) {
			t.Fatal("rejected PIDFD connection not consumed")
		}
	})
	t.Run("per-read-and-defense", func(t *testing.T) {
		r, sender := fixture(t, 8, time.Second)
		enablePIDFD(t, r.conn)
		if packet, err := r.Receive(context.Background()); !errors.Is(err, ErrSocket) || packet.Payload != nil {
			t.Fatal("PIDFD option enabled after construction accepted")
		}
		before := countPIDFDs(t)
		send(t, sender, []byte("x"), nil)
		data, control := make([]byte, 9), make([]byte, controlBytes)
		if r.conn.SetReadDeadline(time.Now().Add(time.Second)) != nil {
			t.Fatal("PIDFD read deadline unavailable")
		}
		// Test the private defensive disposer with actual kernel control after the
		// public guard rejected the option. No runtime path bypass is installed.
		_, size, _, err := r.recvRaw(context.Background(), data, control)
		if err != nil {
			t.Fatal(err)
		}
		control = control[:size]
		disposed := false
		defer func() {
			if !disposed {
				_, _ = decodeControl(control)
			}
		}()
		messages, err := syscall.ParseSocketControlMessage(control)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, message := range messages {
			if message.Header.Level == syscall.SOL_SOCKET && message.Header.Type == unix.SCM_PIDFD {
				found = true
				if len(message.Data) != 4 {
					t.Fatal("kernel pidfd control size invalid")
				}
				installed := int(int32(binary.NativeEndian.Uint32(message.Data)))
				flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(installed), syscall.F_GETFD, 0)
				if errno != 0 || flags&syscall.FD_CLOEXEC == 0 {
					t.Fatal("kernel-installed pidfd was not close-on-exec")
				}
			}
		}
		if !found || countPIDFDs(t) != before+1 {
			t.Fatal("test did not actually install one kernel pidfd")
		}
		_, err = decodeControl(control)
		disposed = true
		if !errors.Is(err, ErrControl) || countPIDFDs(t) != before {
			t.Fatal("rejected SCM_PIDFD leaked an installed descriptor")
		}
	})
}
func TestObservationCannotBecomePublicJSONOrLogPayload(t *testing.T) {
	packet := Packet{[]byte("private-packet-value"), Credentials{1, 2, 3}}
	for _, value := range []any{packet, packet.Credentials} {
		encoded, err := json.Marshal(value)
		if err != nil || string(encoded) != "{}" {
			t.Fatal("observation was exposed as a public JSON DTO")
		}
		if strings.Contains(fmt.Sprintf("%+v %#v", value, value), "private-packet-value") {
			t.Fatal("payload reached generic logging")
		}
	}
}
