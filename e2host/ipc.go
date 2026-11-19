package e2host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/transmit"
)

const maxMessage = 32 << 10

type message struct {
	Operation      string         `json:"operation"`
	Scope          transmit.Scope `json:"scope,omitempty"`
	PolicyRevision string         `json:"policy_revision,omitempty"`
	Event          *e2proxy.Event `json:"event,omitempty"`
}
type reply struct {
	OK bool `json:"ok"`
}

func decodeLine(reader *bufio.Reader, target any) error {
	line, err := reader.ReadSlice('\n')
	if err != nil || len(line) > maxMessage {
		return ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrUnavailable
	}
	return nil
}

// Serve는 Host 비밀 볼륨 밖의 단일 소켓에만 노출된다. 호출자 UID는
// 커널 자격으로 확인하고 접속마다 실행을 하나씩 결속한다.
func (h *Host) Serve(ctx context.Context, socket string, expectedUID uint32) error {
	if h == nil || ctx == nil || socket == "" || expectedUID == uint32(os.Geteuid()) {
		return ErrUnavailable
	}
	if _, err := os.Lstat(socket); !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return ErrUnavailable
	}
	defer listener.Close()
	defer os.Remove(socket)
	if err := os.Chmod(socket, 0660); err != nil {
		return ErrUnavailable
	}
	go func() { <-ctx.Done(); _ = listener.Close() }()
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return ErrUnavailable
		}
		go h.serveConn(ctx, conn, expectedUID)
	}
}

func (h *Host) serveConn(ctx context.Context, conn *net.UnixConn, expectedUID uint32) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	uid, err := peerUID(conn)
	if err != nil || uid != expectedUID {
		return
	}
	reader := bufio.NewReaderSize(conn, maxMessage)
	var session *Session
	defer func() {
		if session != nil {
			session.Abort()
		}
	}()
	for ctx.Err() == nil {
		var request message
		if decodeLine(reader, &request) != nil {
			return
		}
		var actionErr error
		switch request.Operation {
		case "open":
			if session != nil || request.Event != nil || request.PolicyRevision == "" {
				actionErr = ErrUnavailable
			} else {
				session, actionErr = h.Open(ctx, request.Scope, request.PolicyRevision)
			}
		case "record":
			if session == nil || request.Event == nil || request.Scope != (transmit.Scope{}) || request.PolicyRevision != "" {
				actionErr = ErrUnavailable
			} else {
				actionErr = session.Record(ctx, *request.Event)
			}
		case "seal":
			if session == nil || request.Event != nil || request.Scope != (transmit.Scope{}) || request.PolicyRevision != "" {
				actionErr = ErrUnavailable
			} else {
				actionErr = session.Seal(ctx)
			}
		default:
			actionErr = ErrUnavailable
		}
		if json.NewEncoder(conn).Encode(reply{OK: actionErr == nil}) != nil || actionErr != nil || request.Operation == "seal" {
			return
		}
	}
}

// Client는 실행 프로세스가 보유하는 유일한 Host 권한이다. DB 자격·키·저널
// 파일에는 접근하지 않는다. 응답 ACK 불명 시 영구 실패한다.
type Client struct {
	mu     sync.Mutex
	conn   net.Conn
	reader *bufio.Reader
	failed bool
	closed bool
}

func OpenClient(ctx context.Context, socket string, scope transmit.Scope, policyRevision string) (*Client, error) {
	if ctx == nil || ctx.Err() != nil || socket == "" {
		return nil, ErrUnavailable
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, ErrUnavailable
	}
	c := &Client{conn: conn, reader: bufio.NewReaderSize(conn, maxMessage)}
	if c.exchange(ctx, message{Operation: "open", Scope: scope, PolicyRevision: policyRevision}) != nil {
		conn.Close()
		return nil, ErrUnavailable
	}
	return c, nil
}

func (c *Client) exchange(ctx context.Context, request message) error {
	if c.failed || c.closed || ctx == nil || ctx.Err() != nil {
		return ErrUnavailable
	}
	deadline := time.Now().Add(20 * time.Second)
	if userDeadline, ok := ctx.Deadline(); ok && userDeadline.Before(deadline) {
		deadline = userDeadline
	}
	_ = c.conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = c.conn.SetDeadline(time.Now()) })
	defer stop()
	if err := json.NewEncoder(c.conn).Encode(request); err != nil {
		c.failed = true
		return ErrUnavailable
	}
	var response reply
	if decodeLine(c.reader, &response) != nil || !response.OK || ctx.Err() != nil {
		c.failed = true
		return ErrUnavailable
	}
	return nil
}

func (c *Client) Record(ctx context.Context, event e2proxy.Event) error {
	if c == nil {
		return ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exchange(ctx, message{Operation: "record", Event: &event})
}
func (c *Client) Seal(ctx context.Context) error {
	if c == nil {
		return ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exchange(ctx, message{Operation: "seal"}) != nil {
		c.conn.Close()
		c.closed = true
		return ErrUnavailable
	}
	c.closed = true
	return c.conn.Close()
}
func (c *Client) Abort() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failed, c.closed = true, true
	_ = c.conn.Close()
}
