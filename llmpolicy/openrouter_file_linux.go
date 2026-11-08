//go:build linux

package llmpolicy

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// ErrInvalidOpenRouterManifestFile does not expose the path, policy bytes, or an
// underlying filesystem or parser error.
var ErrInvalidOpenRouterManifestFile = errors.New("llmpolicy: invalid profile manifest file")

// LoadOpenRouterManifestFile reads a host-selected, absolute manifest path on
// Linux. The containing directory and file must be owned by the effective UID
// with exact modes 0700 and 0600. Every path component is opened without
// following symlinks; the file must be regular and have exactly one hard link.
// It only returns a resolver. Installing it and invalidating old providers is
// the host's responsibility.
func LoadOpenRouterManifestFile(path string) (*OpenRouterPolicy, error) {
	return loadOpenRouterManifestFile(path, io.ReadAll)
}

// Keep the read dependency call-scoped so rechecks can be tested without shared hooks.
func loadOpenRouterManifestFile(path string, readAll func(io.Reader) ([]byte, error)) (*OpenRouterPolicy, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.IndexByte(path, 0) >= 0 {
		return nil, ErrInvalidOpenRouterManifestFile
	}
	dir, name := filepath.Split(path)
	if name == "" || name == "." || name == ".." {
		return nil, ErrInvalidOpenRouterManifestFile
	}

	root, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrInvalidOpenRouterManifestFile
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
			return nil, ErrInvalidOpenRouterManifestFile
		}
		fd, err := unix.Openat(fds[len(fds)-1], part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, ErrInvalidOpenRouterManifestFile
		}
		fds = append(fds, fd)
	}
	parent := fds[len(fds)-1]
	var dirBefore unix.Stat_t
	if unix.Fstat(parent, &dirBefore) != nil || !ownedManifestDirectory(dirBefore) {
		return nil, ErrInvalidOpenRouterManifestFile
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrInvalidOpenRouterManifestFile
	}
	file := os.NewFile(uintptr(fd), "profile manifest")
	defer file.Close()
	var before unix.Stat_t
	if unix.Fstat(fd, &before) != nil || !ownedManifestFile(before) || before.Size < 1 || before.Size > MaxProfileManifestBytes {
		return nil, ErrInvalidOpenRouterManifestFile
	}
	data, err := readAll(io.LimitReader(file, MaxProfileManifestBytes+1))
	if err != nil || len(data) != int(before.Size) {
		return nil, ErrInvalidOpenRouterManifestFile
	}
	var after, atPath, dirAfter unix.Stat_t
	if unix.Fstat(fd, &after) != nil || unix.Fstatat(parent, name, &atPath, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		unix.Fstat(parent, &dirAfter) != nil || !sameManifestFile(before, after) || !sameManifestFile(before, atPath) ||
		!sameManifestDirectory(dirBefore, dirAfter) {
		return nil, ErrInvalidOpenRouterManifestFile
	}
	// Ensure a path component was not replaced while the bounded read ran.
	for i, part := range parts {
		var atPath, opened unix.Stat_t
		if unix.Fstatat(fds[i], part, &atPath, unix.AT_SYMLINK_NOFOLLOW) != nil ||
			unix.Fstat(fds[i+1], &opened) != nil || atPath.Dev != opened.Dev || atPath.Ino != opened.Ino ||
			atPath.Mode&unix.S_IFMT != unix.S_IFDIR {
			return nil, ErrInvalidOpenRouterManifestFile
		}
	}
	resolver, err := ParseOpenRouterManifest(data)
	if err != nil {
		return nil, ErrInvalidOpenRouterManifestFile
	}
	resolver.consume = func() error {
		// 승인 ID당 최초 송신만 허용한다. 재시작 후 예산 재사용을 차단한다.
		fresh, err := openRouterProtectedDirectory(dir)
		if err != nil {
			return ErrOpenRouterPolicy
		}
		defer unix.Close(fresh)
		var st unix.Stat_t
		if unix.Fstat(fresh, &st) != nil || !sameManifestDirectory(dirBefore, st) {
			return ErrOpenRouterPolicy
		}
		marker := fmt.Sprintf(".openrouter-%x.used", sha256.Sum256([]byte(resolver.manifest.ApprovalID)))
		mark, err := unix.Openat(fresh, marker, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if err != nil {
			return ErrOpenRouterPolicy
		}
		syncErr := unix.Fsync(mark)
		closeErr := unix.Close(mark)
		if syncErr != nil || closeErr != nil || unix.Fsync(fresh) != nil {
			return ErrOpenRouterPolicy
		}
		return nil
	}
	return resolver, nil
}

// 경로의 모든 조상을 다시 열어 소비 시점의 심볼릭 링크 교체도 차단한다.
func openRouterProtectedDirectory(dir string) (int, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, ErrOpenRouterPolicy
	}
	for _, part := range strings.Split(strings.Trim(dir, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			_ = unix.Close(fd)
			return -1, ErrOpenRouterPolicy
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			return -1, ErrOpenRouterPolicy
		}
		fd = next
	}
	return fd, nil
}
