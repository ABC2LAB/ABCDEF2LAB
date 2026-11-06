//go:build linux

package e2proxy

import (
	"net"
	"syscall"
)

// PeerVerifier is supplied by the trusted launcher, e.g. e2sandbox.Process.
// A positive kernel PID, current process lifetime and launch/namespace binding
// must all be verified. A tool-provided UID, token or header is not a verifier.
type PeerVerifier interface {
	LivePeer(pid int32, uid, gid uint32) bool
}

// ServeUnix owns the accept/connection lifetime, but not the pathname or its
// permissions. The host must create a fresh per-call socket in a protected
// directory and retain it until proxy and process drain have completed.
func (p *Proxy) ServeUnix(listener *net.UnixListener, peers PeerVerifier) error {
	if listener == nil || peers == nil {
		return ErrProxy
	}
	return p.Serve(&verifiedProxyListener{UnixListener: listener, peers: peers})
}

type verifiedProxyListener struct {
	*net.UnixListener
	peers PeerVerifier
}

func (l *verifiedProxyListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		credential, err := proxyPeerCredential(conn)
		if err == nil && credential.Pid > 0 &&
			l.peers.LivePeer(credential.Pid, credential.Uid, credential.Gid) {
			return conn, nil
		}
		conn.Close()
	}
}

func proxyPeerCredential(conn *net.UnixConn) (*syscall.Ucred, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil, ErrProxy
	}
	var credential *syscall.Ucred
	var sockErr error
	if err = raw.Control(func(fd uintptr) {
		credential, sockErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || sockErr != nil || credential == nil {
		return nil, ErrProxy
	}
	return credential, nil
}
