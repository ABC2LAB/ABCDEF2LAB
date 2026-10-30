package auditlog

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// Verify authenticates an entire journal against a separately trusted checkpoint.
// There is no unanchored success mode: a valid older prefix is not sufficient.
// Input is streamed with a per-line limit and at most expected.Sequence events.
// A failed verification returns no partial summary. As with regular file writes,
// context checks cannot forcibly interrupt an arbitrary blocking Reader.
func Verify(ctx context.Context, source io.Reader, key []byte, expected Checkpoint) (Summary, error) {
	if ctx == nil || source == nil || len(key) != KeyBytes || expected.Version != Version || !validScope(expected.Scope) || !validRef(expected.KeyRef) || !validHex(expected.JournalID, journalIDBytes) || !validHex(expected.MAC, sha256.Size) {
		return Summary{}, ErrConfig
	}
	reader := bufio.NewReaderSize(source, MaxRecordBytes+1)
	line, err := readLine(ctx, reader)
	if err != nil {
		if err == io.EOF {
			err = ErrIntegrity
		}
		return Summary{}, err
	}
	var first headerLine
	if !decodeCanonical(line, &first) {
		return Summary{}, ErrIntegrity
	}
	h := first.Header
	if h.Version != Version || h.JournalID != expected.JournalID || h.Scope != expected.Scope || h.KeyRef != expected.KeyRef || !validTime(h.CreatedAt) {
		return Summary{}, ErrIntegrity
	}
	mac, err := sign(key, "header", h)
	if err != nil || !equalMAC(mac, first.MAC) {
		return Summary{}, ErrIntegrity
	}
	head := Checkpoint{Version: Version, JournalID: h.JournalID, Scope: h.Scope, KeyRef: h.KeyRef, MAC: first.MAC}
	var intents, results uint64
	for {
		line, err := readLine(ctx, reader)
		if err == io.EOF {
			if head.Sequence != expected.Sequence || !equalMAC(head.MAC, expected.MAC) {
				return Summary{}, ErrIntegrity
			}
			return Summary{Checkpoint: head, Intents: intents, Results: results}, nil
		}
		if err != nil {
			return Summary{}, err
		}
		if head.Sequence >= expected.Sequence {
			return Summary{}, ErrIntegrity
		}
		var entry eventLine
		if !decodeCanonical(line, &entry) || entry.Record.Sequence != head.Sequence+1 || !equalMAC(entry.Record.Previous, head.MAC) || !validEvent(entry.Record.Event, h.Scope) {
			return Summary{}, ErrIntegrity
		}
		mac, err := sign(key, "event", entry.Record)
		if err != nil || !equalMAC(mac, entry.MAC) {
			return Summary{}, ErrIntegrity
		}
		head.Sequence, head.MAC = entry.Record.Sequence, entry.MAC
		if entry.Record.Event.Stage == "intent" {
			intents++
		} else {
			results++
		}
	}
}

func readLine(ctx context.Context, reader *bufio.Reader) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	line, err := reader.ReadSlice('\n')
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err == io.EOF && len(line) == 0 {
		return nil, io.EOF
	}
	if err == bufio.ErrBufferFull || len(line) > MaxRecordBytes || err == io.EOF {
		return nil, ErrIntegrity
	}
	if err != nil {
		return nil, ErrRead
	}
	return line[:len(line)-1], nil
}

// Canonical re-encoding rejects duplicate keys, escaped aliases, missing/unknown
// fields, alternative whitespace, and appended JSON values. The wire format is
// versioned and emits the fixed Go struct field order, rather than generic JSON.
func decodeCanonical(line []byte, destination any) bool {
	if !utf8.Valid(line) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if decoder.Decode(destination) != nil {
		return false
	}
	encoded, err := json.Marshal(destination)
	return err == nil && bytes.Equal(encoded, line)
}
