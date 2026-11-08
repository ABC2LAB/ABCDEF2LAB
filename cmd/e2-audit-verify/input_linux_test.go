//go:build linux

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestRunRejectsNonRegularInputsWithoutBlocking(t *testing.T) {
	journal, checkpoint, key := writeVerifyFixture(t)
	root := t.TempDir()
	for _, tc := range []struct {
		name string
		make func(string) error
	}{
		{name: "journal symlink", make: func(path string) error { return os.Symlink(journal, path) }},
		{name: "checkpoint symlink", make: func(path string) error { return os.Symlink(checkpoint, path) }},
		{name: "key symlink", make: func(path string) error { return os.Symlink(key, path) }},
		{name: "journal fifo", make: func(path string) error { return syscall.Mkfifo(path, 0o600) }},
		{name: "journal directory", make: func(path string) error { return os.Mkdir(path, 0o700) }},
		{name: "journal device", make: func(path string) error { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(root, strings.ReplaceAll(tc.name, " ", "-"))
			if tc.name == "journal device" {
				path = "/dev/null"
			}
			if err := tc.make(path); err != nil {
				t.Fatal(err)
			}
			args := []string{"--journal", journal, "--checkpoint", checkpoint, "--key-file", key}
			switch {
			case strings.HasPrefix(tc.name, "journal"):
				args[1] = path
			case strings.HasPrefix(tc.name, "checkpoint"):
				args[3] = path
			case strings.HasPrefix(tc.name, "key"):
				args[5] = path
			}
			code, stdout, stderr := invokeVerify(t, context.Background(), args...)
			if code != 1 || stdout != "" || stderr == "" {
				t.Fatalf("special input boundary: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
		})
	}
}

func TestRunRejectsKeyWithWrongModeOrLength(t *testing.T) {
	journal, checkpoint, key := writeVerifyFixture(t)
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := invokeVerify(t, context.Background(), "--journal", journal, "--checkpoint", checkpoint, "--key-file", key)
	if code != 1 || stdout != "" || stderr != "audit_input_unavailable\n" {
		t.Fatalf("key mode boundary: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if err := os.Chmod(key, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("short-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = invokeVerify(t, context.Background(), "--journal", journal, "--checkpoint", checkpoint, "--key-file", key)
	if code != 1 || stdout != "" || stderr != "audit_input_invalid\n" {
		t.Fatalf("key length boundary: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if err := os.WriteFile(key, bytes.Repeat([]byte{0x24}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = invokeVerify(t, context.Background(), "--journal", journal, "--checkpoint", checkpoint, "--key-file", key)
	if code != 1 || stdout != "" || stderr != "audit_integrity_failed\n" {
		t.Fatalf("wrong key boundary: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if err := os.WriteFile(key, bytes.Repeat([]byte{0x25}, 33), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = invokeVerify(t, context.Background(), "--journal", journal, "--checkpoint", checkpoint, "--key-file", key)
	if code != 1 || stdout != "" || stderr != "audit_input_unavailable\n" {
		t.Fatalf("oversized key boundary: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
