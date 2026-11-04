//go:build linux

package llmpolicy

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestManifestFileOwnershipAndModes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stat    unix.Stat_t
		accepts func(unix.Stat_t) bool
		modes   []uint32
	}{
		{"directory", unix.Stat_t{Mode: unix.S_IFDIR | 0700, Uid: uint32(os.Geteuid())}, ownedManifestDirectory, []uint32{0500, 02700, 01700}},
		{"file", unix.Stat_t{Mode: unix.S_IFREG | 0600, Uid: uint32(os.Geteuid()), Nlink: 1}, ownedManifestFile, []uint32{0400, 02600, 04600}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.accepts(tc.stat) {
				t.Fatal("valid ownership and exact mode rejected")
			}
			t.Run("wrong owner", func(t *testing.T) {
				other := tc.stat
				other.Uid++
				if tc.accepts(other) {
					t.Fatal("different effective UID accepted")
				}
			})
			for _, mode := range tc.modes {
				t.Run(fmt.Sprintf("mode %04o", mode), func(t *testing.T) {
					other := tc.stat
					other.Mode = other.Mode&unix.S_IFMT | mode
					if tc.accepts(other) {
						t.Fatal("inexact permission or special mode accepted")
					}
				})
			}
		})
	}
}

func TestSameManifestFileRejectsTimestampChanges(t *testing.T) {
	before := unix.Stat_t{
		Dev: 1, Ino: 2, Mode: unix.S_IFREG | 0600, Uid: uint32(os.Geteuid()), Nlink: 1, Size: 100,
		Mtim: unix.Timespec{Sec: 10, Nsec: 20}, Ctim: unix.Timespec{Sec: 30, Nsec: 40},
	}
	if !sameManifestFile(before, before) {
		t.Fatal("identical trusted metadata rejected")
	}
	for name, change := range map[string]func(*unix.Stat_t){
		"mtime seconds":     func(st *unix.Stat_t) { st.Mtim.Sec++ },
		"mtime nanoseconds": func(st *unix.Stat_t) { st.Mtim.Nsec++ },
		"ctime seconds":     func(st *unix.Stat_t) { st.Ctim.Sec++ },
		"ctime nanoseconds": func(st *unix.Stat_t) { st.Ctim.Nsec++ },
	} {
		t.Run(name, func(t *testing.T) {
			after := before
			change(&after)
			if sameManifestFile(before, after) {
				t.Fatal("changed timestamp accepted")
			}
		})
	}
}

func TestLoadProfileManifestFileRechecksAfterRead(t *testing.T) {
	for _, mutate := range []bool{false, true} {
		t.Run(fmt.Sprintf("mutate=%t", mutate), func(t *testing.T) {
			_, path := manifestFileFixture(t)
			body := []byte(manifestValid + " ")
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			// Force a known prior mtime; detection must not depend on clock resolution or sleeps.
			prior := time.Unix(1, 0)
			if err := os.Chtimes(path, prior, prior); err != nil {
				t.Fatal(err)
			}
			read := false
			resolver, err := loadProfileManifestFile(path, func(reader io.Reader) ([]byte, error) {
				data, err := io.ReadAll(reader)
				if err != nil {
					return nil, err
				}
				read = true
				if !bytes.Equal(data, body) {
					t.Fatal("bounded reader did not return the complete fixture")
				}
				if mutate {
					var before, after unix.Stat_t
					if err := unix.Stat(path, &before); err != nil {
						t.Fatal(err)
					}
					file, err := os.OpenFile(path, os.O_WRONLY, 0)
					if err != nil {
						t.Fatal(err)
					}
					written, writeErr := file.WriteAt([]byte("\t"), int64(len(body)-1))
					closeErr := file.Close()
					if writeErr != nil || closeErr != nil || written != 1 {
						t.Fatalf("fixture write: bytes=%d, write=%v, close=%v", written, writeErr, closeErr)
					}
					if err := unix.Stat(path, &after); err != nil {
						t.Fatal(err)
					}
					if before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode ||
						before.Uid != after.Uid || before.Nlink != after.Nlink || before.Size != after.Size || before.Mtim == after.Mtim {
						t.Fatal("fixture must change mtime while preserving inode, size and trust metadata")
					}
					current, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := ParseProfileManifest(current); err != nil {
						t.Fatal("modified fixture must remain a valid manifest")
					}
				}
				return data, nil
			})
			if !read {
				t.Fatal("loader did not reach the bounded read")
			}
			if mutate {
				if resolver != nil || err != ErrInvalidProfileManifestFile {
					t.Fatalf("changed file must return nil and fixed error, got %v %v", resolver, err)
				}
			} else if resolver == nil || err != nil {
				t.Fatalf("unchanged file rejected: %v", err)
			}
		})
	}
}
