//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// writeNew는 현재 사용자 소유 0700 디렉터리에 새 0600 파일만 쓴다. 있는 파일은 덮어쓰지 않는다.
func writeNew(path string, data []byte) string {
	directory, err := syscall.Open(filepath.Dir(path), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return privateDirRequired
	}
	defer syscall.Close(directory)
	var info syscall.Stat_t
	if syscall.Fstat(directory, &info) != nil || info.Uid != uint32(os.Geteuid()) || info.Mode&07777 != 0700 {
		return privateDirRequired
	}
	fd, err := syscall.Openat(directory, filepath.Base(path), syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if errors.Is(err, syscall.EEXIST) {
		return alreadyExists
	}
	if err != nil {
		return storageUnavailable
	}
	file := os.NewFile(uintptr(fd), "e2-eval-policy-output")
	n, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || n != len(data) || syncErr != nil || closeErr != nil || syscall.Fsync(directory) != nil {
		return storageUnavailable
	}
	return ""
}
