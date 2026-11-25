//go:build linux

package diagnosisprep

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type candidateBudgetFixture struct {
	*fakeBackend
	mu   sync.Mutex
	rows map[int64]CandidateBudgetRecord
}

func (b *candidateBudgetFixture) CandidateBudgetUsage(_ context.Context, delegation string) (Budget, int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var used Budget
	var count int64
	for _, row := range b.rows {
		if row.DelegationRef == delegation {
			used = addBudget(used, row.Budget)
			count++
		}
	}
	return used, count, nil
}

func (b *candidateBudgetFixture) ReadCandidateBudget(_ context.Context, id int64) (*CandidateBudgetRecord, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	row, ok := b.rows[id]
	if !ok {
		return nil, nil
	}
	return &row, nil
}

func (b *candidateBudgetFixture) InsertCandidateBudget(_ context.Context, row CandidateBudgetRecord) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.rows[row.RequestID]; ok {
		return errors.New("duplicate")
	}
	b.rows[row.RequestID] = row
	return nil
}

func TestSelectedCandidateReservationCountsAgainstInstallation(t *testing.T) {
	b := &candidateBudgetFixture{fakeBackend: &fakeBackend{inspection: fixtureInspection()}, rows: map[int64]CandidateBudgetRecord{}}
	b.inspection.MaxRuns = 2
	root := privateDir(t)
	s, err := New(context.Background(), root, b)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(context.Background())
	r := Request{Version: Version, RequestID: "create-original", Principal: "ARTEX", Command: Create,
		IdempotencyKey: "original", Input: &Input{TargetURL: "https://example.test/read", ProfileID: "local"}}
	created := call(t, s, r)
	if created.Error != "" {
		t.Fatal(created.Error)
	}
	r = Request{Version: Version, RequestID: "start-original", Principal: "ARTEX", Command: Start,
		JobID: created.Job.ID, ExpectedRevision: created.Job.Revision, IdempotencyKey: "original-start"}
	if got := call(t, s, r); got.Error != "" {
		t.Fatal(got.Error)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		res := call(t, s, Request{Version: Version, RequestID: "status-original", Principal: "ARTEX", Command: Status, JobID: created.Job.ID})
		if res.Error != "" {
			t.Fatal(res.Error)
		}
		if res.Job.State == Completed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("state=%s", res.Job.State)
		}
		time.Sleep(time.Millisecond)
	}
	digest := strings.Repeat("a", 64)
	if err := s.ReserveCandidateBudget(context.Background(), "42", 1, digest); err != nil {
		t.Fatal(err)
	}
	if err := s.ReserveCandidateBudget(context.Background(), "42", 1, digest); err != nil {
		t.Fatal(err)
	}
	if len(b.rows) != 1 {
		t.Fatalf("reservations=%d", len(b.rows))
	}
	if err := s.ReserveCandidateBudget(context.Background(), "42", 1, strings.Repeat("b", 64)); err == nil {
		t.Fatal("changed evidence accepted")
	}
	s.FinishCandidateBudget(1)
	if err := s.ReserveCandidateBudget(context.Background(), "42", 2, digest); err == nil {
		t.Fatal("aggregate run limit bypassed")
	}
	r = Request{Version: Version, RequestID: "create-next", Principal: "ARTEX", Command: Create,
		IdempotencyKey: "next", Input: &Input{TargetURL: "https://example.test/read", ProfileID: "local"}}
	next := call(t, s, r)
	if next.Error != "" {
		t.Fatal(next.Error)
	}
	r = Request{Version: Version, RequestID: "start-next", Principal: "ARTEX", Command: Start,
		JobID: next.Job.ID, ExpectedRevision: next.Job.Revision, IdempotencyKey: "next-start"}
	if got := call(t, s, r).Error; got != "budget_exhausted" {
		t.Fatal(got)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, err := New(context.Background(), root, b)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Shutdown(context.Background())
	if err := recovered.ReserveCandidateBudget(context.Background(), "42", 1, digest); err != nil {
		t.Fatal("the same immutable request lost its reservation after restart", err)
	}
	recovered.FinishCandidateBudget(1)
	if got := call(t, recovered, r).Error; got != "budget_exhausted" {
		t.Fatalf("restart reset candidate aggregate reservation: %s", got)
	}
}
