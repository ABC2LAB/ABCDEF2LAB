package taskmode_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/taskmode"
)

func snapshot(mode taskmode.Mode, marker taskmode.MarkerState) taskmode.Snapshot {
	return taskmode.Snapshot{
		Record: taskmode.Record{TaskID: 17, Mode: mode, Version: 1},
		Marker: taskmode.Marker{TaskID: 17, State: marker},
	}
}

func assertDecision(t *testing.T, got taskmode.Decision, route taskmode.Route, reason taskmode.Reason) {
	t.Helper()
	if got.Route() != route || got.Reason() != reason {
		t.Fatalf("route=%v reason=%s; want route=%v reason=%s", got.Route(), got.Reason(), route, reason)
	}
}

func TestDecideMatrix(t *testing.T) {
	cases := []struct {
		name   string
		mode   taskmode.Mode
		marker taskmode.MarkerState
		route  taskmode.Route
		reason taskmode.Reason
	}{
		{"legacy_clear", taskmode.ModeLegacy, taskmode.MarkerClear, taskmode.Legacy, taskmode.ReasonLegacy},
		{"legacy_present", taskmode.ModeLegacy, taskmode.MarkerPresent, taskmode.Block, taskmode.ReasonLegacyConflict},
		{"legacy_unknown", taskmode.ModeLegacy, taskmode.MarkerUnknown, taskmode.Block, taskmode.ReasonMarkerUnknown},
		{"legacy_invalid", taskmode.ModeLegacy, 255, taskmode.Block, taskmode.ReasonMarkerInvalid},
		{"controlled_present", taskmode.ModeControlled, taskmode.MarkerPresent, taskmode.Controlled, taskmode.ReasonControlled},
		{"controlled_clear", taskmode.ModeControlled, taskmode.MarkerClear, taskmode.Block, taskmode.ReasonControlledPending},
		{"controlled_unknown", taskmode.ModeControlled, taskmode.MarkerUnknown, taskmode.Block, taskmode.ReasonMarkerUnknown},
		{"controlled_invalid", taskmode.ModeControlled, 255, taskmode.Block, taskmode.ReasonMarkerInvalid},
		{"unknown_clear", taskmode.ModeUnknown, taskmode.MarkerClear, taskmode.Block, taskmode.ReasonUnknown},
		{"unknown_present", taskmode.ModeUnknown, taskmode.MarkerPresent, taskmode.Block, taskmode.ReasonUnknown},
		{"unknown_unknown", taskmode.ModeUnknown, taskmode.MarkerUnknown, taskmode.Block, taskmode.ReasonUnknown},
		{"unknown_invalid", taskmode.ModeUnknown, 255, taskmode.Block, taskmode.ReasonUnknown},
		{"invalid_clear", "unregistered", taskmode.MarkerClear, taskmode.Block, taskmode.ReasonRecordInvalid},
		{"invalid_present", "unregistered", taskmode.MarkerPresent, taskmode.Block, taskmode.ReasonRecordInvalid},
		{"invalid_unknown", "unregistered", taskmode.MarkerUnknown, taskmode.Block, taskmode.ReasonRecordInvalid},
		{"invalid_invalid", "unregistered", 255, taskmode.Block, taskmode.ReasonRecordInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertDecision(t, taskmode.Decide(17, snapshot(tc.mode, tc.marker)), tc.route, tc.reason)
		})
	}
}

func TestDecideRejectsInvalidBindings(t *testing.T) {
	cases := []struct {
		name   string
		taskID int64
		change func(*taskmode.Snapshot)
		reason taskmode.Reason
	}{
		{"zero_task", 0, func(*taskmode.Snapshot) {}, taskmode.ReasonInvalidTask},
		{"negative_task", -1, func(*taskmode.Snapshot) {}, taskmode.ReasonInvalidTask},
		{"no_record", 17, func(s *taskmode.Snapshot) { s.Record = taskmode.Record{} }, taskmode.ReasonRecordMissing},
		{"wrong_record_task", 17, func(s *taskmode.Snapshot) { s.Record.TaskID++ }, taskmode.ReasonWrongTask},
		{"empty_record_task", 17, func(s *taskmode.Snapshot) { s.Record.TaskID = 0 }, taskmode.ReasonWrongTask},
		{"wrong_marker_task", 17, func(s *taskmode.Snapshot) { s.Marker.TaskID++ }, taskmode.ReasonWrongTask},
		{"empty_marker_task", 17, func(s *taskmode.Snapshot) { s.Marker.TaskID = 0 }, taskmode.ReasonWrongTask},
		{"zero_version", 17, func(s *taskmode.Snapshot) { s.Record.Version = 0 }, taskmode.ReasonRecordInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := snapshot(taskmode.ModeLegacy, taskmode.MarkerClear)
			tc.change(&s)
			assertDecision(t, taskmode.Decide(tc.taskID, s), taskmode.Block, tc.reason)
		})
	}
}

func TestZeroDecisionBlocks(t *testing.T) {
	assertDecision(t, taskmode.Decision{}, taskmode.Block, taskmode.ReasonUnknown)
	assertDecision(t, taskmode.Decide(17, taskmode.Snapshot{}), taskmode.Block, taskmode.ReasonRecordMissing)
}

func TestResolveValidRoutes(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode taskmode.Mode
		mark taskmode.MarkerState
		want taskmode.Route
	}{
		{"legacy", taskmode.ModeLegacy, taskmode.MarkerClear, taskmode.Legacy},
		{"controlled", taskmode.ModeControlled, taskmode.MarkerPresent, taskmode.Controlled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			ctx := context.WithValue(context.Background(), struct{}{}, "host_context")
			got, err := taskmode.Resolve(ctx, 17, func(received context.Context, id int64) (taskmode.Snapshot, error) {
				calls++
				if received != ctx || id != 17 {
					t.Fatal("reader received a different context or task")
				}
				return snapshot(tc.mode, tc.mark), nil
			})
			if err != nil || calls != 1 || got.Route() != tc.want {
				t.Fatalf("route=%v calls=%d err=%v", got.Route(), calls, err)
			}
		})
	}
}

func TestResolveRefusesBeforeRead(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, finish := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer finish()
	cases := []struct {
		name   string
		ctx    context.Context
		taskID int64
		reason taskmode.Reason
		err    error
	}{
		{"nil_context", nil, 17, taskmode.ReasonInvalidContext, taskmode.ErrInvalidContext},
		{"zero_task", context.Background(), 0, taskmode.ReasonInvalidTask, taskmode.ErrInvalidTask},
		{"negative_task", context.Background(), -1, taskmode.ReasonInvalidTask, taskmode.ErrInvalidTask},
		{"canceled", canceled, 17, taskmode.ReasonCanceled, context.Canceled},
		{"deadline", expired, 17, taskmode.ReasonCanceled, context.DeadlineExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := taskmode.Resolve(tc.ctx, tc.taskID, func(context.Context, int64) (taskmode.Snapshot, error) {
				t.Fatal("refused request reached reader")
				return snapshot(taskmode.ModeLegacy, taskmode.MarkerClear), nil
			})
			assertDecision(t, got, taskmode.Block, tc.reason)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err=%v; want %v", err, tc.err)
			}
		})
	}
}

func TestResolveNilReader(t *testing.T) {
	got, err := taskmode.Resolve(context.Background(), 17, nil)
	assertDecision(t, got, taskmode.Block, taskmode.ReasonReaderMissing)
	if !errors.Is(err, taskmode.ErrReaderMissing) {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveReadFailureDoesNotUseReturnedSnapshot(t *testing.T) {
	cause := errors.New("PRIVATE_READER_DETAIL")
	got, err := taskmode.Resolve(context.Background(), 17, func(context.Context, int64) (taskmode.Snapshot, error) {
		return snapshot(taskmode.ModeLegacy, taskmode.MarkerClear), cause
	})
	assertDecision(t, got, taskmode.Block, taskmode.ReasonReadFailed)
	if !errors.Is(err, taskmode.ErrReadFailed) || !errors.Is(err, cause) {
		t.Fatal("read failure lost its category or inspectable cause")
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, err), cause.Error()) {
			t.Fatal("reader details appeared in formatted error")
		}
	}
	out, marshalErr := json.Marshal(err)
	if marshalErr != nil || strings.Contains(string(out), cause.Error()) {
		t.Fatal("reader details appeared in JSON error")
	}
}

func TestResolveChecksCancellationAfterReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got, err := taskmode.Resolve(ctx, 17, func(context.Context, int64) (taskmode.Snapshot, error) {
		cancel()
		return snapshot(taskmode.ModeLegacy, taskmode.MarkerClear), nil
	})
	assertDecision(t, got, taskmode.Block, taskmode.ReasonCanceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

// These are boundary probes, not the live Engine or RecordFindingTx adapters.
func TestBoundariesReadFreshMode(t *testing.T) {
	for _, boundary := range []string{"execution", "storage"} {
		t.Run(boundary, func(t *testing.T) {
			current := snapshot(taskmode.ModeLegacy, taskmode.MarkerClear)
			reads := 0
			read := func(context.Context, int64) (taskmode.Snapshot, error) { reads++; return current, nil }
			legacy, controlled, blocked := 0, 0, 0
			probe := func(want taskmode.Route) {
				t.Helper()
				d, err := taskmode.Resolve(context.Background(), 17, read)
				if err != nil || d.Route() != want {
					t.Fatalf("route=%v want=%v err=%v", d.Route(), want, err)
				}
				switch d.Route() {
				case taskmode.Legacy:
					legacy++
				case taskmode.Controlled:
					controlled++
				default:
					blocked++
				}
			}
			probe(taskmode.Legacy)
			current.Record.Mode, current.Record.Version = taskmode.ModeControlled, 2
			probe(taskmode.Block) // Fence committed; marker creation has not completed.
			current.Marker.State = taskmode.MarkerPresent
			probe(taskmode.Controlled)
			current.Record = taskmode.Record{}
			probe(taskmode.Block) // Lost canonical state is never legacy.
			if reads != 4 || legacy != 1 || controlled != 1 || blocked != 2 {
				t.Fatalf("reads=%d legacy=%d controlled=%d blocked=%d", reads, legacy, controlled, blocked)
			}
			t.Logf("boundary=%s reads=%d legacy=%d controlled=%d blocked=%d", boundary, reads, legacy, controlled, blocked)
		})
	}
}
