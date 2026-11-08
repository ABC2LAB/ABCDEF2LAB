//go:build linux

package e2sandbox

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSandboxSocketFamiliesAndIOUring(t *testing.T) {
	// Exercise the actual inherited Bash filter, not a model of the BPF.
	// Only socket creation is probed; no host VSOCK endpoint is contacted.
	script := fmt.Sprintf(`import ctypes, errno, os, socket
libc = ctypes.CDLL(None, use_errno=True)
for family in (socket.AF_UNIX, socket.AF_INET, socket.AF_INET6):
    s = socket.socket(family, socket.SOCK_STREAM)
    s.close()
def deny(nr, *args):
    ctypes.set_errno(0)
    result = libc.syscall(ctypes.c_long(nr), *(ctypes.c_ulonglong(a) for a in args))
    error = ctypes.get_errno()
    if result != -1:
        if result >= 0:
            os.close(result)
        raise RuntimeError("forbidden syscall was accepted")
    if error != errno.EPERM:
        raise RuntimeError("forbidden syscall did not receive seccomp EPERM: " + str(error))
for family in (%d, %d, %d):
    deny(%d, family, socket.SOCK_STREAM, 0)
    deny(%d, family, socket.SOCK_STREAM, 0, 0)
deny(%d, (1 << 32) | socket.AF_UNIX, socket.SOCK_STREAM, 0)
for nr in (%d, %d, %d):
    deny(nr, 0, 0, 0, 0, 0, 0)
print("socket-and-uring-guards-ok")
`, unix.AF_VSOCK, unix.AF_NETLINK, unix.AF_PACKET,
		unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_SOCKET,
		unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER)
	f := newSandboxFixture(t, "python3 -c '"+script+"'")
	result := f.run(t)
	if result.ExitCode != 0 || !result.BashExited || !result.Drained ||
		!strings.Contains(string(result.Output), "socket-and-uring-guards-ok") || f.receipts.Load() != 0 {
		t.Fatalf("socket/uring boundary result=%+v target_receipts=%d", result, f.receipts.Load())
	}
}

func TestSandboxBlocksNamespaceCloneAndClone3(t *testing.T) {
	// Invalid clone flag pairs would produce EINVAL in the kernel. The
	// observed EPERM therefore pins the seccomp mask rather than a separate
	// namespace permission check. A null clone3 pointer would not produce
	// ENOSYS on kernels that implement clone3.
	script := fmt.Sprintf(`import ctypes, errno
libc = ctypes.CDLL(None, use_errno=True)
def deny(nr, expected, *args):
    ctypes.set_errno(0)
    result = libc.syscall(ctypes.c_long(nr), *(ctypes.c_ulonglong(a) for a in args))
    error = ctypes.get_errno()
    if result != -1 or error != expected:
        raise RuntimeError("clone guard mismatch: " + str((nr, result, error, expected)))
for flags in (%d, %d, %d, %d):
    deny(%d, errno.EPERM, flags, 0, 0, 0, 0)
deny(%d, errno.ENOSYS, 0, 0)
print("clone-guards-ok")
`, unix.CLONE_NEWUSER|unix.CLONE_THREAD, unix.CLONE_NEWNET|unix.CLONE_THREAD,
		unix.CLONE_NEWNS|unix.CLONE_THREAD, unix.CLONE_NEWPID|unix.CLONE_THREAD,
		unix.SYS_CLONE, unix.SYS_CLONE3)
	f := newSandboxFixture(t, "python3 -c '"+script+"'")
	result := f.run(t)
	if result.ExitCode != 0 || !result.BashExited || !result.Drained ||
		!strings.Contains(string(result.Output), "clone-guards-ok") || f.receipts.Load() != 0 {
		t.Fatalf("clone boundary result=%+v target_receipts=%d", result, f.receipts.Load())
	}
}
