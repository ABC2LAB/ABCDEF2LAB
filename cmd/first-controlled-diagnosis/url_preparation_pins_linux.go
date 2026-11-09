//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// 송신 gate에서는 큰 helper를 재해시하지 않고 준비 때 확보한 FD·경로·metadata를 검사한다.
// 파일 내용은 최초 FD 획득 때 SHA256으로 확인하며, 이후 쓰기·교체는 ctime/inode로 거부한다.
type urlHeldInput struct {
	file           *os.File
	stat           unix.Stat_t
	directories    []*os.File
	directoryStats []unix.Stat_t
	parts          []string
	name           string
}
type urlHeldInputs struct {
	mu     sync.RWMutex
	files  []urlHeldInput
	closed bool
}

func urlFileStatSame(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Uid == b.Uid && a.Gid == b.Gid && a.Nlink == b.Nlink && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}
func urlTrustedDir(s unix.Stat_t) bool {
	return s.Mode&unix.S_IFMT == unix.S_IFDIR && (s.Uid == 0 || s.Uid == urlOperatorUID()) && (s.Mode&0022 == 0 || s.Uid == 0 && s.Mode&unix.S_ISVTX != 0)
}
func urlOpenHeldInput(pin urlInputPin) (out urlHeldInput, err error) {
	failed := func() (urlHeldInput, error) { out.close(); return urlHeldInput{}, errPreparation }
	if !filepath.IsAbs(pin.path) || filepath.Clean(pin.path) != pin.path {
		return failed()
	}
	parent := filepath.Dir(pin.path)
	root, e := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return failed()
	}
	out.directories = append(out.directories, os.NewFile(uintptr(root), "url-installation-root"))
	if parent != "/" {
		out.parts = strings.Split(strings.TrimPrefix(parent, "/"), "/")
	}
	for _, part := range out.parts {
		fd, e := unix.Openat(int(out.directories[len(out.directories)-1].Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			return failed()
		}
		out.directories = append(out.directories, os.NewFile(uintptr(fd), "url-installation-directory"))
	}
	for _, directory := range out.directories {
		var stat unix.Stat_t
		if unix.Fstat(int(directory.Fd()), &stat) != nil || !urlTrustedDir(stat) {
			return failed()
		}
		out.directoryStats = append(out.directoryStats, stat)
	}
	parentInfo, e := out.directories[len(out.directories)-1].Stat()
	if e != nil || !os.SameFile(pin.parentInfo, parentInfo) {
		return failed()
	}
	out.name = filepath.Base(pin.path)
	fd, e := unix.Openat(int(out.directories[len(out.directories)-1].Fd()), out.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if e != nil {
		return failed()
	}
	out.file = os.NewFile(uintptr(fd), "url-installation-input")
	if unix.Fstat(fd, &out.stat) != nil {
		return failed()
	}
	s := out.stat
	if s.Mode&unix.S_IFMT != unix.S_IFREG || s.Nlink != 1 || s.Size < 1 || s.Size > pin.limit {
		return failed()
	}
	if pin.executable {
		if s.Size > 64<<20 || s.Mode&0022 != 0 || s.Mode&0111 == 0 || (s.Uid != 0 && s.Uid != urlOperatorUID()) {
			return failed()
		}
	} else if s.Mode&07777 != 0600 || s.Uid != urlOperatorUID() {
		return failed()
	}
	info, e := out.file.Stat()
	if e != nil || !os.SameFile(pin.info, info) {
		return failed()
	}
	h := sha256.New()
	n, e := io.Copy(h, io.NewSectionReader(out.file, 0, s.Size+1))
	if e != nil || n != s.Size || hex.EncodeToString(h.Sum(nil)) != pin.digest || out.check() != nil {
		return failed()
	}
	return out, nil
}
func (p *urlHeldInput) check() error {
	for i, dir := range p.directories {
		var held, at unix.Stat_t
		expected := p.directoryStats[i]
		if unix.Fstat(int(dir.Fd()), &held) != nil || !urlTrustedDir(held) || expected.Dev != held.Dev || expected.Ino != held.Ino || expected.Mode != held.Mode || expected.Uid != held.Uid || expected.Gid != held.Gid {
			return errPreparation
		}
		if i > 0 {
			if unix.Fstatat(int(p.directories[i-1].Fd()), p.parts[i-1], &at, unix.AT_SYMLINK_NOFOLLOW) != nil || held.Dev != at.Dev || held.Ino != at.Ino || held.Mode != at.Mode || held.Uid != at.Uid || held.Gid != at.Gid {
				return errPreparation
			}
		}
	}
	var held, at unix.Stat_t
	if unix.Fstat(int(p.file.Fd()), &held) != nil || unix.Fstatat(int(p.directories[len(p.directories)-1].Fd()), p.name, &at, unix.AT_SYMLINK_NOFOLLOW) != nil || !urlFileStatSame(p.stat, held) || !urlFileStatSame(p.stat, at) {
		return errPreparation
	}
	return nil
}
func (p *urlHeldInput) close() {
	if p.file != nil {
		_ = p.file.Close()
		p.file = nil
	}
	for i := len(p.directories) - 1; i >= 0; i-- {
		_ = p.directories[i].Close()
	}
	p.directories = nil
}
func (h *urlHeldInputs) check() error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return errPreparation
	}
	for i := range h.files {
		if h.files[i].check() != nil {
			return errPreparation
		}
	}
	return nil
}
func (h *urlHeldInputs) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for i := range h.files {
		h.files[i].close()
	}
}
func (l *urlLoadedProfile) hold() error {
	h := &urlHeldInputs{}
	for _, pin := range l.pins {
		file, err := urlOpenHeldInput(pin)
		if err != nil {
			h.close()
			return errPreparation
		}
		h.files = append(h.files, file)
	}
	l.heldCheck = h.check
	l.releaseInputs = h.close
	return nil
}
