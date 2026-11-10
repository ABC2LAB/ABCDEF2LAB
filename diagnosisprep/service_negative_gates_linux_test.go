//go:build linux

package diagnosisprep

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func awaitNegativeGateResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("backend did not reach the expected gate")
		return ErrUnavailable
	}
}

func TestStartRejectsOtherActiveJobBeforeExecute(t *testing.T) {
	for _, state := range []State{Preparing, Running} {
		for _, principal := range []string{"owner", "other-owner"} {
			t.Run(string(state)+"/"+principal, func(t *testing.T) {
				s, b, _ := newFixture(t)
				entered := make(chan error, 1)
				b.run = func(ctx context.Context, _ Execution, p Publish) error {
					u := Update{State: state, Phase: Planning, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityNotIssued}
					if state == Running {
						u.Phase, u.Started, u.AuthorityState = ExecutionPhase, true, AuthorityConsumed
					}
					err := p(u)
					entered <- err
					if err != nil {
						return err
					}
					<-ctx.Done()
					return ctx.Err()
				}
				first := create(t, s, "first")
				r := request(Create)
				r.Principal, r.IdempotencyKey, r.Input = principal, "second", &first.Input
				second := call(t, s, r)
				if second.Error != "" || second.Job == nil {
					t.Fatalf("second draft failed: %+v", second)
				}
				if got := start(t, s, first, "first-start"); got.Error != "" {
					t.Fatal(got.Error)
				}
				if err := awaitNegativeGateResult(t, entered); err != nil {
					t.Fatal(err)
				}
				activeJob := status(t, s, first.ID)
				if activeJob.State != state {
					t.Fatalf("first state = %s, want %s", activeJob.State, state)
				}

				r = request(Start)
				r.Principal, r.JobID, r.ExpectedRevision = principal, second.Job.ID, second.Job.Revision
				r.IdempotencyKey = "second-start"
				if got := call(t, s, r).Error; got != "busy" {
					t.Fatalf("start with an active job = %q, want busy", got)
				}
				r = request(Status)
				r.Principal, r.JobID = principal, second.Job.ID
				if got := call(t, s, r).Job; !reflect.DeepEqual(got, second.Job) {
					t.Fatalf("busy start changed the second draft: before %+v, after %+v", second.Job, got)
				}
				if got := status(t, s, first.ID); !reflect.DeepEqual(got, activeJob) {
					t.Fatal("busy start changed the active job")
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := s.Shutdown(ctx); err != nil {
					t.Fatal(err)
				}
				if got := b.calls.Load(); got != 1 {
					t.Fatalf("Execute calls = %d, want only the active job", got)
				}
			})
		}
	}
}

func TestPublishAfterStopCannotRestoreRunning(t *testing.T) {
	s, b, _ := newFixture(t)
	entered, late := make(chan error, 1), make(chan error, 1)
	release := make(chan struct{})
	var released sync.Once
	t.Cleanup(func() { released.Do(func() { close(release) }) })
	b.run = func(ctx context.Context, _ Execution, p Publish) error {
		u := Update{State: Running, Phase: ExecutionPhase, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityConsumed, Started: true}
		err := p(u)
		entered <- err
		if err != nil {
			return err
		}
		<-ctx.Done()
		late <- p(u)
		<-release
		return ctx.Err()
	}
	j := create(t, s, "create")
	if got := start(t, s, j, "start"); got.Error != "" {
		t.Fatal(got.Error)
	}
	if err := awaitNegativeGateResult(t, entered); err != nil {
		t.Fatal(err)
	}
	before := status(t, s, j.ID)
	r := request(Stop)
	r.JobID, r.ExpectedRevision = j.ID, before.Revision
	stopped := call(t, s, r)
	if stopped.Error != "" || stopped.Job == nil || stopped.Job.State != Stopping {
		t.Fatalf("stop did not persist stopping: %+v", stopped)
	}
	if err := awaitNegativeGateResult(t, late); err != nil {
		t.Fatalf("same-phase late update was rejected instead of preserving stopping: %v", err)
	}
	got := status(t, s, j.ID)
	if got.State != Stopping || got.Phase != ExecutionPhase || got.TaskID != "42" ||
		!got.PreparationStarted || !got.Started || got.AuthorityState != AuthorityConsumed ||
		got.DrainComplete || got.ResultReady {
		t.Fatalf("late running update changed stop or known facts: %+v", got)
	}
	released.Do(func() { close(release) })
	final := awaitState(t, s, j.ID, Unknown)
	if !final.Started || final.TaskID != "42" || final.AuthorityState != AuthorityConsumed ||
		final.DrainComplete || final.ResultReady {
		t.Fatalf("unconfirmed cancellation invented or erased facts: %+v", final)
	}
}

func TestPublishRejectsTaskIDAlreadyBoundToAnotherJob(t *testing.T) {
	s, b, _ := newFixture(t)
	rejected := make(chan error, 1)
	release := make(chan struct{})
	b.run = func(ctx context.Context, e Execution, p Publish) error {
		if e.Principal == "owner" {
			return completeRun(ctx, e, p)
		}
		u := Update{State: Preparing, Phase: Planning, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityNotIssued}
		rejected <- p(u)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		u.TaskID = "43"
		if err := p(u); err != nil {
			return err
		}
		u.State, u.Phase, u.DrainComplete = Failed, Closure, true
		return p(u)
	}
	first := create(t, s, "first")
	if got := start(t, s, first, "first-start"); got.Error != "" {
		t.Fatal(got.Error)
	}
	completed := awaitState(t, s, first.ID, Completed)
	r := request(Create)
	r.Principal, r.IdempotencyKey, r.Input = "other-owner", "second", &first.Input
	second := call(t, s, r)
	if second.Error != "" || second.Job == nil {
		t.Fatalf("second draft failed: %+v", second)
	}
	r = request(Start)
	r.Principal, r.JobID, r.ExpectedRevision, r.IdempotencyKey = "other-owner", second.Job.ID, second.Job.Revision, "second-start"
	started := call(t, s, r)
	if started.Error != "" || started.Job == nil {
		t.Fatalf("second start failed: %+v", started)
	}
	if err := awaitNegativeGateResult(t, rejected); !errors.Is(err, ErrProtocol) {
		t.Fatalf("duplicate task publication = %v, want ErrProtocol", err)
	}
	r = request(Status)
	r.Principal, r.JobID = "other-owner", second.Job.ID
	if got := call(t, s, r).Job; !reflect.DeepEqual(got, started.Job) {
		t.Fatalf("rejected task binding changed the second job: before %+v, after %+v", started.Job, got)
	}
	if got := status(t, s, first.ID); !reflect.DeepEqual(got, completed) {
		t.Fatal("duplicate task publication changed the original job")
	}
	r = request(AuthorizeTask)
	r.TaskID = "42"
	owned := call(t, s, r)
	if owned.Authorized == nil || !*owned.Authorized || owned.Job == nil || owned.Job.ID != first.ID {
		t.Fatal("original task ownership was lost")
	}
	r.Principal = "other-owner"
	denied := call(t, s, r)
	if denied.Authorized == nil || *denied.Authorized || denied.Job != nil {
		t.Fatal("duplicate task publication transferred ownership")
	}

	close(release)
	r = request(Status)
	r.Principal, r.JobID = "other-owner", second.Job.ID
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := call(t, s, r).Job
		if got.State == Failed {
			if got.TaskID != "43" || got.Started || !got.DrainComplete || got.ResultReady {
				t.Fatalf("distinct task binding has wrong facts: %+v", got)
			}
			break
		}
		if got.State == Unknown || time.Now().After(deadline) {
			t.Fatalf("distinct task binding did not complete: %+v", got)
		}
		time.Sleep(time.Millisecond)
	}
	r = request(AuthorizeTask)
	r.Principal, r.TaskID = "other-owner", "43"
	owned = call(t, s, r)
	if owned.Authorized == nil || !*owned.Authorized || owned.Job == nil || owned.Job.ID != second.Job.ID {
		t.Fatal("distinct task binding was not accepted")
	}
	if got := b.calls.Load(); got != 2 {
		t.Fatalf("Execute calls = %d, want 2", got)
	}
}

func TestStartRejectsChangedInstallationOrDelegationBeforeExecute(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Inspection)
	}{
		{"installation_digest", func(i *Inspection) { i.InstallationDigest = strings.Repeat("b", 64) }},
		{"delegation_ref", func(i *Inspection) { i.DelegationRef = "replacement-grant" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, b, _ := newFixture(t)
			j := create(t, s, "create")
			b.mu.Lock()
			test.change(&b.inspection)
			b.mu.Unlock()
			if got := start(t, s, j, "start").Error; got != "approval_changed" {
				t.Fatalf("changed installation start = %q, want approval_changed", got)
			}
			if got := status(t, s, j.ID); !reflect.DeepEqual(got, j) {
				t.Fatalf("rejected start changed the approved draft: before %+v, after %+v", j, got)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := s.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			if got := b.calls.Load(); got != 0 {
				t.Fatalf("changed installation reached Execute %d times", got)
			}
			if got := b.inspectCalls.Load(); got != 2 {
				t.Fatalf("Inspect calls = %d, want create and start revalidation", got)
			}
		})
	}
}

// 로컬 마감이 확인돼도 외부 모델/대상의 미확인 결과와 예약을 해제하지 않는다.
func TestDrainedUnknownKeepsInstallationBlocked(t *testing.T) {
	s, b, root := newFixture(t)
	published := make(chan error, 1)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	usage := &ModelUsage{Requests: 1, ReservedInputTokens: 30, ReservedOutputTokens: 20}
	b.run = func(ctx context.Context, _ Execution, p Publish) error {
		err := p(Update{State: Unknown, Phase: Closure, PreparationStarted: true, DrainComplete: true, AuthorityState: AuthorityNotIssued, ModelUsage: usage, ReasonCode: "model_usage_unknown"})
		published <- err
		select {
		case <-release:
		case <-ctx.Done():
		}
		return err
	}
	first := create(t, s, "unknown-create")
	if got := start(t, s, first, "unknown-start"); got.Error != "" {
		t.Fatal(got.Error)
	}
	if err := awaitNegativeGateResult(t, published); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	handle := s.executions[first.ID]
	s.mu.Unlock()
	once.Do(func() { close(release) })
	select {
	case <-handle.done:
	case <-time.After(time.Second):
		t.Fatal("backend did not finish after the unknown publication")
	}
	before := status(t, s, first.ID)
	if before.State != Unknown || !before.DrainComplete || before.ResultReady || before.Started || !reflect.DeepEqual(before.ModelUsage, usage) {
		t.Fatalf("unknown facts changed: %+v", before)
	}
	second := create(t, s, "second-create")
	if got := start(t, s, second, "second-start").Error; got != "busy" {
		t.Fatalf("drained unknown start = %q, want busy", got)
	}
	if got := status(t, s, second.ID); !reflect.DeepEqual(got, second) {
		t.Fatal("blocked draft changed")
	}
	if got := status(t, s, first.ID); !reflect.DeepEqual(got, before) {
		t.Fatal("unknown usage or reservation changed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(context.Background(), root, b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Shutdown(context.Background()) })
	if got := status(t, restarted, first.ID); !reflect.DeepEqual(got, before) {
		t.Fatal("restart erased unknown or reservations")
	}
	if got := start(t, restarted, second, "second-start").Error; got != "busy" {
		t.Fatalf("restart bypassed unknown: %q", got)
	}
	if b.calls.Load() != 1 {
		t.Fatal("unknown caused another Execute")
	}
}

func TestDrainedKnownCancellationAllowsNextRunWithoutRefund(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart_%t", restart), func(t *testing.T) {
			s, b, root := newFixture(t)
			persisted := make(chan error, 1)
			usage := &ModelUsage{Requests: 1, ReservedInputTokens: 30, ReservedOutputTokens: 20, InputTokens: 2, OutputTokens: 1, UsageKnown: true}
			b.run = func(ctx context.Context, e Execution, p Publish) error {
				if b.calls.Load() > 1 {
					return completeRun(ctx, e, p)
				}
				err := p(Update{State: Cancelled, Phase: Closure, PreparationStarted: true, TaskID: "42", DrainComplete: true, AuthorityState: AuthorityNotIssued, ModelUsage: usage, ReasonCode: "target_approval_ended"})
				persisted <- err
				return err
			}
			first := create(t, s, "known-create")
			if got := start(t, s, first, "known-start"); got.Error != "" {
				t.Fatal(got.Error)
			}
			if err := awaitNegativeGateResult(t, persisted); err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			handle := s.executions[first.ID]
			s.mu.Unlock()
			if handle.done != nil {
				select {
				case <-handle.done:
				case <-time.After(time.Second):
					t.Fatal("drain incomplete")
				}
			}
			before := status(t, s, first.ID)
			if before.State != Cancelled || !before.DrainComplete || !reflect.DeepEqual(before.ModelUsage, usage) {
				t.Fatal("known cancellation lost")
			}
			if restart {
				if s.Shutdown(t.Context()) != nil {
					t.Fatal("shutdown")
				}
				var err error
				s, err = New(t.Context(), root, b)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
			}
			second := create(t, s, "known-next-create")
			if got := start(t, s, second, "known-next-start"); got.Error != "" {
				t.Fatal("known cancellation blocked next run", got.Error)
			}
			if got := status(t, s, first.ID); !reflect.DeepEqual(got, before) {
				t.Fatal("old usage/reservation refunded or changed")
			}
		})
	}
}
