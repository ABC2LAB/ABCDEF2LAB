//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Autumn-27/artex/budgetlocal"
	"golang.org/x/sys/unix"
)

func openPrivateOperatorInput(path string) (*os.File, error) {
	uid := uint32(os.Geteuid())
	directory, err := openPrivateOperatorDirectory(filepath.Dir(path), uid)
	if err != nil {
		return nil, err
	}
	// Check the opened inode instead of a pathname that can be replaced.
	// NONBLOCK lets fstat reject a FIFO without waiting for a writer.
	fd, openErr := syscall.Openat(int(directory.Fd()), filepath.Base(path),
		syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	closeErr := directory.Close()
	if openErr != nil {
		return nil, budgetlocal.ErrUnavailable
	}
	file := os.NewFile(uintptr(fd), "operator-input")
	if closeErr != nil {
		file.Close()
		return nil, budgetlocal.ErrUnavailable
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		file.Close()
		return nil, budgetlocal.ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid || stat.Nlink != 1 {
		file.Close()
		return nil, budgetlocal.ErrUnavailable
	}
	return file, nil
}

func openPrivateOperatorDirectory(dir string, uid uint32) (*os.File, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, budgetlocal.ErrInvalid
	}
	fd, err := syscall.Open(string(filepath.Separator), unix.O_PATH|syscall.O_CLOEXEC|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, budgetlocal.ErrUnavailable
	}
	directory := os.NewFile(uintptr(fd), "operator-directory")
	info, err := directory.Stat()
	if err != nil {
		directory.Close()
		return nil, budgetlocal.ErrUnavailable
	}
	infos := map[string]os.FileInfo{string(filepath.Separator): info}
	current := string(filepath.Separator)
	for _, name := range strings.Split(strings.TrimPrefix(dir, current), string(filepath.Separator)) {
		if name == "" {
			continue
		}
		// Pin each directory and reject symlinks in every path component.
		fd, openErr := syscall.Openat(int(directory.Fd()), name,
			unix.O_PATH|syscall.O_CLOEXEC|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
		closeErr := directory.Close()
		if openErr != nil {
			return nil, budgetlocal.ErrUnavailable
		}
		directory = os.NewFile(uintptr(fd), "operator-directory")
		if closeErr != nil {
			directory.Close()
			return nil, budgetlocal.ErrUnavailable
		}
		info, err := directory.Stat()
		if err != nil {
			directory.Close()
			return nil, budgetlocal.ErrUnavailable
		}
		current = filepath.Join(current, name)
		infos[current] = info
	}
	if !trustedPrivateRequestDirectoryWithLstat(dir, uid, func(path string) (os.FileInfo, error) {
		info, ok := infos[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return info, nil
	}) {
		directory.Close()
		return nil, budgetlocal.ErrUnavailable
	}
	return directory, nil
}
