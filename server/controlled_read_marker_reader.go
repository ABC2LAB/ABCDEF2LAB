package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/taskmode"
)

const (
	maxTaskMarkerEntries       = 4096
	maxTaskMarkerBytes   int64 = 4096
)

var (
	errTaskMarkerReaderInput       = errors.New("task marker reader input invalid")
	errTaskMarkerReaderScope       = errors.New("task marker registration scope unavailable")
	errTaskMarkerReaderRead        = errors.New("task marker state unavailable")
	errTaskMarkerReaderUnsupported = errors.New("task marker reader platform unsupported")
)

type controlledTaskMarkerReaderOptions struct {
	RootPath string
	// RootAnchor is borrowed from trusted initialization, not derived from the
	// current path to approve a replacement root. Keep it open through each read.
	RootAnchor *os.File
	// CheckScope verifies already-held registration/storage trust. It must not
	// acquire locks, start a transaction or wait for registration from this reader.
	// Supplying a callback does not authenticate its implementation.
	CheckScope func(context.Context, int64) error
	MaxEntries int
}

// Private per-call dependencies permit fault tests without global hook mutation.
// Production construction always uses the fixed platform implementation.
type taskMarkerReadOps struct {
	names func(context.Context, *os.File, int) ([]string, error)
	read  func(*os.File, string, int64) ([]byte, error)
	stat  func(*os.File, string) (os.FileInfo, error)
	close func(*os.File) error
}

// newControlledTaskMarkerReader only builds an explicit callback. Production
// admission, migration and finding storage do not call it in this draft.
func newControlledTaskMarkerReader(options controlledTaskMarkerReaderOptions) (db.TaskModeMarkerReader, error) {
	if err := validateTaskMarkerOptions(options); err != nil {
		return nil, err
	}
	if !taskMarkerPlatformSupported {
		return nil, errTaskMarkerReaderUnsupported
	}
	return func(ctx context.Context, taskID int64) (taskmode.Marker, error) {
		return readTaskModeMarkers(ctx, options, taskID, defaultTaskMarkerOps())
	}, nil
}

func validateTaskMarkerOptions(options controlledTaskMarkerReaderOptions) error {
	if options.RootPath == "" || !filepath.IsAbs(options.RootPath) || filepath.Clean(options.RootPath) != options.RootPath ||
		options.RootAnchor == nil || options.CheckScope == nil || options.MaxEntries <= 0 || options.MaxEntries > maxTaskMarkerEntries {
		return errTaskMarkerReaderInput
	}
	return nil
}

func readTaskModeMarkers(ctx context.Context, options controlledTaskMarkerReaderOptions, taskID int64, ops taskMarkerReadOps) (taskmode.Marker, error) {
	if ctx == nil || taskID <= 0 {
		return taskmode.Marker{}, errTaskMarkerReaderInput
	}
	if err := validateTaskMarkerOptions(options); err != nil {
		return taskmode.Marker{}, err
	}
	if !taskMarkerPlatformSupported {
		return taskmode.Marker{}, errTaskMarkerReaderUnsupported
	}
	if ops.names == nil || ops.read == nil || ops.stat == nil || ops.close == nil {
		return taskmode.Marker{}, errTaskMarkerReaderInput
	}
	if err := checkTaskMarkerScope(ctx, taskID, options.CheckScope); err != nil {
		return taskmode.Marker{}, err
	}
	marker, err := scanTaskModeMarkers(ctx, options, taskID, ops)
	if canceled := ctx.Err(); canceled != nil {
		return taskmode.Marker{}, canceled
	}
	if err != nil {
		return taskmode.Marker{}, err
	}
	if err := checkTaskMarkerScope(ctx, taskID, options.CheckScope); err != nil {
		return taskmode.Marker{}, err
	}
	return marker, nil
}

func checkTaskMarkerScope(ctx context.Context, taskID int64, check func(context.Context, int64) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := check(ctx, taskID)
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	if err != nil {
		return errTaskMarkerReaderScope
	}
	return nil
}
