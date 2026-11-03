//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Autumn-27/artex/budgetlocal"
)

func privateInputDirectory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPrivateInputRejectsHardlinkAndSymlinkAncestors(t *testing.T) {
	dir := privateInputDirectory(t)
	path := filepath.Join(dir, "input.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.json")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, alias} {
		if _, err := readPrivateOperatorFile(name); !errors.Is(err, budgetlocal.ErrUnavailable) {
			t.Fatalf("hardlink accepted: %v", err)
		}
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateOperatorFile(path); err != nil {
		t.Fatalf("single link rejected: %v", err)
	}
	link := filepath.Join(dir, "parent")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateOperatorFile(filepath.Join(link, "input.json")); !errors.Is(err, budgetlocal.ErrUnavailable) {
		t.Fatalf("symlink parent accepted: %v", err)
	}
}

func TestPrivateInputFIFOIsRejectedWithoutWriter(t *testing.T) {
	// Use a subprocess so a blocking open regression is killed at the deadline.
	if path := os.Getenv("E2_BUDGET_TEST_FIFO"); path != "" {
		if _, err := readPrivateOperatorFile(path); !errors.Is(err, budgetlocal.ErrUnavailable) {
			t.Fatalf("FIFO accepted: %v", err)
		}
		return
	}
	dir := privateInputDirectory(t)
	path := filepath.Join(dir, "input.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPrivateInputFIFOIsRejectedWithoutWriter$")
	cmd.Env = append(os.Environ(), "E2_BUDGET_TEST_FIFO="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("FIFO read failed or blocked: %v %s", err, output)
	}
}

func TestPrivateInputBoundsAndCommandRejection(t *testing.T) {
	dir := privateInputDirectory(t)
	path := filepath.Join(dir, "input.json")
	for _, size := range []int{0, operatorFileLimit, operatorFileLimit + 1} {
		data := bytes.Repeat([]byte("x"), size)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := readPrivateOperatorFile(path)
		if size == operatorFileLimit {
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("maximum input rejected: %v", err)
			}
		} else if !errors.Is(err, budgetlocal.ErrInvalid) || len(got) != 0 {
			t.Fatalf("size %d accepted: %v", size, err)
		}
	}
	alias := filepath.Join(dir, "alias.json")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "config.json")
	body, err := json.Marshal(budgetlocal.ClientConfig{
		SocketPath: filepath.Join(dir, "missing.sock"),
		ServerUID:  uint32(os.Geteuid() + 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, body, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--config", alias, "--request", path},
		{"--config", config, "--request", alias},
	} {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); !errors.Is(err, budgetlocal.ErrDenied) || out.Len() != 0 {
			t.Fatalf("untrusted input did not fail closed: %v stdout=%d", err, out.Len())
		}
	}
}

func TestPrivateInputAllowsSearchOnlyAncestor(t *testing.T) {
	parent := privateInputDirectory(t)
	dir := filepath.Join(parent, "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "input.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o111); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parent, 0o700); err != nil {
			t.Error(err)
		}
	})
	if data, err := readPrivateOperatorFile(path); err != nil || string(data) != "{}" {
		t.Fatalf("trusted search-only ancestor rejected: %v", err)
	}
}
