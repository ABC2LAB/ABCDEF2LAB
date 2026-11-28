//go:build linux

package diagnosisprep

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// syntheticTimeLimit은 backend가 집행하는 모양(모델 마감 + 유예 + 확정 몫 + 여유 = 실행 상한)의 합성 값이다.
func syntheticTimeLimit(deadline time.Time) *TimeLimit {
	return &TimeLimit{RunDeadlineAt: deadline, ModelCloseAt: deadline.Add(-(30 + 120 + 15) * time.Second),
		ModelDrainMS: 30_000, ConfirmationReserveMS: 120_000, ShutdownMarginMS: 15_000}
}

func TestTimeLimitValidationRejectsDurationOverflow(t *testing.T) {
	limit := syntheticTimeLimit(time.Date(2026, 11, 27, 3, 0, 0, 0, time.UTC))
	if !validTimeLimit(limit, true) {
		t.Fatal("정상 시간 상한을 거부함")
	}
	// 2^58 ms × 10^6 ns는 int64 Duration에서 0으로 돌아와 식과 하루 검사를 동시에 우회할 수 있었다.
	limit.ModelDrainMS += 1 << 58
	if validTimeLimit(limit, true) {
		t.Fatal("넘친 시간 몫을 수용함")
	}
}

// 시간 상한은 backend가 처음 게시한 값 그대로 실행 중·중지·마감·재기동까지 유지된다. 같은 값 재게시는 revision을 바꾸지
// 않고, 다른 값은 받지 않는다(집행 값이 바뀐 것). 상한을 받기 전 상태 응답에는 없다.
func TestTimeLimitRecordedOnceAndKeptThroughStopAndRestart(t *testing.T) {
	service, backend, root := newFixture(t)
	deadline := time.Date(2026, 11, 27, 3, 0, 0, 0, time.UTC)
	limit := syntheticTimeLimit(deadline)
	published, changed := make(chan struct{}), make(chan error, 1)
	backend.run = func(ctx context.Context, _ Execution, publish Publish) error {
		u := Update{State: Preparing, Phase: Gateway, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityConsumed}
		if err := publish(u); err != nil {
			return err
		}
		u.TimeLimit = limit
		if err := publish(u); err != nil {
			return err
		}
		u.State, u.Phase, u.Started = Running, ExecutionPhase, true
		if err := publish(u); err != nil {
			return err
		}
		close(published)
		// 다른 값(상한을 늦춤)은 거부된다. 같은 값과 nil은 바꾸지 않는다.
		moved := *limit
		moved.RunDeadlineAt, moved.ModelCloseAt = moved.RunDeadlineAt.Add(time.Minute), moved.ModelCloseAt.Add(time.Minute)
		bad := u
		bad.TimeLimit = &moved
		changed <- publish(bad)
		<-ctx.Done()
		u.TimeLimit = nil
		u.State, u.Phase, u.DrainComplete, u.ResultReady = Cancelled, Closure, true, true
		return publish(u)
	}
	job := create(t, service, "time-limit")
	if job.TimeLimit != nil {
		t.Fatal("시작 전 회차에 시간 상한이 있음")
	}
	if response := start(t, service, job, "time-limit-start"); response.Error != "" {
		t.Fatal(response.Error)
	}
	select {
	case <-published:
	case <-time.After(3 * time.Second):
		t.Fatal("시작 게시를 기다리지 못했습니다")
	}
	if err := <-changed; err == nil {
		t.Fatal("바뀐 시간 상한을 받음")
	}
	running := status(t, service, job.ID)
	if !sameTimeLimit(running.TimeLimit, limit) || running.State != Running {
		t.Fatalf("실행 중 시간 상한: %+v", running.TimeLimit)
	}
	// 공개 사본을 바꿔도 보호 기록은 그대로다.
	running.TimeLimit.ModelDrainMS = 1
	if !sameTimeLimit(status(t, service, job.ID).TimeLimit, limit) {
		t.Fatal("공개 시간 상한이 보호 기록과 공유됨")
	}
	stop := request(Stop)
	stop.JobID, stop.ExpectedRevision = job.ID, status(t, service, job.ID).Revision
	if result := call(t, service, stop); result.Error != "" {
		t.Fatal(result.Error)
	}
	closed := awaitState(t, service, job.ID, Cancelled)
	awaitExecutionReturned(t, service, job.ID)
	if !sameTimeLimit(closed.TimeLimit, limit) {
		t.Fatal("중지 마감이 시간 상한을 지움")
	}
	if err := service.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	restored, err := New(t.Context(), root, backend)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Shutdown(t.Context())
	if got := status(t, restored, job.ID); !sameTimeLimit(got.TimeLimit, limit) || backend.calls.Load() != 1 {
		t.Fatal("재기동이 시간 상한을 바꾸거나 재실행함")
	}
}

// 시작이 확인된 뒤(구 backend·누락)에는 시간 상한을 뒤늦게 채우지 않는다. 식이 맞지 않거나 준비 전인 값은 받지 않는다.
func TestTimeLimitRejectsBackfillAndMalformedValues(t *testing.T) {
	deadline := time.Date(2026, 11, 27, 3, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		limit func() *TimeLimit
		late  bool
	}{
		{"backfill after start", func() *TimeLimit { return syntheticTimeLimit(deadline) }, true},
		{"sum mismatch", func() *TimeLimit { l := syntheticTimeLimit(deadline); l.ShutdownMarginMS++; return l }, false},
		{"negative part", func() *TimeLimit {
			l := syntheticTimeLimit(deadline)
			l.ModelDrainMS, l.ShutdownMarginMS = -1, l.ShutdownMarginMS+1
			return l
		}, false},
		{"part over day", func() *TimeLimit {
			l := syntheticTimeLimit(deadline)
			l.ConfirmationReserveMS = int64(25 * time.Hour / time.Millisecond)
			l.ModelCloseAt = deadline.Add(-time.Duration(l.ModelDrainMS+l.ConfirmationReserveMS+l.ShutdownMarginMS) * time.Millisecond)
			return l
		}, false},
		{"overflowed part", func() *TimeLimit {
			l := syntheticTimeLimit(deadline)
			// 2^58 ms × 10^6 ns는 int64 Duration에서 0으로 돌아와 기존 검사를 통과했다.
			l.ModelDrainMS += 1 << 58
			return l
		}, false},
		{"zero deadline", func() *TimeLimit { return &TimeLimit{} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, backend, _ := newFixture(t)
			result := make(chan error, 1)
			backend.run = func(ctx context.Context, _ Execution, publish Publish) error {
				u := Update{State: Preparing, Phase: Gateway, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityConsumed}
				if tc.late {
					u.State, u.Phase, u.Started = Running, ExecutionPhase, true
				}
				if err := publish(u); err != nil {
					return err
				}
				u.TimeLimit = tc.limit()
				result <- publish(u)
				<-ctx.Done()
				u.TimeLimit = nil
				u.State, u.Phase, u.Started, u.DrainComplete, u.ResultReady = Cancelled, Closure, true, true, true
				return publish(u)
			}
			job := create(t, service, "time-limit-bad")
			if response := start(t, service, job, "time-limit-bad-start"); response.Error != "" {
				t.Fatal(response.Error)
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("잘못된 시간 상한을 받음")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("게시를 기다리지 못했습니다")
			}
			if status(t, service, job.ID).TimeLimit != nil {
				t.Fatal("거부한 시간 상한이 상태에 남음")
			}
		})
	}
}

// 해시 연속성이 유효한 기록에서도 시간 상한 지움·변경·사후 보충을 거부한다. 같은 검증기는 부팅과 압축 후보에 쓰인다.
func TestTimeLimitJournalReplayRejectsRemovalRewriteAndBackfill(t *testing.T) {
	input := Input{TargetURL: "https://example.test/", ProfileID: "local"}
	created := receipt{Principal: "owner", CreateKey: "journal-time-limit", Job: Job{
		ID: "prep-time-limit", Revision: 1, SourceDigest: sourceDigest(input, ""), Input: input,
		State: Ready, Phase: DraftPhase, AuthorityState: AuthorityNotIssued,
	}}
	deadline := time.Date(2026, 11, 27, 3, 0, 0, 0, time.UTC)
	for _, change := range []string{"remove", "rewrite", "backfill"} {
		t.Run(change, func(t *testing.T) {
			old := cloneReceipt(created)
			old.StartKey = "journal-time-limit-start"
			old.Job.Revision++
			old.Job.State, old.Job.Phase, old.Job.TaskID = Running, ExecutionPhase, "42"
			old.Job.PreparationStarted, old.Job.Started, old.Job.AuthorityState = true, true, AuthorityConsumed
			if change != "backfill" {
				old.Job.TimeLimit = syntheticTimeLimit(deadline)
			}
			next := cloneReceipt(old)
			next.Job.Revision++
			switch change {
			case "remove":
				next.Job.TimeLimit = nil
			case "rewrite":
				next.Job.TimeLimit.RunDeadlineAt = deadline.Add(time.Minute)
				next.Job.TimeLimit.ModelCloseAt = next.Job.TimeLimit.ModelCloseAt.Add(time.Minute)
			case "backfill":
				next.Job.TimeLimit = syntheticTimeLimit(deadline)
			}
			if !validJob(old.Job) || !validJob(next.Job) || !validReceiptChange(created, old) {
				t.Fatal("시간 상한 변경 이외의 계약을 어긴 음성 대조")
			}
			if validReceiptChange(old, next) {
				t.Fatal("시간 상한 변경을 보호 저널이 수용함")
			}
			first, _ := json.Marshal(created)
			second, _ := json.Marshal(old)
			third, _ := json.Marshal(next)
			records := []json.RawMessage{first, second, third}
			for _, snapshots := range []int{0, 1} {
				if _, err := replayPreparationReceipts(t.Context(), records, snapshots); err == nil {
					t.Fatalf("snapshotCount %d: 변경된 시간 상한으로 저널을 복원함", snapshots)
				}
			}
		})
	}
}
