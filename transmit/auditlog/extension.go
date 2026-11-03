package auditlog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"time"
)

// These provisional ceilings bound verifier resources, not execution permission.
// TODO(이동찬): measure the largest synthetic run's memory/verification cost and
// confirm these deployment limits with the CPD04 reviewers before activation.
const (
	// Conservative snapshot input cap; allocation overhead remains unmeasured.
	MaxExtensionBytes = int64(64 << 20)
	// Independent total-event cap, with headroom above intent/result pairs.
	// Refused attempts can record results without spending a request budget.
	MaxExtensionEvents = uint64(40000)
	// Matches server/controlled_read_store.go's maxControlledTimeoutMS ceiling.
	MaxExtensionTimeout = 2 * time.Minute
	// Follows bufio's maxConsecutiveEmptyReads convention for no progress.
	maxExtensionEmptyReads = 100
)

var ErrExtensionLimit = errors.New("auditlog: extension snapshot limit exceeded")

type ExtensionLimits struct {
	MaxBytes  int64
	MaxEvents uint64
	Timeout   time.Duration
}

// ExtensionSummary is returned only after the candidate and the previous
// checkpoint authenticate against the same reader-owned snapshot. It contains
// no source bytes and establishes no store epoch, phase, publisher or key trust.
type ExtensionSummary struct {
	Previous Checkpoint
	Current  Summary
}

// VerifyExtension checks append-only prefix inclusion. The caller supplies the
// separately trusted previous head and candidate; it authenticates publisher,
// current epoch/revision, phase, and key lifecycle outside this function. An
// exact duplicate is cryptographically valid, not permission to resume/seal it.
//
// Input is consumed once, bounded and copied into private immutable bytes. No
// live file is modified, sought or reopened between candidate/prefix checks.
// Context cannot forcibly interrupt a blocking Reader that ignores cancellation.
func VerifyExtension(ctx context.Context, source io.Reader, key []byte, previous, candidate Checkpoint, limits ExtensionLimits) (ExtensionSummary, error) {
	if ctx == nil || source == nil || len(key) != KeyBytes ||
		limits.MaxBytes <= 0 || limits.MaxBytes > MaxExtensionBytes || limits.MaxEvents == 0 || limits.MaxEvents > MaxExtensionEvents ||
		limits.Timeout <= 0 || limits.Timeout > MaxExtensionTimeout || !validExtensionCheckpoint(previous) || !validExtensionCheckpoint(candidate) {
		return ExtensionSummary{}, ErrConfig
	}
	ctx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return ExtensionSummary{}, err
	}
	if previous.Scope != candidate.Scope || previous.JournalID != candidate.JournalID || previous.KeyRef != candidate.KeyRef ||
		previous.Sequence > candidate.Sequence || previous.Sequence == candidate.Sequence && previous.MAC != candidate.MAC {
		return ExtensionSummary{}, ErrIntegrity
	}
	if candidate.Sequence > limits.MaxEvents {
		return ExtensionSummary{}, ErrExtensionLimit
	}
	ownedKey := bytes.Clone(key)
	defer clear(ownedKey)
	snapshot, err := io.ReadAll(io.LimitReader(&extensionContextReader{ctx: ctx, source: source}, limits.MaxBytes+1))
	defer clear(snapshot)
	if err != nil {
		if ctx.Err() != nil {
			return ExtensionSummary{}, ctx.Err()
		}
		return ExtensionSummary{}, ErrRead
	}
	if ctx.Err() != nil {
		return ExtensionSummary{}, ctx.Err()
	}
	if int64(len(snapshot)) > limits.MaxBytes {
		return ExtensionSummary{}, ErrExtensionLimit
	}
	current, err := Verify(ctx, bytes.NewReader(snapshot), ownedKey, candidate)
	if err != nil {
		return ExtensionSummary{}, err
	}
	// The full candidate is already authenticated. Its first previous.Sequence
	// event lines plus the header form the only possible old-prefix boundary.
	end := 0
	for line := uint64(0); line <= previous.Sequence; line++ {
		if ctx.Err() != nil {
			return ExtensionSummary{}, ctx.Err()
		}
		newline := bytes.IndexByte(snapshot[end:], '\n')
		if newline < 0 {
			return ExtensionSummary{}, ErrIntegrity
		}
		end += newline + 1
	}
	if _, err := Verify(ctx, bytes.NewReader(snapshot[:end]), ownedKey, previous); err != nil {
		return ExtensionSummary{}, err
	}
	if ctx.Err() != nil {
		return ExtensionSummary{}, ctx.Err()
	}
	return ExtensionSummary{Previous: previous, Current: current}, nil
}

func validExtensionCheckpoint(cp Checkpoint) bool {
	return cp.Version == Version && validScope(cp.Scope) && validRef(cp.KeyRef) &&
		validHex(cp.JournalID, journalIDBytes) && validHex(cp.MAC, sha256.Size)
}

type extensionContextReader struct {
	ctx        context.Context
	source     io.Reader
	emptyReads int
}

func (r *extensionContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.source.Read(buffer)
	// A broken Reader must not pass an impossible count to io.ReadAll, whose
	// buffer slicing would otherwise panic before a fixed read error is returned.
	if n < 0 || n > len(buffer) {
		return 0, ErrRead
	}
	if canceled := r.ctx.Err(); canceled != nil {
		return n, canceled
	}
	if n == 0 && err == nil {
		r.emptyReads++
		if r.emptyReads >= maxExtensionEmptyReads {
			return 0, io.ErrNoProgress
		}
	} else {
		r.emptyReads = 0
	}
	return n, err
}
