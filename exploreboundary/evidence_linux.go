//go:build linux

package exploreboundary

import (
	"errors"
	"os"
	"syscall"

	"github.com/Autumn-27/artex/pathmask"
)

// OpenEvidence는 러너가 준비한 근거 디렉터리에 행 파일을 새로 만든다. 계측기(cmd/eval-meter)와 같은 규칙이다:
// 디렉터리 소유를 넘기지 않고, 러너가 자기 uid·0700으로 만든 디렉터리를 같은 uid로 뜬 경계가 쓴다. 그래서 디렉터리는
// 정확히 0700이고 소유자가 경계 euid여야 한다. 이전 회차의 파일이 남아 있으면 이어 쓰지 않고 시작을 거부한다.
// key는 경로 가림 키다(표식의 path_key_ref, 계측기 경로 행과 같은 계산).
func OpenEvidence(path string, maxBytes int64, key []byte) (*Evidence, error) {
	if path == "" || maxBytes <= 0 || len(key) != pathmask.KeyBytes {
		return nil, ErrConfig
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrConfig
	}
	dir := os.NewFile(uintptr(fd), "explore-boundary-evidence")
	var info syscall.Stat_t
	if err := syscall.Fstat(fd, &info); err != nil || info.Mode&syscall.S_IFMT != syscall.S_IFDIR || info.Mode&0o7777 != 0o700 || info.Uid != uint32(os.Geteuid()) {
		_ = dir.Close() // 읽기 전용 디렉터리 fd라 닫기 실패가 결과를 바꾸지 않는다.
		return nil, ErrConfig
	}
	for _, name := range []string{EvidenceMarkerName, EvidenceMarkerName + ".partial"} {
		if exists, err := evidenceExists(dir, name); err != nil || exists {
			_ = dir.Close()
			return nil, ErrConfig
		}
	}
	file, err := createEvidenceFile(dir, EvidenceRowsName)
	if err != nil {
		_ = dir.Close()
		return nil, ErrConfig
	}
	ev, err := newEvidence(file, func(name string, body []byte) error { return publishEvidence(dir, name, body) }, pathmask.KeyRef(key), maxBytes)
	if err != nil {
		_ = file.Close()
		_ = dir.Close()
		return nil, err
	}
	return ev, nil
}

func evidenceExists(dir *os.File, name string) (bool, error) {
	fd, err := syscall.Openat(int(dir.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err == nil {
		_ = syscall.Close(fd)
		return true, nil
	}
	// 링크로 남은 이름(ELOOP)도 있는 것으로 본다.
	if errors.Is(err, syscall.ENOENT) {
		return false, nil
	}
	return true, err
}

// createEvidenceFile은 이미 있는 파일을 다시 쓰지 않는다(다른 회차의 행이 섞이지 않게).
func createEvidenceFile(dir *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(dir.Fd()), name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	// umask가 O_CREAT 권한을 줄일 수 있다. 러너는 정확히 0600을 요구한다.
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	var info syscall.Stat_t
	if err := syscall.Fstat(fd, &info); err != nil || info.Mode&syscall.S_IFMT != syscall.S_IFREG || info.Mode&0o7777 != 0o600 {
		_ = file.Close()
		return nil, ErrConfig
	}
	return file, nil
}

// publishEvidence는 표식 내용과 디렉터리 항목이 디스크에 내려간 뒤에만 이름을 드러낸다. 러너가 반쯤 쓴 표식을 읽지 않게 한다.
func publishEvidence(dir *os.File, name string, body []byte) error {
	file, err := createEvidenceFile(dir, name+".partial")
	if err != nil {
		return err
	}
	_, writeErr := file.Write(body)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	// rename은 조용히 덮어쓴다. 이 디렉터리의 쓰는 쪽은 경계 하나라 먼저 확인하면 이전 표식을 덮지 않는다.
	if exists, err := evidenceExists(dir, name); err != nil || exists {
		return errors.Join(os.ErrExist, err)
	}
	if err := syscall.Renameat(int(dir.Fd()), name+".partial", int(dir.Fd()), name); err != nil {
		return err
	}
	return dir.Sync()
}
