//go:build linux

package main

import (
	"errors"
	"syscall"
)

// tmpfsMagic is TMPFS_MAGIC from linux/magic.h.
const tmpfsMagic = 0x01021994

func statFilesystem(path string) (fsStat, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return fsStat{}, err
	}
	if st.Bsize <= 0 {
		return fsStat{}, errors.New("statfs: invalid block size")
	}
	return fsStat{tmpfs: int64(st.Type) == tmpfsMagic, blocks: st.Blocks, free: st.Bfree, avail: st.Bavail,
		blockSize: uint64(st.Bsize)}, nil
}
