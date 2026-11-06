//go:build linux

package e2sandbox

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	seccompSetModeFilter  = 1
	seccompFilterTSync    = 1
	seccompRetKillProcess = 0x80000000
	seccompRetErrno       = 0x00050000
	seccompRetAllow       = 0x7fff0000
	secureNoRootLocked    = 1 | 2 | 4 | 8
	maxCapabilityNumber   = 64
	seccompArchOffset     = 4
	seccompSyscallOffset  = 0
	seccompArg0LowOffset  = 16
	seccompArg0HighOffset = 20
	x32SyscallBit         = 0x40000000
)

// The helper retains no capability in any set. Seccomp additionally prevents
// creating a new user namespace to recover capabilities or opening host-like
// process/mount escapes. The filter applies to every helper thread and child.
func lockPrivilege() error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_SECUREBITS, secureNoRootLocked, 0, 0, 0); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return err
	}
	for cap := uintptr(0); cap < maxCapabilityNumber; cap++ {
		err := unix.Prctl(unix.PR_CAPBSET_DROP, cap, 0, 0, 0)
		if err != nil && err != unix.EINVAL {
			return err
		}
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := [2]unix.CapUserData{}
	if err := unix.Capset(&header, &data[0]); err != nil {
		return err
	}
	return installSeccomp()
}

func installSeccomp() error {
	var arch uint32
	switch runtime.GOARCH {
	case "amd64":
		arch = unix.AUDIT_ARCH_X86_64
	case "arm64":
		arch = unix.AUDIT_ARCH_AARCH64
	default:
		return ErrUnavailable
	}
	stmt := func(code uint16, k uint32) unix.SockFilter { return unix.SockFilter{Code: code, K: k} }
	jump := func(code uint16, k uint32, jt, jf uint8) unix.SockFilter {
		return unix.SockFilter{Code: code, K: k, Jt: jt, Jf: jf}
	}
	f := []unix.SockFilter{
		stmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompArchOffset),
		jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, arch, 1, 0),
		stmt(unix.BPF_RET|unix.BPF_K, seccompRetKillProcess),
		stmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompSyscallOffset),
	}
	if runtime.GOARCH == "amd64" {
		f = append(f, jump(unix.BPF_JMP|unix.BPF_JGE|unix.BPF_K, x32SyscallBit, 0, 1),
			stmt(unix.BPF_RET|unix.BPF_K, seccompRetErrno|uint32(unix.ENOSYS)))
	}
	// Network namespaces do not isolate AF_VSOCK. Restrict both socket creation
	// syscalls to the families used by the relay and ordinary HTTP clients.
	// Check the whole argument so its high word cannot bypass the allow list.
	f = append(f,
		jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, uint32(unix.SYS_SOCKET), 1, 0),
		jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, uint32(unix.SYS_SOCKETPAIR), 0, 9),
		stmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompArg0HighOffset), // args[0], high word
		jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, 0, 1, 0),
		stmt(unix.BPF_RET|unix.BPF_K, seccompRetErrno|uint32(unix.EPERM)),
		stmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompArg0LowOffset), // args[0], low word
		jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, unix.AF_UNIX, 3, 0),
		jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, unix.AF_INET, 2, 0),
		jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, unix.AF_INET6, 1, 0),
		stmt(unix.BPF_RET|unix.BPF_K, seccompRetErrno|uint32(unix.EPERM)),
		stmt(unix.BPF_RET|unix.BPF_K, seccompRetAllow),
	)
	// clone3's flags live behind a pointer and cannot be safely inspected in
	// classic BPF; ENOSYS lets normal libc threading use the inspected clone.
	f = append(f, jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, uint32(unix.SYS_CLONE3), 0, 1),
		stmt(unix.BPF_RET|unix.BPF_K, seccompRetErrno|uint32(unix.ENOSYS)))
	for _, nr := range []uint32{
		unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PIVOT_ROOT, unix.SYS_CHROOT,
		unix.SYS_SETNS, unix.SYS_UNSHARE, unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV,
		unix.SYS_PROCESS_VM_WRITEV, unix.SYS_PIDFD_GETFD, unix.SYS_OPEN_BY_HANDLE_AT,
		unix.SYS_BPF, unix.SYS_PERF_EVENT_OPEN, unix.SYS_KEYCTL, unix.SYS_ADD_KEY,
		unix.SYS_REQUEST_KEY, unix.SYS_INIT_MODULE, unix.SYS_FINIT_MODULE, unix.SYS_DELETE_MODULE,
		unix.SYS_KEXEC_LOAD, unix.SYS_KEXEC_FILE_LOAD, unix.SYS_REBOOT, unix.SYS_SWAPON, unix.SYS_SWAPOFF,
		unix.SYS_OPEN_TREE, unix.SYS_MOVE_MOUNT, unix.SYS_FSOPEN, unix.SYS_FSCONFIG,
		unix.SYS_FSMOUNT, unix.SYS_FSPICK, unix.SYS_MOUNT_SETATTR,
		// Ring operations could otherwise create sockets outside the inspected
		// socket syscalls. No io_uring descriptor is supported or inherited.
		unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER,
	} {
		f = append(f, jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, nr, 0, 1),
			stmt(unix.BPF_RET|unix.BPF_K, seccompRetErrno|uint32(unix.EPERM)))
	}
	mask := uint32(unix.CLONE_NEWUSER | unix.CLONE_NEWPID | unix.CLONE_NEWNS | unix.CLONE_NEWNET | unix.CLONE_NEWIPC | unix.CLONE_NEWUTS | unix.CLONE_NEWCGROUP)
	f = append(f,
		jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, uint32(unix.SYS_CLONE), 0, 3),
		stmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompArg0LowOffset),
		jump(unix.BPF_JMP|unix.BPF_JSET|unix.BPF_K, mask, 0, 1),
		stmt(unix.BPF_RET|unix.BPF_K, seccompRetErrno|uint32(unix.EPERM)),
		stmt(unix.BPF_RET|unix.BPF_K, seccompRetAllow),
	)
	program := unix.SockFprog{Len: uint16(len(f)), Filter: &f[0]}
	_, _, errno := unix.RawSyscall(unix.SYS_SECCOMP, seccompSetModeFilter, seccompFilterTSync, uintptr(unsafe.Pointer(&program)))
	if errno != 0 {
		return errno
	}
	runtime.KeepAlive(f)
	return nil
}
