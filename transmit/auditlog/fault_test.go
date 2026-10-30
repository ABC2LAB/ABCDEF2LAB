package auditlog

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit"
)

const faultSecret = "PRIVATE-OS-ERROR-SENTINEL"

type faultWriter struct {
	underlying syncWriter
	writeErr   error
	shortWrite bool
	syncErr    error
	closeErr   error
	beforeSync func()
	writes     int
	syncs      int
	closes     int
}

func (f *faultWriter) Write(data []byte) (int, error) {
	f.writes++
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	if f.shortWrite {
		return f.underlying.Write(data[:len(data)/2])
	}
	return f.underlying.Write(data)
}

func (f *faultWriter) Sync() error {
	f.syncs++
	if f.beforeSync != nil {
		f.beforeSync()
	}
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.underlying.Sync()
}

func (f *faultWriter) Close() error {
	f.closes++
	if err := f.underlying.Close(); err != nil {
		return err
	}
	return f.closeErr
}

func faultFixture(t *testing.T) (*Journal, Config, string, transmit.Event) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("file durability is supported on Linux")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Scope: transmit.Scope{RunRef: "run", TaskRef: "task", CaseRef: "case", ResetRef: "reset", PolicyRef: "policy"}, KeyRef: "key-1", Key: bytes.Repeat([]byte{0x59}, KeyBytes)}
	j, err := Create(dir, "events.jsonl", cfg)
	if err != nil {
		t.Fatal(err)
	}
	event := transmit.Event{Stage: "intent", Receipt: transmit.Receipt{
		Scope: cfg.Scope, Attempt: 1, At: time.Now(), Method: "GET",
		AccountRef: "account", SessionRef: "session", ResourceRef: "document:one", ApprovalRef: "read-1",
	}}
	return j, cfg, filepath.Join(dir, "events.jsonl"), event
}

func TestRecordWaitsForSyncAndCanceledWaiterDoesNotWrite(t *testing.T) {
	j, cfg, path, event := faultFixture(t)
	defer j.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	f := &faultWriter{underlying: j.writer, beforeSync: func() { close(entered); <-release }}
	j.writer = f
	done := make(chan error, 1)
	go func() { done <- j.Record(context.Background(), event) }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("Record ended before Sync: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Record did not reach Sync")
	}
	if j.Checkpoint().Sequence != 0 {
		t.Error("checkpoint advanced before Sync completed")
	}
	select {
	case <-done:
		t.Error("Record acknowledged before Sync completed")
	default:
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan error, 1)
	go func() { waiting <- j.Record(ctx, event) }()
	cancel()
	if err := <-waiting; !errors.Is(err, context.Canceled) {
		t.Errorf("canceled waiter = %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f.writes != 1 || f.syncs != 1 || j.Checkpoint().Sequence != 1 {
		t.Fatalf("writes=%d syncs=%d seq=%d", f.writes, f.syncs, j.Checkpoint().Sequence)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if summary, err := Verify(context.Background(), file, cfg.Key, j.Checkpoint()); err != nil || summary.Intents != 1 {
		t.Fatalf("synced event not verifiable: counts=%+v error=%v", summary, err)
	}
}

func TestStorageFailuresPoisonRecorderWithoutRetry(t *testing.T) {
	for _, mode := range []string{"write", "short-write", "sync"} {
		t.Run(mode, func(t *testing.T) {
			j, _, _, event := faultFixture(t)
			f := &faultWriter{underlying: j.writer}
			j.writer = f
			switch mode {
			case "write":
				f.writeErr = errors.New(faultSecret)
			case "short-write":
				f.shortWrite = true
			case "sync":
				f.syncErr = errors.New(faultSecret)
			}
			if err := j.Record(context.Background(), event); !errors.Is(err, ErrStorage) || bytes.Contains([]byte(err.Error()), []byte(faultSecret)) {
				t.Fatalf("storage error not safely surfaced: %v", err)
			}
			if err := j.Record(context.Background(), event); !errors.Is(err, ErrFailed) || f.writes != 1 || j.Checkpoint().Sequence != 0 {
				t.Fatalf("failed recorder retried/advanced: err=%v writes=%d seq=%d", err, f.writes, j.Checkpoint().Sequence)
			}
			if err := j.Close(); !errors.Is(err, ErrFailed) {
				t.Fatalf("Close lost prior failure: %v", err)
			}
		})
	}
}

func TestCancelDuringSyncPreservesDurableButUnacknowledgedHead(t *testing.T) {
	j, cfg, path, event := faultFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	j.writer = &faultWriter{underlying: j.writer, beforeSync: cancel}
	if err := j.Record(ctx, event); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Sync acknowledged: %v", err)
	}
	if j.Checkpoint().Sequence != 1 {
		t.Fatal("durable prefix disappeared after cancellation")
	}
	if err := j.Record(context.Background(), event); !errors.Is(err, ErrFailed) {
		t.Fatalf("uncertain recorder reused: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := Verify(context.Background(), file, cfg.Key, j.Checkpoint()); err != nil {
		t.Fatalf("durable but unacknowledged event cannot be examined: %v", err)
	}
	if err := j.Close(); !errors.Is(err, ErrFailed) {
		t.Fatalf("Close lost cancellation uncertainty: %v", err)
	}
}

func TestCloseFailureAndRepeatedCloseAreSafe(t *testing.T) {
	j, _, _, event := faultFixture(t)
	f := &faultWriter{underlying: j.writer, closeErr: errors.New(faultSecret)}
	j.writer = f
	for range 2 {
		if err := j.Close(); !errors.Is(err, ErrStorage) || bytes.Contains([]byte(err.Error()), []byte(faultSecret)) {
			t.Fatalf("unsafe Close result: %v", err)
		}
	}
	if f.closes != 1 {
		t.Fatal("Close repeated the underlying operation")
	}
	if err := j.Record(context.Background(), event); !errors.Is(err, ErrClosed) {
		t.Fatalf("recorded after Close: %v", err)
	}
}

type faultReader struct{}

func (faultReader) Read([]byte) (int, error) { return 0, errors.New(faultSecret) }

func TestVerifierReadFailureNeverLeaksSourceError(t *testing.T) {
	j, cfg, _, _ := faultFixture(t)
	defer j.Close()
	summary, err := Verify(context.Background(), faultReader{}, cfg.Key, j.Checkpoint())
	if !errors.Is(err, ErrRead) || summary != (Summary{}) || bytes.Contains([]byte(err.Error()), []byte(faultSecret)) {
		t.Fatalf("unsafe verification failure: summary=%+v error=%v", summary, err)
	}
}
