package server

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func readPrivate(path string, max int64) (body []byte, err error) {
	if max < 0 || max == math.MaxInt64 {
		return nil, errControlledReadStorage
	}
	parent, name := filepath.Split(path)
	if name == "" || name == "." || name == ".." {
		return nil, errControlledReadStorage
	}
	if parent == "" {
		parent = "."
	} else if parent = strings.TrimRight(parent, "/"); parent == "" {
		parent = "/"
	}
	// Pin the final directory before opening its child. Ancestor paths and
	// authorized writers remain the operator's trust boundary.
	fd, openErr := syscall.Open(parent, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if openErr != nil {
		return nil, errControlledReadStorage
	}
	directory := os.NewFile(uintptr(fd), "controlled-read-directory")
	defer func() {
		if directory.Close() != nil {
			clear(body)
			body, err = nil, errControlledReadStorage
		}
	}()
	info, statErr := directory.Stat()
	if statErr != nil || info.Mode() != os.ModeDir|0o700 {
		return nil, errControlledReadStorage
	}
	return readPrivateAt(directory, name, max)
}

func readPrivateAt(directory *os.File, name string, max int64) (body []byte, err error) {
	if directory == nil || name == "" || name == "." || name == ".." || filepath.Base(name) != name || max < 0 || max == math.MaxInt64 {
		return nil, errControlledReadStorage
	}
	// O_NONBLOCK avoids waiting for a FIFO writer before we can reject its type.
	// O_NOFOLLOW and fstat bind validation and reading to the same opened inode.
	fd, openErr := syscall.Openat(int(directory.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if openErr != nil {
		return nil, errControlledReadStorage
	}
	file := os.NewFile(uintptr(fd), "controlled-read-input")
	defer func() {
		if file.Close() != nil {
			clear(body)
			body, err = nil, errControlledReadStorage
		}
	}()
	info, statErr := file.Stat()
	if statErr != nil || info.Mode() != 0o600 || info.Size() < 0 || info.Size() > max {
		return nil, errControlledReadStorage
	}
	body, err = io.ReadAll(io.LimitReader(file, max+1))
	if err != nil || int64(len(body)) > max {
		clear(body)
		return nil, errControlledReadStorage
	}
	return body, nil
}
