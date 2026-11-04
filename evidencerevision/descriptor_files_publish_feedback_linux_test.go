//go:build linux

package evidencerevision

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDescriptorFilesRechecksPublishedInodeBeforeAcknowledging(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "same_bytes_different_inode"}[replace], func(t *testing.T) {
			store, host, descriptor := descriptorFixture(t)
			body, err := descriptorFileBytes(descriptor)
			if err != nil {
				t.Fatal(err)
			}
			path := descriptorPath(host, descriptor)
			acknowledged := false
			store.fault = func(stage string) error {
				if stage == "ack" {
					acknowledged = true
				}
				if stage != "dir_sync" || !replace {
					return nil
				}
				before, err := os.Stat(path)
				if err != nil {
					t.Fatal("published file missing")
				}
				if os.Rename(path, path+".original") != nil || os.WriteFile(path, body, 0o600) != nil {
					t.Fatal("owned replacement fixture")
				}
				after, err := os.Stat(path)
				if err != nil || !descriptorFile(after) || os.SameFile(before, after) {
					t.Fatal("replacement must be a different private regular inode")
				}
				return nil
			}
			err = store.Put(t.Context(), descriptor)
			if !replace {
				if err != nil || !acknowledged {
					t.Fatal("unchanged descriptor was not acknowledged")
				}
				return
			}
			var failure descriptorFileError
			if !errors.Is(err, ErrDescriptorFileUnknown) || !errors.As(err, &failure) || failure.stage != "published_metadata" || acknowledged {
				t.Fatalf("published inode replacement must fail before ACK: %v", err)
			}
			for _, retained := range []string{path, path + ".original"} {
				got, err := os.ReadFile(retained)
				if err != nil || !bytes.Equal(got, body) {
					t.Fatal("uncertain publication artifacts changed")
				}
			}
		})
	}
}

// Replacing only an owned operation FD with an unsyncable pipe makes the real
// Sync fail while Close still succeeds. Exact stage checks distinguish Sync from
// later metadata checks, which would otherwise hide a removed syscall.
func TestDescriptorFilesActualSyncErrorsAreNotMaskedByLaterChecks(t *testing.T) {
	for _, stage := range []string{"file_sync", "dir_sync", "retry_file_sync", "retry_dir_sync"} {
		t.Run(stage, func(t *testing.T) {
			store, host, descriptor := descriptorFixture(t)
			if strings.HasPrefix(stage, "retry_") {
				if err := store.Put(t.Context(), descriptor); err != nil {
					t.Fatal(err)
				}
			}
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal("owned pipe fixture")
			}
			defer reader.Close()
			defer writer.Close()
			injected := false
			store.fault = func(at string) error {
				if at != stage {
					return nil
				}
				if injected {
					t.Fatal("sync stage repeated")
				}
				injected = true
				directory := strings.HasSuffix(stage, "dir_sync")
				fd := ownedDescriptorOperationFD(t, host, descriptor, directory)
				if err := unix.Dup3(int(reader.Fd()), fd, unix.O_CLOEXEC); err != nil {
					t.Fatal("replace owned operation FD")
				}
				return nil
			}
			err = store.Put(t.Context(), descriptor)
			var failure descriptorFileError
			if !injected || !errors.Is(err, ErrDescriptorFileUnknown) || !errors.As(err, &failure) || failure.stage != stage {
				t.Fatalf("actual Sync error must remain at %s: %v", stage, err)
			}
		})
	}
}

func ownedDescriptorOperationFD(t *testing.T, host *descriptorTestHost, descriptor Descriptor, directory bool) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal("owned FD inspection")
	}
	var found []int
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil || uintptr(fd) == host.anchor.Fd() {
			continue
		}
		name, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err != nil {
			continue
		}
		match := name == host.dir
		if !directory {
			path := descriptorPath(host, descriptor)
			match = name == path || strings.HasPrefix(name, path+".pending-")
		}
		if !match {
			continue
		}
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) != nil {
			t.Fatal("owned FD metadata")
		}
		expected := uint32(unix.S_IFREG)
		if directory {
			expected = unix.S_IFDIR
		}
		if stat.Mode&unix.S_IFMT != expected {
			t.Fatal("owned FD kind")
		}
		found = append(found, fd)
	}
	if len(found) != 1 {
		t.Fatalf("expected one owned operation FD, got %d", len(found))
	}
	return found[0]
}
