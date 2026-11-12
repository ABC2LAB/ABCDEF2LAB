//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
)

func writeHTTPVerifyFixture(t *testing.T, keyBytes []byte, empty bool) ([]string, e2proxy.JournalCheckpoint) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := e2proxy.CreateJournal(dir, "http.journal", keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	if !empty {
		event := e2proxy.Event{Index: 1, Stage: "result", Receipt: e2proxy.Receipt{
			Version: e2proxy.JournalVersion, IntentID: 22, Scope: verifyTestScope(),
			PolicyRevision: "revision-test", CallRef: "call-test", CommandRef: "command-test", RuleRef: "rule-test",
			AttemptID: "0123456789abcdef0123456789abcdef", Sequence: 1, Method: "GET",
			Decision: "allowed", Reason: e2proxy.Reason("complete"), ForwardAttempted: true, RequestWritten: true,
			ResponseReceived: true, ResponseComplete: true, StatusCode: 200, AuditComplete: true,
			At: time.Unix(1_700_000_000, 0).UTC(),
		}}
		if err := journal.Record(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	head := journal.Checkpoint()
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	checkpoint, key := filepath.Join(dir, "http.checkpoint"), filepath.Join(dir, "http.key")
	for name, data := range map[string][]byte{checkpoint: mustJSON(t, head), key: keyBytes} {
		if err := os.WriteFile(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return []string{"--journal", filepath.Join(dir, "http.journal"), "--checkpoint", checkpoint, "--key-file", key, "--journal-format", "e2-http-v2"}, head
}

func assertHTTPFailure(t *testing.T, args []string, want string) {
	t.Helper()
	code, out, diagnostic := invokeVerify(t, context.Background(), args...)
	if code != 1 || out != "" || diagnostic != want+"\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q want=%q", code, out, diagnostic, want)
	}
}

func TestRunHTTPJournalSummary(t *testing.T) {
	for _, size := range []int{32, 4096} {
		for _, empty := range []bool{false, true} {
			t.Run(fmt.Sprintf("key%d_empty%v", size, empty), func(t *testing.T) {
				args, head := writeHTTPVerifyFixture(t, bytes.Repeat([]byte{0x73}, size), empty)
				code, out, diagnostic := invokeVerify(t, context.Background(), args...)
				var summary e2proxy.JournalSummary
				if code != 0 || diagnostic != "" || json.Unmarshal([]byte(out), &summary) != nil || summary.Checkpoint != head || summary.Events != head.Sequence {
					t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, diagnostic)
				}
				for _, private := range []string{args[1], args[3], args[5], strings.Repeat("s", size), "AttemptID", "AuditComplete", "StatusCode", "Decision"} {
					if strings.Contains(out+diagnostic, private) {
						t.Fatal("private input or event detail exposed")
					}
				}
			})
		}
	}
}

func TestRunHTTPJournalRejectsTamperingAndRollback(t *testing.T) {
	cases := []string{"last byte", "valid prefix", "wrong key", "cross scope", "wrong journal", "size limit"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			args, head := writeHTTPVerifyFixture(t, verifyTestKey, false)
			data, err := os.ReadFile(args[1])
			if err != nil {
				t.Fatal(err)
			}
			want := "audit_integrity_failed"
			switch name {
			case "last byte":
				data[len(data)-2] ^= 1
			case "valid prefix":
				data = data[:bytes.IndexByte(data, '\n')+1]
			case "wrong key":
				if err := os.WriteFile(args[5], bytes.Repeat([]byte{0x74}, 32), 0o600); err != nil {
					t.Fatal(err)
				}
			case "cross scope":
				head.Scope.RunRef = "another-run"
				if err := os.WriteFile(args[3], mustJSON(t, head), 0o600); err != nil {
					t.Fatal(err)
				}
			case "wrong journal":
				other, _ := writeHTTPVerifyFixture(t, verifyTestKey, false)
				args[1] = other[1]
			case "size limit":
				data = bytes.Repeat([]byte{'x'}, 16385)
				want = "audit_verification_limit_exceeded"
			}
			if name != "wrong journal" {
				if err := os.WriteFile(args[1], data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			assertHTTPFailure(t, args, want)
		})
	}
}

func TestRunHTTPCheckpointStrictEncoding(t *testing.T) {
	mutations := map[string]func([]byte) []byte{
		"duplicate": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"Version":`), []byte(`"Version":"e2-http-v2","Version":`), 1)
		},
		"unknown":  func(b []byte) []byte { return append(append([]byte(`{"PRIVATE-CONTENT":0,`), b[1:]...), '\n') },
		"alias":    func(b []byte) []byte { return bytes.Replace(b, []byte(`"Version"`), []byte(`"version"`), 1) },
		"missing":  func(b []byte) []byte { return bytes.Replace(b, []byte(`"Version":"e2-http-v2",`), nil, 1) },
		"trailing": func(b []byte) []byte { return append(b, []byte(` {"private":true}`)...) },
		"reordered": func(b []byte) []byte {
			parts := bytes.SplitN(b[1:], []byte(","), 3)
			return append(append(append(append([]byte("{"), parts[1]...), ','), append(parts[0], ',')...), parts[2]...)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			args, head := writeHTTPVerifyFixture(t, verifyTestKey, false)
			if err := os.WriteFile(args[3], mutate(mustJSON(t, head)), 0o600); err != nil {
				t.Fatal(err)
			}
			assertHTTPFailure(t, args, "audit_input_invalid")
		})
	}
	t.Run("pretty", func(t *testing.T) {
		args, head := writeHTTPVerifyFixture(t, verifyTestKey, false)
		data, err := json.MarshalIndent(head, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(args[3], data, 0o600); err != nil {
			t.Fatal(err)
		}
		code, _, diagnostic := invokeVerify(t, context.Background(), args...)
		if code != 0 || diagnostic != "" {
			t.Fatalf("exit=%d stderr=%q", code, diagnostic)
		}
	})
}

func TestRunHTTPFormatIsExplicitAndCannotUseGETTimeline(t *testing.T) {
	httpArgs, _ := writeHTTPVerifyFixture(t, verifyTestKey, false)
	oldJournal, oldCheckpoint, oldKey := writeVerifyFixture(t)
	assertHTTPFailure(t, httpArgs[:6], "audit_input_invalid")
	assertHTTPFailure(t, []string{"--journal", oldJournal, "--checkpoint", oldCheckpoint, "--key-file", oldKey, "--journal-format", "e2-http-v2"}, "audit_input_invalid")
	for _, suffix := range [][]string{{"--timeline"}, {"--max-events", "1"}, {"--journal-format", "PRIVATE-UNSUPPORTED"}, {"--journal-format", ""}} {
		args := append(append([]string(nil), httpArgs...), suffix...)
		code, out, diagnostic := invokeVerify(t, context.Background(), args...)
		if code != 2 || out != "" || diagnostic != usage || strings.Contains(diagnostic, "PRIVATE-UNSUPPORTED") {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, diagnostic)
		}
	}
	code, _, diagnostic := invokeVerify(t, context.Background(), "--journal", oldJournal, "--checkpoint", oldCheckpoint, "--key-file", oldKey, "--journal-format", "registered-get-v1")
	if code != 0 || diagnostic != "" {
		t.Fatalf("explicit old format: exit=%d stderr=%q", code, diagnostic)
	}
}

func TestRunHTTPPrivateInputBoundary(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "directory", "missing"} {
		for _, arg := range []int{1, 3, 5} {
			t.Run(fmt.Sprintf("%s_arg%d", kind, arg), func(t *testing.T) {
				args, _ := writeHTTPVerifyFixture(t, verifyTestKey, false)
				path := filepath.Join(filepath.Dir(args[arg]), "PRIVATE-PATH")
				var err error
				switch kind {
				case "symlink":
					err = os.Symlink(args[arg], path)
				case "fifo":
					err = syscall.Mkfifo(path, 0o600)
				case "directory":
					err = os.Mkdir(path, 0o700)
				}
				if err != nil {
					t.Fatal(err)
				}
				args[arg] = path
				assertHTTPFailure(t, args, "audit_input_unavailable")
			})
		}
	}
	for _, size := range []int{0, 31, 4097} {
		t.Run(fmt.Sprintf("key_length_%d", size), func(t *testing.T) {
			args, _ := writeHTTPVerifyFixture(t, verifyTestKey, false)
			if err := os.WriteFile(args[5], bytes.Repeat([]byte{0x71}, size), 0o600); err != nil {
				t.Fatal(err)
			}
			want := "audit_input_invalid"
			if size > 4096 {
				want = "audit_input_unavailable"
			}
			assertHTTPFailure(t, args, want)
		})
	}
	t.Run("key permissions", func(t *testing.T) {
		args, _ := writeHTTPVerifyFixture(t, verifyTestKey, false)
		if err := os.Chmod(args[5], 0o640); err != nil {
			t.Fatal(err)
		}
		assertHTTPFailure(t, args, "audit_input_unavailable")
	})
}

func TestRunHTTPCancellationAndOutputFailure(t *testing.T) {
	args, _ := writeHTTPVerifyFixture(t, verifyTestKey, false)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	code, out, diagnostic := invokeVerify(t, canceled, args...)
	if code != 1 || out != "" || diagnostic != "audit_verification_canceled\n" {
		t.Fatalf("cancel exit=%d stdout=%q stderr=%q", code, out, diagnostic)
	}
	code, out, diagnostic = invokeVerify(t, nil, args...)
	if code != 1 || out != "" || diagnostic != "audit_input_invalid\n" {
		t.Fatalf("nil exit=%d stdout=%q stderr=%q", code, out, diagnostic)
	}
	for _, sink := range []io.Writer{failingWriter{err: io.ErrClosedPipe}, shortWriter{}} {
		var stderr bytes.Buffer
		if code := run(context.Background(), args, sink, &stderr); code != 1 || stderr.String() != "audit_output_unavailable\n" {
			t.Fatalf("output exit=%d stderr=%q", code, stderr.String())
		}
	}
}

// 고른 판과 신뢰 checkpoint의 판이 다르면 검증하지 않는다. v2 기록을 v1 규칙으로 통과시키지 않는다.
func TestRunHTTPFormatMustMatchCheckpointVersion(t *testing.T) {
	args, head := writeHTTPVerifyFixture(t, bytes.Repeat([]byte{0x73}, 32), false)
	if head.Version != e2proxy.Version {
		t.Fatalf("fixture version %s", head.Version)
	}
	args[len(args)-1] = e2proxy.VersionV1
	assertHTTPFailure(t, args, "audit_input_invalid")
}
