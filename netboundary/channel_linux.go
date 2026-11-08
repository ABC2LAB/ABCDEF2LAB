//go:build linux

package netboundary

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/Autumn-27/artex/auditpeer"
	"github.com/Autumn-27/artex/transmit"
)

var (
	ErrChannel = errors.New("netboundary: channel unavailable")
	ErrPeer    = errors.New("netboundary: peer unavailable")
)

// PeerAuthorizer is a protected host adapter. Credentials are kernel packet
// observations, not authority by themselves. Acquire must compare the current
// run, live process handle, namespace mapping and revocation state, and hold
// that barrier until Release. It must honor ctx cancellation while waiting,
// so StopAndWait can drain. It must not trust a run claim in the packet.
type PeerAuthorizer interface {
	AcquirePeer(context.Context, transmit.Scope, auditpeer.Credentials) (Permit, error)
}

// ChannelAck contains no capture body, credential, URL, raw error or audit
// event. The caller retains the full Outcome even when writing this ACK fails.
type ChannelAck struct {
	Version          int    `json:"version"`
	State            State  `json:"state"`
	Reason           Reason `json:"reason"`
	Admitted         bool   `json:"admitted"`
	ResponseReceived bool   `json:"response_received"`
}

// ServePacketConn consumes one accepted SOCK_SEQPACKET connection. It emits a
// ready packet with a fresh per-connection nonce after enabling per-packet
// credentials, then consumes one request packet and emits at most one ACK.
// The nonce only orders the handshake; peer authentication still requires the
// host's current kernel-credential check.
// The host owns listener path, ACL and peer
// identity proof. This method does not create or activate a socket service.
// On an ACK failure, out still preserves the possibly completed transmission.
func (b *Boundary) ServePacketConn(conn *net.UnixConn, peer PeerAuthorizer) (out Outcome, err error) {
	if conn == nil {
		return out, ErrConfig
	}
	if b == nil || !isPresent(peer) {
		_ = conn.Close()
		return out, ErrConfig
	}
	b.mu.Lock()
	if b.closed || b.failed {
		b.mu.Unlock()
		_ = conn.Close()
		return out, ErrChannel
	}
	b.pending.Add(1)
	b.mu.Unlock()
	defer b.pending.Done()

	receiver, receiveErr := auditpeer.NewReceiver(conn, auditpeer.Limits{MaxPacketBytes: 4096, Timeout: 5 * time.Second})
	if receiveErr != nil {
		return out, ErrChannel
	}
	defer receiver.Close()
	stopClose := context.AfterFunc(b.ctx, func() { _ = receiver.Close() })
	defer stopClose()
	var nonceBytes [16]byte
	if _, randomErr := rand.Read(nonceBytes[:]); randomErr != nil {
		return out, ErrChannel
	}
	nonce := hex.EncodeToString(nonceBytes[:])
	if writeChannelPacket(receiver, conn, []byte("E2NB/1 "+nonce)) != nil {
		return out, ErrChannel
	}
	packet, receiveErr := receiver.Receive(b.ctx)
	if receiveErr != nil {
		return out, ErrChannel
	}
	accountRef, resourceRef, decodeErr := decodeChannelRequest(packet.Payload, nonce)
	if decodeErr != nil {
		return out, ErrChannel
	}
	permit, peerErr := peer.AcquirePeer(b.ctx, b.scope, packet.Credentials)
	if peerErr != nil {
		if isPresent(permit) {
			permit.Release()
		}
		return out, ErrPeer
	}
	if !isPresent(permit) {
		return out, ErrPeer
	}
	defer permit.Release()
	if b.ctx.Err() != nil {
		return out, ErrChannel
	}
	out = b.SendRegisteredRead(accountRef, resourceRef)
	ack := ChannelAck{Version: 1, State: out.State, Reason: out.Reason,
		Admitted: out.Receipt.Admitted, ResponseReceived: out.Receipt.ResponseReceived}
	encoded, encodeErr := json.Marshal(ack)
	if encodeErr != nil {
		return out, ErrChannel
	}
	if writeChannelPacket(receiver, conn, encoded) != nil {
		return out, ErrChannel
	}
	return out, nil
}

func writeChannelPacket(receiver *auditpeer.Receiver, conn *net.UnixConn, packet []byte) error {
	timer := time.AfterFunc(5*time.Second, func() { _ = receiver.Close() })
	defer timer.Stop()
	n, err := conn.Write(packet)
	if err != nil || n != len(packet) {
		return ErrChannel
	}
	return nil
}

func decodeChannelRequest(payload []byte, expectedNonce string) (accountRef, resourceRef string, err error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return "", "", ErrChannel
	}
	seen := make(map[string]bool, 4)
	var version int
	var nonce string
	for decoder.More() {
		token, tokenErr := decoder.Token()
		key, ok := token.(string)
		if tokenErr != nil || !ok || seen[key] {
			return "", "", ErrChannel
		}
		seen[key] = true
		switch key {
		case "version":
			err = decoder.Decode(&version)
		case "account_ref":
			err = decoder.Decode(&accountRef)
		case "resource_ref":
			err = decoder.Decode(&resourceRef)
		case "nonce":
			err = decoder.Decode(&nonce)
		default:
			return "", "", ErrChannel
		}
		if err != nil {
			return "", "", ErrChannel
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return "", "", ErrChannel
	}
	if err = decoder.Decode(&token); err != io.EOF || len(seen) != 4 || version != 1 || nonce != expectedNonce ||
		accountRef == "" || resourceRef == "" || len(accountRef) > 256 || len(resourceRef) > 256 ||
		strings.IndexFunc(accountRef, controlRune) >= 0 || strings.IndexFunc(resourceRef, controlRune) >= 0 {
		return "", "", ErrChannel
	}
	return accountRef, resourceRef, nil
}

func controlRune(r rune) bool { return r < 0x20 || r == 0x7f }
