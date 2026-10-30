package auditlog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

const (
	auditExternalName   = "run.ndjson"
	auditExternalKeyRef = "audit-key-1"
)

func auditExternalScope() transmit.Scope {
	return transmit.Scope{RunRef: "run-1", TaskRef: "task-1", CaseRef: "case-1", ResetRef: "reset-1", PolicyRef: "policy-1"}
}

func auditExternalKey() []byte { return bytes.Repeat([]byte{0x42}, auditlog.KeyBytes) }

func auditExternalConfig() auditlog.Config {
	return auditlog.Config{Scope: auditExternalScope(), KeyRef: auditExternalKeyRef, Key: auditExternalKey()}
}

func auditExternalIntent(scope transmit.Scope, attempt uint64) transmit.Event {
	return transmit.Event{Stage: "intent", Receipt: transmit.Receipt{
		Scope: scope, Attempt: attempt, AccountRef: "owner", SessionRef: "session-a",
		ResourceRef: "doc:one", ApprovalRef: "approval-1", Method: "GET", At: time.Now().UTC(),
	}}
}

func auditExternalStopped(scope transmit.Scope, attempt uint64) transmit.Event {
	return transmit.Event{Stage: "result", Receipt: transmit.Receipt{
		Scope: scope, Attempt: attempt, AccountRef: "owner", SessionRef: "session-a",
		ResourceRef: "doc:one", ApprovalRef: "approval-1", Method: "GET", Reason: transmit.Stopped,
		At: time.Now().UTC(),
	}}
}

func auditExternalCompleted(scope transmit.Scope, attempt uint64) transmit.Event {
	started := time.Now().UTC()
	return transmit.Event{Stage: "result", Receipt: transmit.Receipt{
		Scope: scope, Attempt: attempt, AccountRef: "owner", SessionRef: "session-a",
		ResourceRef: "doc:one", ApprovalRef: "approval-1", Method: "GET", Reason: transmit.Completed,
		Admitted: true, AdmittedAt: started, RequestWritten: true, ResponseReceived: true,
		StatusCode: http.StatusOK, At: started.Add(time.Millisecond),
	}}
}

func auditExternalCreate(t *testing.T) (*auditlog.Journal, string, auditlog.Checkpoint) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	j, err := auditlog.Create(dir, auditExternalName, auditExternalConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j, filepath.Join(dir, auditExternalName), j.Checkpoint()
}

func auditExternalRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func auditExternalWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestJournalExternalWritesAndVerifiesSafeEvents(t *testing.T) {
	j, path, initial := auditExternalCreate(t)
	if initial.Sequence != 0 || initial.JournalID == "" || initial.MAC == "" {
		t.Fatalf("initial checkpoint = %+v", initial)
	}
	if err := j.Record(context.Background(), auditExternalIntent(initial.Scope, 1)); err != nil {
		t.Fatal(err)
	}
	if err := j.Record(context.Background(), auditExternalCompleted(initial.Scope, 1)); err != nil {
		t.Fatal(err)
	}
	want := j.Checkpoint()
	if want.Sequence != 2 {
		t.Fatalf("checkpoint sequence = %d, want 2", want.Sequence)
	}
	data := auditExternalRead(t, path)
	if len(data) == 0 || !bytes.HasSuffix(data, []byte{'\n'}) {
		t.Fatal("journal is not newline framed")
	}
	if bytes.Contains(data, auditExternalKey()) || bytes.Contains(data, []byte("PRIVATE")) {
		t.Fatal("secret material escaped into journal")
	}
	var summary auditlog.Summary
	var err error
	if summary, err = auditlog.Verify(context.Background(), bytes.NewReader(data), auditExternalKey(), want); err != nil {
		t.Fatal(err)
	}
	if summary.Intents != 1 || summary.Results != 1 || summary.Checkpoint != want {
		t.Fatalf("summary = %+v, want one intent/result and checkpoint %+v", summary, want)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := j.Record(context.Background(), auditExternalIntent(initial.Scope, 2)); !errors.Is(err, auditlog.ErrClosed) {
		t.Fatalf("record after close = %v, want ErrClosed", err)
	}
}

func TestJournalExternalRejectsUnsafeCreationAndConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := auditlog.Create(dir, auditExternalName, auditExternalConfig()); !errors.Is(err, auditlog.ErrConfig) {
		t.Fatalf("directory mode accepted: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../escape", "nested/file", "run.ndjson/", ".", ""} {
		if _, err := auditlog.Create(dir, name, auditExternalConfig()); !errors.Is(err, auditlog.ErrConfig) {
			t.Errorf("unsafe name %q error = %v, want ErrConfig", name, err)
		}
	}
	path := filepath.Join(dir, auditExternalName)
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := auditlog.Create(dir, auditExternalName, auditExternalConfig()); !errors.Is(err, auditlog.ErrExists) {
		t.Fatalf("existing file error = %v, want ErrExists", err)
	}
	sym := filepath.Join(dir, "link.ndjson")
	if err := os.Symlink(path, sym); err != nil {
		t.Fatal(err)
	}
	if _, err := auditlog.Create(dir, "link.ndjson", auditExternalConfig()); !errors.Is(err, auditlog.ErrExists) {
		t.Fatalf("symlink error = %v, want ErrExists", err)
	}
	bad := auditExternalConfig()
	bad.Key = bad.Key[:auditlog.KeyBytes-1]
	if _, err := auditlog.Create(dir, "bad-key.ndjson", bad); !errors.Is(err, auditlog.ErrConfig) {
		t.Fatalf("short key error = %v", err)
	}
	bad = auditExternalConfig()
	bad.Scope.RunRef = ""
	if _, err := auditlog.Create(dir, "bad-scope.ndjson", bad); !errors.Is(err, auditlog.ErrConfig) {
		t.Fatalf("bad scope error = %v", err)
	}
}

func TestJournalExternalConcurrentRecordsRemainVerifiable(t *testing.T) {
	j, path, initial := auditExternalCreate(t)
	const count = 48
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(attempt uint64) {
			defer wg.Done()
			if err := j.Record(context.Background(), auditExternalStopped(initial.Scope, attempt)); err != nil {
				t.Errorf("record %d: %v", attempt, err)
			}
		}(uint64(i + 1))
	}
	wg.Wait()
	want := j.Checkpoint()
	if want.Sequence != count {
		t.Fatalf("sequence = %d, want %d", want.Sequence, count)
	}
	data := auditExternalRead(t, path)
	summary, err := auditlog.Verify(context.Background(), bytes.NewReader(data), auditExternalKey(), want)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Results != count || summary.Intents != 0 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestJournalExternalRejectsInvalidEventsAndCanceledContext(t *testing.T) {
	j, path, initial := auditExternalCreate(t)
	before := auditExternalRead(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := j.Record(ctx, auditExternalIntent(initial.Scope, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled record = %v", err)
	}
	invalid := auditExternalIntent(initial.Scope, 1)
	invalid.Reason = transmit.Reason("raw-secret-reason")
	if err := j.Record(context.Background(), invalid); !errors.Is(err, auditlog.ErrEvent) {
		t.Fatalf("invalid reason = %v", err)
	}
	invalid = auditExternalIntent(initial.Scope, 1)
	invalid.AccountRef = "secret\nvalue"
	if err := j.Record(context.Background(), invalid); !errors.Is(err, auditlog.ErrEvent) {
		t.Fatalf("invalid ref = %v", err)
	}
	invalid = auditExternalCompleted(initial.Scope, 1)
	invalid.StatusCode = 0
	if err := j.Record(context.Background(), invalid); !errors.Is(err, auditlog.ErrEvent) {
		t.Fatalf("invalid status = %v", err)
	}
	invalid = auditExternalIntent(initial.Scope, 1)
	invalid.ResourceRef = "https://PRIVATE.example/path"
	if err := j.Record(context.Background(), invalid); !errors.Is(err, auditlog.ErrEvent) || bytes.Contains([]byte(err.Error()), []byte("PRIVATE")) {
		t.Fatalf("literal URL reference accepted/leaked: %v", err)
	}
	if !bytes.Equal(auditExternalRead(t, path), before) || j.Checkpoint() != initial {
		t.Fatal("canceled/invalid records changed journal")
	}
}

func TestJournalExternalDetectsTamperingAndWrongCheckpoints(t *testing.T) {
	mutations := []struct {
		name string
		edit func([]byte) []byte
	}{
		{"byte", func(data []byte) []byte { data[len(data)-2] ^= 1; return data }},
		{"valid-JSON-event-change", func(data []byte) []byte { return bytes.Replace(data, []byte(`"Attempt":1`), []byte(`"Attempt":2`), 1) }},
		{"truncate", func(data []byte) []byte { return data[:len(data)-2] }},
		{"whole-tail-rollback", func(data []byte) []byte {
			lines := bytes.SplitAfter(data, []byte{'\n'})
			return bytes.Join(lines[:len(lines)-2], nil)
		}},
		{"middle-delete", func(data []byte) []byte {
			lines := bytes.SplitAfter(data, []byte{'\n'})
			return append(lines[0], bytes.Join(lines[2:], nil)...)
		}},
		{"duplicate-event", func(data []byte) []byte {
			lines := bytes.SplitAfter(data, []byte{'\n'})
			return bytes.Join(append(lines[:2:2], lines[1:]...), nil)
		}},
		{"extra-line", func(data []byte) []byte { return append(data, []byte("{}\n")...) }},
		{"reorder", func(data []byte) []byte {
			lines := bytes.Split(data, []byte{'\n'})
			if len(lines) > 3 {
				lines[1], lines[2] = lines[2], lines[1]
			}
			return bytes.Join(lines, []byte{'\n'})
		}},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			j, path, initial := auditExternalCreate(t)
			if err := j.Record(context.Background(), auditExternalIntent(initial.Scope, 1)); err != nil {
				t.Fatal(err)
			}
			if err := j.Record(context.Background(), auditExternalStopped(initial.Scope, 1)); err != nil {
				t.Fatal(err)
			}
			want := j.Checkpoint()
			data := tc.edit(auditExternalRead(t, path))
			if summary, err := auditlog.Verify(context.Background(), bytes.NewReader(data), auditExternalKey(), want); err == nil || summary != (auditlog.Summary{}) {
				t.Fatalf("tampering accepted: summary=%+v err=%v", summary, err)
			}
		})
	}
	j, path, initial := auditExternalCreate(t)
	if err := j.Record(context.Background(), auditExternalIntent(initial.Scope, 1)); err != nil {
		t.Fatal(err)
	}
	want := j.Checkpoint()
	data := auditExternalRead(t, path)
	wrong := want
	wrong.Sequence++
	if summary, err := auditlog.Verify(context.Background(), bytes.NewReader(data), auditExternalKey(), wrong); err == nil || summary != (auditlog.Summary{}) {
		t.Fatalf("wrong checkpoint accepted: %+v %v", summary, err)
	}
	wrongKey := auditExternalKey()
	wrongKey[0]++
	if summary, err := auditlog.Verify(context.Background(), bytes.NewReader(data), wrongKey, want); err == nil || summary != (auditlog.Summary{}) {
		t.Fatalf("wrong key accepted: %+v %v", summary, err)
	}
}

func TestJournalExternalVerifyRejectsCrossScopeAndBoundedInput(t *testing.T) {
	j, path, initial := auditExternalCreate(t)
	if err := j.Record(context.Background(), auditExternalIntent(initial.Scope, 1)); err != nil {
		t.Fatal(err)
	}
	want := j.Checkpoint()
	data := auditExternalRead(t, path)
	wrongScope := want
	wrongScope.Scope.CaseRef = "other-case"
	if summary, err := auditlog.Verify(context.Background(), bytes.NewReader(data), auditExternalKey(), wrongScope); err == nil || summary != (auditlog.Summary{}) {
		t.Fatalf("cross-scope accepted: %+v %v", summary, err)
	}
	for _, malformed := range [][]byte{
		[]byte("not-json\n"),
		bytes.Replace(data, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1),
		bytes.Replace(data, []byte(`"version":1`), []byte(`"version":1,"unknown":0`), 1),
		bytes.Replace(data, []byte{'\n'}, []byte(" {}\n"), 1),
		append([]byte{0xff}, data...),
		append(append([]byte(nil), data...), bytes.Repeat([]byte{'x'}, auditlog.MaxRecordBytes+1)...),
		append(append([]byte(nil), data...), []byte("\n")...),
	} {
		if summary, err := auditlog.Verify(context.Background(), bytes.NewReader(malformed), auditExternalKey(), want); err == nil || summary != (auditlog.Summary{}) {
			t.Fatalf("malformed input accepted: %+v %v", summary, err)
		}
	}
}

func TestJournalExternalConnectsToHTTPTransmitter(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	scope := auditExternalScope()
	j, err := auditlog.Create(dir, auditExternalName, auditlog.Config{Scope: scope, KeyRef: auditExternalKeyRef, Key: auditExternalKey()})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	expiry := time.Now().Add(time.Minute)
	baseURL := server.URL
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	u, err := netip.ParseAddr(parsedURL.Hostname())
	if err != nil {
		t.Fatal(err)
	}
	cfg := transmit.Config{
		Scope: scope, Origin: baseURL, ConnectIP: u, ExpiresAt: expiry,
		Sessions:    []transmit.Session{{Account: idor.AccountRef{Label: "owner", Handle: "session-a", Status: oracle.SubjectVerified}, ExpiresAt: expiry}},
		Resources:   []transmit.Resource{{Ref: "doc:one", URL: baseURL + "/doc", ApprovalRef: "approval-1", ReadOnly: true, Accounts: []string{"owner"}}},
		MaxRequests: 1, RequestTimeout: time.Second, MaxBodyBytes: 1024, Recorder: j,
	}
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, receipt, ok := tx.SendDetailed(idor.AccessRequest{As: cfg.Sessions[0].Account, Method: http.MethodGet, ResourceRef: "doc:one"}); !ok || receipt.Reason != transmit.Completed {
		t.Fatalf("send = ok %v receipt %+v", ok, receipt)
	}
	if hits.Load() != 1 {
		t.Fatalf("target hits = %d, want 1", hits.Load())
	}
	file := auditExternalRead(t, filepath.Join(dir, auditExternalName))
	if summary, err := auditlog.Verify(context.Background(), bytes.NewReader(file), auditExternalKey(), j.Checkpoint()); err != nil || summary.Intents != 1 || summary.Results != 1 {
		t.Fatalf("real transmission audit incomplete: summary=%+v err=%v", summary, err)
	}
	tx.Stop()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	blocked, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Stop()
	if _, receipt, ok := blocked.SendDetailed(idor.AccessRequest{As: cfg.Sessions[0].Account, Method: http.MethodGet, ResourceRef: "doc:one"}); ok || receipt.Admitted || receipt.Reason != transmit.AuditUnavailable || hits.Load() != 1 {
		t.Fatalf("closed recorder allowed transmission: ok=%v receipt=%+v hits=%d", ok, receipt, hits.Load())
	}
}

func TestJournalExternalKeyAndContentNeverAppearInJSONSummary(t *testing.T) {
	j, path, initial := auditExternalCreate(t)
	if err := j.Record(context.Background(), auditExternalStopped(initial.Scope, 1)); err != nil {
		t.Fatal(err)
	}
	want := j.Checkpoint()
	data := auditExternalRead(t, path)
	summary, err := auditlog.Verify(context.Background(), bytes.NewReader(data), auditExternalKey(), want)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, auditExternalKey()) || bytes.Contains(encoded, []byte("PRIVATE")) {
		t.Fatal("summary contains secret material")
	}
}

func TestJournalExternalCopiesConfigAndRejectsSameKeyJournalSwap(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := auditExternalConfig()
	originalScope := cfg.Scope
	j, err := auditlog.Create(dir, "first.ndjson", cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	cfg.Key[0]++
	cfg.Scope.ResetRef = "changed"
	if err := j.Record(context.Background(), auditExternalStopped(originalScope, 1)); err != nil {
		t.Fatal(err)
	}
	data := auditExternalRead(t, filepath.Join(dir, "first.ndjson"))
	if _, err := auditlog.Verify(context.Background(), bytes.NewReader(data), auditExternalKey(), j.Checkpoint()); err != nil {
		t.Fatalf("caller mutated live config/key: %v", err)
	}
	other, err := auditlog.Create(dir, "second.ndjson", auditExternalConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.Record(context.Background(), auditExternalStopped(originalScope, 1)); err != nil {
		t.Fatal(err)
	}
	if summary, err := auditlog.Verify(context.Background(), bytes.NewReader(data), auditExternalKey(), other.Checkpoint()); err == nil || summary != (auditlog.Summary{}) {
		t.Fatalf("same-key journal swap accepted: %+v %v", summary, err)
	}
}

func TestJournalExternalRejectsUppercaseMACEncoding(t *testing.T) {
	for _, position := range []string{"header", "final-event"} {
		t.Run(position, func(t *testing.T) {
			j, path, initial := auditExternalCreate(t)
			if err := j.Record(context.Background(), auditExternalIntent(initial.Scope, 1)); err != nil {
				t.Fatal(err)
			}
			lines := bytes.SplitAfter(auditExternalRead(t, path), []byte{'\n'})
			index := 0
			if position == "final-event" {
				index = 1
			}
			prefix := []byte(`"mac":"`)
			start := bytes.Index(lines[index], prefix) + len(prefix)
			if start < len(prefix) || len(lines[index]) < start+64 {
				t.Fatal("fixture MAC missing")
			}
			mac := lines[index][start : start+64]
			upper := bytes.ToUpper(mac)
			if bytes.Equal(mac, upper) {
				t.Fatal("fixture MAC contains no hex letters")
			}
			copy(mac, upper)
			data := bytes.Join(lines, nil)
			if summary, err := auditlog.Verify(context.Background(), bytes.NewReader(data), auditExternalKey(), j.Checkpoint()); err == nil || summary != (auditlog.Summary{}) {
				t.Fatalf("noncanonical MAC accepted: summary=%+v err=%v", summary, err)
			}
		})
	}
}
