//go:build !linux

package e2host

import "net"

// 제품 Host는 Linux 전용이다. 다른 플랫폼에서는 peer 권한을 발급하지 않는다.
func peerUID(*net.UnixConn) (uint32, error) { return 0, ErrUnavailable }
