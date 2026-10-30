package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

func invokeKeygen(ctx context.Context, path string) (int, string, string) {
	var out, diagnostic bytes.Buffer
	code := run(ctx, []string{"--key-file", path}, &out, &diagnostic)
	return code, out.String(), diagnostic.String()
}

func privateKeyDirectory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// testing.TempDir's numbered child can inherit umask-based permissions;
	// provision the same exact mode that operators must supply to the CLI.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readGeneratedKey(t *testing.T, path string) []byte {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != auditlog.KeyBytes {
		t.Fatal("generated key type, mode or size is invalid")
	}
	key, err := os.ReadFile(path)
	if err != nil || len(key) != auditlog.KeyBytes {
		t.Fatal("could not read complete generated key")
	}
	return key
}

func TestRunCreatesCompatiblePrivateKeyWithoutPrintingIt(t *testing.T) {
	t.Parallel()
	dir := privateKeyDirectory(t)
	path := filepath.Join(dir, "audit.key")
	code, out, diagnostic := invokeKeygen(context.Background(), path)
	if code != 0 || out != created+"\n" || diagnostic != "" {
		t.Fatalf("generation boundary: exit=%d, diagnostic=%q", code, diagnostic)
	}
	key := readGeneratedKey(t, path)
	defer clear(key)
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("parent permissions changed")
	}
	journalDir := privateKeyDirectory(t)
	scope := transmit.Scope{RunRef: "run-test", TaskRef: "task-test", CaseRef: "case-test", ResetRef: "reset-test", PolicyRef: "policy-test"}
	j, err := auditlog.Create(journalDir, "journal", auditlog.Config{Key: key, KeyRef: "key-test", Scope: scope})
	if err != nil {
		t.Fatal("generated key rejected by audit writer")
	}
	cp := j.Checkpoint()
	if err := j.Close(); err != nil {
		t.Fatal("could not close test journal")
	}
	file, err := os.Open(filepath.Join(journalDir, "journal"))
	if err != nil {
		t.Fatal("could not open test journal")
	}
	summary, verifyErr := auditlog.Verify(context.Background(), file, key, cp)
	closeErr := file.Close()
	if verifyErr != nil || closeErr != nil || summary.Intents != 0 || summary.Results != 0 {
		t.Fatal("generated binary key was incompatible with journal verification")
	}
}

func TestRunDoesNotOverwriteOrChmodExistingDestination(t *testing.T) {
	t.Parallel()
	path := filepath.Join(privateKeyDirectory(t), "audit.key")
	original := []byte("EXISTING-PRIVATE-SENTINEL")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, diagnostic := invokeKeygen(context.Background(), path)
	after, readErr := os.ReadFile(path)
	info, statErr := os.Stat(path)
	if code != 1 || out != "" || diagnostic != alreadyExists+"\n" || readErr != nil || statErr != nil || !bytes.Equal(after, original) || info.Mode().Perm() != 0o644 {
		t.Fatal("existing destination changed or its detail leaked")
	}
}

func TestRunRejectsExistingSpecialDestinations(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"symlink", "dangling", "fifo", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateKeyDirectory(t)
			path, target := filepath.Join(dir, "audit.key"), filepath.Join(privateKeyDirectory(t), "existing")
			original := []byte("EXISTING-PRIVATE-SENTINEL")
			if kind == "symlink" {
				if err := os.WriteFile(target, original, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			switch kind {
			case "symlink", "dangling":
				err = os.Symlink(target, path)
			case "fifo":
				err = syscall.Mkfifo(path, 0o600)
			case "directory":
				err = os.Mkdir(path, 0o700)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			code, out, diagnostic := invokeKeygen(context.Background(), path)
			after, statErr := os.Lstat(path)
			if code != 1 || out != "" || diagnostic != alreadyExists+"\n" || statErr != nil || !os.SameFile(before, after) {
				t.Fatal("special destination was replaced or accepted")
			}
			if kind == "symlink" {
				data, err := os.ReadFile(target)
				if err != nil || !bytes.Equal(data, original) {
					t.Fatal("symlink target changed")
				}
			}
			if kind == "dangling" {
				if _, err := os.Stat(target); !os.IsNotExist(err) {
					t.Fatal("dangling symlink target created")
				}
			}
		})
	}
}

func TestRunRejectsUntrustedParentWithoutChangingIt(t *testing.T) {
	t.Parallel()
	for _, mode := range []os.FileMode{0o755, 0o770, 0o701, 0o700 | os.ModeSticky, 0o700 | os.ModeSetgid, 0o700 | os.ModeSetuid} {
		dir := privateKeyDirectory(t)
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "PRIVATE-SENTINEL.key")
		code, out, diagnostic := invokeKeygen(context.Background(), path)
		info, err := os.Stat(dir)
		if code != 1 || out != "" || diagnostic != privateDirRequired+"\n" || err != nil || info.Mode().Perm() != mode.Perm() || info.Mode()&os.ModeSticky != mode&os.ModeSticky {
			t.Fatal("untrusted parent accepted, changed or exposed")
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("key created in untrusted directory")
		}
	}
}

func TestRunRejectsMissingNonDirectoryAndLinkedParents(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"missing", "file", "symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			parent := filepath.Join(privateKeyDirectory(t), "PRIVATE-SENTINEL")
			var err error
			switch kind {
			case "file":
				err = os.WriteFile(parent, []byte("private"), 0o600)
			case "symlink":
				err = os.Symlink(privateKeyDirectory(t), parent)
			case "fifo":
				err = syscall.Mkfifo(parent, 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			code, out, diagnostic := invokeKeygen(context.Background(), filepath.Join(parent, "audit.key"))
			if code != 1 || out != "" || diagnostic != privateDirRequired+"\n" {
				t.Fatal("invalid parent accepted or raw error exposed")
			}
		})
	}
}

func TestRunRejectsParentOwnedByAnotherUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing test-directory ownership requires root")
	}
	dir := privateKeyDirectory(t)
	if err := os.Chown(dir, 1, -1); err != nil {
		t.Fatal(err)
	}
	code, out, diagnostic := invokeKeygen(context.Background(), filepath.Join(dir, "audit.key"))
	if code != 1 || out != "" || diagnostic != privateDirRequired+"\n" {
		t.Fatal("foreign-owned directory accepted")
	}
}

func TestRunCancellationAndNilContextDoNotCreateKey(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tt := range []struct {
		ctx  context.Context
		want string
	}{{nil, invalidInput}, {ctx, generationCanceled}} {
		path := filepath.Join(privateKeyDirectory(t), "audit.key")
		code, out, diagnostic := invokeKeygen(tt.ctx, path)
		if code != 1 || out != "" || diagnostic != tt.want+"\n" {
			t.Fatal("invalid context result")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("canceled/invalid call created key")
		}
	}
}

func TestConcurrentGenerationHasOneWinner(t *testing.T) {
	t.Parallel()
	path := filepath.Join(privateKeyDirectory(t), "audit.key")
	results := make(chan string, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			code, out, diagnostic := invokeKeygen(context.Background(), path)
			if code == 0 && out == created+"\n" && diagnostic == "" {
				results <- "created"
				return
			}
			if code == 1 && out == "" && diagnostic == alreadyExists+"\n" {
				results <- "exists"
				return
			}
			results <- "unexpected"
		}()
	}
	close(start)
	a, b := <-results, <-results
	if !(a == "created" && b == "exists" || a == "exists" && b == "created") {
		t.Fatal("exclusive creation did not have exactly one winner")
	}
	key := readGeneratedKey(t, path)
	clear(key)
}

func TestOutputFailurePreservesGeneratedKeyAndRejectsRetry(t *testing.T) {
	t.Parallel()
	for _, short := range []bool{false, true} {
		path := filepath.Join(privateKeyDirectory(t), "audit.key")
		var diagnostic bytes.Buffer
		code := run(context.Background(), []string{"--key-file", path}, brokenWriter{short}, &diagnostic)
		if code != 1 || diagnostic.String() != outputUnavailable+"\n" {
			t.Fatal("output failure was acknowledged or exposed")
		}
		before := readGeneratedKey(t, path)
		code, out, message := invokeKeygen(context.Background(), path)
		after := readGeneratedKey(t, path)
		same := bytes.Equal(before, after)
		clear(before)
		clear(after)
		if code != 1 || out != "" || message != alreadyExists+"\n" || !same {
			t.Fatal("unacknowledged key was replaced")
		}
	}
}

func TestRestrictiveUmaskStillProducesVerifierCompatibleMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(privateKeyDirectory(t), "audit.key")
	command := exec.Command(os.Args[0], "-test.run=^TestKeygenUmaskChild$")
	command.Env = append(os.Environ(), "E2_KEYGEN_TEST_CHILD=1", "E2_KEYGEN_TEST_PATH="+path)
	if err := command.Run(); err != nil {
		t.Fatal("umask child failed")
	}
	key := readGeneratedKey(t, path)
	clear(key)
}

func TestKeygenUmaskChild(t *testing.T) {
	if os.Getenv("E2_KEYGEN_TEST_CHILD") != "1" {
		return
	}
	previous := syscall.Umask(0o777)
	defer syscall.Umask(previous)
	if code := run(context.Background(), []string{"--key-file", os.Getenv("E2_KEYGEN_TEST_PATH")}, io.Discard, io.Discard); code != 0 {
		t.Fatal("restrictive umask generation failed")
	}
}

func TestOpenPrivateKeyRejectsInvalidNameWithoutEcho(t *testing.T) {
	t.Parallel()
	file, dir, code := openPrivateKey("PRIVATE-SENTINEL\n")
	if file != nil || dir != nil || code != invalidInput || strings.Contains(code, "PRIVATE-SENTINEL") {
		t.Fatal("invalid path reached storage")
	}
}
