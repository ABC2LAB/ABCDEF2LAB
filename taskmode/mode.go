// Package taskmode classifies trusted task-mode snapshots for execution and
// finding storage. Classification chooses a pipeline; it authorizes neither
// HTTP transmission nor a confirmed finding.
package taskmode

import (
	"context"
	"errors"
)

type Mode string

const (
	ModeUnknown    Mode = ""
	ModeLegacy     Mode = "explicit_legacy"
	ModeControlled Mode = "controlled"
)

type MarkerState uint8

const (
	MarkerUnknown MarkerState = iota
	MarkerClear
	MarkerPresent
)

// Record comes from the server's canonical mode store. An absent record must
// never be synthesized as ModeLegacy. Version is the mode-fence revision.
type Record struct {
	TaskID  int64
	Mode    Mode
	Version uint64
}

// MarkerClear means absence was checked successfully, not that a read failed.
type Marker struct {
	TaskID int64
	State  MarkerState
}

// Snapshot is adapter input, not a model-facing JSON contract. The adapter must
// read the canonical record and marker consistently with mode registration.
type Snapshot struct {
	Record Record
	Marker Marker
}

type Route uint8

const (
	Block Route = iota
	Legacy
	Controlled
)

type Reason string

const (
	ReasonUnknown           Reason = "mode_unknown"
	ReasonInvalidTask       Reason = "task_invalid"
	ReasonRecordMissing     Reason = "mode_record_missing"
	ReasonRecordInvalid     Reason = "mode_record_invalid"
	ReasonWrongTask         Reason = "task_mismatch"
	ReasonMarkerUnknown     Reason = "marker_unknown"
	ReasonMarkerInvalid     Reason = "marker_invalid"
	ReasonLegacyConflict    Reason = "legacy_marker_conflict"
	ReasonControlledPending Reason = "controlled_marker_pending"
	ReasonLegacy            Reason = "explicit_legacy"
	ReasonControlled        Reason = "controlled"
	ReasonReaderMissing     Reason = "mode_reader_missing"
	ReasonReadFailed        Reason = "mode_read_failed"
	ReasonCanceled          Reason = "mode_read_canceled"
	ReasonInvalidContext    Reason = "context_invalid"
)

// Decision has no exported writable fields. A zero value blocks, and a nonzero
// classification must not be cached as authority for a later execution/save.
type Decision struct {
	route  Route
	reason Reason
}

func (d Decision) Route() Route { return d.route }

func (d Decision) Reason() Reason {
	if d.reason == "" {
		return ReasonUnknown
	}
	return d.reason
}

var (
	ErrInvalidTask    = errors.New("taskmode: invalid task")
	ErrInvalidContext = errors.New("taskmode: invalid context")
	ErrReaderMissing  = errors.New("taskmode: reader missing")
	ErrReadFailed     = errors.New("taskmode: mode read failed")
)

// Reader is supplied by the host, normally bound to its current admission lock
// or storage transaction. A read error, including no rows, is not legacy mode.
type Reader func(context.Context, int64) (Snapshot, error)

// Decide classifies one server-owned snapshot. It has no I/O or side effects.
func Decide(taskID int64, snapshot Snapshot) Decision {
	if taskID <= 0 {
		return Decision{reason: ReasonInvalidTask}
	}
	if snapshot.Record == (Record{}) {
		return Decision{reason: ReasonRecordMissing}
	}
	if snapshot.Record.TaskID != taskID {
		return Decision{reason: ReasonWrongTask}
	}
	if snapshot.Record.Version == 0 {
		return Decision{reason: ReasonRecordInvalid}
	}
	switch snapshot.Record.Mode {
	case ModeUnknown:
		return Decision{reason: ReasonUnknown}
	case ModeLegacy, ModeControlled:
	default:
		return Decision{reason: ReasonRecordInvalid}
	}
	if snapshot.Marker.TaskID != taskID {
		return Decision{reason: ReasonWrongTask}
	}
	switch snapshot.Marker.State {
	case MarkerUnknown:
		return Decision{reason: ReasonMarkerUnknown}
	case MarkerClear, MarkerPresent:
	default:
		return Decision{reason: ReasonMarkerInvalid}
	}
	if snapshot.Record.Mode == ModeLegacy {
		if snapshot.Marker.State == MarkerPresent {
			return Decision{reason: ReasonLegacyConflict}
		}
		return Decision{route: Legacy, reason: ReasonLegacy}
	}
	if snapshot.Marker.State == MarkerClear {
		return Decision{reason: ReasonControlledPending}
	}
	return Decision{route: Controlled, reason: ReasonControlled}
}

// Resolve reads afresh at a boundary. The caller must keep its registration /
// transaction synchronization through the admission or write; a read followed
// by an unrelated later action is not protected against mode changes.
func Resolve(ctx context.Context, taskID int64, read Reader) (Decision, error) {
	if ctx == nil {
		return Decision{reason: ReasonInvalidContext}, ErrInvalidContext
	}
	if taskID <= 0 {
		return Decision{reason: ReasonInvalidTask}, ErrInvalidTask
	}
	if err := ctx.Err(); err != nil {
		return Decision{reason: ReasonCanceled}, err
	}
	if read == nil {
		return Decision{reason: ReasonReaderMissing}, ErrReaderMissing
	}
	snapshot, err := read(ctx, taskID)
	if canceled := ctx.Err(); canceled != nil {
		return Decision{reason: ReasonCanceled}, canceled
	}
	if err != nil {
		return Decision{reason: ReasonReadFailed}, readFailure{cause: err}
	}
	return Decide(taskID, snapshot), nil
}

// Preserve errors.Is/As diagnostics without exporting raw database/path errors
// through ordinary error formatting or JSON. Hosts must not log Unwrap() data.
type readFailure struct{ cause error }

func (e readFailure) Error() string        { return ErrReadFailed.Error() }
func (e readFailure) Unwrap() error        { return e.cause }
func (e readFailure) Is(target error) bool { return target == ErrReadFailed }
func (e readFailure) GoString() string     { return "taskmode.readFailure{cause:[REDACTED]}" }
