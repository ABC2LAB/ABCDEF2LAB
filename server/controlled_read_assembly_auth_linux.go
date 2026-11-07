// 승인된 기존 공급자의 수명과 실제 검증 경계만 조립하며 권한을 발급하지 않는다.
package server

import (
	"bytes"
	"github.com/Autumn-27/artex/internal/privateinput"
	"path/filepath"
	"strings"
)

func readControlledReadAssemblyJWTKey(keyDir, dataDir string) ([]byte, error) {
	if privateinput.CheckDirectory(dataDir, false) != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	data, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	keys, err := filepath.EvalSymlinks(keyDir)
	if err != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	relative, err := filepath.Rel(data, keys)
	if err != nil || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, ErrControlledReadHostConfiguration
	}
	body, err := privateinput.ReadFile(filepath.Join(keyDir, jwtKeyFilename), 4096)
	defer clear(body)
	if err != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) < 32 {
		return nil, ErrControlledReadHostConfiguration
	}
	return bytes.Clone(trimmed), nil
}
