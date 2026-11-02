package server

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestControlledReadPrivateRegularAndLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  []byte
		limit int64
	}{
		{"empty", nil, 0},
		{"exact limit", []byte("synthetic"), 9},
		{"below limit", []byte("synthetic"), 16},
		{"binary", []byte{0, 1, 2, 255}, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(privateReadTestDir(t), "input")
			if err := os.WriteFile(path, tc.body, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := readPrivate(path, tc.limit)
			if err != nil || !bytes.Equal(got, tc.body) {
				t.Fatalf("private read: bytes=%d err=%v", len(got), err)
			}
		})
	}
}

func TestControlledReadPrivateRejectsUnsafeInput(t *testing.T) {
	for _, name := range []string{
		"missing", "symlink", "dangling symlink", "directory", "fifo",
		"read only", "group readable", "world readable", "special mode",
		"public parent", "special parent", "symlink parent", "trailing slash",
		"empty path", "dot", "dotdot",
		"oversized", "negative limit", "overflow limit",
	} {
		t.Run(name, func(t *testing.T) {
			root := privateReadTestDir(t)
			path := filepath.Join(root, "input")
			if err := os.WriteFile(path, []byte("synthetic"), 0o600); err != nil {
				t.Fatal(err)
			}
			limit := int64(16)
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "missing":
				path = filepath.Join(root, "missing")
			case "symlink", "dangling symlink":
				target := path
				if name == "dangling symlink" {
					target += ".missing"
				}
				path = filepath.Join(root, "link")
				must(os.Symlink(target, path))
			case "directory":
				path = root
			case "fifo":
				path = filepath.Join(root, "fifo")
				must(syscall.Mkfifo(path, 0o600))
			case "read only":
				must(os.Chmod(path, 0o400))
			case "group readable":
				must(os.Chmod(path, 0o640))
			case "world readable":
				must(os.Chmod(path, 0o644))
			case "special mode":
				must(os.Chmod(path, 0o600|os.ModeSetuid))
			case "public parent":
				must(os.Chmod(root, 0o755))
			case "special parent":
				must(os.Chmod(root, 0o700|os.ModeSticky))
			case "symlink parent":
				link := filepath.Join(privateReadTestDir(t), "linked")
				must(os.Symlink(root, link))
				path = filepath.Join(link, "input")
			case "trailing slash":
				path += "/"
			case "empty path":
				path = ""
			case "dot":
				path = "."
			case "dotdot":
				path = ".."
			case "oversized":
				limit = 8
			case "negative limit":
				limit = -1
			case "overflow limit":
				limit = math.MaxInt64
			}
			done := make(chan struct{})
			var got []byte
			var err error
			go func() { got, err = readPrivate(path, limit); close(done) }()
			select {
			case <-done:
				if err != errControlledReadStorage || got != nil {
					t.Fatalf("unsafe read accepted or error escaped: bytes=%d err=%v", len(got), err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("private read blocked on unsafe input")
			}
		})
	}
}

func TestControlledReadPrivateAtPinsDirectory(t *testing.T) {
	for _, replacement := range []string{"directory", "symlink"} {
		t.Run(replacement, func(t *testing.T) {
			root := privateReadTestDir(t)
			parent := filepath.Join(root, "private")
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			original := []byte("original fixture")
			if err := os.WriteFile(filepath.Join(parent, "input"), original, 0o600); err != nil {
				t.Fatal(err)
			}
			directory, err := os.Open(parent)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := directory.Close(); err != nil {
					t.Error(err)
				}
			})
			if err := os.Rename(parent, filepath.Join(root, "moved")); err != nil {
				t.Fatal(err)
			}
			other := parent
			if replacement == "symlink" {
				other = filepath.Join(root, "replacement")
			}
			if err := os.Mkdir(other, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(other, "input"), []byte("replacement fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			if replacement == "symlink" {
				if err := os.Symlink(other, parent); err != nil {
					t.Fatal(err)
				}
			}
			got, err := readPrivateAt(directory, "input", 64)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("pinned directory read changed: bytes=%d err=%v", len(got), err)
			}
		})
	}
}

func TestControlledReadPrivateAtRejectsInvalidReference(t *testing.T) {
	directory, err := os.Open(privateReadTestDir(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := directory.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, name := range []string{"", ".", "..", "../input", "/input", "nested/input", "input/", "nul\x00input"} {
		t.Run(name, func(t *testing.T) {
			got, err := readPrivateAt(directory, name, 16)
			if err != errControlledReadStorage || got != nil {
				t.Fatalf("invalid reference read: bytes=%d err=%v", len(got), err)
			}
		})
	}
	for _, max := range []int64{-1, math.MaxInt64} {
		got, err := readPrivateAt(directory, "input", max)
		if err != errControlledReadStorage || got != nil {
			t.Fatalf("invalid limit read: bytes=%d err=%v", len(got), err)
		}
	}
	if got, err := readPrivateAt(nil, "input", 16); err != errControlledReadStorage || got != nil {
		t.Fatal("nil directory accepted")
	}
}

func TestControlledReadPrivateConcurrentReplacement(t *testing.T) {
	root := privateReadTestDir(t)
	path := filepath.Join(root, "input")
	allowed := []byte("allowed fixture")
	if err := os.WriteFile(path, allowed, 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(privateReadTestDir(t), "outside")
	if err := os.WriteFile(outside, []byte("excluded fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readPrivate(path, 64); err != nil || !bytes.Equal(got, allowed) {
		t.Fatal("replacement fixture is not readable before mutation")
	}
	stop := make(chan struct{})
	done := make(chan error, 1)
	defer func() {
		close(stop)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	go func() {
		staged := filepath.Join(root, "staged")
		for {
			for _, variant := range []string{"symlink", "public", "regular"} {
				select {
				case <-stop:
					done <- nil
					return
				default:
				}
				var err error
				if variant == "symlink" {
					err = os.Symlink(outside, staged)
				} else {
					body, mode := allowed, os.FileMode(0o600)
					if variant == "public" {
						body, mode = []byte("excluded fixture"), 0o644
					}
					err = os.WriteFile(staged, body, mode)
				}
				if err == nil {
					err = os.Rename(staged, path)
				}
				if err != nil {
					done <- err
					return
				}
			}
		}
	}()
	for range 1000 {
		got, err := readPrivate(path, 64)
		if err != nil {
			if err != errControlledReadStorage || got != nil {
				t.Fatal("replacement returned raw error or partial data")
			}
			continue
		}
		if !bytes.Equal(got, allowed) {
			t.Fatal("replacement bypassed opened-file validation")
		}
	}
}

func privateReadTestDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}
