package auditlog

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit"
)

// 키를 가진 쪽이 다시 서명해도 순번과 이전 MAC을 각각 깨면 거부돼야 한다.
// 둘 중 하나만 깨서 다른 검사가 대신 막지 못하게 한다.
func chainFixture(t *testing.T, key []byte, tamper func(int, *eventPayload)) ([]byte, Checkpoint) {
	t.Helper()
	at := time.Date(2026, 11, 3, 0, 0, 0, 123456789, time.UTC)
	scope := transmit.Scope{RunRef: "run-1", TaskRef: "1", CaseRef: "case-1", ResetRef: "reset-1", PolicyRef: "policy-1"}
	head := header{Version: Version, JournalID: strings.Repeat("a", 32), Scope: scope, KeyRef: "synthetic-key", CreatedAt: at}
	mac, err := sign(key, "header", head)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	write := func(value any) {
		line, err := encodeLine(value)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(line)
	}
	write(headerLine{Header: head, MAC: mac})
	current := Checkpoint{Version: Version, JournalID: head.JournalID, Scope: scope, KeyRef: head.KeyRef, MAC: mac}
	for sequence := 1; sequence <= 2; sequence++ {
		event := transmit.Event{Stage: "intent", Receipt: transmit.Receipt{Scope: scope, Attempt: 1, AccountRef: "owner", SessionRef: "owner",
			ResourceRef: "document:one", ApprovalRef: "read-1", Method: "GET", At: at}}
		if sequence == 2 {
			event.Stage, event.Reason = "result", transmit.Completed
			event.Admitted, event.AdmittedAt = true, at
			event.RequestWritten, event.ResponseReceived, event.StatusCode = true, true, 200
		}
		payload := eventPayload{Sequence: uint64(sequence), Previous: current.MAC, Event: event}
		if tamper != nil {
			tamper(sequence, &payload)
		}
		eventMAC, err := sign(key, "event", payload)
		if err != nil {
			t.Fatal(err)
		}
		write(eventLine{Record: payload, MAC: eventMAC})
		current.Sequence, current.MAC = payload.Sequence, eventMAC
	}
	return out.Bytes(), current
}

func TestVerifyRejectsResignedChainBreaks(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeyBytes)
	source, expected := chainFixture(t, key, nil)
	if _, err := verify(context.Background(), bytes.NewReader(source), key, expected, nil); err != nil {
		t.Fatalf("control rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		tamper func(int, *eventPayload)
	}{
		{"sequence gap", func(n int, p *eventPayload) {
			if n == 2 {
				p.Sequence = 3
			}
		}},
		{"previous mac", func(n int, p *eventPayload) {
			if n == 2 {
				p.Previous = strings.Repeat("0", len(p.Previous))
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, expected := chainFixture(t, key, tc.tamper)
			if _, err := verify(context.Background(), bytes.NewReader(source), key, expected, nil); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("got %v want ErrIntegrity", err)
			}
		})
	}
}
