//go:build linux

package diagnosticlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCreateProtectedFile(t *testing.T) {
	d := t.TempDir()
	l, err := Create(d, "run", 42, "../../private-run?token=secret")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Path()
	if filepath.Dir(p) != filepath.Join(d, "diagnostic-logs", "task-42") {
		t.Fatal("path")
	}
	if l.Record(testEvent()) != nil || l.Close() != nil {
		t.Fatal("write")
	}
	for _, path := range []string{filepath.Join(d, "diagnostic-logs"), filepath.Dir(p)} {
		i, e := os.Stat(path)
		if e != nil || i.Mode().Perm() != 0700 {
			t.Fatal("directory protection")
		}
	}
	i, e := os.Stat(p)
	if e != nil || i.Mode().Perm() != 0600 {
		t.Fatal("file protection")
	}
	raw, e := os.ReadFile(p)
	var ev Event
	if e != nil || json.Unmarshal(raw, &ev) != nil || ev.TaskID != 42 || len(ev.RunRef) != 64 {
		t.Fatal("event")
	}
	l2, e := Create(d, "run", 42, "same")
	if e != nil {
		t.Fatal(e)
	}
	defer l2.Close()
	if l2.Path() == p {
		t.Fatal("overwrite")
	}
}
func TestProtectedPathRejections(t *testing.T) {
	for _, kind := range []string{"symlink", "permissions", "escape", "repository", "public", "untrusted_parent"} {
		t.Run(kind, func(t *testing.T) {
			d := t.TempDir()
			switch kind {
			case "symlink":
				if os.Symlink(d, filepath.Join(d, "link")) != nil {
					t.Fatal("fixture")
				}
				d = filepath.Join(d, "link")
			case "permissions":
				p := filepath.Join(d, "diagnostic-logs")
				if os.Mkdir(p, 0755) != nil {
					t.Fatal("fixture")
				}
			case "escape":
				d = d + "/../escape"
			case "repository":
				if os.Mkdir(filepath.Join(d, ".git"), 0700) != nil {
					t.Fatal("fixture")
				}
			case "public":
				d = filepath.Join(d, "public")
				os.Mkdir(d, 0700)
			case "untrusted_parent":
				os.Chmod(d, 0777)
			}
			if l, e := Create(d, "run", 1, ""); !errors.Is(e, ErrCreate) {
				if l != nil {
					l.Close()
				}
				t.Fatal("unsafe path accepted")
			}
		})
	}
}
func TestMutationAndHardlinkFailSticky(t *testing.T) {
	for _, kind := range []string{"hardlink", "file_mode", "directory_mode", "rename", "append"} {
		t.Run(kind, func(t *testing.T) {
			d := t.TempDir()
			l, e := Create(d, "run", 1, "")
			if e != nil {
				t.Fatal(e)
			}
			defer l.Close()
			p := l.Path()
			switch kind {
			case "hardlink":
				e = os.Link(p, filepath.Join(d, "extra"))
			case "file_mode":
				e = os.Chmod(p, 0644)
			case "directory_mode":
				e = os.Chmod(filepath.Dir(p), 0755)
			case "append":
				f, openErr := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
				if openErr != nil {
					t.Fatal(openErr)
				}
				_, e = f.WriteString("external")
				f.Close()
			case "rename":
				e = os.Rename(p, p+"-moved")
			}
			if e != nil {
				t.Fatal(e)
			}
			if !errors.Is(l.Record(testEvent()), ErrWrite) || !errors.Is(l.Err(), ErrWrite) {
				t.Fatal("mutation accepted")
			}
		})
	}
}

func TestActualConcurrentFile(t *testing.T) {
	l, err := Create(t.TempDir(), "run", 42, "actual-run")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if l.Record(testEvent()) != nil {
					t.Error("record")
				}
			}
		}()
	}
	wg.Wait()
	raw, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	if len(lines) != 200 {
		t.Fatal("live visibility")
	}
	var previous time.Time
	for i, line := range lines {
		var e Event
		if json.Unmarshal(line, &e) != nil || e.Sequence != uint64(i+1) || e.TaskID != 42 || e.Timestamp.Before(previous) {
			t.Fatal("sequence")
		}
		previous = e.Timestamp
	}
	if l.Close() != nil {
		t.Fatal("close")
	}
}
func TestReferenceSecretsHashed(t *testing.T) {
	l, err := Create(t.TempDir(), "run", 42, "postgres://name:password@host/private?secret=token")
	if err != nil {
		t.Fatal(err)
	}
	e := testEvent()
	e.CallRef = "Bearer raw-key"
	e.AttemptRef = "Cookie: session=private"
	e.ModelRef = "private prompt"
	e.ProviderRef = "private provider"
	if l.Record(e) != nil || l.Close() != nil {
		t.Fatal("write")
	}
	raw, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"postgres:", "password", "Bearer", "Cookie", "private prompt", "private provider"} {
		if bytes.Contains(raw, []byte(v)) {
			t.Fatal("secret leaked")
		}
	}
}
