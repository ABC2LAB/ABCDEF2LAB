package diagnosisprep

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// purgingBackend는 끝난 회차 결과 삭제(RunPurger)를 흉내 낸다. 실제 DB·폴더 삭제는 제품 구현이 맡는다.
type purgingBackend struct {
	*fakeBackend
	mu     sync.Mutex
	purged []Execution
	fail   error
}

func (b *purgingBackend) PurgeRun(_ context.Context, e Execution) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return b.fail
	}
	b.purged = append(b.purged, e)
	return nil
}

func newPurgeFixture(t *testing.T) (*Service, *purgingBackend, string) {
	t.Helper()
	b := &purgingBackend{fakeBackend: &fakeBackend{inspection: fixtureInspection()}}
	root := privateDir(t)
	s, err := New(context.Background(), root, b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return s, b, root
}

func deleteRun(t *testing.T, s Endpoint, j Job) Response {
	t.Helper()
	r := request(Delete)
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
	page := runList(t, s, "owner", RunListRequest{}).RunPage
	for _, run := range page.Runs {
		if run.ID == id {
			return true
		}
	}
	return false
}

// 끝난 회차를 지우면 목록·조회·보고서 권한에서 사라지지만, 저널에는 남아 실행 횟수·예산을 계속 센다. 재기동·압축 뒤에도 같다.
func TestDeleteFinishedRunRemovesItAndKeepsBudget(t *testing.T) {
	s, b, root := newPurgeFixture(t)
	b.inspection.MaxRuns = 1
	j := finishedRun(t, s, "create")
	if !listedRun(t, s, j.ID) {
		t.Fatal("시험 준비: 목록에 없음")
	}
	stale := j
	stale.Revision--
	if got := deleteRun(t, s, stale).Error; got != "revision_conflict" {
		t.Fatal("옛 revision", got)
	}
	res := deleteRun(t, s, j)
	if res.Error != "" || res.Job.ID != j.ID {
		t.Fatal("삭제", res.Error)
	}
	if len(b.purged) != 1 || b.purged[0].Job.ID != j.ID || b.purged[0].Job.TaskID != "42" || b.purged[0].Principal != "owner" {
		t.Fatalf("결과 삭제 호출 %+v", b.purged)
	}
	assertGone := func(s *Service) {
		t.Helper()
		if got := statusError(t, s, j.ID); got != "not_found" {
			t.Fatal("조회", got)
		}
		if got := deleteRun(t, s, j).Error; got != "not_found" {
			t.Fatal("두 번 지우기", got)
		}
		lookup := request(Lookup)
		lookup.IdempotencyKey = "create"
		if got := call(t, s, lookup).Error; got != "not_found" {
			t.Fatal("생성 키 조회", got)
		}
		authorize := request(AuthorizeTask)
		authorize.TaskID = "42"
		if res := call(t, s, authorize); res.Authorized == nil || *res.Authorized {
			t.Fatal("지운 작업의 보고서 권한이 남음")
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
	assertGone(s)
	// 지운 회차도 실행 횟수에 든다(max_runs 1 → 다음 시작은 예산 소진)
	next := create(t, s, "next")
	if got := start(t, s, next, "next-start").Error; got != "budget_exhausted" {
		t.Fatal("지운 회차가 실행 횟수를 돌려줌", got)
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
		assertGone(s)
	}
	defer s.Shutdown(context.Background())
	if got := start(t, s, status(t, s, next.ID), "next-start-2").Error; got != "budget_exhausted" {
		t.Fatal("재기동 뒤 실행 횟수", got)
	}
}

// 끝나지 않았거나 결과 불명인 회차는 지우지 않는다.
func TestDeleteRefusesUnfinishedRuns(t *testing.T) {
	s, b, _ := newPurgeFixture(t)
	draft := create(t, s, "draft")
	if got := deleteRun(t, s, draft).Error; got != "run_active" {
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
	// 시작 기록이 뒤따라 revision을 올릴 수 있어 지우기 직전 상태로 보낸다
	time.Sleep(20 * time.Millisecond)
	if got := deleteRun(t, s, status(t, s, live.ID)).Error; got != "run_active" {
		t.Fatal("진행 중", got)
	}
	close(release)
	unknown := awaitState(t, s, live.ID, Unknown)
	if got := deleteRun(t, s, unknown).Error; got != "run_active" {
		t.Fatal("결과 불명", got)
	}
	if len(b.purged) != 0 {
		t.Fatal("지우면 안 되는 회차의 결과를 지움")
	}
}

// 결과 삭제가 실패하면 표시하지 않아 다시 시도할 수 있다. 삭제 구현이 없는 backend는 지우지 않는다.
func TestDeleteBackendFailureKeepsRunAndRetries(t *testing.T) {
	s, b, _ := newPurgeFixture(t)
	j := finishedRun(t, s, "create")
	b.fail = errors.New("db unavailable")
	if got := deleteRun(t, s, j).Error; got != "unavailable" {
		t.Fatal("삭제 실패", got)
	}
	if got := statusError(t, s, j.ID); got != "" || !listedRun(t, s, j.ID) {
		t.Fatal("실패한 삭제가 회차를 숨김", got)
	}
	b.fail = nil
	if got := deleteRun(t, s, j).Error; got != "" {
		t.Fatal("다시 시도", got)
	}

	plain, _, _ := newFixture(t)
	other := finishedRun(t, plain, "plain")
	if got := deleteRun(t, plain, other).Error; got != "unavailable" {
		t.Fatal("삭제 구현 없는 backend", got)
	}
	if got := statusError(t, plain, other.ID); got != "" {
		t.Fatal("구현 없는데 숨김", got)
	}
}

// 저널을 고쳐 지운 표시를 되돌리거나, 끝나지 않은 회차에 지운 표시를 붙이면 기동하지 않는다.
func TestPurgedReceiptTamperingRejectedOnReplay(t *testing.T) {
	for name, tamper := range map[string]func(s *Service, finished, live string){
		"지운 표시 되돌리기": func(s *Service, finished, _ string) {
			r := cloneReceipt(s.jobs[finished])
			r.Purged = false
			_ = s.save(r)
		},
		"진행 중 회차에 지운 표시": func(s *Service, _, live string) {
			r := cloneReceipt(s.jobs[live])
			r.Purged = true
			_ = s.save(r)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, b, root := newPurgeFixture(t)
			j := finishedRun(t, s, "create")
			if got := deleteRun(t, s, j).Error; got != "" {
				t.Fatal(got)
			}
			draft := create(t, s, "draft")
			s.mu.Lock()
			tamper(s, j.ID, draft.ID)
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
