package learnedlogin

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/Autumn-27/artex/internal/privateinput"
)

// AttemptsUnavailableFile은 탐색 이력이 온전하게 인계되지 않았다는 표지다. 내용이나 권한과 관계없이 존재하면 차단한다.
const AttemptsUnavailableFile = "login-attempts.unavailable"

// ReadConfirmationHistory는 확정 단계가 상한을 복원할 이력을 읽는다. 정상적인 기존 회차는 상태 디렉터리 안에
// 시도 파일이 없을 수 있다. 이력 필수 회차의 인계 완료는 실행 경계에서 별도로 확인해야 한다.
func ReadConfirmationHistory(stateDir string) ([]Row, int, error) {
	if privateinput.CheckDirectory(stateDir, true) != nil {
		return nil, 0, ErrRecord
	}
	marker := filepath.Join(stateDir, AttemptsUnavailableFile)
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		return nil, 0, ErrRecord
	}
	rows, skipped, err := ReadRows(stateDir)
	if err != nil {
		return nil, skipped, err
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		return nil, 0, ErrRecord
	}
	return rows, 0, nil
}
