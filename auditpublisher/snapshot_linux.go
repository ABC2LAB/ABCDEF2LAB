//go:build linux

package auditpublisher

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// journalRoot holds a second descriptor for the host's long-lived root anchor.
// The host must already have proved deployment provenance and exclusive writer
// scope; checking an inode, UID and 0700 mode cannot create that authority.
type journalRoot struct {
	path string
	info os.FileInfo
	dir  *os.File
}

// snapshotFile keeps the read/stat boundary injectable without exposing a public capability.
type snapshotFile interface {
	io.ReaderAt
	Stat() (os.FileInfo, error)
	Close() error
}

type journalSnapshot struct {
	root *journalRoot
	name string
	file snapshotFile
	info os.FileInfo
	max  int64
}

func openJournalRoot(path string, anchor *os.File) (*journalRoot, error) {
	if anchor == nil || path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrConfig
	}
	trusted, err := anchor.Stat()
	if err != nil || !validDirectory(trusted) {
		return nil, ErrConfig
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	dir := os.NewFile(uintptr(fd), "auditpublisher-root")
	opened, err := dir.Stat()
	if err != nil || !validDirectory(opened) || !os.SameFile(trusted, opened) {
		logCloseFailure("root-open-close", dir.Close())
		return nil, ErrUnavailable
	}
	return &journalRoot{path: path, info: trusted, dir: dir}, nil
}

func validDirectory(info os.FileInfo) bool {
	if info == nil || info.Mode() != os.ModeDir|0o700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && stat.Nlink > 0
}

func validFile(info os.FileInfo, max int64) bool {
	if info == nil || info.Mode() != 0o600 || info.Size() < 0 || info.Size() > max {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1
}

func (r *journalRoot) current() error {
	if r == nil || r.dir == nil {
		return ErrUnavailable
	}
	info, err := r.dir.Stat()
	if err != nil || !validDirectory(info) || !os.SameFile(r.info, info) {
		return ErrUnavailable
	}
	fd, err := syscall.Open(r.path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return ErrUnavailable
	}
	current := os.NewFile(uintptr(fd), "auditpublisher-current-root")
	info, err = current.Stat()
	closeErr := current.Close()
	logCloseFailure("snapshot-current-close", closeErr)
	if err != nil || closeErr != nil || !validDirectory(info) || !os.SameFile(r.info, info) {
		return ErrUnavailable
	}
	return nil
}

func (r *journalRoot) attach(name string, max int64) (*journalSnapshot, error) {
	if r.current() != nil || name == "" || name == "." || name == ".." || filepath.Base(name) != name || max <= 0 {
		return nil, ErrUnavailable
	}
	fd, err := syscall.Openat(int(r.dir.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	file := os.NewFile(uintptr(fd), "auditpublisher-journal-snapshot")
	info, err := file.Stat()
	if err != nil || !validFile(info, max) || r.current() != nil {
		logCloseFailure("snapshot-attach-close", file.Close())
		return nil, ErrUnavailable
	}
	return &journalSnapshot{root: r, name: name, file: file, info: info, max: max}, nil
}

func (s *journalSnapshot) current() error {
	if s == nil || s.file == nil || s.root.current() != nil {
		return ErrUnavailable
	}
	info, err := s.file.Stat()
	if err != nil || !validFile(info, s.max) || !os.SameFile(s.info, info) {
		return ErrUnavailable
	}
	fd, err := syscall.Openat(int(s.root.dir.Fd()), s.name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return ErrUnavailable
	}
	current := os.NewFile(uintptr(fd), "auditpublisher-current-journal")
	currentInfo, statErr := current.Stat()
	closeErr := current.Close()
	logCloseFailure("snapshot-current-close", closeErr)
	if statErr != nil || closeErr != nil || !validFile(currentInfo, s.max) || !os.SameFile(info, currentInfo) {
		return ErrUnavailable
	}
	return nil
}

func (s *journalSnapshot) read(ctx context.Context) ([]byte, error) {
	if s == nil || s.file == nil || ctx == nil || ctx.Err() != nil || s.current() != nil {
		return nil, ErrUnavailable
	}
	before, err := s.file.Stat()
	if err != nil || !validFile(before, s.max) || !os.SameFile(s.info, before) {
		return nil, ErrUnavailable
	}
	owned := make([]byte, int(before.Size()))
	if _, err := io.ReadFull(io.NewSectionReader(s.file, 0, before.Size()), owned); err != nil {
		clear(owned)
		return nil, ErrUnavailable
	}
	after, err := s.file.Stat()
	if err != nil || !validFile(after, s.max) || !os.SameFile(before, after) ||
		after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) ||
		s.current() != nil || ctx.Err() != nil {
		clear(owned)
		return nil, ErrUnavailable
	}
	return owned, nil
}

func (s *journalSnapshot) close() error {
	if s == nil || s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func (r *journalRoot) close() error {
	if r == nil || r.dir == nil {
		return nil
	}
	err := r.dir.Close()
	r.dir = nil
	if err != nil {
		return ErrUnavailable
	}
	return nil
}
