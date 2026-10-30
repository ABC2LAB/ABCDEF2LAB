package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// openPrivateKey pins the chosen existing directory before checking its owner
// and mode. Ancestor paths must be trusted by the operator; O_NOFOLLOW protects
// the final directory component, not every ancestor component.
func openPrivateKey(path string) (file keyWriter, directory syncCloser, code string) {
	if !validKeyPath(path) {
		return nil, nil, invalidInput
	}
	parent, name := filepath.Split(path)
	if parent == "" {
		parent = "."
	} else if parent = strings.TrimRight(parent, "/"); parent == "" {
		parent = "/"
	}
	fd, err := syscall.Open(parent, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, privateDirRequired
	}
	dir := os.NewFile(uintptr(fd), "audit-key-directory")
	defer func() {
		if code != "" {
			if file != nil {
				if err := file.Close(); err != nil {
					code = storageUnavailable
				}
			}
			if err := dir.Close(); err != nil {
				code = storageUnavailable
			}
			file, directory = nil, nil
		}
	}()
	var info syscall.Stat_t
	if err := syscall.Fstat(fd, &info); err != nil {
		return nil, nil, storageUnavailable
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFDIR || info.Mode&0o7777 != 0o700 || info.Uid != uint32(os.Geteuid()) {
		return nil, nil, privateDirRequired
	}
	keyFD, err := syscall.Openat(fd, name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, nil, alreadyExists
		}
		return nil, nil, storageUnavailable
	}
	keyFile := os.NewFile(uintptr(keyFD), "audit-key-file")
	file = keyFile
	// O_CREAT permissions may be reduced by umask; the verifier requires exact
	// 0600. Chmod affects only this newly created, exclusive, pinned file.
	if err := keyFile.Chmod(0o600); err != nil {
		return file, nil, storageUnavailable
	}
	if err := syscall.Fstat(keyFD, &info); err != nil || info.Mode&syscall.S_IFMT != syscall.S_IFREG || info.Mode&0o7777 != 0o600 || info.Uid != uint32(os.Geteuid()) {
		return file, nil, storageUnavailable
	}
	return keyFile, dir, ""
}
