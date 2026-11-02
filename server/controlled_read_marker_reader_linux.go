package server

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/Autumn-27/artex/taskmode"
)

const taskMarkerPlatformSupported = true
const taskMarkerListBatch = 64

func defaultTaskMarkerOps() taskMarkerReadOps {
	return taskMarkerReadOps{
		names: taskMarkerNames,
		read:  readPrivateAt,
		stat:  taskMarkerStat,
		close: func(file *os.File) error { return file.Close() },
	}
}

type taskMarkerName struct {
	name   string
	task   string
	intent int64
}

func scanTaskModeMarkers(ctx context.Context, options controlledTaskMarkerReaderOptions, taskID int64, ops taskMarkerReadOps) (marker taskmode.Marker, err error) {
	anchor, statErr := options.RootAnchor.Stat()
	if statErr != nil || anchor.Mode() != os.ModeDir|0o700 {
		return taskmode.Marker{}, errTaskMarkerReaderRead
	}
	fd, openErr := syscall.Open(options.RootPath, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if openErr != nil {
		return taskmode.Marker{}, errTaskMarkerReaderRead
	}
	directory := os.NewFile(uintptr(fd), "task-marker-directory")
	defer func() {
		if ops.close(directory) != nil {
			marker, err = taskmode.Marker{}, errTaskMarkerReaderRead
		}
	}()
	opened, statErr := directory.Stat()
	if statErr != nil || opened.Mode() != os.ModeDir|0o700 || !os.SameFile(anchor, opened) {
		return taskmode.Marker{}, errTaskMarkerReaderRead
	}
	first, listErr := taskMarkerInventory(ctx, directory, options.MaxEntries, ops.names)
	if listErr != nil {
		return taskmode.Marker{}, listErr
	}
	infos := make([]os.FileInfo, len(first))
	target := strconv.FormatInt(taskID, 10)
	taskPresent, intentPresent := false, false
	for i, entry := range first {
		if canceled := ctx.Err(); canceled != nil {
			return taskmode.Marker{}, canceled
		}
		before, readErr := ops.stat(directory, entry.name)
		if readErr != nil || !validTaskMarkerInfo(before) {
			return taskmode.Marker{}, errTaskMarkerReaderRead
		}
		body, readErr := ops.read(directory, entry.name, maxTaskMarkerBytes)
		if readErr != nil || int64(len(body)) > maxTaskMarkerBytes {
			clear(body)
			return taskmode.Marker{}, errTaskMarkerReaderRead
		}
		var decoded markerFile
		decodeErr := strictJSON(body, &decoded)
		clear(body)
		if decodeErr != nil || decoded.TaskID != entry.task || decoded.IntentID != entry.intent || !decoded.Controlled {
			return taskmode.Marker{}, errTaskMarkerReaderRead
		}
		after, readErr := ops.stat(directory, entry.name)
		if readErr != nil || !sameTaskMarkerInfo(before, after) {
			return taskmode.Marker{}, errTaskMarkerReaderRead
		}
		infos[i] = after
		if entry.task == target {
			if entry.intent == 0 {
				taskPresent = true
			} else {
				intentPresent = true
			}
		}
	}
	if scopeErr := checkTaskMarkerScope(ctx, taskID, options.CheckScope); scopeErr != nil {
		return taskmode.Marker{}, scopeErr
	}
	// Two observations detect observed changes, not ABA or uncoordinated writers.
	// The caller must already hold the registration/storage scope throughout.
	second, listErr := taskMarkerInventory(ctx, directory, options.MaxEntries, ops.names)
	if listErr != nil || len(first) != len(second) {
		return taskmode.Marker{}, errTaskMarkerReaderRead
	}
	for i, entry := range second {
		if canceled := ctx.Err(); canceled != nil {
			return taskmode.Marker{}, canceled
		}
		if entry != first[i] {
			return taskmode.Marker{}, errTaskMarkerReaderRead
		}
		current, readErr := ops.stat(directory, entry.name)
		if readErr != nil || !sameTaskMarkerInfo(infos[i], current) {
			return taskmode.Marker{}, errTaskMarkerReaderRead
		}
	}
	currentRoot, statErr := os.Lstat(options.RootPath)
	if statErr != nil || currentRoot.Mode() != os.ModeDir|0o700 || !os.SameFile(anchor, currentRoot) {
		return taskmode.Marker{}, errTaskMarkerReaderRead
	}
	if intentPresent && !taskPresent {
		return taskmode.Marker{}, errTaskMarkerReaderRead
	}
	state := taskmode.MarkerClear
	if taskPresent {
		state = taskmode.MarkerPresent
	}
	return taskmode.Marker{TaskID: taskID, State: state}, nil
}

func taskMarkerInventory(ctx context.Context, directory *os.File, max int, names func(context.Context, *os.File, int) ([]string, error)) ([]taskMarkerName, error) {
	entries, err := names(ctx, directory, max)
	if err != nil || len(entries) > max {
		return nil, errTaskMarkerReaderRead
	}
	sort.Strings(entries)
	markers := make([]taskMarkerName, 0, len(entries))
	for i, name := range entries {
		if canceled := ctx.Err(); canceled != nil {
			return nil, canceled
		}
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsRune(name, 0) || (i > 0 && entries[i-1] == name) {
			return nil, errTaskMarkerReaderRead
		}
		entry, reserved, parseErr := parseTaskMarkerName(name)
		if parseErr != nil {
			return nil, parseErr
		}
		if reserved {
			markers = append(markers, entry)
		}
	}
	return markers, nil
}

func parseTaskMarkerName(name string) (taskMarkerName, bool, error) {
	lower := strings.ToLower(name)
	if !strings.HasPrefix(lower, "controlled-") && !strings.HasSuffix(lower, ".marker") {
		return taskMarkerName{}, false, nil
	}
	invalid := func() (taskMarkerName, bool, error) { return taskMarkerName{}, true, errTaskMarkerReaderRead }
	if !strings.HasPrefix(name, "controlled-") {
		return invalid()
	}
	stem := strings.TrimPrefix(name, "controlled-")
	var task string
	var intent int64
	if strings.HasSuffix(stem, ".task.marker") {
		task = strings.TrimSuffix(stem, ".task.marker")
	} else if strings.HasSuffix(stem, ".marker") {
		parts := strings.Split(strings.TrimSuffix(stem, ".marker"), "-")
		if len(parts) != 2 || !numericID.MatchString(parts[1]) {
			return invalid()
		}
		task = parts[0]
		var err error
		intent, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || intent <= 0 {
			return invalid()
		}
	} else {
		return invalid()
	}
	if !numericID.MatchString(task) {
		return invalid()
	}
	if _, err := strconv.ParseInt(task, 10, 64); err != nil {
		return invalid()
	}
	return taskMarkerName{name: name, task: task, intent: intent}, true, nil
}

func validTaskMarkerInfo(info os.FileInfo) bool {
	return info != nil && info.Mode() == 0o600 && info.Size() >= 0 && info.Size() <= maxTaskMarkerBytes
}

func sameTaskMarkerInfo(before, after os.FileInfo) bool {
	return validTaskMarkerInfo(before) && validTaskMarkerInfo(after) && os.SameFile(before, after) &&
		before.Mode() == after.Mode() && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

func taskMarkerStat(directory *os.File, name string) (info os.FileInfo, err error) {
	fd, openErr := syscall.Openat(int(directory.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if openErr != nil {
		return nil, errTaskMarkerReaderRead
	}
	file := os.NewFile(uintptr(fd), "task-marker-metadata")
	defer func() {
		if file.Close() != nil {
			info, err = nil, errTaskMarkerReaderRead
		}
	}()
	info, err = file.Stat()
	if err != nil {
		return nil, errTaskMarkerReaderRead
	}
	return info, nil
}

func taskMarkerNames(ctx context.Context, directory *os.File, max int) (names []string, err error) {
	// A fresh open description avoids shared directory offsets between scans.
	fd, openErr := syscall.Openat(int(directory.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if openErr != nil {
		return nil, errTaskMarkerReaderRead
	}
	listing := os.NewFile(uintptr(fd), "task-marker-inventory")
	defer func() {
		if listing.Close() != nil {
			names, err = nil, errTaskMarkerReaderRead
		}
	}()
	for {
		if canceled := ctx.Err(); canceled != nil {
			return nil, canceled
		}
		batch, readErr := listing.Readdirnames(taskMarkerListBatch)
		names = append(names, batch...)
		if len(names) > max {
			return nil, errTaskMarkerReaderRead
		}
		if readErr == io.EOF {
			return names, nil
		}
		if readErr != nil || len(batch) == 0 {
			return nil, errTaskMarkerReaderRead
		}
	}
}
