//go:build linux

package diagnosisprep

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Autumn-27/artex/diagnosis"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeBackend struct {
	mu           sync.Mutex
	inspection   Inspection
	calls        atomic.Int64
	inspectCalls atomic.Int64
	run          func(context.Context, Execution, Publish) error
}

func fixtureInspection() Inspection {
	return Inspection{Scope: &Scope{TargetRef: "target", ScopeRef: "scope", Origin: "https://example.test", PathPrefixes: []string{"/read"}, Methods: []string{"GET"}, QueryPolicy: "any", BodyPolicy: "bounded", MaxBodyBytes: 1024, ApprovalMode: "installed_delegation", AttestationRef: "attestation", Budget: Budget{Requests: 4, ModelRequests: 3, InputTokens: 30, OutputTokens: 20, CostUSDMicros: 10, DurationSeconds: 60}, ExpiresAt: time.Now().UTC().Add(time.Hour)}, InstallationDigest: strings.Repeat("a", 64), DelegationRef: "stable-grant", MaxRuns: 3, AggregateBudget: Budget{Requests: 12, ModelRequests: 9, InputTokens: 90, OutputTokens: 60, CostUSDMicros: 30, DurationSeconds: 180}}
}
func (b *fakeBackend) Profiles(context.Context, string) ([]Profile, error) {
	return []Profile{{ID: "local", Label: "로컬", ModelProfileRef: "model", ModelLabel: "모델"}}, nil
}
func (b *fakeBackend) Inspect(context.Context, string, Input) (Inspection, error) {
	b.inspectCalls.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	raw, _ := json.Marshal(b.inspection)
	var i Inspection
	_ = json.Unmarshal(raw, &i)
	return i, nil
}
func (b *fakeBackend) Execute(ctx context.Context, e Execution, p Publish) error {
	b.calls.Add(1)
	if b.run != nil {
		return b.run(ctx, e, p)
	}
	return completeRun(ctx, e, p)
}
func completeRun(_ context.Context, _ Execution, p Publish) error {
	u := Update{State: Preparing, Phase: TaskCreation, PreparationStarted: true, AuthorityState: AuthorityNotIssued}
	if err := p(u); err != nil {
		return err
	}
	u.TaskID = "42"
	if err := p(u); err != nil {
		return err
	}
	u.Phase = Planning
	if err := p(u); err != nil {
		return err
	}
	u.Phase = Authority
	if err := p(u); err != nil {
		return err
	}
	u.AuthorityState = AuthorityIssued
	u.Phase = Gateway
	if err := p(u); err != nil {
		return err
	}
	u.AuthorityState = AuthorityConsumed
	if err := p(u); err != nil {
		return err
	}
	u.Started = true
	u.State = Running
	u.Phase = ExecutionPhase
	if err := p(u); err != nil {
		return err
	}
	u.State = Completed
	u.Phase = Report
	u.DrainComplete = true
	u.ResultReady = true
	u.Result = &diagnosis.RunResult{Outcome: diagnosis.OutcomeComplete, IntentOutcomes: diagnosis.IntentOutcomes{Done: 1}}
	return p(u)
}
func privateDir(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("비root 보호 경계 시험")
	}
	p := t.TempDir()
	if err := os.Chmod(p, 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func newFixture(t *testing.T) (*Service, *fakeBackend, string) {
	t.Helper()
	b := &fakeBackend{inspection: fixtureInspection()}
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
func request(command Command) Request {
	return Request{Version: Version, RequestID: "request", Principal: "owner", Command: command}
}
func call(t *testing.T, s Endpoint, r Request) Response {
	t.Helper()
	response, err := s.Call(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateResponse(r, response); err != nil {
		t.Fatalf("invalid response: %#v", response)
	}
	return response
}
func create(t *testing.T, s Endpoint, key string) Job {
	t.Helper()
	r := request(Create)
	r.IdempotencyKey = key
	r.Input = &Input{TargetURL: "https://example.test/read", ProfileID: "local"}
	res := call(t, s, r)
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	return *res.Job
}
func start(t *testing.T, s Endpoint, j Job, key string) Response {
	t.Helper()
	r := request(Start)
	r.JobID = j.ID
	r.ExpectedRevision = j.Revision
	r.IdempotencyKey = key
	return call(t, s, r)
}
func status(t *testing.T, s Endpoint, id string) Job {
	t.Helper()
	r := request(Status)
	r.JobID = id
	res := call(t, s, r)
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	return *res.Job
}
func awaitState(t *testing.T, s Endpoint, id string, state State) Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		j := status(t, s, id)
		if j.State == state {
			return j
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("state did not reach %s", state)
	return Job{}
}
func crash(t *testing.T, s *Service) {
	t.Helper()
	s.mu.Lock()
	s.released = true
	s.closed = true
	s.cancel()
	err := s.journal.close()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}

func TestCreateValidateNoExecutionAndIdempotency(t *testing.T) {
	s, b, _ := newFixture(t)
	j := create(t, s, "create")
	var wg sync.WaitGroup
	var wrong atomic.Int64
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := request(Create)
			r.IdempotencyKey = "create"
			r.Input = &Input{TargetURL: "https://EXAMPLE.test:443/read", ProfileID: "local"}
			res, err := s.Call(context.Background(), r)
			if err != nil || res.Job == nil || res.Job.ID != j.ID {
				wrong.Add(1)
			}
		}()
	}
	wg.Wait()
	if wrong.Load() != 0 || b.calls.Load() != 0 || b.inspectCalls.Load() != 1 {
		t.Fatalf("duplicates %d calls %d inspect %d", wrong.Load(), b.calls.Load(), b.inspectCalls.Load())
	}
	r := request(Create)
	r.IdempotencyKey = "create"
	r.Input = &Input{TargetURL: "https://example.test/other", ProfileID: "local"}
	if got := call(t, s, r).Error; got != "idempotency_conflict" {
		t.Fatal(got)
	}
	r = request(Validate)
	r.JobID = j.ID
	r.ExpectedRevision = j.Revision
	if got := call(t, s, r).Job; got.Revision != j.Revision {
		t.Fatal("no-op validation changed revision")
	}
	r = request(Lookup)
	r.IdempotencyKey = "create"
	r.Principal = "other"
	if got := call(t, s, r).Error; got != "not_found" {
		t.Fatal(got)
	}
}
func TestStartClaimFsyncedBeforeExecutionAndConcurrentDuplicates(t *testing.T) {
	s, b, root := newFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	b.run = func(ctx context.Context, e Execution, p Publish) error {
		raw, err := os.ReadFile(filepath.Join(root, journalName))
		if err != nil || !strings.Contains(string(raw), `"start_key":"start"`) {
			t.Error("missing durable start claim")
		}
		headRaw, err := os.ReadFile(filepath.Join(root, headName))
		if err != nil || len(headRaw) == 0 {
			t.Error("missing durable head")
		}
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return completeRun(ctx, e, p)
	}
	j := create(t, s, "create")
	var wg sync.WaitGroup
	var wrong atomic.Int64
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := request(Start)
			r.JobID = j.ID
			r.ExpectedRevision = j.Revision
			r.IdempotencyKey = "start"
			res, err := s.Call(context.Background(), r)
			if err != nil || res.Error != "" {
				wrong.Add(1)
			}
		}()
	}
	wg.Wait()
	<-entered
	if wrong.Load() != 0 || b.calls.Load() != 1 {
		t.Fatalf("start duplicates %d calls %d", wrong.Load(), b.calls.Load())
	}
	if got := start(t, s, j, "different").Error; got != "idempotency_conflict" {
		t.Fatal(got)
	}
	close(release)
	awaitState(t, s, j.ID, Completed)
}
func TestClientContextCancellationDoesNotCancelAcceptedJob(t *testing.T) {
	s, b, _ := newFixture(t)
	entered := make(chan context.Context)
	release := make(chan struct{})
	b.run = func(ctx context.Context, e Execution, p Publish) error {
		entered <- ctx
		<-release
		return completeRun(ctx, e, p)
	}
	j := create(t, s, "create")
	r := request(Start)
	r.JobID = j.ID
	r.ExpectedRevision = j.Revision
	r.IdempotencyKey = "start"
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := s.Call(ctx, r); err != nil {
		t.Fatal(err)
	}
	cancel()
	runCtx := <-entered
	if runCtx.Err() != nil {
		t.Fatal("HTTP cancellation leaked into accepted execution")
	}
	close(release)
	awaitState(t, s, j.ID, Completed)
}
func TestChangedApprovalAndRevisionNeverExecute(t *testing.T) {
	s, b, _ := newFixture(t)
	j := create(t, s, "create")
	r := request(Start)
	r.JobID = j.ID
	r.ExpectedRevision = j.Revision + 1
	r.IdempotencyKey = "start"
	if got := call(t, s, r).Error; got != "revision_conflict" {
		t.Fatal(got)
	}
	b.mu.Lock()
	b.inspection.Scope.Budget.Requests--
	b.mu.Unlock()
	if got := start(t, s, j, "start").Error; got != "approval_changed" {
		t.Fatal(got)
	}
	if b.calls.Load() != 0 {
		t.Fatal("approval change executed")
	}
	r = request(Validate)
	r.JobID = j.ID
	r.ExpectedRevision = j.Revision
	updated := call(t, s, r).Job
	if updated.Revision != j.Revision+1 {
		t.Fatal("validation did not update review revision")
	}
}
func TestRestartInterruptionsRemainUnknownAndOwned(t *testing.T) {
	for _, phase := range []Phase{TaskCreation, Planning, Authority, Gateway, ExecutionPhase, Report} {
		t.Run(string(phase), func(t *testing.T) {
			root := privateDir(t)
			b := &fakeBackend{inspection: fixtureInspection()}
			published := make(chan struct{})
			b.run = func(ctx context.Context, e Execution, p Publish) error {
				u := Update{State: Preparing, Phase: phase, PreparationStarted: true, AuthorityState: AuthorityNotIssued}
				if phase != TaskCreation {
					u.TaskID = "42"
				}
				if phase == Gateway {
					u.AuthorityState = AuthorityIssued
				}
				if phase == ExecutionPhase || phase == Report {
					u.Started = true
					u.State = Running
					u.AuthorityState = AuthorityConsumed
				}
				if phase == Report {
					u.State = Stopping
					u.DrainComplete = true
				}
				if err := p(u); err != nil {
					return err
				}
				close(published)
				<-ctx.Done()
				return ctx.Err()
			}
			s, err := New(context.Background(), root, b)
			if err != nil {
				t.Fatal(err)
			}
			j := create(t, s, "create")
			start(t, s, j, "start")
			<-published
			before := status(t, s, j.ID)
			crash(t, s)
			recovered, err := New(context.Background(), root, b)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Shutdown(context.Background())
			got := status(t, recovered, j.ID)
			expectedAuthority := before.AuthorityState
			if (phase == Authority || phase == Gateway) && expectedAuthority != AuthorityConsumed {
				expectedAuthority = AuthorityUnknown
			}
			if got.State != Unknown || got.Started != before.Started || got.DrainComplete != before.DrainComplete || got.ResultReady != before.ResultReady || got.AuthorityState != expectedAuthority || got.TaskID != before.TaskID {
				t.Fatalf("facts changed: before %#v after %#v", before, got)
			}
			if res := start(t, recovered, j, "start"); res.Error != "" || res.Job.State != Unknown {
				t.Fatal("same claim was not recovered")
			}
			next := create(t, recovered, "next")
			if got := start(t, recovered, next, "newstart").Error; got != "busy" {
				t.Fatal(got)
			}
			r := request(AuthorizeTask)
			r.TaskID = "42"
			if got := *call(t, recovered, r).Authorized; got != (before.TaskID == "42") {
				t.Fatal("ownership was lost")
			}
			r.Principal = "other"
			if *call(t, recovered, r).Authorized {
				t.Fatal("cross-owner task authorized")
			}
			if b.calls.Load() != 1 {
				t.Fatal("execution replayed")
			}
		})
	}
}
func TestTerminalReportsAndReservationsSurviveRestart(t *testing.T) {
	s, b, root := newFixture(t)
	b.inspection.MaxRuns = 1
	j := create(t, s, "create")
	start(t, s, j, "start")
	final := awaitState(t, s, j.ID, Completed)
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, err := New(context.Background(), root, b)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Shutdown(context.Background())
	got := status(t, recovered, j.ID)
	if !reflect.DeepEqual(got, final) {
		a, _ := json.Marshal(got)
		c, _ := json.Marshal(final)
		if string(a) != string(c) {
			t.Fatalf("terminal report changed %s %s", a, c)
		}
	}
	// 설치 파일의 무관한 변경은 stable delegation의 사용량을 초기화하지 않는다.
	b.mu.Lock()
	b.inspection.InstallationDigest = strings.Repeat("b", 64)
	b.mu.Unlock()
	next := create(t, recovered, "next")
	if got := start(t, recovered, next, "newstart").Error; got != "budget_exhausted" {
		t.Fatal(got)
	}
}
func TestAggregateReservationAndUnknownRetention(t *testing.T) {
	s, b, _ := newFixture(t)
	b.inspection.MaxRuns = 100
	b.inspection.AggregateBudget = b.inspection.Scope.Budget
	b.run = func(context.Context, Execution, Publish) error { return errors.New("unknown") }
	j := create(t, s, "create")
	start(t, s, j, "start")
	awaitState(t, s, j.ID, Unknown)
	next := create(t, s, "next")
	if got := start(t, s, next, "start2").Error; got != "busy" {
		t.Fatal(got)
	}
}
func TestStopRevisionAndPreparationCancellation(t *testing.T) {
	s, b, _ := newFixture(t)
	entered := make(chan struct{})
	b.run = func(ctx context.Context, e Execution, p Publish) error {
		u := Update{State: Preparing, Phase: Planning, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityNotIssued}
		if err := p(u); err != nil {
			return err
		}
		close(entered)
		<-ctx.Done()
		u.State = Cancelled
		u.Phase = Closure
		u.DrainComplete = true
		return p(u)
	}
	j := create(t, s, "create")
	start(t, s, j, "start")
	<-entered
	current := status(t, s, j.ID)
	r := request(Stop)
	r.JobID = j.ID
	r.ExpectedRevision = j.Revision
	if got := call(t, s, r).Error; got != "revision_conflict" {
		t.Fatal(got)
	}
	r.ExpectedRevision = current.Revision
	if got := call(t, s, r).Job.State; got != Stopping {
		t.Fatal(got)
	}
	final := awaitState(t, s, j.ID, Cancelled)
	if final.Started || final.ResultReady || !final.DrainComplete {
		t.Fatal("invented runtime facts")
	}
}
func TestPublishRejectsRegressionsAndKeepsKnownFacts(t *testing.T) {
	s, b, _ := newFixture(t)
	errCh := make(chan error, 6)
	b.run = func(ctx context.Context, e Execution, p Publish) error {
		u := Update{State: Running, Phase: ExecutionPhase, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityConsumed, Started: true, ModelUsage: &ModelUsage{Requests: 1, ReservedInputTokens: 10, UnknownUsageCalls: 1, UsageKnown: true}}
		if err := p(u); err != nil {
			return err
		}
		bad := u
		bad.TaskID = "43"
		errCh <- p(bad)
		bad = u
		bad.Started = false
		bad.State = Preparing
		errCh <- p(bad)
		bad = u
		bad.AuthorityState = AuthorityUnknown
		errCh <- p(bad)
		bad = u
		bad.Phase = Planning
		errCh <- p(bad)
		// 상한으로 정산한 호출 수는 줄지 않고 요청 수를 넘지 않는다(C05).
		bad = u
		bad.ModelUsage = &ModelUsage{Requests: 1, ReservedInputTokens: 10, UsageKnown: true}
		errCh <- p(bad)
		bad = u
		bad.ModelUsage = &ModelUsage{Requests: 1, ReservedInputTokens: 10, UnknownUsageCalls: 2, UsageKnown: true}
		errCh <- p(bad)
		return errors.New("interrupted")
	}
	j := create(t, s, "create")
	start(t, s, j, "start")
	got := awaitState(t, s, j.ID, Unknown)
	for i := range 6 {
		if <-errCh == nil {
			t.Fatalf("regression %d allowed", i)
		}
	}
	if !got.Started || got.AuthorityState != AuthorityConsumed || got.TaskID != "42" {
		t.Fatal("known facts changed")
	}
}
func TestShutdownTimeoutPersistsUnknown(t *testing.T) {
	s, b, root := newFixture(t)
	release := make(chan struct{})
	entered := make(chan struct{})
	b.run = func(context.Context, Execution, Publish) error { close(entered); <-release; return nil }
	j := create(t, s, "create")
	start(t, s, j, "start")
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := s.Shutdown(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	recovered, err := New(context.Background(), root, b)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Shutdown(context.Background())
	if got := status(t, recovered, j.ID).State; got != Unknown {
		t.Fatal(got)
	}
	close(release)
}

func TestStartKeyCannotBindAnotherJob(t *testing.T) {
	s, _, _ := newFixture(t)
	first := create(t, s, "first")
	start(t, s, first, "same-key")
	awaitState(t, s, first.ID, Completed)
	second := create(t, s, "second")
	if got := start(t, s, second, "same-key").Error; got != "idempotency_conflict" {
		t.Fatal(got)
	}
}
func TestAggregateCapSurvivesNewProfileDigest(t *testing.T) {
	s, b, _ := newFixture(t)
	b.inspection.MaxRuns = 100
	b.inspection.AggregateBudget = b.inspection.Scope.Budget
	first := create(t, s, "first")
	start(t, s, first, "first-start")
	awaitState(t, s, first.ID, Completed)
	b.mu.Lock()
	b.inspection.InstallationDigest = strings.Repeat("b", 64)
	b.mu.Unlock()
	second := create(t, s, "second")
	if got := start(t, s, second, "second-start").Error; got != "budget_exhausted" {
		t.Fatal(got)
	}
}

func TestBackendReturnAndPanicCancelChildContext(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "panic"}[panics], func(t *testing.T) {
			s, b, _ := newFixture(t)
			cancelled := make(chan struct{})
			b.run = func(ctx context.Context, _ Execution, _ Publish) error {
				go func() { <-ctx.Done(); close(cancelled) }()
				if panics {
					panic("interrupted backend")
				}
				return errors.New("interrupted backend")
			}
			j := create(t, s, "create")
			start(t, s, j, "start")
			got := awaitState(t, s, j.ID, Unknown)
			if got.Started || got.DrainComplete || got.ResultReady {
				t.Fatal("unknown facts invented")
			}
			select {
			case <-cancelled:
			case <-time.After(time.Second):
				t.Fatal("child work survived backend return")
			}
		})
	}
}
func TestStopUnknownCancelsLiveContext(t *testing.T) {
	s, b, _ := newFixture(t)
	cancelled := make(chan struct{})
	b.run = func(ctx context.Context, _ Execution, p Publish) error {
		if err := p(Update{State: Unknown, Phase: Planning, PreparationStarted: true, AuthorityState: AuthorityNotIssued}); err != nil {
			return err
		}
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}
	j := create(t, s, "create")
	start(t, s, j, "start")
	unknown := awaitState(t, s, j.ID, Unknown)
	r := request(Stop)
	r.JobID = j.ID
	r.ExpectedRevision = unknown.Revision
	got := call(t, s, r).Job
	if got.State != Unknown || got.Started || got.DrainComplete || got.ResultReady {
		t.Fatal("stop changed unknown facts")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("unknown job could not be cancelled")
	}
}
func TestConfirmedFailureBeforeTaskDoesNotInventTaskOrReport(t *testing.T) {
	s, b, _ := newFixture(t)
	b.run = func(_ context.Context, _ Execution, p Publish) error {
		return p(Update{State: Failed, Phase: Closure, PreparationStarted: true, DrainComplete: true, AuthorityState: AuthorityNotIssued})
	}
	j := create(t, s, "create")
	start(t, s, j, "start")
	got := awaitState(t, s, j.ID, Failed)
	if got.TaskID != "" || got.Started || got.ResultReady || !got.DrainComplete {
		t.Fatal("prep failure invented facts")
	}
}

func TestExternalTransferConsentIsRequiredAndDurable(t *testing.T) {
	s, b, root := newFixture(t)
	b.inspection.Scope.Model = &ModelInfo{Mode: "openrouter", Model: "qwen/qwen3.5-35b-a3b", Provider: "fixture/endpoint", ExternalTransfer: true, InputPerCall: 262144, OutputPerCall: 100}
	j := create(t, s, "external-create")
	denied := start(t, s, j, "external-start")
	if denied.Error != "approval_required" || b.calls.Load() != 0 {
		t.Fatal("external execution without consent")
	}
	r := request(Start)
	r.JobID = j.ID
	r.ExpectedRevision = j.Revision
	r.IdempotencyKey = "external-start"
	r.ExternalTransferConfirmed = true
	if result := call(t, s, r); result.Error != "" {
		t.Fatal(result.Error)
	}
	for deadline := time.Now().Add(time.Second); status(t, s, j.ID).State != Completed; {
		if time.Now().After(deadline) {
			t.Fatal("synthetic completion timeout")
		}
		time.Sleep(time.Millisecond)
	}
	if s.Shutdown(context.Background()) != nil {
		t.Fatal("shutdown")
	}
	restored, err := New(context.Background(), root, b)
	if err != nil {
		t.Fatal("consent receipt restart", err)
	}
	defer restored.Shutdown(context.Background())
	if status(t, restored, j.ID).State != Completed || b.calls.Load() != 1 {
		t.Fatal("consent receipt replayed execution")
	}
}

// 결과 없이 completed로 마감하려는 갱신은 거부한다. 실행은 완료로 보이지 않고 결과 불명으로 끝난다(절대 규칙 5).
func TestPublishRejectsCompletionWithoutRunResult(t *testing.T) {
	s, b, _ := newFixture(t)
	var completionErr error
	b.run = func(ctx context.Context, e Execution, p Publish) error {
		withoutResult := func(u Update) error {
			if u.State == Completed {
				u.Result = nil
				completionErr = p(u)
				return completionErr
			}
			return p(u)
		}
		return completeRun(ctx, e, withoutResult)
	}
	j := create(t, s, "create")
	r := request(Start)
	r.JobID = j.ID
	r.ExpectedRevision = j.Revision
	r.IdempotencyKey = "start"
	if res, err := s.Call(context.Background(), r); err != nil || res.Error != "" {
		t.Fatal(res, err)
	}
	final := awaitState(t, s, j.ID, Unknown)
	if completionErr == nil || final.Outcome != "" || final.IntentOutcomes != nil {
		t.Fatalf("completion without result: err=%v job=%+v", completionErr, final)
	}
}

// 실행이 넘긴 시험 계정별 인증 결과는 completed·cancelled·failed 작업에 남고(재시작 뒤에도), 다른 상태에는 남지 않는다(§3.4).
func TestAccountAuthReachesTerminalJob(t *testing.T) {
	s, b, _ := newFixture(t)
	auth := map[string]string{"tester-a": "authenticated", "tester-b": "login_failed"}
	b.run = func(_ context.Context, _ Execution, p Publish) error {
		return p(Update{State: Failed, Phase: Closure, PreparationStarted: true, DrainComplete: true, AuthorityState: AuthorityNotIssued, AccountAuth: auth})
	}
	j := create(t, s, "create")
	start(t, s, j, "start")
	got := awaitState(t, s, j.ID, Failed)
	if got.AccountAuth["tester-a"] != "authenticated" || got.AccountAuth["tester-b"] != "login_failed" || len(got.AccountAuth) != 2 {
		t.Fatalf("failed job lost account_auth: %v", got.AccountAuth)
	}
	auth["tester-a"] = "not_sent"
	if again := status(t, s, j.ID); again.AccountAuth["tester-a"] != "authenticated" {
		t.Fatal("job shares the update map")
	}
}

func TestAccountAuthDroppedOutsideTerminalStates(t *testing.T) {
	s, b, _ := newFixture(t)
	release := make(chan struct{})
	accepted := make(chan error, 1)
	b.run = func(ctx context.Context, _ Execution, p Publish) error {
		// 끝나지 않은 상태의 갱신은 받되 계정별 결과 칸만 버린다(갱신 전체를 거부하지 않는다).
		accepted <- p(Update{State: Preparing, Phase: TaskCreation, PreparationStarted: true, AuthorityState: AuthorityNotIssued, AccountAuth: map[string]string{"tester-a": "authenticated"}})
		<-release
		return nil
	}
	j := create(t, s, "create")
	start(t, s, j, "start")
	if err := <-accepted; err != nil {
		t.Fatal("non-terminal update with account_auth rejected:", err)
	}
	got := status(t, s, j.ID)
	close(release)
	if got.Phase != TaskCreation || got.AccountAuth != nil {
		t.Fatalf("preparing job: phase=%s account_auth=%v", got.Phase, got.AccountAuth)
	}
}
