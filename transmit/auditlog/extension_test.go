package auditlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit"
)

type extensionFixture struct {
	config              Config
	path                string
	previous, candidate Checkpoint
	data                []byte
	limits              ExtensionLimits
}

// Actual writer/MAC with synthetic audit events; no target requests, source
// authorities, identity decisions, store epochs or sealed state are created.
func newExtensionFixture(t *testing.T, previousEvents uint64) *extensionFixture {
	t.Helper()
	j, config, path, event := faultFixture(t)
	t.Cleanup(func() { _ = j.Close() })
	f := &extensionFixture{config: config, path: path, previous: j.Checkpoint(), limits: ExtensionLimits{MaxBytes: 1 << 20, MaxEvents: 4, Timeout: time.Second}}
	for i := uint64(1); i <= 4; i++ {
		event.Attempt = (i + 1) / 2
		if i%2 == 1 {
			event.Stage = "intent"
			event.Reason = ""
		} else {
			event.Stage = "result"
			event.Reason = transmit.Stopped
		}
		if err := j.Record(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		if i == previousEvents {
			f.previous = j.Checkpoint()
		}
	}
	f.candidate = j.Checkpoint()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestVerifyExtensionAuthenticatesSameSnapshotPrefix(t *testing.T) {
	for _, previousEvents := range []uint64{0, 1, 2, 3, 4} {
		t.Run(string(rune('0'+previousEvents)), func(t *testing.T) {
			f := newExtensionFixture(t, previousEvents)
			f.limits.MaxBytes = int64(len(f.data))
			got, err := VerifyExtension(context.Background(), bytes.NewReader(f.data), f.config.Key, f.previous, f.candidate, f.limits)
			if err != nil || got.Previous != f.previous || got.Current.Checkpoint != f.candidate || got.Current.Intents != 2 || got.Current.Results != 2 {
				t.Fatalf("full/prefix did not authenticate: %v", err)
			}
			if !bytes.Equal(f.config.Key, bytes.Repeat([]byte{0x59}, KeyBytes)) {
				t.Fatal("borrowed key was altered")
			}
		})
	}
}

func TestVerifyExtensionRejectsValidMACFork(t *testing.T) {
	f := newExtensionFixture(t, 2)
	lines := bytes.Split(f.data[:len(f.data)-1], []byte{'\n'})
	var head headerLine
	if json.Unmarshal(lines[0], &head) != nil {
		t.Fatal("fixture header")
	}
	previous := head.MAC
	for i := 1; i < len(lines); i++ {
		var entry eventLine
		if json.Unmarshal(lines[i], &entry) != nil {
			t.Fatal("fixture event")
		}
		entry.Record.Previous = previous
		if i == 1 {
			entry.Record.Event.ResourceRef = "document:different"
		}
		mac, err := sign(f.config.Key, "event", entry.Record)
		if err != nil {
			t.Fatal(err)
		}
		entry.MAC = mac
		previous = mac
		lines[i], err = json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
	}
	fork := append(bytes.Join(lines, []byte{'\n'}), '\n')
	forkHead := f.candidate
	forkHead.MAC = previous
	// A single-head check accepts this independently valid MAC chain.
	if _, err := Verify(context.Background(), bytes.NewReader(fork), f.config.Key, forkHead); err != nil {
		t.Fatalf("fork fixture is not MAC-valid: %v", err)
	}
	got, err := VerifyExtension(context.Background(), bytes.NewReader(fork), f.config.Key, f.previous, forkHead, f.limits)
	if !errors.Is(err, ErrIntegrity) || got.Current.Checkpoint != (Checkpoint{}) {
		t.Fatalf("independent valid fork was treated as extension: %v", err)
	}
}

func TestVerifyExtensionRejectsInvalidOrPartialInput(t *testing.T) {
	for _, name := range []string{"wrong-key", "scope", "journal", "key-ref", "rollback", "same-sequence-other-mac", "old-prefix-mac", "tail", "truncated", "tampered", "empty", "byte-limit", "event-limit", "reader-error", "reader-no-progress", "cancelled", "cancelled-during-read", "timeout"} {
		t.Run(name, func(t *testing.T) {
			f := newExtensionFixture(t, 2)
			ctx := context.Background()
			var source io.Reader = bytes.NewReader(f.data)
			want := ErrIntegrity
			switch name {
			case "wrong-key":
				f.config.Key = bytes.Repeat([]byte{0x58}, KeyBytes)
			case "scope":
				f.candidate.Scope.TaskRef = "other-task"
			case "journal":
				f.candidate.JournalID = strings.Repeat("f", 32)
			case "key-ref":
				f.candidate.KeyRef = "other-key"
			case "rollback":
				f.previous, f.candidate = f.candidate, f.previous
			case "same-sequence-other-mac":
				f.previous = f.candidate
				f.previous.MAC = strings.Repeat("e", 64)
			case "old-prefix-mac":
				f.previous.MAC = strings.Repeat("e", 64)
			case "tail":
				source = bytes.NewReader(append(bytes.Clone(f.data), []byte("{}\n")...))
			case "truncated":
				source = bytes.NewReader(f.data[:len(f.data)-1])
			case "tampered":
				data := bytes.Clone(f.data)
				data[bytes.Index(data, []byte("account"))] = 'x'
				source = bytes.NewReader(data)
			case "empty":
				source = bytes.NewReader(nil)
			case "byte-limit":
				f.limits.MaxBytes = int64(len(f.data) - 1)
				want = ErrExtensionLimit
			case "event-limit":
				f.limits.MaxEvents = 3
				want = ErrExtensionLimit
			case "reader-error":
				source = extensionFaultReader{err: errors.New("PRIVATE-READ-FAILURE")}
				want = ErrRead
			case "reader-no-progress":
				source = extensionEmptyReader{}
				want = ErrRead
			case "cancelled":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
				want = context.Canceled
			case "cancelled-during-read":
				c, cancel := context.WithCancel(ctx)
				ctx = c
				source = &extensionCancelReader{reader: bytes.NewReader(f.data), cancel: cancel}
				want = context.Canceled
			case "timeout":
				f.limits.Timeout = time.Millisecond
				source = extensionWaitReader{}
				want = context.DeadlineExceeded
			}
			got, err := VerifyExtension(ctx, source, f.config.Key, f.previous, f.candidate, f.limits)
			if !errors.Is(err, want) || got != (ExtensionSummary{}) {
				t.Fatalf("invalid input returned a result: %v want %v", err, want)
			}
			if strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("raw reader error escaped")
			}
		})
	}
}

func TestVerifyExtensionRequiresExplicitConfiguration(t *testing.T) {
	for _, name := range []string{"nil-context", "nil-reader", "key-size", "bytes", "events", "timeout", "bytes-ceiling", "events-ceiling", "timeout-ceiling", "previous-version", "candidate-version", "invalid-scope", "invalid-journal", "invalid-mac"} {
		t.Run(name, func(t *testing.T) {
			f := newExtensionFixture(t, 0)
			ctx := context.Background()
			var source io.Reader = bytes.NewReader(f.data)
			switch name {
			case "nil-context":
				ctx = nil
			case "nil-reader":
				source = nil
			case "key-size":
				f.config.Key = f.config.Key[:KeyBytes-1]
			case "bytes":
				f.limits.MaxBytes = 0
			case "events":
				f.limits.MaxEvents = 0
			case "timeout":
				f.limits.Timeout = 0
			case "bytes-ceiling":
				f.limits.MaxBytes = MaxExtensionBytes + 1
			case "events-ceiling":
				f.limits.MaxEvents = MaxExtensionEvents + 1
			case "timeout-ceiling":
				f.limits.Timeout = MaxExtensionTimeout + 1
			case "previous-version":
				f.previous.Version++
			case "candidate-version":
				f.candidate.Version++
			case "invalid-scope":
				f.previous.Scope.PolicyRef = ""
			case "invalid-journal":
				f.candidate.JournalID = "bad"
			case "invalid-mac":
				f.previous.MAC = "bad"
			}
			got, err := VerifyExtension(ctx, source, f.config.Key, f.previous, f.candidate, f.limits)
			if !errors.Is(err, ErrConfig) || got != (ExtensionSummary{}) {
				t.Fatalf("invalid configuration accepted: %v", err)
			}
		})
	}
}

func TestVerifyExtensionSnapshotsOnceAndDoesNotChangeSource(t *testing.T) {
	f := newExtensionFixture(t, 2)
	source := &extensionCountingReader{reader: bytes.NewReader(f.data)}
	before := bytes.Clone(f.data)
	if _, err := VerifyExtension(context.Background(), source, f.config.Key, f.previous, f.candidate, f.limits); err != nil {
		t.Fatal(err)
	}
	if source.bytes != len(f.data) || !bytes.Equal(before, f.data) {
		t.Fatal("source was reread or modified")
	}
	actual, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, before) {
		t.Fatal("live journal was modified")
	}
}

type extensionFaultReader struct{ err error }

func (r extensionFaultReader) Read([]byte) (int, error) { return 0, r.err }

type extensionEmptyReader struct{}

func (extensionEmptyReader) Read([]byte) (int, error) { return 0, nil }

type extensionWaitReader struct{}

func (extensionWaitReader) Read([]byte) (int, error) {
	time.Sleep(5 * time.Millisecond)
	return 0, io.EOF
}

type extensionCancelReader struct {
	reader io.Reader
	cancel context.CancelFunc
}

func (r *extensionCancelReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.cancel()
	return n, err
}

type extensionCountingReader struct {
	reader io.Reader
	bytes  int
}

func (r *extensionCountingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.bytes += n
	return n, err
}

func TestVerifyExtensionHeaderOnlyExactRetry(t *testing.T) {
	f := newExtensionFixture(t, 0)
	header := f.data[:bytes.IndexByte(f.data, '\n')+1]
	f.limits.MaxBytes = int64(len(header))
	got, err := VerifyExtension(context.Background(), bytes.NewReader(header), f.config.Key, f.previous, f.previous, f.limits)
	if err != nil || got.Current.Checkpoint != f.previous || got.Current.Intents != 0 || got.Current.Results != 0 {
		t.Fatalf("header-only exact retry rejected: %v", err)
	}
}

func TestVerifyExtensionOwnsSnapshotAfterSourceChanges(t *testing.T) {
	f := newExtensionFixture(t, 2)
	source := &extensionChangingReader{data: bytes.Clone(f.data)}
	got, err := VerifyExtension(context.Background(), source, f.config.Key, f.previous, f.candidate, f.limits)
	if err != nil || got.Current.Checkpoint != f.candidate || !source.changed {
		t.Fatalf("verification did not use one owned snapshot: %v", err)
	}
}

type extensionChangingReader struct {
	data    []byte
	offset  int
	changed bool
}

func (r *extensionChangingReader) Read(p []byte) (int, error) {
	if r.offset == len(r.data) {
		clear(r.data)
		r.changed = true
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}
