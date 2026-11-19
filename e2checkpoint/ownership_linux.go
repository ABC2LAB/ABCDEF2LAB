//go:build linux

package e2checkpoint

import (
	"os"
	"syscall"
)

func validJournalDirectory(info os.FileInfo) bool {
	if info == nil || info.Mode() != os.ModeDir|0700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && stat.Nlink > 0
}

func validJournalFile(info os.FileInfo) bool {
	if info == nil || info.Mode() != 0600 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1
}
