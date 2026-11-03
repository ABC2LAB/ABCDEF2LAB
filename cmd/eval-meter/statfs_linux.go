//go:build linux

package main

import (
	"errors"

	"golang.org/x/sys/unix"
)

// tmpfsMagic is TMPFS_MAGIC from linux/magic.h.
const tmpfsMagic = 0x01021994

// statOpened opens path once without following a final symlink and asks both
// questions of that one object: which mount it is on (statx STATX_MNT_ID) and
// what that filesystem reports (fstatfs). Separate path lookups could each land
// on a different object if the path changed in between.
func statOpened(path string) (fsStat, error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fsStat{}, err
	}
	defer unix.Close(fd)
	var stx unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID, &stx); err != nil {
		return fsStat{}, err
	}
	if stx.Mask&unix.STATX_MNT_ID == 0 || stx.Mnt_id == 0 {
		return fsStat{}, errNoMountID
	}
	var st unix.Statfs_t
	if err := unix.Fstatfs(fd, &st); err != nil {
		return fsStat{}, err
	}
	if st.Bsize <= 0 {
		return fsStat{}, errors.New("statfs: invalid block size")
	}
	return fsStat{tmpfs: int64(st.Type) == tmpfsMagic, blocks: st.Blocks, free: st.Bfree, avail: st.Bavail,
		blockSize: uint64(st.Bsize), mountID: stx.Mnt_id}, nil
}
