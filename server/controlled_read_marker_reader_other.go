//go:build !linux

package server

import (
	"context"

	"github.com/Autumn-27/artex/taskmode"
)

const taskMarkerPlatformSupported = false

func defaultTaskMarkerOps() taskMarkerReadOps { return taskMarkerReadOps{} }

func scanTaskModeMarkers(context.Context, controlledTaskMarkerReaderOptions, int64, taskMarkerReadOps) (taskmode.Marker, error) {
	return taskmode.Marker{}, errTaskMarkerReaderUnsupported
}
