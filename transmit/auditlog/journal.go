package auditlog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/Autumn-27/artex/transmit"
)

type syncWriter interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

// Journal appends one HMAC-chained event at a time. A file is never resumed or
// repaired in place: start a new journal for every recorder instance. This avoids
// a second process appending from a stale head without a cross-process lock.
type Journal struct {
	writer   syncWriter
	key      []byte
	gate     chan struct{}
	mu       sync.Mutex
	head     Checkpoint
	failed   bool
	closed   bool
	closeErr error
}

var _ transmit.Recorder = (*Journal)(nil)

// Create creates a new file in an existing, trusted 0700 directory. The name is
// a single slash-free component, never a path. Linux is the supported durability
// platform; success requires syncing both the header file and its directory.
// On failure an incomplete file may remain for diagnosis; it is never removed.
func Create(directory, name string, cfg Config) (*Journal, error) {
	if runtime.GOOS != "linux" || !filePattern.MatchString(name) || !validScope(cfg.Scope) || !validRef(cfg.KeyRef) || len(cfg.Key) != KeyBytes {
		return nil, ErrConfig
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, ErrStorage
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, ErrConfig
	}
	dir, err := root.Open(".")
	if err != nil {
		return nil, ErrStorage
	}
	defer dir.Close()
	// O_EXCL rejects existing files and symlinks. Restricting names also avoids
	// path traversal/trailing-slash behaviors of older os.Root implementations.
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, ErrExists
		}
		return nil, ErrStorage
	}
	key := append([]byte(nil), cfg.Key...)
	var id [journalIDBytes]byte
	if _, err := rand.Read(id[:]); err != nil {
		file.Close()
		clear(key)
		return nil, ErrStorage
	}
	h := header{Version: Version, JournalID: hex.EncodeToString(id[:]), Scope: cfg.Scope, KeyRef: cfg.KeyRef, CreatedAt: time.Now().UTC()}
	mac, err := sign(key, "header", h)
	if err != nil {
		file.Close()
		clear(key)
		return nil, err
	}
	encoded, err := encodeLine(headerLine{Header: h, MAC: mac})
	if err != nil {
		file.Close()
		clear(key)
		return nil, err
	}
	n, writeErr := file.Write(encoded)
	if writeErr != nil || n != len(encoded) {
		file.Close()
		clear(key)
		return nil, ErrStorage
	}
	if err := file.Sync(); err != nil {
		file.Close()
		clear(key)
		return nil, ErrStorage
	}
	if err := dir.Sync(); err != nil {
		file.Close()
		clear(key)
		return nil, ErrStorage
	}
	return &Journal{
		writer: file, key: key, gate: make(chan struct{}, 1),
		head: Checkpoint{Version: Version, JournalID: h.JournalID, Scope: h.Scope, KeyRef: h.KeyRef, MAC: mac},
	}, nil
}

// Record acknowledges only a complete Write+Sync. A failure after IO begins
// poisons the recorder, even if bytes may already be durable. No background write
// continues after return. Regular file IO is not context-interruptible: cancel is
// checked at the waiting/IO boundaries, not enforced by abandoning a goroutine.
func (j *Journal) Record(ctx context.Context, event transmit.Event) error {
	if ctx == nil || j == nil || j.gate == nil {
		return ErrConfig
	}
	select {
	case j.gate <- struct{}{}:
		defer func() { <-j.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	j.mu.Lock()
	head, failed, closed := j.head, j.failed, j.closed
	j.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if failed {
		return ErrFailed
	}
	if !validEvent(event, head.Scope) || head.Sequence == math.MaxUint64 {
		return ErrEvent
	}
	event.At = event.At.UTC()
	if !event.AdmittedAt.IsZero() {
		event.AdmittedAt = event.AdmittedAt.UTC()
	}
	payload := eventPayload{Sequence: head.Sequence + 1, Previous: head.MAC, Event: event}
	mac, err := sign(j.key, "event", payload)
	if err != nil {
		return err
	}
	encoded, err := encodeLine(eventLine{Record: payload, MAC: mac})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	n, err := j.writer.Write(encoded)
	if err != nil || n != len(encoded) {
		j.poison()
		return ErrStorage
	}
	if err := j.writer.Sync(); err != nil {
		j.poison()
		return ErrStorage
	}
	j.mu.Lock()
	j.head.Sequence, j.head.MAC = payload.Sequence, mac
	j.mu.Unlock()
	if err := ctx.Err(); err != nil {
		j.poison()
		return err
	}
	return nil
}

func (j *Journal) poison() {
	j.mu.Lock()
	j.failed = true
	j.mu.Unlock()
}

// Checkpoint returns the last Sync-confirmed prefix, including after failure or
// Close. A failed write may have left additional bytes; Verify will reject them.
// The caller must preserve the checkpoint externally to detect rollback.
func (j *Journal) Checkpoint() Checkpoint {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.head
}

// Close serializes with Record, clears the recorder's key copy, and reports any
// prior IO failure. Repeated Close returns the same outcome. It never deletes data.
func (j *Journal) Close() error {
	if j == nil || j.gate == nil {
		return ErrClosed
	}
	j.gate <- struct{}{}
	defer func() { <-j.gate }()
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return j.closeErr
	}
	j.closed = true
	if err := j.writer.Close(); err != nil {
		j.failed, j.closeErr = true, ErrStorage
	} else if j.failed {
		j.closeErr = ErrFailed
	}
	clear(j.key)
	return j.closeErr
}
