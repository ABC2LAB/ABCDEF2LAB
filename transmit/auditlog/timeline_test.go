package auditlog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

func TestVerifyTimelineSortsByTimeAndKeepsSequenceForTies(t *testing.T) {
	j, path, initial := auditExternalCreate(t)
	base := time.Unix(1_700_000_000, 0).UTC()
	later := auditExternalIntent(initial.Scope, 1)
	later.At = base.Add(time.Second)
	later.AccountRef = "later"
	earlier := auditExternalIntent(initial.Scope, 2)
	earlier.At = base
	earlier.AccountRef = "first-tie"
	tie := auditExternalIntent(initial.Scope, 3)
	tie.At = base
	tie.AccountRef = "second-tie"
	for _, event := range []transmit.Event{later, earlier, tie} {
		if err := j.Record(context.Background(), event); err != nil {
			t.Fatalf("record event: %v", err)
		}
	}
	want := j.Checkpoint()
	data := auditExternalRead(t, path)
	events, err := auditlog.VerifyTimeline(context.Background(), bytes.NewReader(data), auditExternalKey(), want, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].AccountRef != "first-tie" || events[1].AccountRef != "second-tie" || events[2].AccountRef != "later" {
		t.Fatalf("time/sequence ordering: %+v", events)
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range [][]byte{[]byte("session-a"), []byte("approval-1"), []byte("GET"), []byte("run-1"), []byte("key_ref"), []byte("attempt")} {
		if bytes.Contains(encoded, field) {
			t.Fatalf("timeline exposed non-projected field %q", field)
		}
	}
}

func TestVerifyTimelineEmptyAndLimitWithoutPartialResult(t *testing.T) {
	j, path, initial := auditExternalCreate(t)
	data := auditExternalRead(t, path)
	events, err := auditlog.VerifyTimeline(context.Background(), bytes.NewReader(data), auditExternalKey(), initial, 1)
	if err != nil || events == nil || len(events) != 0 {
		t.Fatalf("empty journal: events=%v err=%v", events, err)
	}
	for _, limit := range []int{0, auditlog.MaxTimelineEvents + 1} {
		if events, err := auditlog.VerifyTimeline(context.Background(), bytes.NewReader(data), auditExternalKey(), initial, limit); events != nil || !errors.Is(err, auditlog.ErrConfig) {
			t.Fatalf("invalid limit %d: events=%v err=%v", limit, events, err)
		}
	}
	for i := uint64(1); i <= 3; i++ {
		if err := j.Record(context.Background(), auditExternalIntent(initial.Scope, i)); err != nil {
			t.Fatal(err)
		}
	}
	want := j.Checkpoint()
	data = auditExternalRead(t, path)
	if events, err := auditlog.VerifyTimeline(context.Background(), bytes.NewReader(data), auditExternalKey(), want, 2); events != nil || !errors.Is(err, auditlog.ErrTimelineLimit) {
		t.Fatalf("limit overrun: events=%v err=%v", events, err)
	}
	corrupt := append([]byte(nil), data...)
	corrupt[len(corrupt)-2] ^= 1 // A later bad record must be checked after the cap is reached.
	if events, err := auditlog.VerifyTimeline(context.Background(), bytes.NewReader(corrupt), auditExternalKey(), want, 2); events != nil || !errors.Is(err, auditlog.ErrIntegrity) {
		t.Fatalf("late corruption: events=%v err=%v", events, err)
	}
}
