package auditlog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit/auditlog"
)

func TestVerifyTimelineSortsByTimeAndKeepsSequenceForTies(t *testing.T) {
	j, path, initial := auditExternalCreate(t)
	base := time.Unix(1_700_000_000, 0).UTC()
	// More than twelve mixed timestamps exercise stable sorting beyond the
	// insertion-sort path, which would hide a SliceStable -> Slice mutation.
	for i := uint64(1); i <= 40; i++ {
		event := auditExternalIntent(initial.Scope, i)
		event.At = base.Add(time.Duration(i%3) * time.Second)
		event.AccountRef = fmt.Sprintf("event-%02d", i)
		if err := j.Record(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	events, err := auditlog.VerifyTimeline(context.Background(), bytes.NewReader(auditExternalRead(t, path)), auditExternalKey(), j.Checkpoint(), 40)
	if err != nil || len(events) != 40 {
		t.Fatalf("timeline length=%d err=%v", len(events), err)
	}
	index := 0
	for bucket := uint64(0); bucket < 3; bucket++ {
		for i := uint64(1); i <= 40; i++ {
			if i%3 != bucket {
				continue
			}
			if got := events[index]; got.AccountRef != fmt.Sprintf("event-%02d", i) || !got.At.Equal(base.Add(time.Duration(bucket)*time.Second)) {
				t.Fatalf("time/sequence ordering at %d: %+v", index, got)
			}
			index++
		}
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
	for i := uint64(1); i <= 4; i++ {
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
