//go:build linux

package evidencerevision

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func descriptorOwner(info os.FileInfo, file bool) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid()) && ((!file && st.Nlink > 0) || file && st.Nlink == 1)
}

func descriptorSameStat(info os.FileInfo, st unix.Stat_t) bool {
	x, ok := info.Sys().(*syscall.Stat_t)
	return ok && x.Dev == st.Dev && x.Ino == st.Ino
}
