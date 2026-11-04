//go:build linux

package auditpeer

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestConstructorRejectsEachLimitIndependently(t *testing.T) {
	cases := []struct {
		name   string
		limits Limits
	}{
		{"zero-timeout", Limits{1, 0}},
		{"negative-timeout", Limits{1, -time.Nanosecond}},
		{"over-timeout", Limits{1, MaxReceiveTimeout + time.Nanosecond}},
		{"over-packet", Limits{MaxPacketBytes + 1, time.Second}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, _ := socketPair(t, syscall.SOCK_SEQPACKET)
			receiver, err := NewReceiver(conn, tc.limits)
			if receiver != nil || !errors.Is(err, ErrInput) {
				t.Fatalf("limit error %v", err)
			}
			if _, err := conn.Write([]byte("closed")); err == nil {
				t.Fatal("rejected connection not consumed")
			}
		})
	}
	for _, limits := range []Limits{{1, time.Nanosecond}, {MaxPacketBytes, MaxReceiveTimeout}} {
		conn, _ := socketPair(t, syscall.SOCK_SEQPACKET)
		receiver, err := NewReceiver(conn, limits)
		if err != nil {
			t.Fatalf("exact constructor boundary rejected: %v", err)
		}
		if err := receiver.Close(); err != nil {
			t.Fatal("boundary receiver close failed")
		}
	}
}

func TestPacketQueuedBeforePasscredIsRejected(t *testing.T) {
	conn, sender := socketPair(t, syscall.SOCK_SEQPACKET)
	raw, err := conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var enabled int
	var optionErr error
	if err := raw.Control(func(fd uintptr) {
		enabled, optionErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_PASSCRED)
	}); err != nil || optionErr != nil || enabled != 0 {
		t.Fatal("passcred-off fixture unavailable")
	}
	send(t, sender, []byte("queued-before-passcred"), nil)
	receiver, err := NewReceiver(conn, Limits{64, time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	packet, err := receiver.Receive(context.Background())
	if !errors.Is(err, ErrControl) {
		t.Fatalf("pre-passcred credential was accepted: %v", err)
	}
	requireEmpty(t, packet)
	send(t, sender, []byte("after-passcred"), nil)
	packet, err = receiver.Receive(context.Background())
	if err != nil || string(packet.Payload) != "after-passcred" || packet.Credentials.PID <= 0 {
		t.Fatalf("post-passcred positive control failed: %v", err)
	}
}
