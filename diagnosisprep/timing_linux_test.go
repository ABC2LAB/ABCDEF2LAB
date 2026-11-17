//go:build linux

package diagnosisprep

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func timingFields(t *testing.T, value any) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func TestRunTimingStableThroughConcurrentPublishStopAndRestart(t *testing.T) {
	service, backend, root := newFixture(t)
	at := time.Date(2026, 11, 17, 1, 2, 3, 456, time.UTC)
	var nanos atomic.Int64
	nanos.Store(at.UnixNano())
	var clockCalls atomic.Int64
	service.now = func() time.Time {
		clockCalls.Add(1)
		return time.Unix(0, nanos.Load()).In(time.FixedZone("local", 9*60*60))
	}
	published := make(chan struct{})
	backend.run = func(ctx context.Context, _ Execution, publish Publish) error {
		u := Update{State: Running, Phase: ExecutionPhase, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityConsumed, Started: true}
		if err := publish(u); err != nil {
			return err
		}
		close(published)
		<-ctx.Done()
		u.State, u.Phase, u.DrainComplete, u.ResultReady = Cancelled, Closure, true, true
		return publish(u)
	}
	job := create(t, service, "stable-timing")
	if response := start(t, service, job, "stable-start"); response.Error != "" {
		t.Fatal(response.Error)
	}
	select {
	case <-published:
	case <-time.After(3 * time.Second):
		t.Fatal("시작 확인 게시를 기다리지 못했습니다")
	}
	running := status(t, service, job.ID)
	if running.StartedAt == nil || !running.StartedAt.Equal(at) || running.FinishedAt != nil {
		t.Fatal("실행 중 시각이 잘못됐습니다")
	}
	nanos.Store(at.Add(5 * time.Minute).UnixNano())
	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- service.publish(job.ID, Update{State: Running, Phase: ExecutionPhase, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityConsumed, Started: true})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if status(t, service, job.ID).Revision != running.Revision || clockCalls.Load() != 1 {
		t.Fatal("중복 게시가 시각이나 revision을 갱신했습니다")
	}
	// 공개 포인터를 바꿔도 서비스의 보호 기록을 바꾸지 못한다.
	*running.StartedAt = at.Add(time.Hour)
	list := runList(t, service, "owner", RunListRequest{})
	*list.RunPage.Runs[0].StartedAt = at.Add(time.Hour)
	if !status(t, service, job.ID).StartedAt.Equal(at) {
		t.Fatal("공개 시각 포인터가 보호 기록과 공유됩니다")
	}
	stop := request(Stop)
	stop.JobID, stop.ExpectedRevision = job.ID, running.Revision
	if result := call(t, service, stop); result.Error != "" {
		t.Fatal(result.Error)
	}
	closed := awaitState(t, service, job.ID, Cancelled)
	awaitExecutionReturned(t, service, job.ID)
	if closed.FinishedAt == nil || !closed.FinishedAt.Equal(at.Add(5*time.Minute)) || clockCalls.Load() != 2 {
		t.Fatal("최초 마감 확인 시각이 아닙니다")
	}
	if result := start(t, service, job, "stable-start"); result.Error != "" || backend.calls.Load() != 1 {
		t.Fatal("멱등 재조회가 다시 실행했습니다")
	}
	if err := service.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	restored, err := New(t.Context(), root, backend)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Shutdown(t.Context())
	got := status(t, restored, job.ID)
	row := runList(t, restored, "owner", RunListRequest{}).RunPage.Runs[0]
	if got.StartedAt == nil || got.FinishedAt == nil || !got.StartedAt.Equal(*closed.StartedAt) || !got.FinishedAt.Equal(*closed.FinishedAt) ||
		row.StartedAt == nil || row.FinishedAt == nil || !row.StartedAt.Equal(*got.StartedAt) || !row.FinishedAt.Equal(*got.FinishedAt) || backend.calls.Load() != 1 {
		t.Fatal("재기동·압축이 확인 시각을 바꾸거나 재실행했습니다")
	}
}

func TestRunTimingUnknownKeepsOnlyConfirmedTimes(t *testing.T) {
	for _, started := range []bool{false, true} {
		for _, drained := range []bool{false, true} {
			name := "not_started"
			if started {
				name = "started"
			}
			if drained {
				name += "_drained"
			}
			t.Run(name, func(t *testing.T) {
				service, backend, root := newFixture(t)
				backend.run = func(_ context.Context, _ Execution, publish Publish) error {
					return publish(Update{State: Unknown, Phase: Closure, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityConsumed, Started: started, DrainComplete: drained})
				}
				job := create(t, service, "unknown-timing")
				if result := start(t, service, job, "unknown-start"); result.Error != "" {
					t.Fatal(result.Error)
				}
				got := awaitState(t, service, job.ID, Unknown)
				awaitExecutionReturned(t, service, job.ID)
				if (got.StartedAt != nil) != started || (got.FinishedAt != nil) != drained {
					t.Fatal("결과 불명에서 확인 사실과 시각이 달라졌습니다")
				}
				if err := service.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
				restored, err := New(t.Context(), root, backend)
				if err != nil {
					t.Fatal(err)
				}
				defer restored.Shutdown(t.Context())
				for _, value := range []any{status(t, restored, job.ID), runList(t, restored, "owner", RunListRequest{}).RunPage.Runs[0]} {
					fields := timingFields(t, value)
					for key, present := range map[string]bool{"started_at": started, "finished_at": drained} {
						_, exists := fields[key]
						if exists != present {
							t.Fatalf("복원된 %s가 확인 사실과 다릅니다", key)
						}
						if present {
							requireRecordedTime(t, fields, key)
						}
					}
				}
			})
		}
	}
}

func TestRunTimingLegacyJournalNeverBackfills(t *testing.T) {
	for _, state := range []State{Running, Completed} {
		t.Run(string(state), func(t *testing.T) {
			service, backend, root := newFixture(t)
			job := create(t, service, "legacy-timing")
			service.mu.Lock()
			record := cloneReceipt(service.jobs[job.ID])
			record.StartKey = "legacy-start"
			record.Job.Revision++
			record.Job.State, record.Job.Phase = state, ExecutionPhase
			record.Job.TaskID, record.Job.PreparationStarted, record.Job.Started = "42", true, true
			record.Job.AuthorityState = AuthorityConsumed
			if state == Completed {
				record.Job.Phase, record.Job.DrainComplete, record.Job.ResultReady = Report, true, true
			}
			err := service.save(record)
			service.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			crash(t, service)
			restored, err := New(t.Context(), root, backend)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Shutdown(t.Context())
			got := status(t, restored, job.ID)
			if got.StartedAt != nil || got.FinishedAt != nil || !got.Started || backend.calls.Load() != 0 {
				t.Fatal("구 보호 기록에서 시각을 추정하거나 재실행했습니다")
			}
		})
	}
}

func TestRunTimingClockRollbackOmitsFinishWithoutFailingRun(t *testing.T) {
	service, _, _ := newFixture(t)
	at := time.Date(2026, 11, 17, 1, 2, 3, 0, time.UTC)
	var calls atomic.Int64
	service.now = func() time.Time { return at.Add(-time.Duration(calls.Add(1)) * time.Second) }
	job := create(t, service, "rollback-timing")
	if result := start(t, service, job, "rollback-start"); result.Error != "" {
		t.Fatal(result.Error)
	}
	got := awaitState(t, service, job.ID, Completed)
	if got.StartedAt == nil || got.FinishedAt != nil || !got.DrainComplete || !got.ResultReady {
		t.Fatal("시계 역행으로 시간을 조작하거나 실행 사실을 바꿨습니다")
	}
}

func TestRunTimingJournalRejectsRewriteRemovalAndLegacyBackfill(t *testing.T) {
	service, _, _ := newFixture(t)
	job := create(t, service, "journal-timing")
	service.mu.Lock()
	created := cloneReceipt(service.jobs[job.ID])
	service.mu.Unlock()
	at := time.Date(2026, 11, 17, 1, 2, 3, 0, time.UTC)
	finish := at.Add(time.Minute)
	for _, field := range []string{"started", "finished"} {
		for _, change := range []string{"rewrite", "remove", "backfill"} {
			t.Run(field+"/"+change, func(t *testing.T) {
				old := cloneReceipt(created)
				old.StartKey = "journal-start"
				old.Job.Revision++
				old.Job.State, old.Job.Phase, old.Job.TaskID = Running, ExecutionPhase, "42"
				old.Job.PreparationStarted, old.Job.Started, old.Job.AuthorityState = true, true, AuthorityConsumed
				old.Job.StartedAt = &at
				if field == "finished" {
					old.Job.State, old.Job.Phase, old.Job.DrainComplete, old.Job.FinishedAt = Stopping, Closure, true, &finish
				}
				next := cloneReceipt(old)
				next.Job.Revision++
				before, after := &old.Job.StartedAt, &next.Job.StartedAt
				if field == "finished" {
					before, after = &old.Job.FinishedAt, &next.Job.FinishedAt
				}
				switch change {
				case "rewrite":
					changed := (**after).Add(time.Nanosecond)
					*after = &changed
				case "remove":
					*after = nil
				case "backfill":
					*before = nil
				}
				if !validJob(old.Job) || !validJob(next.Job) || !validReceiptChange(created, old) {
					t.Fatal("시각 변경 이외의 계약을 어긴 음성 대조입니다")
				}
				if validReceiptChange(old, next) {
					t.Fatal("보호 저널이 확인된 시각의 변경을 수용했습니다")
				}
				first, _ := json.Marshal(created)
				second, _ := json.Marshal(old)
				third, _ := json.Marshal(next)
				if _, err := replayPreparationReceipts(t.Context(), []json.RawMessage{first, second, third}, 0); err == nil {
					t.Fatal("복원이 변경된 시각을 정본으로 수용했습니다")
				}
			})
		}
	}
}

func requireRecordedTime(t *testing.T, fields map[string]json.RawMessage, key string) time.Time {
	t.Helper()
	var text string
	if json.Unmarshal(fields[key], &text) != nil || text == "" {
		t.Fatalf("확인된 %s 시각이 없습니다", key)
	}
	at, err := time.Parse(time.RFC3339Nano, text)
	if err != nil || at.IsZero() || at.Location() != time.UTC {
		t.Fatalf("%s가 유효한 UTC 시각이 아닙니다", key)
	}
	return at
}

func TestRunTimingRecordsConfirmedFactsAndList(t *testing.T) {
	service, backend, _ := newFixture(t)
	job := create(t, service, "timing")
	for _, key := range []string{"started_at", "finished_at"} {
		if _, exists := timingFields(t, job)[key]; exists {
			t.Fatalf("시작 전 %s를 생성했습니다", key)
		}
	}
	before := time.Now().UTC()
	if response := start(t, service, job, "timing-start"); response.Error != "" {
		t.Fatal(response.Error)
	}
	completed := awaitState(t, service, job.ID, Completed)
	awaitExecutionReturned(t, service, job.ID)
	fields := timingFields(t, completed)
	started := requireRecordedTime(t, fields, "started_at")
	finished := requireRecordedTime(t, fields, "finished_at")
	if started.Before(before) || finished.Before(started) || finished.After(time.Now().UTC()) {
		t.Fatal("확인 시각의 순서가 맞지 않습니다")
	}
	list := runList(t, service, "owner", RunListRequest{})
	if list.Error != "" || len(list.RunPage.Runs) != 1 {
		t.Fatal("저장된 회차를 찾을 수 없습니다", list.Error)
	}
	row := timingFields(t, list.RunPage.Runs[0])
	if !requireRecordedTime(t, row, "started_at").Equal(started) || !requireRecordedTime(t, row, "finished_at").Equal(finished) {
		t.Fatal("목록이 저장된 확인 시각을 보존하지 않습니다")
	}
	if backend.calls.Load() != 1 {
		t.Fatal("시각 조회가 실행을 다시 시작했습니다")
	}
	if err := service.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
