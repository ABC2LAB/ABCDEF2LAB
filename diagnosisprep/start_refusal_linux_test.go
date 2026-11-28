//go:build linux

package diagnosisprep

import (
	"context"
	"errors"
	"testing"
	"time"
)

// startBlocked는 새 작업을 만들어 시작해 보고, busy와 거절 사유를 확인한다. 거절이면 backend를 다시 부르지 않는다.
func startBlocked(t *testing.T, s *Service, b *fakeBackend, key, want string) {
	t.Helper()
	before := b.calls.Load()
	job := create(t, s, key)
	res := start(t, s, job, key+"-start")
	if res.Error != "busy" || res.BusyReason != want || res.Job != nil {
		t.Fatalf("시작 거절: error=%q busy_reason=%q want %q", res.Error, res.BusyReason, want)
	}
	if b.calls.Load() != before {
		t.Fatal("거절된 시작이 실행을 불렀다")
	}
	if err := ValidateResponse(func() Request {
		r := request(Start)
		r.JobID = job.ID
		r.IdempotencyKey = key + "-start"
		r.ExpectedRevision = job.Revision
		return r
	}(), res); err != nil {
		t.Fatal("거절 응답이 계약 검사를 통과하지 못함", err)
	}
}

// 새 시작 거절 사유는 운영자 해제 판단과 같은 근거에서 나온다(해제 상태가 바뀌면 사유도 같이 바뀐다). 결과 불명은 그대로다.
func TestStartBusyReasonFollowsReleaseView(t *testing.T) {
	t.Run("closure unconfirmed then release available then pending restart", func(t *testing.T) {
		f := unknownFixture(t)
		f.clock.Advance(DefaultReleaseQuiet - time.Second)
		if view := releaseStatus(t, f.s); view.Refusal != ReleaseNotQuiet {
			t.Fatalf("fixture view: %#v", view)
		}
		startBlocked(t, f.s, f.b.fakeBackend, "after-unknown", BusyClosureUnconfirmed)
		f.clock.Advance(time.Second)
		if view := releaseStatus(t, f.s); !view.Eligible {
			t.Fatalf("fixture view: %#v", view)
		}
		startBlocked(t, f.s, f.b.fakeBackend, "after-quiet", BusyReleaseAvailable)
		if res := release(t, f.s, f.job, "운영자 확인"); res.Error != "" {
			t.Fatalf("해제 기록 실패: %#v", res)
		}
		startBlocked(t, f.s, f.b.fakeBackend, "after-release", BusyReleasePendingRestart)
		if got := status(t, f.s, f.job.ID); got.State != Unknown {
			t.Fatal("결과 불명이 다른 상태로 바뀜")
		}
	})
	t.Run("stopping then release available", func(t *testing.T) {
		f := hungFixture(t)
		startBlocked(t, f.s, f.b.fakeBackend, "while-stopping", BusyRunStopping)
		f.clock.Advance(DefaultReleaseQuiet)
		startBlocked(t, f.s, f.b.fakeBackend, "stopping-quiet", BusyReleaseAvailable)
	})
	t.Run("run active", func(t *testing.T) {
		service, backend, _ := newFixture(t)
		published, unblock := make(chan struct{}), make(chan struct{})
		defer close(unblock)
		backend.run = func(ctx context.Context, e Execution, p Publish) error {
			if err := p(Update{State: Running, Phase: ExecutionPhase, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityConsumed, Started: true}); err != nil {
				return err
			}
			close(published)
			select {
			case <-unblock:
			case <-ctx.Done():
			}
			return p(Update{State: Cancelled, Phase: Closure, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityConsumed, Started: true, DrainComplete: true, ResultReady: true})
		}
		job := create(t, service, "active")
		if start(t, service, job, "active-start").Error != "" {
			t.Fatal("시작 실패")
		}
		<-published
		startBlocked(t, service, backend, "second", BusyRunActive)
	})
	t.Run("terminal state published before execution returns", func(t *testing.T) {
		service, backend, _ := newFixture(t)
		published, unblock := make(chan struct{}), make(chan struct{})
		publishErr := make(chan error, 1)
		defer close(unblock)
		backend.run = func(_ context.Context, _ Execution, p Publish) error {
			if err := p(Update{State: Running, Phase: ExecutionPhase, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityConsumed, Started: true}); err != nil {
				publishErr <- err
				return err
			}
			if err := p(Update{State: Cancelled, Phase: Closure, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityConsumed, Started: true, DrainComplete: true, ResultReady: true}); err != nil {
				publishErr <- err
				return err
			}
			close(published)
			<-unblock
			return nil
		}
		job := create(t, service, "terminal")
		if start(t, service, job, "terminal-start").Error != "" {
			t.Fatal("시작 실패")
		}
		select {
		case <-published:
		case err := <-publishErr:
			t.Fatalf("끝 상태 게시 실패: %v", err)
		case <-time.After(3 * time.Second):
			t.Fatal("끝 상태 게시를 기다리지 못함")
		}
		if view := releaseStatus(t, service); view.Refusal != ReleaseJobRunning || view.JobState != Cancelled {
			t.Fatalf("끝 상태 게시 뒤 실행 반환 전 해제 상태: %#v", view)
		}
		startBlocked(t, service, backend, "terminal-next", BusyRunStopping)
	})
	t.Run("recovery unsupported", func(t *testing.T) {
		boot := executionBootID()
		if boot == "" {
			t.Skip("boot ID를 읽을 수 없는 환경")
		}
		b := &releaseBackend{fakeBackend: &fakeBackend{inspection: fixtureInspection()}, activity: map[string]time.Time{}, verifyErr: errors.New("실패")}
		b.run = func(_ context.Context, _ Execution, p Publish) error {
			return p(Update{State: Unknown, Phase: Closure, PreparationStarted: true, AuthorityState: AuthorityNotIssued})
		}
		clock := newReleaseClock()
		s := newReleaseService(t, privateDir(t), b, boot, "", clock)
		defer s.Shutdown(context.Background())
		job := create(t, s, "no-init")
		if start(t, s, job, "no-init-start").Error != "" {
			t.Fatal("시작 실패")
		}
		awaitExecutionReturned(t, s, job.ID)
		clock.Advance(DefaultReleaseQuiet)
		startBlocked(t, s, b.fakeBackend, "no-init-next", BusyRecoveryUnsupported)
	})
	t.Run("candidate verification", func(t *testing.T) {
		service, backend, _ := newFixture(t)
		service.mu.Lock()
		service.candidateActive[7] = true
		service.mu.Unlock()
		startBlocked(t, service, backend, "candidate", BusyCandidateVerification)
	})
	t.Run("multiple blockers", func(t *testing.T) {
		f := unknownFixture(t)
		f.s.mu.Lock()
		other := cloneReceipt(f.s.jobs[f.job.ID])
		other.Job.ID, other.StartKey = "prep-other-blocker", "other-start"
		f.s.jobs[other.Job.ID] = other
		f.s.mu.Unlock()
		startBlocked(t, f.s, f.b.fakeBackend, "multiple", BusyMultipleBlockers)
	})
}

// busy_reason은 Start의 busy에만, 닫힌 목록 값으로만 온다. 다른 오류·성공 응답·다른 명령에 붙으면 계약 위반이다.
func TestBusyReasonResponseContract(t *testing.T) {
	startReq := request(Start)
	startReq.JobID, startReq.IdempotencyKey, startReq.ExpectedRevision = "prep-a", "key-a", 1
	statusReq := request(Status)
	statusReq.JobID = "prep-a"
	for _, tc := range []struct {
		name  string
		req   Request
		resp  Response
		valid bool
	}{
		{"start busy with reason", startReq, Response{Error: "busy", BusyReason: BusyRunActive}, true},
		{"start busy without reason", startReq, Response{Error: "busy"}, true},
		{"unknown reason", startReq, Response{Error: "busy", BusyReason: "job_running"}, false},
		{"other error", startReq, Response{Error: "budget_exhausted", BusyReason: BusyRunActive}, false},
		{"other command", statusReq, Response{Error: "busy", BusyReason: BusyRunActive}, false},
		{"success", statusReq, Response{Job: &Job{}, BusyReason: BusyRunActive}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.resp.Version, tc.resp.RequestID = Version, tc.req.RequestID
			if err := ValidateResponse(tc.req, tc.resp); (err == nil) != tc.valid {
				t.Fatalf("계약 검사: %v", err)
			}
		})
	}
}
