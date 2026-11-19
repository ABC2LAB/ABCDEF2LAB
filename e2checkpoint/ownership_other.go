//go:build !linux

package e2checkpoint

import "os"

// 일반 v3 기록 파일의 소유권 검사는 Linux 전용이다.
func validJournalDirectory(os.FileInfo) bool { return false }
func validJournalFile(os.FileInfo) bool      { return false }
