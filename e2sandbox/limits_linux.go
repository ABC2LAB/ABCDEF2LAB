//go:build linux

package e2sandbox

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	defaultScratchBytes = 32 << 20
	defaultTempBytes    = 16 << 20
	defaultFileBytes    = 8 << 20
	defaultExtraTasks   = 128
	defaultOpenFiles    = 128
	maxScratchBytes     = 256 << 20
	maxTempBytes        = 64 << 20
	maxExtraTasks       = 128
	minOpenFiles        = 128 // Trusted re-exec uses descriptors 100 and 101.
	maxOpenFiles        = 1024
)

type resourceLimits struct {
	ScratchBytes, TempBytes, FileBytes uint64
	TaskCeiling, CPUSeconds, OpenFiles uint64
}

func limitsFor(cfg Config) (resourceLimits, error) {
	l := resourceLimits{ScratchBytes: cfg.ScratchBytes, TempBytes: cfg.TempBytes, FileBytes: cfg.MaxFileBytes, CPUSeconds: cfg.CPUSeconds, OpenFiles: cfg.OpenFiles}
	tasks := cfg.MaxProcesses
	if l.ScratchBytes == 0 {
		l.ScratchBytes = defaultScratchBytes
	}
	if l.TempBytes == 0 {
		l.TempBytes = defaultTempBytes
	}
	if l.FileBytes == 0 {
		l.FileBytes = defaultFileBytes
	}
	if tasks == 0 {
		tasks = defaultExtraTasks
	}
	if l.OpenFiles == 0 {
		l.OpenFiles = defaultOpenFiles
	}
	if cfg.Timeout <= 0 || cfg.Timeout > time.Hour {
		return l, ErrConfig
	}
	wallSeconds := uint64((cfg.Timeout + time.Second - 1) / time.Second)
	if l.CPUSeconds == 0 {
		l.CPUSeconds = wallSeconds
	}
	if l.ScratchBytes < 1<<20 || l.ScratchBytes > maxScratchBytes || l.TempBytes < 1<<20 || l.TempBytes > maxTempBytes ||
		l.FileBytes == 0 || l.FileBytes > l.ScratchBytes || tasks < 8 || tasks > maxExtraTasks || l.CPUSeconds > wallSeconds ||
		l.OpenFiles < minOpenFiles || l.OpenFiles > maxOpenFiles {
		return l, ErrConfig
	}
	count, err := hostUserTaskCount()
	if err != nil || count > 1<<30 {
		return l, ErrUnavailable
	}
	l.TaskCeiling = count + tasks
	return l, nil
}

// Linux RLIMIT_NPROC counts the mapped host UID's tasks, not just this PID
// namespace. This bounds aggregate fork pressure without changing any other
// host process's limits; it is not a per-call cgroup pids.max claim.
func hostUserTaskCount() (uint64, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	var count uint64
	uid := strconv.Itoa(os.Getuid())
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.ParseUint(entry.Name(), 10, 32); err != nil {
			continue
		}
		status, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
		if err != nil {
			continue
		}
		own := false
		for _, line := range strings.Split(string(status), "\n") {
			f := strings.Fields(line)
			if len(f) == 5 && f[0] == "Uid:" {
				own = f[1] == uid
				break
			}
		}
		if !own {
			continue
		}
		tasks, err := os.ReadDir(filepath.Join("/proc", entry.Name(), "task"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		count += uint64(len(tasks))
	}
	if count == 0 {
		return 0, ErrUnavailable
	}
	return count, nil
}

func applyLimits(l resourceLimits) error {
	for _, item := range []struct {
		resource int
		ceiling  uint64
	}{
		{unix.RLIMIT_FSIZE, l.FileBytes}, {unix.RLIMIT_CPU, l.CPUSeconds},
		{unix.RLIMIT_NOFILE, l.OpenFiles}, {unix.RLIMIT_NPROC, l.TaskCeiling},
		{unix.RLIMIT_CORE, 0},
	} {
		var old unix.Rlimit
		if err := unix.Getrlimit(item.resource, &old); err != nil {
			return err
		}
		value := item.ceiling
		if old.Cur < value {
			value = old.Cur
		}
		if old.Max < value {
			value = old.Max
		}
		if err := unix.Setrlimit(item.resource, &unix.Rlimit{Cur: value, Max: value}); err != nil {
			return err
		}
	}
	return nil
}
