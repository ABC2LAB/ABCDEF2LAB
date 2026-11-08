//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPrivateInputRejectsSpecialFilesAndUnsafePaths(t *testing.T) {
	for _, mode := range []string{"symlink", "hardlink", "fifo", "directory", "file-mode", "parent-mode", "ancestor-symlink", "oversize", "relative"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "input.json")
			if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "symlink":
				alias := filepath.Join(root, "alias")
				if err := os.Symlink(path, alias); err != nil {
					t.Fatal(err)
				}
				path = alias
			case "hardlink":
				if err := os.Link(path, filepath.Join(root, "alias")); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				path = filepath.Join(root, "pipe")
				if err := unix.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				path = root
			case "file-mode":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "parent-mode":
				if err := os.Chmod(root, 0755); err != nil {
					t.Fatal(err)
				}
			case "ancestor-symlink":
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(root, alias); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(alias, "input.json")
			case "oversize":
				if err := os.WriteFile(path, make([]byte, 65), 0600); err != nil {
					t.Fatal(err)
				}
			case "relative":
				path = "relative.json"
			}
			start := time.Now()
			if data, err := readPrivateFile(path, 64); err != errPreparation || data != nil {
				t.Fatal("unsafe input accepted")
			}
			if time.Since(start) > time.Second {
				t.Fatal("special-file check blocked")
			}
		})
	}
}

func TestPrivateInputAndDirectoryCheck(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "input.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := readPrivateFile(path, 2); err != nil || string(got) != "{}" {
		t.Fatal("valid private input refused")
	}
	if checkDirectory(root, true) != nil {
		t.Fatal("valid private directory refused")
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if checkDirectory(root, false) != nil || checkDirectory(root, true) == nil {
		t.Fatal("directory policy mismatch")
	}
}
