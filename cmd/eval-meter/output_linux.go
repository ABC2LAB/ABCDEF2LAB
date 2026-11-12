package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
)

// The runner seals the engine front with SIGUSR1 (docker kill -s USR1).
var sealSignals = []os.Signal{syscall.SIGUSR1}

func isSealSignal(sig os.Signal) bool { return sig == syscall.SIGUSR1 }

// openPrivateDir pins the evidence directory the runner prepared. Ancestor
// paths belong to the operator's trust boundary; O_NOFOLLOW protects the final
// component and the owner/mode check keeps other local users out.
func openPrivateDir(path string) (*os.File, string) {
	if path == "" {
		return nil, invalidInput
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, privateDirRequired
	}
	dir := os.NewFile(uintptr(fd), "eval-meter-directory")
	var info syscall.Stat_t
	if err := syscall.Fstat(fd, &info); err != nil {
		_ = dir.Close()
		return nil, storageUnavailable
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFDIR || info.Mode&0o7777 != 0o700 || info.Uid != uint32(os.Geteuid()) {
		_ = dir.Close()
		return nil, privateDirRequired
	}
	return dir, ""
}

// createPrivateFile never reuses an existing file: a leftover log from another
// run must not be appended to or mistaken for this run's evidence.
func createPrivateFile(dir *os.File, name string) (*os.File, string) {
	fd, err := syscall.Openat(int(dir.Fd()), name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, logExists
		}
		return nil, storageUnavailable
	}
	file := os.NewFile(uintptr(fd), name)
	// O_CREAT permissions may be reduced by umask; the runner requires 0600.
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, storageUnavailable
	}
	var info syscall.Stat_t
	if err := syscall.Fstat(fd, &info); err != nil || info.Mode&syscall.S_IFMT != syscall.S_IFREG || info.Mode&0o7777 != 0o600 {
		_ = file.Close()
		return nil, storageUnavailable
	}
	return file, ""
}

// readPathKey는 회차 묶음이 함께 쓰는 경로 가림 키를 읽는다. 다른 사용자가 읽을 수 있거나 길이가 다르면 거부한다.
// O_NONBLOCK은 FIFO를 넘겨받았을 때 writer를 기다리며 멈추지 않고 아래 파일 종류 검사에서 거부하게 한다.
func readPathKey(path string) ([]byte, string) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, pathKeyRequired
	}
	file := os.NewFile(uintptr(fd), "eval-meter-path-key")
	defer func() { _ = file.Close() }() // 읽기 전용 파일이라 닫기 실패가 결과를 바꾸지 않는다.
	var info syscall.Stat_t
	if err := syscall.Fstat(fd, &info); err != nil || info.Mode&syscall.S_IFMT != syscall.S_IFREG ||
		info.Mode&0o7777 != 0o600 || info.Uid != uint32(os.Geteuid()) || info.Size != pathKeyBytes {
		return nil, pathKeyRequired
	}
	key := make([]byte, pathKeyBytes)
	if _, err := io.ReadFull(file, key); err != nil {
		return nil, pathKeyRequired
	}
	return key, ""
}

func removePrivateFile(dir *os.File, name string) error {
	return syscall.Unlinkat(int(dir.Fd()), name)
}

// writeMarker publishes a state file only after its content and directory
// entry are durable, so the runner never reads a partial marker.
func writeMarker(dir *os.File, name string, marker any) error {
	body, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	file, code := createPrivateFile(dir, name+".partial")
	if code != "" {
		return errors.New(code)
	}
	_, writeErr := file.Write(append(body, '\n'))
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	// rename replaces silently; the private directory has no other writer, so
	// checking first is enough to never overwrite an earlier marker.
	existing, err := syscall.Openat(int(dir.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err == nil {
		_ = syscall.Close(existing)
		return os.ErrExist
	}
	if !errors.Is(err, syscall.ENOENT) {
		return err
	}
	if err := syscall.Renameat(int(dir.Fd()), name+".partial", int(dir.Fd()), name); err != nil {
		return err
	}
	return dir.Sync()
}
