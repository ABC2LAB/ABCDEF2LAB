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

	"github.com/Autumn-27/artex/e2checkpoint"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/transmit"
)

// ProductSocketPath는 제품 설치와 실행에서 사용하는 감사 Host 소켓 경로다.
const ProductSocketPath = "/run/e2-host/audit.sock"

const maxMessage = 32 << 10

type message struct {
	Operation      string                 `json:"operation"`
	Scope          transmit.Scope         `json:"scope,omitempty"`
	PolicyRevision string                 `json:"policy_revision,omitempty"`
	Event          *e2proxy.Event         `json:"event,omitempty"`
	Binding        *e3contract.RunBinding `json:"binding,omitempty"`
	Ref            *e3contract.CallRef    `json:"ref,omitempty"`
	Observation    *ObservationRef        `json:"observation,omitempty"`
}
type reply struct {
	OK         bool                       `json:"ok"`
	Missing    bool                       `json:"missing,omitempty"`
	Mismatch   bool                       `json:"mismatch,omitempty"`
	Event      *e2proxy.Event             `json:"event,omitempty"`
	Checkpoint *e2proxy.JournalCheckpoint `json:"checkpoint,omitempty"`
	Seal       *SealResult                `json:"seal,omitempty"`
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
	var sealed bool
	defer func() {
		if session != nil {
			abortErr := session.Abort()
			// 연결이 사라져 추가 송신 ACK는 불가능하다. 현재 Host가 살아
			// 있으면 미완료 시도가 없는 저널만 별도로 검증해 마감한다.
			if !sealed && abortErr == nil && ctx.Err() == nil {
				_ = h.RecoverDisconnectedHeads(ctx)
			}
		}
	}()
	for ctx.Err() == nil {
		var request message
		if decodeLine(reader, &request) != nil {
			return
		}
		var actionErr error
		var responseEvent *e2proxy.Event
		var responseCheckpoint *e2proxy.JournalCheckpoint
		var responseSeal *SealResult
		switch request.Operation {
		case "open":
			if session != nil || request.Event != nil || request.Binding != nil || request.Ref != nil || request.Observation != nil || request.PolicyRevision == "" {
				actionErr = ErrUnavailable
			} else {
				session, actionErr = h.Open(ctx, request.Scope, request.PolicyRevision)
			}
		case "record":
			if session == nil || request.Event == nil || request.Binding != nil || request.Ref != nil || request.Observation != nil ||
				request.Scope != (transmit.Scope{}) || request.PolicyRevision != "" {
				actionErr = ErrUnavailable
			} else {
				actionErr = session.Record(ctx, *request.Event)
			}
		case "seal":
			if session == nil || request.Event != nil || request.Binding != nil || request.Ref != nil || request.Observation != nil ||
				request.Scope != (transmit.Scope{}) || request.PolicyRevision != "" {
				actionErr = ErrUnavailable
			} else {
				responseSeal, actionErr = session.SealChecked(ctx)
			}
		case "observed":
			if session == nil || request.Event != nil || request.Binding != nil || request.Ref != nil || request.Observation == nil ||
				request.Scope != (transmit.Scope{}) || request.PolicyRevision != "" {
				actionErr = ErrUnavailable
			} else {
				var observed Verification
				observed, actionErr = session.ObservedAt(ctx, *request.Observation)
				if actionErr == nil {
					responseEvent, responseCheckpoint = &observed.Event, &observed.Checkpoint
				}
			}
		case "verified":
			if session == nil || request.Event != nil || request.Scope != (transmit.Scope{}) ||
				request.PolicyRevision != "" || request.Binding == nil || request.Ref == nil || request.Observation != nil {
				actionErr = ErrUnavailable
			} else {
				var verified Verification
				verified, actionErr = session.VerifiedAt(ctx, *request.Binding, *request.Ref)
				if actionErr == nil {
					responseEvent, responseCheckpoint = &verified.Event, &verified.Checkpoint
				}
			}
		default:
			actionErr = ErrUnavailable
		}
		if request.Operation == "seal" && actionErr == nil {
			sealed = true
		}
		response := reply{OK: actionErr == nil, Missing: errors.Is(actionErr, ErrEventMissing),
			Mismatch: errors.Is(actionErr, ErrBindingMismatch), Event: responseEvent, Checkpoint: responseCheckpoint, Seal: responseSeal}
		if json.NewEncoder(conn).Encode(response) != nil ||
			actionErr != nil && !response.Missing && !response.Mismatch || request.Operation == "seal" {
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
	_, err := c.exchangeReply(ctx, request)
	return err
}

func (c *Client) exchangeReply(ctx context.Context, request message) (reply, error) {
	if c.failed || c.closed || ctx == nil || ctx.Err() != nil {
		return reply{}, ErrUnavailable
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
		return reply{}, ErrUnavailable
	}
	var response reply
	if decodeLine(c.reader, &response) != nil || ctx.Err() != nil ||
		request.Operation != "verified" && request.Operation != "observed" && (!response.OK || response.Missing || response.Mismatch || response.Event != nil || response.Checkpoint != nil) ||
		request.Operation != "seal" && response.Seal != nil ||
		request.Operation == "seal" && response.Seal != nil && !validSealResult(*response.Seal) ||
		(request.Operation == "verified" || request.Operation == "observed") && (!response.OK && !response.Missing && !response.Mismatch ||
			response.OK && (response.Missing || response.Mismatch || response.Event == nil || response.Checkpoint == nil ||
				!validCheckpoint(*response.Checkpoint) || response.Checkpoint.Sequence == 0) ||
			response.Missing && (response.Mismatch || response.Event != nil || response.Checkpoint != nil) ||
			response.Mismatch && (response.Event != nil || response.Checkpoint != nil)) {
		c.failed = true
		return reply{}, ErrUnavailable
	}
	if response.Missing {
		return reply{}, ErrEventMissing
	}
	if response.Mismatch {
		return reply{}, ErrBindingMismatch
	}
	return response, nil
}

// Verified는 같은 Host 세션의 현재 head를 인증한 결과만 받는다.
func (c *Client) Verified(ctx context.Context, binding e3contract.RunBinding, ref e3contract.CallRef) (e2proxy.Event, error) {
	v, err := c.VerifiedAt(ctx, binding, ref)
	return v.Event, err
}

// VerifiedAt은 Verified와 같고, Host가 검증에 쓴 현재 head 체크포인트를 함께 받는다.
func (c *Client) VerifiedAt(ctx context.Context, binding e3contract.RunBinding, ref e3contract.CallRef) (Verification, error) {
	if c == nil {
		return Verification{}, ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	response, err := c.exchangeReply(ctx, message{Operation: "verified", Binding: &binding, Ref: &ref})
	if err != nil {
		return Verification{}, err
	}
	if response.Event == nil || response.Checkpoint == nil {
		c.failed = true
		return Verification{}, ErrUnavailable
	}
	return Verification{Event: *response.Event, Checkpoint: *response.Checkpoint}, nil
}

func (c *Client) ObservedAt(ctx context.Context, ref ObservationRef) (Verification, error) {
	if c == nil {
		return Verification{}, ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	response, err := c.exchangeReply(ctx, message{Operation: "observed", Observation: &ref})
	if err != nil {
		return Verification{}, err
	}
	if response.Event == nil || response.Checkpoint == nil {
		c.failed = true
		return Verification{}, ErrUnavailable
	}
	return Verification{Event: *response.Event, Checkpoint: *response.Checkpoint}, nil
}

// validCheckpoint는 v3 체크포인트 모양만 본다(저널 ID·범위 있음, MAC은 소문자 16진 64자). 인증은 Host 몫이다.
func validCheckpoint(c e2proxy.JournalCheckpoint) bool {
	return c.Version == e2proxy.JournalVersionV3 && c.JournalID != "" && c.Scope.RunRef != "" && validMAC(c.MAC)
}

func validMAC(mac string) bool {
	if len(mac) != 64 {
		return false
	}
	for _, ch := range mac {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func validSealResult(s SealResult) bool {
	if s.Phase != e2checkpoint.Sealed || !validCheckpoint(s.Head) || s.Checks == nil || len(s.Checks) > maxSealChecks {
		return false
	}
	for _, check := range s.Checks {
		if check.Sequence == 0 || check.Sequence > s.Head.Sequence && check.Included || !validMAC(check.MAC) {
			return false
		}
	}
	return true
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
	_, err := c.SealChecked(ctx)
	return err
}

// SealChecked는 Seal과 같고, Host가 봉인 뒤 확인한 근거 체크포인트별 포함 결과를 함께 받는다. 봉인 ACK가 왔지만
// 결과가 없으면 nil 결과다(호출자는 "봉인 미확인"). 봉인 ACK 자체가 불명이면 오류다.
func (c *Client) SealChecked(ctx context.Context) (*SealResult, error) {
	if c == nil {
		return nil, ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	response, err := c.exchangeReply(ctx, message{Operation: "seal"})
	if err != nil {
		c.conn.Close()
		c.closed = true
		return nil, ErrUnavailable
	}
	c.closed = true
	return response.Seal, c.conn.Close()
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
