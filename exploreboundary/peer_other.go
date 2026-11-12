//go:build !linux

package exploreboundary

import "net"

// PeerUID는 리눅스 밖에서 지원하지 않는다. 제어 소켓을 열지 않는다.
func PeerUID(net.Conn) (uint32, error) { return 0, ErrUnavailable }
