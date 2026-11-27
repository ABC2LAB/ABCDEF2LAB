//go:build linux

package server

import (
	"crypto/sha256"
	"github.com/Autumn-27/artex/internal/privateinput"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
)

func e2HTTPExecutableDigest(path string) ([32]byte, error) {
	var empty [32]byte
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || privateinput.CheckDirectory(filepath.Dir(path), false) != nil {
		return empty, ErrE2HTTPHost
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return empty, ErrE2HTTPHost
	}
	f := os.NewFile(uintptr(fd), "e2-helper")
	var before, after, atPath unix.Stat_t
	if unix.Fstat(fd, &before) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&0022 != 0 || before.Mode&0111 == 0 || before.Nlink != 1 || (before.Uid != 0 && before.Uid != uint32(os.Geteuid())) || before.Size <= 0 || before.Size > 64<<20 {
		_ = f.Close()
		return empty, ErrE2HTTPHost
	}
	h := sha256.New()
	n, readErr := io.Copy(h, io.LimitReader(f, (64<<20)+1))
	valid := readErr == nil && n == before.Size && unix.Fstat(fd, &after) == nil && unix.Lstat(path, &atPath) == nil && before.Dev == after.Dev && before.Ino == after.Ino && before.Mtim == after.Mtim && before.Ctim == after.Ctim && before.Size == after.Size && before.Dev == atPath.Dev && before.Ino == atPath.Ino && before.Mtim == atPath.Mtim && before.Ctim == atPath.Ctim && before.Mode == atPath.Mode && before.Size == atPath.Size
	closeErr := f.Close()
	if !valid || closeErr != nil || privateinput.CheckDirectory(filepath.Dir(path), false) != nil {
		return empty, ErrE2HTTPHost
	}
	copy(empty[:], h.Sum(nil))
	return empty, nil
}
func e2HTTPCreatePrivateDirectory(path string) error {
	if privateinput.CheckDirectory(filepath.Dir(path), true) != nil || os.Mkdir(path, 0700) != nil || privateinput.CheckDirectory(path, true) != nil {
		return ErrE2HTTPHost
	}
	return nil
}

// Record run conditions before the first Bash call. O_EXCL refuses replacement
// or reuse, and the file and directory must both reach durable storage.
func e2HTTPRecordRuntimeConditions(dir string, data []byte) error {
	return e2HTTPRecordStateFile(dir, "runtime-conditions.json", data)
}

// e2HTTPRecordAcquire는 E②를 잡았다는 기록(E2AcquireRecordFile)을 남긴다. 화면용 저널을 만들기 전에 부른다.
func e2HTTPRecordAcquire(dir string, data []byte) error {
	return e2HTTPRecordStateFile(dir, E2AcquireRecordFile, data)
}

// e2HTTPRecordStateFile은 보호 상태 디렉터리에 파일 하나를 배타 생성하고 파일과 디렉터리를 저장 장치까지 내린다.
func e2HTTPRecordStateFile(dir, name string, data []byte) error {
	if privateinput.CheckDirectory(dir, true) != nil {
		return ErrE2HTTPHost
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return ErrE2HTTPHost
	}
	defer root.Close()
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return ErrE2HTTPHost
	}
	n, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	d, err := root.Open(".")
	if err != nil {
		return ErrE2HTTPHost
	}
	dirSyncErr := d.Sync()
	dirCloseErr := d.Close()
	if n != len(data) || writeErr != nil || syncErr != nil || closeErr != nil || dirSyncErr != nil || dirCloseErr != nil || privateinput.CheckDirectory(dir, true) != nil {
		return ErrE2HTTPHost
	}
	return nil
}
