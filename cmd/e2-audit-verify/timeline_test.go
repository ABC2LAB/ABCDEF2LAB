//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit"

	"github.com/Autumn-27/artex/transmit/auditlog"
)

func TestRunTimelineProjectsSafeFields(t *testing.T) {
	journal, checkpoint, key := writeVerifyFixture(t)
	code, stdout, stderr := invokeVerify(t, context.Background(), "--journal", journal, "--checkpoint", checkpoint, "--key-file", key, "--timeline")
	if code != 0 || stderr != "" {
		t.Fatalf("timeline exit=%d stderr=%q", code, stderr)
	}
	want := `[{"at":"2023-11-14T22:13:20Z","account_ref":"account-a","resource_ref":"resource-a","stage":"intent","reason":"","observed":{"admitted":false,"request_written":false,"response_received":false,"status_code":0}},{"at":"2023-11-14T22:13:21Z","account_ref":"account-a","resource_ref":"resource-a","stage":"result","reason":"completed","observed":{"admitted":true,"request_written":true,"response_received":true,"status_code":200}}]` + "\n"
	if stdout != want {
		t.Fatalf("timeline projection: got=%s want=%s", stdout, want)
	}
	var events []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &events); err != nil || len(events) != 2 {
		t.Fatalf("timeline JSON/events: count=%d err=%v", len(events), err)
	}
	for _, event := range events {
		if len(event) != 6 {
			t.Fatalf("unexpected timeline fields: %v", event)
		}
		for _, name := range []string{"at", "account_ref", "resource_ref", "stage", "reason", "observed"} {
			if _, ok := event[name]; !ok {
				t.Fatalf("missing field %s", name)
			}
		}
		var observed map[string]json.RawMessage
		if err := json.Unmarshal(event["observed"], &observed); err != nil || len(observed) != 4 {
			t.Fatalf("observed projection: %v %v", observed, err)
		}
	}
	for _, secret := range []string{"session-a", "approval-a", "GET", "run-test", "key-test", string(verifyTestKey)} {
		if strings.Contains(stdout, secret) {
			t.Fatalf("timeline contains excluded data %q", secret)
		}
	}
}

func TestRunTimelineLimitAndLateFailureAreSilentOnStdout(t *testing.T) {
	// Record 2 must authenticate and set exceeded before record 3 is broken.
	journal, checkpoint, key := writeTimelineRecordsFixture(t, []transmit.Event{
		timelineTestIntent(1), timelineTestIntent(2), timelineTestIntent(3),
	})
	args := []string{"--journal", journal, "--checkpoint", checkpoint, "--key-file", key, "--timeline", "--max-events", "1"}
	code, stdout, stderr := invokeVerify(t, context.Background(), args...)
	if code != 1 || stdout != "" || stderr != "audit_timeline_limit_exceeded\n" {
		t.Fatalf("limit boundary: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	data, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-2] ^= 1
	if err := os.WriteFile(journal, data, 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = invokeVerify(t, context.Background(), args...)
	if code != 1 || stdout != "" || stderr != "audit_integrity_failed\n" {
		t.Fatalf("late integrity boundary: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestRunTimelineEmptyAndInvalidLimit(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "journal-dir")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(root, "key")
	if err := os.WriteFile(key, verifyTestKey, 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := auditlog.Create(dir, "journal", auditlog.Config{Scope: verifyTestScope(), KeyRef: "key-test", Key: verifyTestKey})
	if err != nil {
		t.Fatal(err)
	}
	cp := j.Checkpoint()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(root, "checkpoint")
	if err := os.WriteFile(checkpoint, mustJSON(t, cp), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--journal", filepath.Join(dir, "journal"), "--checkpoint", checkpoint, "--key-file", key}
	code, stdout, stderr := invokeVerify(t, context.Background(), append(append([]string(nil), args...), "--timeline")...)
	if code != 0 || stdout != "[]\n" || stderr != "" {
		t.Fatalf("empty timeline: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, extra := range [][]string{{"--timeline", "--max-events", "0"}, {"--timeline", "--max-events", "1001"}, {"--max-events", "1"}} {
		code, stdout, stderr = invokeVerify(t, context.Background(), append(append([]string(nil), args...), extra...)...)
		if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "Usage:") {
			t.Fatalf("invalid flags %v: exit=%d stdout=%q stderr=%q", extra, code, stdout, stderr)
		}
	}
}

func TestRunTimelineOutputErrorUsesFixedDiagnostic(t *testing.T) {
	journal, checkpoint, key := writeVerifyFixture(t)
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"--journal", journal, "--checkpoint", checkpoint, "--key-file", key, "--timeline"}, failingWriter{err: errInput}, &stderr)
	if code != 1 || stderr.String() != "audit_output_unavailable\n" {
		t.Fatalf("output error: exit=%d stderr=%q", code, stderr.String())
	}
}

func timelineTestIntent(attempt uint64) transmit.Event {
	return transmit.Event{Stage: "intent", Receipt: transmit.Receipt{
		Scope: verifyTestScope(), Attempt: attempt, AccountRef: "account-a", SessionRef: "session-a",
		ResourceRef: "resource-a", ApprovalRef: "approval-a", Method: "GET",
		At: time.Unix(1_700_000_000, 0).UTC().Add(time.Duration(attempt) * time.Second),
	}}
}

func writeTimelineRecordsFixture(t *testing.T, events []transmit.Event) (journal, checkpoint, key string) {
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
	t.Cleanup(func() { _ = j.Close() })
	for _, event := range events {
		if err := j.Record(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	cp := j.Checkpoint()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	checkpoint = filepath.Join(root, "checkpoint")
	if err := os.WriteFile(checkpoint, mustJSON(t, cp), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "journal"), checkpoint, key
}

func TestRunTimelinePreservesAdmissionBeforeWriteFailure(t *testing.T) {
	event := timelineTestIntent(1)
	event.Stage, event.Reason = "result", transmit.TransportError
	event.Admitted, event.AdmittedAt = true, event.At
	// Admission is independent from successful writing or receiving a response.
	journal, checkpoint, key := writeTimelineRecordsFixture(t, []transmit.Event{event})
	code, stdout, stderr := invokeVerify(t, context.Background(), "--journal", journal, "--checkpoint", checkpoint, "--key-file", key, "--timeline")
	want := `[{"at":"2023-11-14T22:13:21Z","account_ref":"account-a","resource_ref":"resource-a","stage":"result","reason":"transport_error","observed":{"admitted":true,"request_written":false,"response_received":false,"status_code":0}}]` + "\n"
	if code != 0 || stderr != "" || stdout != want {
		t.Fatalf("admitted write failure: exit=%d stderr=%q got=%s want=%s", code, stderr, stdout, want)
	}
}

func TestRunTimelineDefaultAndExplicitMaximumBoundary(t *testing.T) {
	for _, count := range []int{1000, 1001} {
		events := make([]transmit.Event, count)
		for i := range events {
			events[i] = timelineTestIntent(uint64(i + 1))
		}
		journal, checkpoint, key := writeTimelineRecordsFixture(t, events)
		args := []string{"--journal", journal, "--checkpoint", checkpoint, "--key-file", key, "--timeline"}
		for _, explicit := range []bool{false, true} {
			current := append([]string(nil), args...)
			if explicit {
				current = append(current, "--max-events", "1000")
			}
			code, stdout, stderr := invokeVerify(t, context.Background(), current...)
			if count == 1001 {
				if code != 1 || stdout != "" || stderr != "audit_timeline_limit_exceeded\n" {
					t.Fatalf("count=%d explicit=%t exit=%d bytes=%d stderr=%q", count, explicit, code, len(stdout), stderr)
				}
				continue
			}
			var got []json.RawMessage
			if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &got) != nil || len(got) != 1000 {
				t.Fatalf("count=%d explicit=%t exit=%d events=%d stderr=%q", count, explicit, code, len(got), stderr)
			}
		}
	}
}
