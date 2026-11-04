//go:build !linux

package server

import (
	"context"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

func checkControlledAuditRoot(ControlledTaskModeHost) error { return ErrControlledReadAuditSource }
func readControlledAuditFiles(context.Context, ControlledTaskModeHost, auditlog.Checkpoint, int64) ([]byte, error) {
	return nil, ErrControlledReadAuditSource
}
