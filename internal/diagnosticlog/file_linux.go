//go:build linux

package diagnosticlog

import (
	"crypto/rand"
	"encoding/hex"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 연결된 디렉터리 FD를 수명 내내 보유해 경로 교체를 매 쓰기 전에 확인한다.
func Create(dataDir, phase string, taskID int64, runRef string) (*Logger, error) {
	if !filepath.IsAbs(dataDir) || filepath.Clean(dataDir) != dataDir || taskID < 0 || len(runRef) > maxReferenceBytes || !vocabulary("prepare-task prepare-intents run serve report check preparation")[phase] {
		return nil, ErrCreate
	}
	fds := []int{}
	parts := []string{}
	privateStart := -1
	release := func() error {
		var err error
		for i := len(fds) - 1; i >= 0; i-- {
			if unix.Close(fds[i]) != nil {
				err = ErrWrite
			}
		}
		fds = nil
		return err
	}
	fail := func() (*Logger, error) { _ = release(); return nil, ErrCreate }
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail()
	}
	fds = append(fds, fd)
	trusted := func(st unix.Stat_t, private bool) bool {
		return st.Mode&unix.S_IFMT == unix.S_IFDIR && (st.Uid == 0 || st.Uid == uint32(os.Geteuid())) && (st.Mode&0022 == 0 || st.Uid == 0 && st.Mode&unix.S_ISVTX != 0) && (!private || st.Uid == uint32(os.Geteuid()) && st.Mode&07777 == 0700)
	}
	add := func(part string, create bool) bool {
		parent := fds[len(fds)-1]
		if create {
			if err := unix.Mkdirat(parent, part, 0700); err != nil && err != unix.EEXIST {
				return false
			}
		}
		if create && unix.Fsync(parent) != nil {
			return false
		}
		next, e := unix.Openat(parent, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			return false
		}
		fds = append(fds, next)
		parts = append(parts, part)
		var st unix.Stat_t
		return unix.Fstat(next, &st) == nil && trusted(st, create)
	}
	if dataDir != "/" {
		for _, part := range strings.Split(strings.TrimPrefix(dataDir, "/"), "/") {
			if part == "web" || part == "public" || part == "www" || part == "wwwroot" {
				return fail()
			}
			if !add(part, false) {
				return fail()
			}
			var git unix.Stat_t
			if unix.Fstatat(fds[len(fds)-1], ".git", &git, unix.AT_SYMLINK_NOFOLLOW) == nil {
				return fail()
			}
		}
	}
	privateStart = len(fds)
	if !add("diagnostic-logs", true) {
		return fail()
	}
	group := "preparation"
	if taskID > 0 {
		group = "task-" + strconv.FormatInt(taskID, 10)
	}
	if !add(group, true) {
		return fail()
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fail()
	}
	name := phase + "-" + hex.EncodeToString(nonce[:]) + ".jsonl"
	parent := fds[len(fds)-1]
	filefd, err := unix.Openat(parent, name, unix.O_CREAT|unix.O_EXCL|unix.O_APPEND|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return fail()
	}
	file := os.NewFile(uintptr(filefd), "diagnostic-log")
	var logger *Logger
	check := func() error {
		for i, f := range fds {
			var opened unix.Stat_t
			if unix.Fstat(f, &opened) != nil || !trusted(opened, i >= privateStart) {
				return ErrWrite
			}
			if i > 0 {
				var current unix.Stat_t
				if unix.Fstatat(fds[i-1], parts[i-1], &current, unix.AT_SYMLINK_NOFOLLOW) != nil || current.Ino != opened.Ino || current.Dev != opened.Dev || current.Mode != opened.Mode || current.Uid != opened.Uid {
					return ErrWrite
				}
			}
		}
		var st, at unix.Stat_t
		if unix.Fstat(filefd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || logger != nil && st.Size != logger.size || unix.Fstatat(parent, name, &at, unix.AT_SYMLINK_NOFOLLOW) != nil || at.Ino != st.Ino || at.Dev != st.Dev {
			return ErrWrite
		}
		return nil
	}
	if check() != nil || unix.Fsync(parent) != nil {
		_ = file.Close()
		return fail()
	}
	logger = &Logger{failed: make(chan struct{}), writer: file, path: filepath.Join(dataDir, "diagnostic-logs", group, name), task: taskID, run: runRef, check: check, release: release}
	return logger, nil
}
