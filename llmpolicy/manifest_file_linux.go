//go:build linux

package llmpolicy

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// ErrInvalidProfileManifestFile does not expose the path, policy bytes, or an
// underlying filesystem or parser error.
var ErrInvalidProfileManifestFile = errors.New("llmpolicy: invalid profile manifest file")

// LoadProfileManifestFile reads a host-selected, absolute manifest path on
// Linux. The containing directory and file must be owned by the effective UID
// with exact modes 0700 and 0600. Every path component is opened without
// following symlinks; the file must be regular and have exactly one hard link.
// It only returns a resolver. Installing it and invalidating old providers is
// the host's responsibility.
func LoadProfileManifestFile(path string) (*ProfileResolver, error) {
	return loadProfileManifestFile(path, io.ReadAll)
}

// Keep the read dependency call-scoped so rechecks can be tested without shared hooks.
func loadProfileManifestFile(path string, readAll func(io.Reader) ([]byte, error)) (*ProfileResolver, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.IndexByte(path, 0) >= 0 {
		return nil, ErrInvalidProfileManifestFile
	}
	dir, name := filepath.Split(path)
	if name == "" || name == "." || name == ".." {
		return nil, ErrInvalidProfileManifestFile
	}

	root, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrInvalidProfileManifestFile
	}
	fds := []int{root}
	defer func() {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}()
	parts := strings.Split(strings.Trim(dir, "/"), "/")
	if dir == "/" {
		parts = nil
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, ErrInvalidProfileManifestFile
		}
		fd, err := unix.Openat(fds[len(fds)-1], part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, ErrInvalidProfileManifestFile
		}
		fds = append(fds, fd)
	}
	parent := fds[len(fds)-1]
	var dirBefore unix.Stat_t
	if unix.Fstat(parent, &dirBefore) != nil || !ownedManifestDirectory(dirBefore) {
		return nil, ErrInvalidProfileManifestFile
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrInvalidProfileManifestFile
	}
	file := os.NewFile(uintptr(fd), "profile manifest")
	defer file.Close()
	var before unix.Stat_t
	if unix.Fstat(fd, &before) != nil || !ownedManifestFile(before) || before.Size < 1 || before.Size > MaxProfileManifestBytes {
		return nil, ErrInvalidProfileManifestFile
	}
	data, err := readAll(io.LimitReader(file, MaxProfileManifestBytes+1))
	if err != nil || len(data) != int(before.Size) {
		return nil, ErrInvalidProfileManifestFile
	}
	var after, atPath, dirAfter unix.Stat_t
	if unix.Fstat(fd, &after) != nil || unix.Fstatat(parent, name, &atPath, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		unix.Fstat(parent, &dirAfter) != nil || !sameManifestFile(before, after) || !sameManifestFile(before, atPath) ||
		!sameManifestDirectory(dirBefore, dirAfter) {
		return nil, ErrInvalidProfileManifestFile
	}
	// Ensure a path component was not replaced while the bounded read ran.
	for i, part := range parts {
		var atPath, opened unix.Stat_t
		if unix.Fstatat(fds[i], part, &atPath, unix.AT_SYMLINK_NOFOLLOW) != nil ||
			unix.Fstat(fds[i+1], &opened) != nil || atPath.Dev != opened.Dev || atPath.Ino != opened.Ino ||
			atPath.Mode&unix.S_IFMT != unix.S_IFDIR {
			return nil, ErrInvalidProfileManifestFile
		}
	}
	resolver, err := ParseProfileManifest(data)
	if err != nil {
		return nil, ErrInvalidProfileManifestFile
	}
	return resolver, nil
}

func ownedManifestDirectory(st unix.Stat_t) bool {
	return st.Mode&unix.S_IFMT == unix.S_IFDIR && st.Mode&07777 == 0700 && st.Uid == uint32(os.Geteuid())
}

func ownedManifestFile(st unix.Stat_t) bool {
	return st.Mode&unix.S_IFMT == unix.S_IFREG && st.Mode&07777 == 0600 && st.Uid == uint32(os.Geteuid()) && st.Nlink == 1
}

func sameManifestDirectory(a, b unix.Stat_t) bool {
	return ownedManifestDirectory(b) && a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Uid == b.Uid
}

func sameManifestFile(a, b unix.Stat_t) bool {
	return ownedManifestFile(b) && a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Uid == b.Uid &&
		a.Nlink == b.Nlink && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}
