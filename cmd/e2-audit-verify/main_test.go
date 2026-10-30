//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

var verifyTestKey = bytes.Repeat([]byte{0x42}, auditlog.KeyBytes)

func verifyTestScope() transmit.Scope {
	return transmit.Scope{RunRef: "run-test", TaskRef: "task-test", CaseRef: "case-test", ResetRef: "reset-test", PolicyRef: "policy-test"}
}

func writeVerifyFixture(t *testing.T) (journal, checkpoint, key string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "audit")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key = filepath.Join(root, "key")
	if err := os.WriteFile(key, verifyTestKey, 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := auditlog.Create(dir, "journal", auditlog.Config{Scope: verifyTestScope(), KeyRef: "key-test", Key: verifyTestKey})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1_700_000_000, 0).UTC()
	intent := transmit.Event{Stage: "intent", Receipt: transmit.Receipt{
		Scope: verifyTestScope(), Attempt: 1, AccountRef: "account-a", SessionRef: "session-a", ResourceRef: "resource-a", ApprovalRef: "approval-a", Method: "GET", At: at,
	}}
	result := transmit.Event{Stage: "result", Receipt: transmit.Receipt{
		Scope: verifyTestScope(), Attempt: 1, AccountRef: "account-a", SessionRef: "session-a", ResourceRef: "resource-a", ApprovalRef: "approval-a", Method: "GET", Reason: transmit.Completed, Admitted: true, AdmittedAt: at, RequestWritten: true, ResponseReceived: true, StatusCode: 200, At: at.Add(time.Second),
	}}
	if err := j.Record(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if err := j.Record(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	cp := j.Checkpoint()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	cpPath := filepath.Join(root, "checkpoint")
	encoded, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cpPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "journal"), cpPath, key
}

func invokeVerify(t *testing.T, ctx context.Context, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(ctx, args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestRunVerifiesJournalAndEmitsOnlySummary(t *testing.T) {
	journal, checkpoint, key := writeVerifyFixture(t)
	code, stdout, stderr := invokeVerify(t, context.Background(), "--journal", journal, "--checkpoint", checkpoint, "--key-file", key)
	if code != 0 {
		t.Fatalf("run exit=%d stderr=%q", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr %q", stderr)
	}
	var got auditlog.Summary
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not summary JSON: %v", err)
	}
	if got.Intents != 1 || got.Results != 1 || got.Checkpoint.Sequence != 2 {
		t.Fatalf("unexpected summary counts: %+v", got)
	}
	if strings.Contains(stdout, string(verifyTestKey)) {
		t.Fatal("summary exposed key material")
	}
}

func TestRunHelpAndArgumentErrorsHaveStableExitBoundary(t *testing.T) {
	_, checkpoint, key := writeVerifyFixture(t)
	cases := []struct {
		name string
		args []string
		code int
	}{
		{name: "help", args: []string{"--help"}, code: 0},
		{name: "short help", args: []string{"-h"}, code: 0},
		{name: "missing", args: []string{"--journal", "j", "--checkpoint", checkpoint}, code: 2},
		{name: "unknown", args: []string{"--journal", "j", "--checkpoint", checkpoint, "--key-file", key, "--PRIVATE-SENTINEL"}, code: 2},
		{name: "positional", args: []string{"--journal", "j", "--checkpoint", checkpoint, "--key-file", key, "PRIVATE-SENTINEL"}, code: 2},
		{name: "missing path", args: []string{"--journal", "/tmp/PRIVATE-SENTINEL", "--checkpoint", checkpoint, "--key-file", key}, code: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := invokeVerify(t, context.Background(), tc.args...)
			if code != tc.code {
				t.Fatalf("exit=%d, want %d (stderr=%q)", code, tc.code, stderr)
			}
			if tc.code == 0 {
				if stderr != "" || !strings.Contains(stdout, "Usage") {
					t.Fatalf("help output boundary violated: stdout=%q stderr=%q", stdout, stderr)
				}
			} else if stdout != "" {
				t.Fatalf("argument failure wrote stdout %q", stdout)
			}
			if strings.Contains(stderr, "PRIVATE-SENTINEL") || strings.Contains(stderr, key) {
				t.Fatalf("raw argument/path leaked in stderr %q", stderr)
			}
		})
	}
}

func TestRunRejectsIntegrityAndInputFailuresWithoutPartialSummary(t *testing.T) {
	journal, checkpoint, key := writeVerifyFixture(t)
	data, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, append(data[:len(data)-1], '\n', 'x'), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := invokeVerify(t, context.Background(), "--journal", journal, "--checkpoint", checkpoint, "--key-file", key)
	if code != 1 || stdout != "" || stderr == "" {
		t.Fatalf("integrity failure boundary: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.Contains(stderr, "x") || strings.Contains(stderr, journal) || strings.Contains(stderr, key) {
		t.Fatalf("integrity failure leaked sensitive detail: %q", stderr)
	}
}

func TestRunRejectsWholeEventRollbackWithLatestCheckpoint(t *testing.T) {
	journal, checkpoint, key := writeVerifyFixture(t)
	data, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(data, []byte{'\n'})
	if len(lines) < 4 {
		t.Fatalf("fixture journal has too few lines: %d", len(lines))
	}
	// Keep the complete header and intent lines, but remove the final result.
	rolledBack := append(bytes.Join(lines[:len(lines)-2], []byte{'\n'}), '\n')
	if err := os.WriteFile(journal, rolledBack, 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := invokeVerify(t, context.Background(), "--journal", journal, "--checkpoint", checkpoint, "--key-file", key)
	if code != 1 || stdout != "" || stderr == "" {
		t.Fatalf("whole-event rollback boundary: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestRunRejectsMalformedCheckpointVariants(t *testing.T) {
	journal, checkpoint, key := writeVerifyFixture(t)
	original, err := os.ReadFile(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		data []byte
	}{
		{name: "duplicate", data: append(append([]byte(nil), original[:len(original)-1]...), []byte(`,"Version":1}`)...)},
		{name: "unknown", data: []byte(`{"Version":1,"JournalID":"x","Scope":{},"KeyRef":"x","Sequence":0,"MAC":"x","PrivateSentinel":"do-not-leak"}`)},
		{name: "missing", data: bytes.Replace(original, []byte(`"MAC":`), []byte(`"MissingMAC":`), 1)},
		{name: "alias", data: bytes.Replace(original, []byte(`"Version":`), []byte(`"version":`), 1)},
		{name: "null", data: []byte(`null`)},
		{name: "corrupt", data: []byte(`{"Version":`)},
		{name: "trailing", data: append(append([]byte(nil), original...), []byte(` {}`)...)},
		{name: "oversized", data: append(append([]byte(nil), original...), bytes.Repeat([]byte{' '}, maxCheckpointBytes+1)...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(checkpoint, tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			code, stdout, stderr := invokeVerify(t, context.Background(), "--journal", journal, "--checkpoint", checkpoint, "--key-file", key)
			if code != 1 || stdout != "" || stderr == "" {
				t.Fatalf("checkpoint failure: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if strings.Contains(stderr, "PrivateSentinel") || strings.Contains(stderr, checkpoint) {
				t.Fatalf("checkpoint detail leaked: %q", stderr)
			}
			if err := os.WriteFile(checkpoint, original, 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunAcceptsPrettyCheckpointButRejectsRollbackAndCrossScope(t *testing.T) {
	journal, checkpoint, key := writeVerifyFixture(t)
	original, err := os.ReadFile(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, original, "", "  "); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checkpoint, pretty.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := invokeVerify(t, context.Background(), "--journal", journal, "--checkpoint", checkpoint, "--key-file", key)
	if code != 0 || stderr != "" || stdout == "" {
		t.Fatalf("pretty checkpoint should verify: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var cp auditlog.Checkpoint
	if err := json.Unmarshal(original, &cp); err != nil {
		t.Fatal(err)
	}
	cp.Sequence--
	if err := os.WriteFile(checkpoint, mustJSON(t, cp), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = invokeVerify(t, context.Background(), "--journal", journal, "--checkpoint", checkpoint, "--key-file", key)
	if code != 1 || stdout != "" || stderr == "" {
		t.Fatalf("rollback checkpoint boundary: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if err := json.Unmarshal(original, &cp); err != nil {
		t.Fatal(err)
	}
	cp.Scope.CaseRef = "other-case"
	if err := os.WriteFile(checkpoint, mustJSON(t, cp), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = invokeVerify(t, context.Background(), "--journal", journal, "--checkpoint", checkpoint, "--key-file", key)
	if code != 1 || stdout != "" || stderr == "" {
		t.Fatalf("cross-scope checkpoint boundary: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRunCanceledContextIsFailureWithoutOutput(t *testing.T) {
	journal, checkpoint, key := writeVerifyFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code, stdout, stderr := invokeVerify(t, ctx, "--journal", journal, "--checkpoint", checkpoint, "--key-file", key)
	if code != 1 || stdout != "" || stderr == "" {
		t.Fatalf("cancel boundary: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestRunOutputFailureIsFixedFailure(t *testing.T) {
	journal, checkpoint, key := writeVerifyFixture(t)
	errSentinel := errors.New("writer-secret-sentinel")
	code := run(context.Background(), []string{"--journal", journal, "--checkpoint", checkpoint, "--key-file", key}, failingWriter{err: errSentinel}, io.Discard)
	if code != 1 {
		t.Fatalf("output failure exit=%d, want 1", code)
	}
	short := shortWriter{}
	code = run(context.Background(), []string{"--journal", journal, "--checkpoint", checkpoint, "--key-file", key}, short, io.Discard)
	if code != 1 {
		t.Fatalf("short output failure exit=%d, want 1", code)
	}
}

func TestRunUsageOutputFailureIsNonzeroOutputFailure(t *testing.T) {
	for _, sink := range []io.Writer{failingWriter{err: errors.New("PRIVATE-WRITER-ERROR")}, shortWriter{}} {
		var stdout bytes.Buffer
		if code := run(context.Background(), nil, &stdout, sink); code != 1 || stdout.Len() != 0 {
			t.Fatalf("broken usage sink: exit=%d stdout_bytes=%d", code, stdout.Len())
		}
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}
