//go:build linux

package e2sandbox

import (
	"time"

	"golang.org/x/sys/unix"
)

const (
	defaultScratchBytes   = 32 << 20
	defaultTempBytes      = 16 << 20
	defaultFileBytes      = 8 << 20
	defaultNamespaceTasks = 128
	defaultOpenFiles      = 128
	maxCallTimeout        = time.Hour
	minScratchBytes       = 1 << 20
	minTempBytes          = 1 << 20
	minNamespaceTasks     = 8
	maxScratchBytes       = 256 << 20
	maxTempBytes          = 64 << 20
	maxNamespaceTasks     = 128
	minOpenFiles          = 128 // Trusted re-exec uses descriptors 100 and 101.
	maxOpenFiles          = 1024
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
		tasks = defaultNamespaceTasks
	}
	if l.OpenFiles == 0 {
		l.OpenFiles = defaultOpenFiles
	}
	if cfg.Timeout <= 0 || cfg.Timeout > maxCallTimeout {
		return l, ErrConfig
	}
	wallSeconds := uint64((cfg.Timeout + time.Second - 1) / time.Second)
	if l.CPUSeconds == 0 {
		l.CPUSeconds = wallSeconds
	}
	if l.ScratchBytes < minScratchBytes || l.ScratchBytes > maxScratchBytes || l.TempBytes < minTempBytes || l.TempBytes > maxTempBytes ||
		l.FileBytes == 0 || l.FileBytes > l.ScratchBytes || tasks < minNamespaceTasks || tasks > maxNamespaceTasks || l.CPUSeconds > wallSeconds ||
		l.OpenFiles < minOpenFiles || l.OpenFiles > maxOpenFiles {
		return l, ErrConfig
	}
	// RLIMIT_NPROC is applied after CLONE_NEWUSER in the isolated helper.
	l.TaskCeiling = tasks
	return l, nil
}

// The helper installs this limit inside its fresh user namespace. The
// namespace task count must not include existing tasks of the mapped host UID.
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
