//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Autumn-27/artex/budgetlocal"
)

func TestOperatorRequestRequiresPrivateOwnerAndMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "approval.json")
	data := []byte(`{"approval_request_ref":"request.1","task_ref":"task.1","policy_ref":"policy.1","reset_ref":"reset.1","max_requests":2,"valid_from":"2026-01-01T00:00:00Z","valid_until":"2027-01-01T00:00:00Z"}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if loaded, err := readPrivateOperatorFile(path); err != nil || !bytes.Equal(loaded, data) {
		t.Fatalf("private request rejected: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateOperatorFile(path); !errors.Is(err, budgetlocal.ErrUnavailable) {
		t.Fatalf("readable request accepted: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateOperatorFile(link); !errors.Is(err, budgetlocal.ErrUnavailable) {
		t.Fatalf("symlink request accepted: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateOperatorFile(path); !errors.Is(err, budgetlocal.ErrUnavailable) {
		t.Fatalf("traversable request directory accepted: %v", err)
	}
}

func TestOperatorCommandRejectsMissingInputWithoutOutput(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--config", "/tmp/unknown.json"}, &output); !errors.Is(err, budgetlocal.ErrInvalid) {
		t.Fatalf("missing private request accepted: %v", err)
	}
	if output.Len() != 0 {
		t.Fatal("rejected request emitted approval metadata")
	}
}
