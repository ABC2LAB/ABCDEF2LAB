package main

import (
	"os"
	"path/filepath"
	"syscall"
)

func openRegular(path string, private bool) (*os.File, error) {
	parent, name := filepath.Split(path)
	if name == "" {
		return nil, errInput
	}
	if parent == "" {
		parent = "."
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, errInput
	}
	directory, openErr := root.Open(".")
	closeErr := root.Close()
	if openErr != nil {
		return nil, errInput
	}
	if closeErr != nil {
		directory.Close()
		return nil, errInput
	}
	// Pin the parent directory, reject the final symlink at open, and avoid
	// blocking on a FIFO before we can check its type. Root.Open follows links.
	fd, openErr := syscall.Openat(int(directory.Fd()), name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	closeErr = directory.Close()
	if openErr != nil {
		return nil, errInput
	}
	file := os.NewFile(uintptr(fd), "audit-input")
	if closeErr != nil {
		file.Close()
		return nil, errInput
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || private && info.Mode().Perm() != 0o600 {
		file.Close()
		return nil, errInput
	}
	return file, nil
}
