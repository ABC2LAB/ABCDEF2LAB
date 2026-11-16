//go:build linux

package e2runner

import (
	"errors"
	"io"
	"io/fs"
	"strconv"
	"strings"
)

// Product containers expose their own cgroup v2 root here. Hosts without this
// file retain the existing non-container path; an exposed but unreadable,
// malformed or smaller limit must not silently invalidate the task allocation.
func checkProcessBudget(root fs.FS, required uint64) error {
	f, err := root.Open("pids.max")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrConfig
	}
	data, readErr := io.ReadAll(io.LimitReader(f, 33))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || len(data) > 32 {
		return ErrConfig
	}
	value := strings.TrimSpace(string(data))
	if value == "max" {
		return nil
	}
	limit, err := strconv.ParseUint(value, 10, 64)
	if err != nil || limit < required {
		return ErrConfig
	}
	return nil
}
