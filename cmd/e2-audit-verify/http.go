package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/Autumn-27/artex/e2proxy"
)

const (
	registeredGETJournalFormat = "registered-get-v1"
	e2proxyJournalFormat       = e2proxy.JournalVersion
	// Match CreateJournal/VerifyJournal's accepted raw HMAC key range. The
	// existing e2-audit-keygen produces the supported 32-byte minimum.
	minHTTPJournalKeyBytes = 32
	maxHTTPJournalKeyBytes = 4096
)

// This verifies only the supplied bytes and checkpoint. JournalSummary is not
// an Execution ACK, a closure certificate, a current host head or E3 evidence.
func verifyHTTPFiles(ctx context.Context, journalPath, checkpointPath, keyPath string) (result any, code string) {
	if ctx.Err() != nil {
		return nil, "audit_verification_canceled"
	}
	key, err := readBounded(keyPath, maxHTTPJournalKeyBytes, true)
	if err != nil {
		return nil, "audit_input_unavailable"
	}
	defer clear(key)
	if len(key) < minHTTPJournalKeyBytes {
		return nil, "audit_input_invalid"
	}
	encoded, err := readBounded(checkpointPath, maxCheckpointBytes, false)
	if err != nil {
		return nil, "audit_input_unavailable"
	}
	checkpoint, err := decodeHTTPCheckpoint(encoded)
	if err != nil {
		return nil, "audit_input_invalid"
	}
	journal, err := openRegular(journalPath, false)
	if err != nil {
		return nil, "audit_input_unavailable"
	}
	defer func() {
		if err := journal.Close(); err != nil && code == "" {
			result, code = nil, "audit_input_unavailable"
		}
	}()
	// Always supply the explicit head: VerifyJournal's optional unanchored mode
	// accepts authentic older prefixes and must not be used by this command.
	result, err = e2proxy.VerifyJournal(ctx, journal, key, checkpoint)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			code = "audit_verification_canceled"
		case errors.Is(err, e2proxy.ErrJournalIntegrity):
			code = "audit_integrity_failed"
		case errors.Is(err, e2proxy.ErrJournalLimit):
			code = "audit_verification_limit_exceeded"
		case errors.Is(err, e2proxy.ErrJournalConfig):
			code = "audit_input_invalid"
		default:
			code = "audit_input_unavailable"
		}
		return nil, code
	}
	return result, ""
}

func decodeHTTPCheckpoint(encoded []byte) (e2proxy.JournalCheckpoint, error) {
	var checkpoint e2proxy.JournalCheckpoint
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&checkpoint) != nil {
		return e2proxy.JournalCheckpoint{}, errInput
	}
	var compact bytes.Buffer
	if json.Compact(&compact, encoded) != nil {
		return e2proxy.JournalCheckpoint{}, errInput
	}
	canonical, err := json.Marshal(checkpoint)
	// Preserve the trust anchor's complete wire representation, including field
	// order. Reject duplicates, case aliases, missing fields and trailing values.
	if err != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return e2proxy.JournalCheckpoint{}, errInput
	}
	return checkpoint, nil
}
