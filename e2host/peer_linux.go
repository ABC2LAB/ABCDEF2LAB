//go:build linux

package e2host

import (
	"net"

	"golang.org/x/sys/unix"
)

func peerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, ErrUnavailable
	}
	var uid uint32
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if e != nil {
			controlErr = e
			return
		}
		uid = cred.Uid
	}); err != nil || controlErr != nil {
		return 0, ErrUnavailable
	}
	return uid, nil
}
