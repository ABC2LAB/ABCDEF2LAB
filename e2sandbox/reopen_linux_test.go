//go:build linux

package e2sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNamespaceReopenIdentityAndFixedDiagnostics(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	for _, path := range []string{a, b} {
		if err := os.WriteFile(path, []byte("synthetic"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fd, err := unix.Open(a, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := reopenInNamespace(a, fd); err != nil {
		t.Fatalf("same owned source rejected: %v", err)
	}
	if err := jailError("source_reopen", reopenInNamespace(b, fd)); err == nil || err.Error() != "e2sandbox: jail_source_reopen_inode" {
		t.Fatalf("different inode diagnostic: %v", err)
	}
	if err := jailError("source_reopen", reopenInNamespace(a, -1)); err == nil || err.Error() != "e2sandbox: jail_source_reopen_inherited_stat_descriptor" {
		t.Fatalf("invalid inherited descriptor diagnostic: %v", err)
	}
	// The rejected replacement must never overwrite the authorized descriptor.
	if err := reopenInNamespace(a, fd); err != nil {
		t.Fatalf("rejected source replaced the authorized descriptor: %v", err)
	}
}
