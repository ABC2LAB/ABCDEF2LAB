//go:build linux

package diagnosisprep

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestJournalCorruptionAndTruncationFailClosed(t *testing.T) {
	cases := map[string]func(string) error{
		"partial_record": func(root string) error {
			p := filepath.Join(root, journalName)
			raw, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			return os.WriteFile(p, raw[:len(raw)-1], 0600)
		},
		"whole_record": func(root string) error {
			p := filepath.Join(root, journalName)
			raw, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			at := bytes.LastIndexByte(raw[:len(raw)-1], '\n')
			return os.WriteFile(p, raw[:at+1], 0600)
		},
		"changed_payload": func(root string) error {
			p := filepath.Join(root, journalName)
			raw, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			raw = bytes.Replace(raw, []byte("owner"), []byte("other"), 1)
			return os.WriteFile(p, raw, 0600)
		},
		"extra_record": func(root string) error {
			p := filepath.Join(root, journalName)
			f, e := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			defer f.Close()
			_, e = f.WriteString("{}\n")
			return e
		},
		"missing_head":  func(root string) error { return os.Remove(filepath.Join(root, headName)) },
		"empty_journal": func(root string) error { return os.WriteFile(filepath.Join(root, journalName), nil, 0600) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			root := privateDir(t)
			b := &fakeBackend{inspection: fixtureInspection()}
			s, e := New(context.Background(), root, b)
			if e != nil {
				t.Fatal(e)
			}
			create(t, s, "create")
			if e = s.Shutdown(context.Background()); e != nil {
				t.Fatal(e)
			}
			if e = change(root); e != nil {
				t.Fatal(e)
			}
			if opened, e := New(context.Background(), root, b); e == nil {
				_ = opened.Shutdown(context.Background())
				t.Fatal("corrupt journal reopened")
			}
			if b.calls.Load() != 0 {
				t.Fatal("corrupt journal executed")
			}
		})
	}
}
func TestProtectedRootAndFileRequirements(t *testing.T) {
	t.Run("root_mode", func(t *testing.T) {
		root := privateDir(t)
		_ = os.Chmod(root, 0755)
		if _, e := New(context.Background(), root, &fakeBackend{}); e == nil {
			t.Fatal("public root accepted")
		}
	})
	t.Run("root_symlink", func(t *testing.T) {
		root := privateDir(t)
		link := filepath.Join(privateDir(t), "link")
		if e := os.Symlink(root, link); e != nil {
			t.Fatal(e)
		}
		if _, e := New(context.Background(), link, &fakeBackend{}); e == nil {
			t.Fatal("symlink root accepted")
		}
	})
	t.Run("ancestor_symlink", func(t *testing.T) {
		root := privateDir(t)
		child := filepath.Join(root, "private")
		_ = os.Mkdir(child, 0700)
		link := filepath.Join(privateDir(t), "link")
		_ = os.Symlink(root, link)
		if _, e := New(context.Background(), filepath.Join(link, "private"), &fakeBackend{}); e == nil {
			t.Fatal("symlink ancestor accepted")
		}
	})
	for _, kind := range []string{"symlink", "hardlink", "mode", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			root := privateDir(t)
			other := filepath.Join(privateDir(t), "file")
			if e := os.WriteFile(other, []byte("{}"), 0600); e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(root, journalName)
			switch kind {
			case "symlink":
				_ = os.Symlink(other, path)
			case "hardlink":
				_ = os.Link(other, path)
			case "mode":
				_ = os.WriteFile(path, []byte("{}"), 0644)
			case "fifo":
				_ = syscall.Mkfifo(path, 0600)
			}
			if _, e := New(context.Background(), root, &fakeBackend{}); e == nil {
				t.Fatal("unsafe journal accepted")
			}
		})
	}
	t.Run("exclusive_lease", func(t *testing.T) {
		s, _, root := newFixture(t)
		if _, e := New(context.Background(), root, &fakeBackend{}); e == nil {
			t.Fatal("second process opened journal")
		}
		_ = s
	})
}
func TestRuntimeJournalReplacementStopsNewClaims(t *testing.T) {
	s, b, root := newFixture(t)
	j := create(t, s, "create")
	path := filepath.Join(root, journalName)
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(path, path+".old"); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if got := start(t, s, j, "start").Error; got != "unavailable" {
		t.Fatal(got)
	}
	if b.calls.Load() != 0 {
		t.Fatal("replaced journal executed")
	}
}
func TestJournalHeadCommitInterruptionFailsClosed(t *testing.T) {
	s, b, _ := newFixture(t)
	// head 임시 이름이 이미 있으면 append는 journal fsync 이후 실패한다.
	s.mu.Lock()
	next := s.journal.sequence + 1
	dir := s.journal.root.path
	s.mu.Unlock()
	name := filepath.Join(dir, ".head-"+strconv.FormatUint(next, 10))
	if e := os.WriteFile(name, []byte("interrupted"), 0600); e != nil {
		t.Fatal(e)
	}
	r := request(Create)
	r.IdempotencyKey = "create"
	r.Input = &Input{TargetURL: "https://example.test/read", ProfileID: "local"}
	if got := call(t, s, r).Error; got != "unavailable" {
		t.Fatal(got)
	}
	crash(t, s)
	if opened, e := New(context.Background(), dir, b); e == nil {
		_ = opened.Shutdown(context.Background())
		t.Fatal("uncommitted journal tail recovered as new claim")
	}
}
