package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"syscall"

	"github.com/Autumn-27/artex/transmit/auditlog"
)

const maxControlledAuditCheckpointBytes = int64(4096)

func openControlledAuditRoot(host ControlledTaskModeHost) (*os.File, error) {
	if host.RootAnchor == nil {
		return nil, ErrControlledReadAuditSource
	}
	anchor, err := host.RootAnchor.Stat()
	if err != nil || anchor.Mode() != os.ModeDir|0o700 {
		return nil, ErrControlledReadAuditSource
	}
	fd, err := syscall.Open(host.RootPath, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrControlledReadAuditSource
	}
	root := os.NewFile(uintptr(fd), "controlled-audit-root")
	info, err := root.Stat()
	if err != nil || info.Mode() != os.ModeDir|0o700 || !os.SameFile(anchor, info) {
		_ = root.Close()
		return nil, ErrControlledReadAuditSource
	}
	return root, nil
}

func checkControlledAuditRoot(host ControlledTaskModeHost) error {
	root, err := openControlledAuditRoot(host)
	if err != nil {
		return err
	}
	if root.Close() != nil {
		return ErrControlledReadAuditSource
	}
	return nil
}

func readControlledAuditFiles(ctx context.Context, host ControlledTaskModeHost, cp auditlog.Checkpoint, maxBytes int64) (body []byte, err error) {
	root, err := openControlledAuditRoot(host)
	if err != nil {
		return nil, err
	}
	defer func() {
		if root.Close() != nil {
			clear(body)
			body, err = nil, ErrControlledReadAuditSource
		}
	}()
	fd, err := syscall.Openat(int(root.Fd()), "journals", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrControlledReadAuditSource
	}
	directory := os.NewFile(uintptr(fd), "controlled-audit-journals")
	defer func() {
		if directory.Close() != nil {
			clear(body)
			body, err = nil, ErrControlledReadAuditSource
		}
	}()
	opened, err := directory.Stat()
	rootInfo, rootErr := root.Stat()
	if err != nil || rootErr != nil || opened.Mode() != os.ModeDir|0o700 {
		return nil, ErrControlledReadAuditSource
	}
	rootStat, rootOK := rootInfo.Sys().(*syscall.Stat_t)
	dirStat, dirOK := opened.Sys().(*syscall.Stat_t)
	if !rootOK || !dirOK || rootStat.Uid != dirStat.Uid {
		return nil, ErrControlledReadAuditSource
	}
	checkpointName, journalName := cp.JournalID+".checkpoint", cp.Scope.RunRef+".jsonl"
	cpBefore, err := taskMarkerStat(directory, checkpointName)
	if err != nil || !validControlledAuditInfo(cpBefore, maxControlledAuditCheckpointBytes, rootStat.Uid) {
		return nil, ErrControlledReadAuditSource
	}
	journalBefore, err := taskMarkerStat(directory, journalName)
	if err != nil || !validControlledAuditInfo(journalBefore, maxBytes, rootStat.Uid) {
		return nil, ErrControlledReadAuditSource
	}
	encoded, err := json.Marshal(cp)
	if err != nil {
		return nil, ErrControlledReadAuditSource
	}
	expected := append(encoded, '\n')
	checkpoint, err := readControlledAuditFile(ctx, directory, checkpointName, cpBefore, maxControlledAuditCheckpointBytes, rootStat.Uid)
	if err != nil || !bytes.Equal(checkpoint, expected) {
		clear(checkpoint)
		return nil, ErrControlledReadAuditSource
	}
	clear(checkpoint)
	if ctx.Err() != nil {
		return nil, ErrControlledReadAuditSource
	}
	body, err = readControlledAuditFile(ctx, directory, journalName, journalBefore, maxBytes, rootStat.Uid)
	if err != nil {
		return nil, ErrControlledReadAuditSource
	}
	checkpoint, err = readControlledAuditFile(ctx, directory, checkpointName, cpBefore, maxControlledAuditCheckpointBytes, rootStat.Uid)
	if err != nil || !bytes.Equal(checkpoint, expected) {
		clear(checkpoint)
		clear(body)
		return nil, ErrControlledReadAuditSource
	}
	clear(checkpoint)
	cpAfter, err := taskMarkerStat(directory, checkpointName)
	if err != nil || !sameControlledAuditInfo(cpBefore, cpAfter, maxControlledAuditCheckpointBytes, rootStat.Uid) {
		clear(body)
		return nil, ErrControlledReadAuditSource
	}
	journalAfter, err := taskMarkerStat(directory, journalName)
	if err != nil || !sameControlledAuditInfo(journalBefore, journalAfter, maxBytes, rootStat.Uid) {
		clear(body)
		return nil, ErrControlledReadAuditSource
	}
	current, err := taskMarkerStat(root, "journals")
	if err != nil || current.Mode() != os.ModeDir|0o700 || !os.SameFile(opened, current) || ctx.Err() != nil {
		clear(body)
		return nil, ErrControlledReadAuditSource
	}
	return body, nil
}

func validControlledAuditInfo(info os.FileInfo, max int64, uid uint32) bool {
	if info == nil || info.Mode() != 0o600 || info.Size() < 0 || info.Size() > max {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uid && stat.Nlink == 1
}

func sameControlledAuditInfo(before, after os.FileInfo, max int64, uid uint32) bool {
	return validControlledAuditInfo(before, max, uid) && validControlledAuditInfo(after, max, uid) &&
		os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

// Validate the actual opened inode, not only separate pathname observations.
func readControlledAuditFile(ctx context.Context, directory *os.File, name string, before os.FileInfo, max int64, uid uint32) (body []byte, err error) {
	if ctx.Err() != nil || !safeFile.MatchString(name) || !validControlledAuditInfo(before, max, uid) {
		return nil, ErrControlledReadAuditSource
	}
	fd, err := syscall.Openat(int(directory.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrControlledReadAuditSource
	}
	file := os.NewFile(uintptr(fd), "controlled-audit-input")
	defer func() {
		if file.Close() != nil {
			clear(body)
			body, err = nil, ErrControlledReadAuditSource
		}
	}()
	opened, err := file.Stat()
	if err != nil || !sameControlledAuditInfo(before, opened, max, uid) {
		return nil, ErrControlledReadAuditSource
	}
	body, err = io.ReadAll(io.LimitReader(file, max+1))
	if err != nil || int64(len(body)) > max || ctx.Err() != nil {
		clear(body)
		return nil, ErrControlledReadAuditSource
	}
	after, err := file.Stat()
	if err != nil || !sameControlledAuditInfo(opened, after, max, uid) {
		clear(body)
		return nil, ErrControlledReadAuditSource
	}
	return body, nil
}
