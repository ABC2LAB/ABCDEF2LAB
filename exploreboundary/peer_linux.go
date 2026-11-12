//go:build linux

package exploreboundary

import (
	"net"
	"syscall"
)

// PeerUID는 유닉스 소켓 상대 프로세스의 uid다(SO_PEERCRED). 제어 소켓은 이 값으로 엔진 서버만 받는다.
func PeerUID(conn net.Conn) (uint32, error) {
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, ErrConfig
	}
	raw, err := unix.SyscallConn()
	if err != nil {
		return 0, ErrUnavailable
	}
	var cred *syscall.Ucred
	var credErr error
	if err = raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || credErr != nil || cred == nil {
		return 0, ErrUnavailable
	}
	return cred.Uid, nil
}
