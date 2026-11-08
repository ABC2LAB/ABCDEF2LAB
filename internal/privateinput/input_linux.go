//go:build linux

package privateinput

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type pinnedDirectory struct {
	fds     []int
	parts   []string
	private bool
}

func openDirectory(path string, private bool) (*pinnedDirectory, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.IndexByte(path, 0) >= 0 {
		return nil, ErrUnavailable
	}
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	p := &pinnedDirectory{fds: []int{fd}, private: private}
	if path != "/" {
		p.parts = strings.Split(strings.TrimPrefix(path, "/"), "/")
	}
	for _, part := range p.parts {
		fd, err = unix.Openat(p.fds[len(p.fds)-1], part, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			p.close()
			return nil, ErrUnavailable
		}
		p.fds = append(p.fds, fd)
	}
	for i, fd := range p.fds {
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) != nil || !trustedDirectory(stat) {
			p.close()
			return nil, ErrUnavailable
		}
		if i == len(p.fds)-1 && private && (stat.Uid != uint32(os.Geteuid()) || stat.Mode&07777 != 0700) {
			p.close()
			return nil, ErrUnavailable
		}
		if i == len(p.fds)-1 && !private && stat.Mode&0022 != 0 {
			p.close()
			return nil, ErrUnavailable
		}
	}
	if p.recheck() != nil {
		p.close()
		return nil, ErrUnavailable
	}
	return p, nil
}

func trustedDirectory(s unix.Stat_t) bool {
	return s.Mode&unix.S_IFMT == unix.S_IFDIR && (s.Uid == 0 || s.Uid == uint32(os.Geteuid())) &&
		(s.Mode&0022 == 0 || s.Uid == 0 && s.Mode&unix.S_ISVTX != 0)
}

func (p *pinnedDirectory) recheck() error {
	for i, fd := range p.fds {
		var opened unix.Stat_t
		if unix.Fstat(fd, &opened) != nil || !trustedDirectory(opened) {
			return ErrUnavailable
		}
		if i == len(p.fds)-1 && p.private && (opened.Uid != uint32(os.Geteuid()) || opened.Mode&07777 != 0700) {
			return ErrUnavailable
		}
		if i == len(p.fds)-1 && !p.private && opened.Mode&0022 != 0 {
			return ErrUnavailable
		}
		if i > 0 {
			var atPath unix.Stat_t
			if unix.Fstatat(p.fds[i-1], p.parts[i-1], &atPath, unix.AT_SYMLINK_NOFOLLOW) != nil ||
				atPath.Dev != opened.Dev || atPath.Ino != opened.Ino || atPath.Mode != opened.Mode || atPath.Uid != opened.Uid {
				return ErrUnavailable
			}
		}
	}
	return nil
}

func (p *pinnedDirectory) close() error {
	var failure error
	for i := len(p.fds) - 1; i >= 0; i-- {
		if unix.Close(p.fds[i]) != nil {
			failure = ErrUnavailable
		}
	}
	return failure
}

func CheckDirectory(path string, private bool) error {
	p, err := openDirectory(path, private)
	if err != nil {
		return ErrUnavailable
	}
	if p.close() != nil {
		return ErrUnavailable
	}
	return nil
}

func ReadFile(path string, limit int64) (data []byte, resultErr error) {
	if limit <= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrUnavailable
	}
	p, err := openDirectory(filepath.Dir(path), true)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() {
		if p.close() != nil {
			clear(data)
			data = nil
			resultErr = ErrUnavailable
		}
	}()
	parent := p.fds[len(p.fds)-1]
	fd, err := unix.Openat(parent, filepath.Base(path), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), "preparation-input")
	defer func() {
		if f.Close() != nil {
			clear(data)
			data = nil
			resultErr = ErrUnavailable
		}
	}()
	var before unix.Stat_t
	if unix.Fstat(fd, &before) != nil || !privateFile(before) || before.Size < 1 || before.Size > limit {
		return nil, ErrUnavailable
	}
	data, err = io.ReadAll(io.LimitReader(f, limit+1))
	var after, atPath unix.Stat_t
	if err != nil || int64(len(data)) != before.Size || unix.Fstat(fd, &after) != nil ||
		unix.Fstatat(parent, filepath.Base(path), &atPath, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		!samePrivateFile(before, after) || !samePrivateFile(before, atPath) || p.recheck() != nil {
		clear(data)
		return nil, ErrUnavailable
	}
	return data, nil
}

func privateFile(s unix.Stat_t) bool {
	return s.Mode&unix.S_IFMT == unix.S_IFREG && s.Mode&07777 == 0600 && s.Uid == uint32(os.Geteuid()) && s.Nlink == 1
}

func samePrivateFile(a, b unix.Stat_t) bool {
	return privateFile(b) && a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Uid == b.Uid && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}
