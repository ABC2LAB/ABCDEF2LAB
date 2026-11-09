package diagnosticlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"syscall"
	"testing"
)

type memoryWriter struct {
	bytes.Buffer
	writeErr, errorSync, errorClose, errorTruncate error
	short                                          bool
}

func (m *memoryWriter) Write(p []byte) (int, error) {
	if m.writeErr != nil {
		return 0, m.writeErr
	}
	if m.short {
		return m.Buffer.Write(p[:len(p)/2])
	}
	return m.Buffer.Write(p)
}
func (m *memoryWriter) Sync() error  { return m.errorSync }
func (m *memoryWriter) Close() error { return m.errorClose }
func (m *memoryWriter) Truncate(n int64) error {
	if m.errorTruncate != nil {
		return m.errorTruncate
	}
	m.Buffer.Truncate(int(n))
	return nil
}
func testEvent() Event {
	return Event{Component: "cli", Event: "stage_finished", Stage: "configuration", Status: "succeeded"}
}
func TestConcurrentJSONLAndCorrelation(t *testing.T) {
	w := &memoryWriter{}
	l := &Logger{writer: w, task: 42, run: "raw-private-run"}
	ctx := With(context.Background(), l)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 80; j++ {
				e := testEvent()
				e.IntentID = 7
				e.CallRef = "raw-private-call"
				e.AttemptRef = "raw-private-attempt"
				if err := Emit(ctx, e); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(w.Bytes()), []byte("\n"))
	if len(lines) != 960 {
		t.Fatal(len(lines))
	}
	for i, line := range lines {
		var e Event
		if json.Unmarshal(line, &e) != nil || e.Version != 1 || e.Sequence != uint64(i+1) || e.TaskID != 42 || e.IntentID != 7 || len(e.RunRef) != 64 || len(e.CallRef) != 64 || len(e.AttemptRef) != 64 || e.Timestamp.IsZero() {
			t.Fatalf("invalid row %d", i)
		}
	}
	if bytes.Contains(w.Bytes(), []byte("raw-private")) {
		t.Fatal("raw reference leak")
	}
}
func TestFiniteFieldsRejectRawSecrets(t *testing.T) {
	for _, field := range []string{"component", "event", "stage", "status", "reason", "tool", "role", "model", "provider", "level"} {
		t.Run(field, func(t *testing.T) {
			w := &memoryWriter{}
			l := &Logger{writer: w}
			e := testEvent()
			secret := "Authorization: Bearer secret-value"
			switch field {
			case "component":
				e.Component = secret
			case "event":
				e.Event = secret
			case "stage":
				e.Stage = secret
			case "status":
				e.Status = secret
			case "reason":
				e.ReasonCode = secret
			case "tool":
				e.Tool = secret
			case "role":
				e.Role = secret
			case "model":
				e.Model = secret
			case "provider":
				e.Provider = secret
			case "level":
				e.Level = secret
			}
			if !errors.Is(l.Record(e), ErrEvent) || w.Len() != 0 {
				t.Fatal("invalid event accepted")
			}
			if !errors.Is(l.Record(testEvent()), ErrEvent) {
				t.Fatal("failure not sticky")
			}
		})
	}
}
func TestWriteFailureAndLimits(t *testing.T) {
	for _, kind := range []string{"disk_full", "short_write", "sync", "events", "bytes", "close"} {
		t.Run(kind, func(t *testing.T) {
			w := &memoryWriter{}
			l := &Logger{writer: w}
			if err := l.Record(testEvent()); err != nil {
				t.Fatal(err)
			}
			prefix := w.Len()
			want := ErrWrite
			switch kind {
			case "disk_full":
				w.writeErr = syscall.ENOSPC
			case "short_write":
				w.short = true
			case "sync":
				w.errorSync = io.ErrClosedPipe
			case "events":
				l.count = MaxEvents
				want = ErrLimit
			case "bytes":
				l.size = MaxBytes
				want = ErrLimit
			case "close":
				w.errorClose = io.ErrClosedPipe
				if !errors.Is(l.Close(), ErrWrite) {
					t.Fatal("close ignored")
				}
				return
			}
			if !errors.Is(l.Record(testEvent()), want) {
				t.Fatal("missing failure")
			}
			if !errors.Is(l.Err(), want) {
				t.Fatal("sticky missing")
			}
			if kind == "short_write" && w.Len() != prefix {
				t.Fatal("partial row retained")
			}
			if !errors.Is(l.Record(testEvent()), want) {
				t.Fatal("recovered incorrectly")
			}
		})
	}
}
func TestBindingAndContext(t *testing.T) {
	w := &memoryWriter{}
	l := &Logger{writer: w}
	if l.BindTask(42) != nil || l.BindRun("run") != nil || l.BindTask(43) == nil || l.BindRun("other") == nil {
		t.Fatal("binding")
	}
	e := testEvent()
	e.TaskID = 43
	if !errors.Is(l.Record(e), ErrEvent) {
		t.Fatal("task mismatch")
	}
	if Emit(context.Background(), e) != nil {
		t.Fatal("optional observer")
	}
	if RoleFrom(WithRole(context.Background(), "secret")) != "unknown" {
		t.Fatal("role")
	}
	if strings.Contains(l.String(), "run") {
		t.Fatal("string leak")
	}
}

func TestIncompleteDistinctFromFailure(t *testing.T) {
	for _, field := range []string{"status", "flag"} {
		t.Run(field, func(t *testing.T) {
			l := &Logger{writer: &memoryWriter{}}
			e := testEvent()
			if field == "status" {
				e.Status = "incomplete"
			} else {
				v := false
				e.ObservationComplete = &v
			}
			if l.Record(e) != nil || !l.Incomplete() || l.Err() != nil {
				t.Fatal("observation incorrectly classified")
			}
			if l.Record(testEvent()) != nil || !l.Incomplete() {
				t.Fatal("incomplete lost")
			}
		})
	}
}

func TestFailureNotification(t *testing.T) {
	for _, kind := range []string{"write", "limit", "late_subscriber", "binding", "close"} {
		t.Run(kind, func(t *testing.T) {
			w := &memoryWriter{}
			l := &Logger{writer: w}
			var notification <-chan struct{}
			if kind != "late_subscriber" {
				notification = l.Failed()
				if l.Failed() != notification {
					t.Fatal("channel replaced")
				}
				select {
				case <-notification:
					t.Fatal("success notified")
				default:
				}
			}
			if l.Record(testEvent()) != nil {
				t.Fatal("success failed")
			}
			switch kind {
			case "write", "late_subscriber":
				w.writeErr = syscall.ENOSPC
				_ = l.Record(testEvent())
			case "limit":
				l.count = MaxEvents
				_ = l.Record(testEvent())
			case "binding":
				_ = l.BindTask(-1)
			case "close":
				w.errorClose = io.ErrClosedPipe
				_ = l.Close()
			}
			if notification == nil {
				notification = l.Failed()
			}
			select {
			case <-notification:
			default:
				t.Fatal("failure not notified")
			}
			original := l.Err()
			if original == nil {
				t.Fatal("missing error")
			}
			for i := 0; i < 3; i++ {
				_ = l.Record(testEvent())
				_ = l.Close()
				if l.Failed() != notification || l.Err() != original {
					t.Fatal("failure changed")
				}
			}
		})
	}
}
