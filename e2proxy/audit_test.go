package e2proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit"
)

var journalTestKey = bytes.Repeat([]byte{0x5a}, 32)

func journalTestEvent() Event {
	return Event{Index: 1, Stage: "result", Receipt: Receipt{
		Version: JournalVersion, IntentID: 22, Scope: transmit.Scope{TaskRef: "task-1", RunRef: "run-1", CaseRef: "case-1", ResetRef: "reset-1", PolicyRef: "policy-1"},
		PolicyRevision: "revision-1", CallRef: "call-1", CommandRef: "command-1", RuleRef: "rule-1",
		AttemptID: "0123456789abcdef0123456789abcdef", Sequence: 1, Method: "GET",
		Decision: "allowed", Reason: Reason("complete"), ForwardAttempted: true, RequestWritten: true,
		ResponseReceived: true, ResponseComplete: true, StatusCode: 200, AuditComplete: true,
		At: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)}}
}

func journalTestDirectory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestJournalDurableRoundTrip(t *testing.T) {
	dir := journalTestDirectory(t)
	key := bytes.Clone(journalTestKey)
	j, err := CreateJournal(dir, "run.journal", key)
	if err != nil {
		t.Fatal(err)
	}
	clear(key)
	header := j.Checkpoint()
	if header.Sequence != 0 || header.Version != JournalVersion {
		t.Fatalf("header = %#v", header)
	}
	if err := j.Record(context.Background(), journalTestEvent()); err != nil {
		t.Fatal(err)
	}
	head := j.Checkpoint()
	if head.Sequence != 1 || head.Scope.RunRef != "run-1" {
		t.Fatalf("head = %#v", head)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := j.Record(context.Background(), journalTestEvent()); !errors.Is(err, ErrJournalClosed) {
		t.Fatalf("record after close: %v", err)
	}
	file, err := os.Open(filepath.Join(dir, "run.journal"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	summary, err := VerifyJournal(context.Background(), file, journalTestKey, head)
	if err != nil || summary.Events != 1 || summary.Checkpoint != head {
		t.Fatalf("verify: %#v %v", summary, err)
	}
	info, err := file.Stat()
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode: %#v %v", info, err)
	}
	if !bytes.Equal(j.key, make([]byte, len(j.key))) {
		t.Fatal("closed journal retains key")
	}
}

func TestJournalCreateRejectsPathsAndPermissions(t *testing.T) {
	dir := journalTestDirectory(t)
	for _, name := range []string{"", "../outside", "/absolute", "nested/file", ".", "..", "token\nvalue"} {
		t.Run(name, func(t *testing.T) {
			if _, err := CreateJournal(dir, name, journalTestKey); !errors.Is(err, ErrJournalConfig) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	j, err := CreateJournal(dir, "existing", journalTestKey)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err := CreateJournal(dir, "existing", journalTestKey); !errors.Is(err, ErrJournalExists) {
		t.Fatalf("existing: %v", err)
	}
	if err := os.Symlink("existing", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateJournal(dir, "link", journalTestKey); !errors.Is(err, ErrJournalExists) {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := CreateJournal(dir, "short-key", []byte("short")); !errors.Is(err, ErrJournalConfig) {
		t.Fatalf("key: %v", err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateJournal(dir, "public-dir", journalTestKey); !errors.Is(err, ErrJournalConfig) {
		t.Fatalf("permissions: %v", err)
	}
}

func TestJournalScopeCannotChange(t *testing.T) {
	w := newJournalTestWriter()
	j, err := newJournal(w, journalTestKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.Record(context.Background(), journalTestEvent()); err != nil {
		t.Fatal(err)
	}
	before := j.Checkpoint()
	event := journalTestEvent()
	event.Index = 2
	event.Scope.RunRef = "other-run"
	if err := j.Record(context.Background(), event); !errors.Is(err, ErrJournalEvent) {
		t.Fatalf("scope changed: %v", err)
	}
	if j.Checkpoint() != before {
		t.Fatal("rejected event changed checkpoint")
	}
}

func TestJournalRejectsUntrustedEventFields(t *testing.T) {
	cases := map[string]func(*Event){
		"missing index":          func(e *Event) { e.Index = 0 },
		"intent with response":   func(e *Event) { e.Stage = "intent" },
		"missing allowed rule":   func(e *Event) { e.RuleRef = "" },
		"version":                func(e *Event) { e.Version = "registered-get" },
		"method":                 func(e *Event) { e.Method = "GET\r\nAuthorization" },
		"raw url":                func(e *Event) { e.RuleRef = "https://secret.invalid/path" },
		"raw error":              func(e *Event) { e.Reason = Reason("transport: secret-password") },
		"attempt":                func(e *Event) { e.AttemptID = "caller-selected" },
		"contradictory not sent": func(e *Event) { e.NotSent = true },
		"response without send":  func(e *Event) { e.ForwardAttempted = false },
		"unknown stage":          func(e *Event) { e.Stage = "raw_body" },
		"missing policy":         func(e *Event) { e.PolicyRevision = "" },
		"missing time":           func(e *Event) { e.At = time.Time{} },
		"old version on write":   func(e *Event) { e.Version = VersionV1 },
		"withheld complete":      func(e *Event) { e.Reason = Withheld },
		"withheld not received": func(e *Event) {
			e.Reason, e.ResponseComplete, e.ResponseReceived, e.StatusCode = Withheld, false, false, 0
		},
		"withheld intent stage": func(e *Event) {
			e.Stage, e.Reason, e.ResponseComplete, e.ResponseReceived, e.StatusCode = "intent", Withheld, false, false, 0
			e.ForwardAttempted, e.RequestWritten = false, false
		},
		"truncated without withheld": func(e *Event) { e.ResponseTruncated, e.ResponseComplete = true, false },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			w := newJournalTestWriter()
			j, err := newJournal(w, journalTestKey, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			event := journalTestEvent()
			change(&event)
			before := w.length()
			if err := j.Record(context.Background(), event); !errors.Is(err, ErrJournalEvent) {
				t.Fatalf("error = %v", err)
			}
			if w.length() != before || j.Checkpoint().Sequence != 0 {
				t.Fatal("invalid event performed IO")
			}
		})
	}
}

func TestJournalControlAndCallClose(t *testing.T) {
	w := newJournalTestWriter()
	j, err := newJournal(w, journalTestKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	for _, stage := range []string{"control", "call_closed"} {
		e := journalTestEvent()
		e.Index = j.Checkpoint().Sequence + 1
		e.Stage = stage
		e.AttemptID = ""
		e.Sequence = 0
		e.Decision = ""
		e.Method = ""
		e.RuleRef = ""
		e.Reason = Reason("closed")
		e.ForwardAttempted = false
		e.RequestWritten = false
		e.ResponseReceived = false
		e.ResponseComplete = false
		e.StatusCode = 0
		if stage == "control" {
			e.CallRef = ""
			e.CommandRef = ""
		}
		if err := j.Record(context.Background(), e); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}
	if _, err := VerifyJournal(context.Background(), bytes.NewReader(w.bytes()), journalTestKey, j.Checkpoint()); err != nil {
		t.Fatal(err)
	}
}

func TestJournalVerificationRejectsTamperAndRollback(t *testing.T) {
	w := newJournalTestWriter()
	j, err := newJournal(w, journalTestKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.Record(context.Background(), journalTestEvent()); err != nil {
		t.Fatal(err)
	}
	anchored := j.Checkpoint()
	data := w.bytes()
	firstEnd := bytes.IndexByte(data, '\n') + 1
	// A complete older prefix is authentic only when no external current head
	// was supplied. It must fail the exact current-head comparison.
	if _, err := VerifyJournal(context.Background(), bytes.NewReader(data[:firstEnd]), journalTestKey); err != nil {
		t.Fatalf("valid old prefix: %v", err)
	}
	tests := map[string][]byte{
		"truncated line":   data[:len(data)-1],
		"valid old prefix": data[:firstEnd],
		"wrong field":      bytes.Replace(data, []byte(`"status_code":200`), []byte(`"status_code":201`), 1),
		"unknown field":    bytes.Replace(data, []byte(`"Kind":"event"`), []byte(`"Kind":"event","Secret":"x"`), 1),
		"duplicate field":  bytes.Replace(data, []byte(`"Kind":"event"`), []byte(`"Kind":"event","Kind":"event"`), 1),
		"duplicate record": append(bytes.Clone(data), data[firstEnd:]...),
		"whitespace":       append([]byte(" "), data...),
		"trailing junk":    append(bytes.Clone(data), []byte("junk\n")...),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyJournal(context.Background(), bytes.NewReader(input), journalTestKey, anchored); !errors.Is(err, ErrJournalIntegrity) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if _, err := VerifyJournal(context.Background(), bytes.NewReader(data), bytes.Repeat([]byte{1}, 32)); !errors.Is(err, ErrJournalIntegrity) {
		t.Fatalf("wrong key: %v", err)
	}
}

func TestJournalVerificationBoundsAndCancellation(t *testing.T) {
	if _, err := VerifyJournal(context.Background(), strings.NewReader(strings.Repeat("x", journalMaxLineBytes+1)), journalTestKey); !errors.Is(err, ErrJournalLimit) {
		t.Fatalf("line limit: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := VerifyJournal(ctx, strings.NewReader(""), journalTestKey); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := VerifyJournal(nil, strings.NewReader(""), journalTestKey); !errors.Is(err, ErrJournalConfig) {
		t.Fatalf("nil context: %v", err)
	}
	if _, err := VerifyJournal(context.Background(), nil, journalTestKey); !errors.Is(err, ErrJournalConfig) {
		t.Fatalf("nil reader: %v", err)
	}
	if _, err := VerifyJournal(context.Background(), strings.NewReader(""), journalTestKey, JournalCheckpoint{}, JournalCheckpoint{}); !errors.Is(err, ErrJournalConfig) {
		t.Fatalf("multiple anchors: %v", err)
	}
}

func TestJournalPartialWritePoisonsAndRetainsCheckpoint(t *testing.T) {
	w := newJournalTestWriter()
	j, err := newJournal(w, journalTestKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	before := j.Checkpoint()
	w.short = true
	if err := j.Record(context.Background(), journalTestEvent()); !errors.Is(err, ErrJournalStorage) {
		t.Fatalf("partial: %v", err)
	}
	if j.Checkpoint() != before {
		t.Fatal("partial write acknowledged")
	}
	w.short = false
	if err := j.Record(context.Background(), journalTestEvent()); !errors.Is(err, ErrJournalFailed) {
		t.Fatalf("poisoned: %v", err)
	}
	if err := j.Close(); !errors.Is(err, ErrJournalFailed) {
		t.Fatalf("close: %v", err)
	}
	if _, err := VerifyJournal(context.Background(), bytes.NewReader(w.bytes()), journalTestKey, before); !errors.Is(err, ErrJournalIntegrity) {
		t.Fatalf("verify partial: %v", err)
	}
}

func TestJournalSyncFailurePoisonsWithoutAcknowledgement(t *testing.T) {
	w := newJournalTestWriter()
	j, err := newJournal(w, journalTestKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	before := j.Checkpoint()
	w.failSync = true
	if err := j.Record(context.Background(), journalTestEvent()); !errors.Is(err, ErrJournalStorage) {
		t.Fatalf("sync: %v", err)
	}
	if j.Checkpoint() != before {
		t.Fatal("failed sync advanced checkpoint")
	}
	if err := j.Close(); !errors.Is(err, ErrJournalFailed) {
		t.Fatalf("close: %v", err)
	}
}

func TestJournalCanceledAfterSyncIsIncomplete(t *testing.T) {
	w := newJournalTestWriter()
	j, err := newJournal(w, journalTestKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.afterSync = cancel
	if err := j.Record(ctx, journalTestEvent()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled after sync: %v", err)
	}
	if j.Checkpoint().Sequence != 1 {
		t.Fatal("last confirmed prefix not preserved")
	}
	if err := j.Record(context.Background(), journalTestEvent()); !errors.Is(err, ErrJournalFailed) {
		t.Fatalf("poison after cancellation: %v", err)
	}
	if err := j.Close(); !errors.Is(err, ErrJournalFailed) {
		t.Fatalf("close: %v", err)
	}
}

func TestJournalCancellationBeforeWriteLeavesUsableJournal(t *testing.T) {
	w := newJournalTestWriter()
	j, err := newJournal(w, journalTestKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := w.length()
	if err := j.Record(ctx, journalTestEvent()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if w.length() != before || j.Checkpoint().Sequence != 0 {
		t.Fatal("canceled record performed IO")
	}
	if err := j.Record(context.Background(), journalTestEvent()); err != nil {
		t.Fatal(err)
	}
}

func TestJournalConcurrentRecordAndClose(t *testing.T) {
	w := newJournalTestWriter()
	j, err := newJournal(w, journalTestKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	var order sync.Mutex
	results := make(chan error, 20)
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			order.Lock()
			event := journalTestEvent()
			event.Index = j.Checkpoint().Sequence + 1
			results <- j.Record(context.Background(), event)
			order.Unlock()
		}()
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var closers sync.WaitGroup
	for i := 0; i < 3; i++ {
		closers.Add(1)
		go func() {
			defer closers.Done()
			if err := j.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	closers.Wait()
	if w.closed != 1 {
		t.Fatalf("close count = %d", w.closed)
	}
	head := j.Checkpoint()
	if head.Sequence != 20 {
		t.Fatalf("sequence = %d", head.Sequence)
	}
	summary, err := VerifyJournal(context.Background(), bytes.NewReader(w.bytes()), journalTestKey, head)
	if err != nil || summary.Events != 20 {
		t.Fatalf("verify: %#v %v", summary, err)
	}
}

func TestJournalCloseErrorIsStable(t *testing.T) {
	w := newJournalTestWriter()
	j, err := newJournal(w, journalTestKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	w.failClose = true
	if err := j.Close(); !errors.Is(err, ErrJournalStorage) {
		t.Fatalf("first: %v", err)
	}
	if err := j.Close(); !errors.Is(err, ErrJournalStorage) {
		t.Fatalf("second: %v", err)
	}
	if w.closed != 1 {
		t.Fatalf("close count = %d", w.closed)
	}
}

func TestJournalCreateInitializationFailureClosesWriter(t *testing.T) {
	for _, kind := range []string{"write", "sync"} {
		t.Run(kind, func(t *testing.T) {
			w := newJournalTestWriter()
			w.short = kind == "write"
			w.failSync = kind == "sync"
			if _, err := newJournal(w, journalTestKey, time.Now().UTC()); !errors.Is(err, ErrJournalStorage) {
				t.Fatalf("error = %v", err)
			}
			if w.closed != 1 {
				t.Fatalf("close count = %d", w.closed)
			}
		})
	}
}

type journalTestWriter struct {
	mu                         sync.Mutex
	data                       bytes.Buffer
	short, failSync, failClose bool
	afterSync                  func()
	closed                     int
}

func newJournalTestWriter() *journalTestWriter { return &journalTestWriter{} }
func (w *journalTestWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.short {
		n := len(data) / 2
		w.data.Write(data[:n])
		return n, io.ErrShortWrite
	}
	return w.data.Write(data)
}
func (w *journalTestWriter) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.afterSync != nil {
		w.afterSync()
	}
	if w.failSync {
		return errors.New("synthetic sync error with secret value")
	}
	return nil
}
func (w *journalTestWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed++
	if w.failClose {
		return errors.New("synthetic close error with secret value")
	}
	return nil
}
func (w *journalTestWriter) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return bytes.Clone(w.data.Bytes())
}
func (w *journalTestWriter) length() int { w.mu.Lock(); defer w.mu.Unlock(); return w.data.Len() }

func TestJournalVerifierRejectsBrokenReaders(t *testing.T) {
	for _, kind := range []int{-1, 1, 0} {
		if _, err := VerifyJournal(context.Background(), brokenJournalReader(kind), journalTestKey); !errors.Is(err, ErrJournalIntegrity) {
			t.Fatalf("broken reader %d: %v", kind, err)
		}
	}
}

type brokenJournalReader int

func (r brokenJournalReader) Read(p []byte) (int, error) {
	if r < 0 {
		return -1, nil
	}
	if r > 0 {
		return len(p) + 1, nil
	}
	return 0, nil
}

func TestJournalRejectsOrdinalGapsAndLimitBeforeIO(t *testing.T) {
	for _, tc := range []struct{ sequence, index uint64 }{{0, 2}, {journalMaxEvents, journalMaxEvents + 1}} {
		w := newJournalTestWriter()
		j, err := newJournal(w, journalTestKey, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		j.head.Sequence = tc.sequence
		j.head.Scope = journalTestEvent().Scope
		event := journalTestEvent()
		event.Index = tc.index
		before := w.length()
		if err := j.Record(context.Background(), event); !errors.Is(err, ErrJournalEvent) {
			t.Fatalf("sequence %d index %d: %v", tc.sequence, tc.index, err)
		}
		if w.length() != before {
			t.Fatal("rejected ordinal performed IO")
		}
		j.Close()
	}
}

func TestJournalDiagnosticFormattingDoesNotExposeKey(t *testing.T) {
	w := newJournalTestWriter()
	j, err := newJournal(w, journalTestKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	for _, output := range []string{fmt.Sprint(j), fmt.Sprintf("%+v", j), fmt.Sprintf("%#v", j)} {
		if output != "e2proxy.Journal{private}" {
			t.Fatal("private journal was expanded by formatter")
		}
	}
}

func TestJournalMultipleActualIntentsShareExecutionScope(t *testing.T) {
	w := newJournalTestWriter()
	j, err := newJournal(w, journalTestKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	first := journalTestEvent()
	if err := j.Record(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Index = 2
	second.IntentID = 23
	second.Sequence = 2
	second.AttemptID = "11111111111111111111111111111111"
	if err := j.Record(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if summary, err := VerifyJournal(context.Background(), bytes.NewReader(w.bytes()), journalTestKey, j.Checkpoint()); err != nil || summary.Events != 2 {
		t.Fatalf("events=%d error=%v", summary.Events, err)
	}
}
func TestJournalCallEventsRequireActualIntent(t *testing.T) {
	for _, intentID := range []int64{0, -1} {
		for _, stage := range []string{"intent", "result", "call_closed"} {
			w := newJournalTestWriter()
			j, err := newJournal(w, journalTestKey, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			event := journalTestEvent()
			event.IntentID = intentID
			event.Stage = stage
			if stage == "intent" {
				event.ForwardAttempted = false
				event.RequestWritten = false
				event.ResponseReceived = false
				event.ResponseComplete = false
				event.StatusCode = 0
				event.NotSent = true
			}
			if stage == "call_closed" {
				event.AttemptID = ""
				event.Sequence = 0
				event.Decision = ""
				event.ForwardAttempted = false
				event.RequestWritten = false
				event.ResponseReceived = false
				event.ResponseComplete = false
				event.StatusCode = 0
				event.NotSent = true
			}
			if err := j.Record(context.Background(), event); !errors.Is(err, ErrJournalEvent) {
				t.Fatalf("stage=%s intent=%d error=%v", stage, intentID, err)
			}
			if j.Checkpoint().Sequence != 0 {
				t.Fatal("unbound intent changed journal head")
			}
			j.Close()
		}
	}
}

// Both inputs are independently authentic under the same key. A record from a
// different journal must still fail its predecessor binding, without an anchor
// or a bad ordinal masking that check.
func TestJournalVerificationRejectsSameKeyChainSplice(t *testing.T) {
	makeJournal := func() ([]byte, JournalCheckpoint) {
		t.Helper()
		w := newJournalTestWriter()
		j, err := newJournal(w, journalTestKey, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = j.Close() })
		if err := j.Record(context.Background(), journalTestEvent()); err != nil {
			t.Fatal(err)
		}
		data, head := w.bytes(), j.Checkpoint()
		if _, err := VerifyJournal(context.Background(), bytes.NewReader(data), journalTestKey, head); err != nil {
			t.Fatalf("positive journal: %v", err)
		}
		return data, head
	}
	first, firstHead := makeJournal()
	second, secondHead := makeJournal()
	if firstHead.JournalID == secondHead.JournalID {
		t.Fatal("journal IDs unexpectedly match")
	}
	firstEnd := bytes.IndexByte(first, '\n') + 1
	secondEnd := bytes.IndexByte(second, '\n') + 1
	spliced := append(bytes.Clone(first[:firstEnd]), second[secondEnd:]...)
	if _, err := VerifyJournal(context.Background(), bytes.NewReader(spliced), journalTestKey); !errors.Is(err, ErrJournalIntegrity) {
		t.Fatalf("same-key chain splice: %v", err)
	}
}

// Change a valid header field and retain a correctly shaped MAC. No event or
// expected checkpoint is present, so only header authentication can reject it.
func TestJournalVerificationAuthenticatesHeaderWithoutAnchor(t *testing.T) {
	w := newJournalTestWriter()
	j, err := newJournal(w, journalTestKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err := VerifyJournal(context.Background(), bytes.NewReader(w.bytes()), journalTestKey); err != nil {
		t.Fatalf("positive header: %v", err)
	}
	var line journalLine
	if err := json.Unmarshal(bytes.TrimSpace(w.bytes()), &line); err != nil {
		t.Fatal(err)
	}
	line.Header.CreatedAt = line.Header.CreatedAt.Add(time.Second)
	tampered, err := encodeJournalLine(line)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyJournal(context.Background(), bytes.NewReader(tampered), journalTestKey); !errors.Is(err, ErrJournalIntegrity) {
		t.Fatalf("changed authenticated header: %v", err)
	}
}

// A canceled HTTP caller must not cancel its durable intent/result evidence.
// It still consumes its admitted reservation; it does not poison future calls.
func TestJournalExecutionCallerCancellationPreservesAudit(t *testing.T) {
	w := newJournalTestWriter()
	j, err := newJournal(w, journalTestKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		_, _ = io.WriteString(w, "actual target")
	}))
	defer target.Close()
	e, c := coreOpen(t, coreConfig(t, target.URL, j, 2))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp, receipt, err := c.Do(ctx, coreRequest(t, "GET", target.URL+"/allowed"))
	if err == nil || resp != nil || errors.Is(err, ErrAudit) || !receipt.AuditComplete || receipt.Reason == AuditFailed || received.Load() != 0 {
		t.Fatalf("canceled caller response=%v receipt=%+v error=%v received=%d", resp != nil, receipt, err, received.Load())
	}
	if j.Checkpoint().Sequence != 2 || e.Snapshot().Reason == AuditFailed {
		t.Fatalf("canceled caller lost intent/result: head=%+v execution=%+v", j.Checkpoint(), e.Snapshot())
	}
	if remaining, err := e.BudgetRemaining(); err != nil || remaining != 1 {
		t.Fatalf("admitted reservation remaining=%d error=%v", remaining, err)
	}
	resp, receipt, err = c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err != nil || resp == nil || !receipt.ResponseComplete || !receipt.AuditComplete || received.Load() != 1 {
		t.Fatalf("later caller response=%v receipt=%+v error=%v received=%d", resp != nil, receipt, err, received.Load())
	}
	_ = resp.Body.Close()
	if j.Checkpoint().Sequence != 4 {
		t.Fatalf("later caller ordinal=%d", j.Checkpoint().Sequence)
	}
	if _, err := VerifyJournal(context.Background(), bytes.NewReader(w.bytes()), journalTestKey, j.Checkpoint()); err != nil {
		t.Fatalf("actual execution journal: %v", err)
	}
}

// A storage failure is different: no ACK means no committed event ordinal, no
// subsequent recorder invocation, and no successful response released to caller.
func TestJournalExecutionResultFailureLatchesWithoutOrdinalGap(t *testing.T) {
	w := newJournalTestWriter()
	j, err := newJournal(w, journalTestKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	target := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		w.failSync = true
		w.mu.Unlock()
		_, _ = io.WriteString(rw, "actual target")
	}))
	defer target.Close()
	e, c := coreOpen(t, coreConfig(t, target.URL, j, 2))
	resp, receipt, err := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed"))
	if !errors.Is(err, ErrAudit) || resp != nil || receipt.AuditComplete || !receipt.ForwardAttempted || !receipt.ResponseReceived || e.Snapshot().Reason != AuditFailed {
		t.Fatalf("result failure response=%v receipt=%+v error=%v", resp != nil, receipt, err)
	}
	if j.Checkpoint().Sequence != 1 || e.eventIndex != 1 || len(e.Events(0, 100)) != 1 {
		t.Fatalf("unacknowledged event committed: head=%+v eventIndex=%d", j.Checkpoint(), e.eventIndex)
	}
	before := w.length()
	if err := e.record(context.Background(), Event{Stage: "result", Receipt: receipt}); !errors.Is(err, ErrAudit) || w.length() != before {
		t.Fatalf("failed execution retried recorder: error=%v bytes=%d->%d", err, before, w.length())
	}
	if _, err := VerifyJournal(context.Background(), bytes.NewReader(w.bytes()), journalTestKey, j.Checkpoint()); !errors.Is(err, ErrJournalIntegrity) {
		t.Fatalf("unacknowledged tail accepted with confirmed head: %v", err)
	}
}
