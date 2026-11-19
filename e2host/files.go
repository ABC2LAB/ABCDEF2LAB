package e2host

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

func createRunDirectory(root, runRef string) (string, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrUnavailable
	}
	digest := sha256.Sum256([]byte(runRef))
	directory := filepath.Join(root, hex.EncodeToString(digest[:]))
	if err := os.Mkdir(directory, 0700); err != nil {
		return "", ErrUnavailable
	}
	parent, err := os.Open(root)
	if err != nil {
		return "", ErrUnavailable
	}
	err = parent.Sync()
	_ = parent.Close()
	if err != nil {
		return "", ErrUnavailable
	}
	return directory, nil
}
