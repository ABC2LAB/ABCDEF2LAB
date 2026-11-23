package diagnosisprep

import (
	"context"
	"testing"
	"time"
)

func hideRun(t *testing.T, s Endpoint, j Job) Response {
	t.Helper()
	r := request(Hide)
	r.JobID = j.ID
	r.ExpectedRevision = j.Revision
	return call(t, s, r)
}

func statusError(t *testing.T, s Endpoint, id string) string {
	t.Helper()
	r := request(Status)
	r.JobID = id
	return call(t, s, r).Error
}

// finishedRun은 끝까지 돌고 실행 반환이 기록된 회차를 만든다.
func finishedRun(t *testing.T, s *Service, key string) Job {
	t.Helper()
	j := create(t, s, key)
	start(t, s, j, key+"-start")
	awaitState(t, s, j.ID, Completed)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		returned := s.jobs[j.ID].ExecutionReturned
		s.mu.Unlock()
		if returned {
			return status(t, s, j.ID)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("실행 반환 미기록")
	return Job{}
}

func listedRun(t *testing.T, s Endpoint, id string) bool {
	t.Helper()
	for _, run := range runList(t, s, "owner", RunListRequest{}).RunPage.Runs {
		if run.ID == id {
			return true
		}
	}
	return false
}

// 끝난 회차를 숨기면 목록·조회·보고서 권한에서 사라지지만, 저널에는 남아 실행 횟수·예산을 계속 센다. Job은 그대로이고
// 숨긴 주체·시각이 남는다. 재기동·압축 뒤에도 같다.
func TestHideFinishedRunRemovesItFromViewsAndKeepsBudget(t *testing.T) {
	s, b, root := newFixture(t)
	b.inspection.MaxRuns = 1
	j := finishedRun(t, s, "create")
	if !listedRun(t, s, j.ID) {
		t.Fatal("시험 준비: 목록에 없음")
	}
	stale := j
	stale.Revision--
	if got := hideRun(t, s, stale).Error; got != "revision_conflict" {
		t.Fatal("옛 revision", got)
	}
	before := time.Now().UTC()
	res := hideRun(t, s, j)
	if res.Error != "" || res.Job.ID != j.ID || res.Job.Revision != j.Revision {
		t.Fatal("숨기기", res.Error)
	}
	s.mu.Lock()
	record := s.jobs[j.ID]
	s.mu.Unlock()
	if !record.Hidden || record.HiddenAt == nil || record.HiddenAt.Before(before.Add(-time.Second)) || record.Principal != "owner" || record.Job.State != Completed {
		t.Fatalf("숨김 기록 %+v", record)
	}
	assertHidden := func(s *Service) {
		t.Helper()
		if got := statusError(t, s, j.ID); got != "not_found" {
			t.Fatal("조회", got)
		}
		if got := hideRun(t, s, j).Error; got != "not_found" {
			t.Fatal("두 번 숨기기", got)
		}
		lookup := request(Lookup)
		lookup.IdempotencyKey = "create"
		if got := call(t, s, lookup).Error; got != "not_found" {
			t.Fatal("생성 키 조회", got)
		}
		authorize := request(AuthorizeTask)
		authorize.TaskID = "42"
		if res := call(t, s, authorize); res.Authorized == nil || *res.Authorized {
			t.Fatal("숨긴 작업의 보고서 권한이 남음")
		}
		again := request(Create)
		again.IdempotencyKey = "create"
		again.Input = &Input{TargetURL: "https://example.test/read", ProfileID: "local"}
		if got := call(t, s, again).Error; got != "idempotency_conflict" {
			t.Fatal("같은 생성 키로 되살림", got)
		}
		if listedRun(t, s, j.ID) {
			t.Fatal("목록에 남음")
		}
	}
	assertHidden(s)
	// 숨긴 회차도 실행 횟수에 든다(max_runs 1 → 다음 시작은 예산 소진)
	next := create(t, s, "next")
	if got := start(t, s, next, "next-start").Error; got != "budget_exhausted" {
		t.Fatal("숨긴 회차가 실행 횟수를 돌려줌", got)
	}
	for range 2 {
		if err := s.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		var err error
		s, err = New(context.Background(), root, b)
		if err != nil {
			t.Fatal("재기동", err)
		}
		assertHidden(s)
	}
	defer s.Shutdown(context.Background())
	if got := start(t, s, status(t, s, next.ID), "next-start-2").Error; got != "budget_exhausted" {
		t.Fatal("재기동 뒤 실행 횟수", got)
	}
}

// 끝나지 않았거나 결과 불명인 회차는 숨기지 않는다.
func TestHideRefusesUnfinishedRuns(t *testing.T) {
	s, b, _ := newFixture(t)
	draft := create(t, s, "draft")
	if got := hideRun(t, s, draft).Error; got != "run_active" {
		t.Fatal("시작 전 초안", got)
	}
	release := make(chan struct{})
	b.run = func(ctx context.Context, _ Execution, p Publish) error {
		if err := p(Update{State: Preparing, Phase: TaskCreation, PreparationStarted: true, AuthorityState: AuthorityNotIssued}); err != nil {
			return err
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return p(Update{State: Unknown, Phase: Closure, PreparationStarted: true, AuthorityState: AuthorityNotIssued})
	}
	b.inspection.MaxRuns = 10
	b.inspection.AggregateBudget = Budget{Requests: 40, ModelRequests: 30, InputTokens: 300, OutputTokens: 200, CostUSDMicros: 100, DurationSeconds: 600}
	live := create(t, s, "live")
	start(t, s, live, "live-start")
	awaitState(t, s, live.ID, Preparing)
	// 시작 기록이 뒤따라 revision을 올릴 수 있어 숨기기 직전 상태로 보낸다
	time.Sleep(20 * time.Millisecond)
	if got := hideRun(t, s, status(t, s, live.ID)).Error; got != "run_active" {
		t.Fatal("진행 중", got)
	}
	close(release)
	unknown := awaitState(t, s, live.ID, Unknown)
	if got := hideRun(t, s, unknown).Error; got != "run_active" {
		t.Fatal("결과 불명", got)
	}
	if !listedRun(t, s, live.ID) || !listedRun(t, s, draft.ID) {
		t.Fatal("숨기면 안 되는 회차가 목록에서 빠짐")
	}
}

// 저널을 고쳐 숨김을 되돌리거나, 끝나지 않은 회차에 숨김을 붙이거나, 시각 없이 숨기면 기동하지 않는다.
func TestHiddenReceiptTamperingRejectedOnReplay(t *testing.T) {
	for name, tamper := range map[string]func(s *Service, hidden, draft, finished string){
		"숨김 되돌리기": func(s *Service, hidden, _, _ string) {
			r := cloneReceipt(s.jobs[hidden])
			r.Hidden, r.HiddenAt = false, nil
			_ = s.save(r)
		},
		"시작 전 초안에 숨김": func(s *Service, _, draft, _ string) {
			r := cloneReceipt(s.jobs[draft])
			at := time.Now().UTC()
			r.Hidden, r.HiddenAt = true, &at
			_ = s.save(r)
		},
		// 숨길 수 있는 끝난 회차라도 숨긴 시각이 없으면 거부한다
		"시각 없는 숨김": func(s *Service, _, _, finished string) {
			r := cloneReceipt(s.jobs[finished])
			r.Hidden = true
			_ = s.save(r)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, b, root := newFixture(t)
			j := finishedRun(t, s, "create")
			if got := hideRun(t, s, j).Error; got != "" {
				t.Fatal(got)
			}
			// 두 번째 회차는 다른 작업 번호로 끝낸다(같은 작업 번호는 한 회차만 가진다)
			b.run = func(ctx context.Context, e Execution, p Publish) error {
				return completeRun(ctx, e, func(u Update) error {
					if u.TaskID != "" {
						u.TaskID = "43"
					}
					return p(u)
				})
			}
			other := finishedRun(t, s, "other")
			draft := create(t, s, "draft")
			s.mu.Lock()
			tamper(s, j.ID, draft.ID, other.ID)
			s.mu.Unlock()
			if err := s.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			if again, err := New(context.Background(), root, b); err == nil {
				_ = again.Shutdown(context.Background())
				t.Fatal("고친 저널로 기동함")
			}
		})
	}
}
