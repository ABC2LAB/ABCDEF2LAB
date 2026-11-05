package e2proxy

import (
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type unavailableDeadlineConn struct {
	net.Conn
	failClose bool
}

func (c unavailableDeadlineConn) SetDeadline(time.Time) error {
	return errors.New("synthetic-private-deadline-detail")
}

func (c unavailableDeadlineConn) Close() error {
	err := c.Conn.Close()
	if c.failClose {
		return errors.New("synthetic-private-close-detail")
	}
	return err
}

func TestRequestDeadlineFailureRefusesConnectionAndClosesPeer(t *testing.T) {
	for _, failClose := range []bool{false, true} {
		name := "closed"
		if failClose {
			name = "close-error"
		}
		t.Run(name, func(t *testing.T) {
			local, peer := net.Pipe()
			defer local.Close()
			defer peer.Close()
			if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			call := &Call{e: &Execution{cfg: Config{Limits: Limits{RequestTimeout: time.Second}}}}
			conn, err := newDeadlineGuardedConn(unavailableDeadlineConn{Conn: local, failClose: failClose}, call, "synthetic-revision", &observed{})
			if conn != nil || !errors.Is(err, ErrDenied) || strings.Contains(err.Error(), "private") {
				t.Fatal("deadline failure returned a usable connection or private error")
			}
			if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
				t.Fatal("refused connection remained open instead of closing its peer")
			}
		})
	}
}
