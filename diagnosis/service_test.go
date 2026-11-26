package diagnosis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testRunner struct {
	closure  *Closure
	calls    atomic.Int32
	closes   atomic.Int32
	finish   chan struct{}
	runErr   error
	closeErr error
}

func (r *testRunner) Run(ctx context.Context) error {
	r.calls.Add(1)
	select {
	case <-r.finish:
		return r.runErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *testRunner) Close(context.Context) (Closure, error) {
	r.closes.Add(1)
	if r.closure != nil {
		return *r.closure, r.closeErr
	}
	return Closure{DrainComplete: r.closeErr == nil, ResultReady: r.closeErr == nil, Result: completeResult()}, r.closeErr
}

func completeResult() *RunResult {
	return &RunResult{Outcome: OutcomeComplete, IntentOutcomes: IntentOutcomes{Done: 1}}
}
func request(command Command) Request {
	r := Request{Version: Version, RequestID: "request-1", Command: command, TaskID: "23", PreparationRef: "operator-prepared"}
	if command == Start {
		r.IdempotencyKey = "start-once"
	}
	return r
}
func newTestService(t *testing.T, r *testRunner) *Service {
	t.Helper()
	s, e := NewPrepared(context.Background(), "23", "operator-prepared", r, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return s
}
func awaitState(t *testing.T, s *Service, want State) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		response, e := s.Call(context.Background(), request(Status))
		if e == nil && response.Snapshot != nil && response.Snapshot.State == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("expected", want)
}
func TestServiceBoundSingleStart(t *testing.T) {
	runner := &testRunner{finish: make(chan struct{})}
	s := newTestService(t, runner)
	wrong := request(Start)
	wrong.TaskID = "24"
	response, _ := s.Call(context.Background(), wrong)
	if response.Error != "binding_mismatch" {
		t.Fatal(response)
	}
	wrong = request(Start)
	wrong.PreparationRef = "other"
	response, _ = s.Call(context.Background(), wrong)
	if response.Error != "binding_mismatch" {
		t.Fatal(response)
	}
	var group sync.WaitGroup
	for range 40 {
		group.Go(func() {
			res, e := s.Call(context.Background(), request(Start))
			if e != nil || res.Error != "" {
				t.Errorf("start: %v %v", res, e)
			}
		})
	}
	group.Wait()
	wrong = request(Start)
	wrong.IdempotencyKey = "new-key"
	response, _ = s.Call(context.Background(), wrong)
	if response.Error != "already_started" {
		t.Fatal(response)
	}
	close(runner.finish)
	awaitState(t, s, Completed)
	if runner.calls.Load() != 1 || runner.closes.Load() != 1 {
		t.Fatal(runner.calls.Load(), runner.closes.Load())
	}
	response, _ = s.Call(context.Background(), request(Result))
	if response.Snapshot == nil || !response.Snapshot.DrainComplete {
		t.Fatal(response)
	}
}
func TestStopAndUnknownClosure(t *testing.T) {
	for _, test := range []struct {
		name     string
		closeErr error
		state    State
	}{{"cancel", nil, Cancelled}, {"unknown", errors.New("private error"), Unknown}} {
		t.Run(test.name, func(t *testing.T) {
			runner := &testRunner{finish: make(chan struct{}), closeErr: test.closeErr}
			s := newTestService(t, runner)
			_, _ = s.Call(context.Background(), request(Start))
			_, _ = s.Call(context.Background(), request(Stop))
			_, _ = s.Call(context.Background(), request(Stop))
			awaitState(t, s, test.state)
			result, _ := s.Call(context.Background(), request(Result))
			if test.closeErr != nil && result.Error != "result_not_ready" {
				t.Fatal(result)
			}
		})
	}
}
func TestPreparedShutdownCannotRestart(t *testing.T) {
	runner := &testRunner{finish: make(chan struct{})}
	s := newTestService(t, runner)
	if e := s.Shutdown(context.Background()); e != nil {
		t.Fatal(e)
	}
	response, _ := s.Call(context.Background(), request(Start))
	if response.Error != "service_closed" || runner.calls.Load() != 0 || runner.closes.Load() != 1 {
		t.Fatal(response)
	}
}
func TestProtocolRejectsAuthorityInputs(t *testing.T) {
	for _, raw := range []string{
		`{"version":"diagnosis.v1","version":"other"}`,
		`{"version":"diagnosis.v1","config_path":"/secret"}`,
		`{"version":"diagnosis.v1"} {}`,
		`[]`,
		`{"Version":"diagnosis.v1"}`,
		`{"version":null}`,
	} {
		var r Request
		if decodeFrame([]byte(raw), &r) == nil {
			t.Fatal("accepted malformed")
		}
	}
	for _, mutate := range []func(*Request){
		func(r *Request) { r.Version = "v2" }, func(r *Request) { r.TaskID = "023" }, func(r *Request) { r.TaskID = "9223372036854775808" },
		func(r *Request) { r.PreparationRef = "../authority" }, func(r *Request) { r.Command = "shell" }, func(r *Request) { r.IdempotencyKey = "" },
	} {
		r := request(Start)
		mutate(&r)
		if validRequest(r) {
			t.Fatal("invalid request accepted", r)
		}
	}
}

func TestFailuresDoNotBecomeSuccessfulCompletion(t *testing.T) {
	runner := &testRunner{finish: make(chan struct{}), runErr: errors.New("private provider failure")}
	s := newTestService(t, runner)
	_, _ = s.Call(context.Background(), request(Start))
	close(runner.finish)
	awaitState(t, s, Failed)
	response, _ := s.Call(context.Background(), request(Result))
	if response.Snapshot == nil || response.Snapshot.State != Failed {
		t.Fatal(response)
	}
}
func TestCancelledRequestCannotStart(t *testing.T) {
	runner := &testRunner{finish: make(chan struct{})}
	s := newTestService(t, runner)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Call(ctx, request(Start)); err == nil {
		t.Fatal("cancelled request allowed")
	}
	response, _ := s.Call(context.Background(), request(Status))
	if response.Snapshot.State != Prepared || runner.calls.Load() != 0 {
		t.Fatal(response)
	}
}

func TestClosureEvidenceIsIndependentOfExecutionFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		runErr   error
		closeErr error
		closure  Closure
		state    State
	}{
		{"close_error_only", nil, errors.New("close failure"), Closure{DrainComplete: true, ResultReady: true}, Unknown},
		{"drain_incomplete_without_error", nil, nil, Closure{DrainComplete: false, ResultReady: true}, Unknown},
		{"result_missing_without_error", nil, nil, Closure{DrainComplete: true, ResultReady: false}, Unknown},
		{"failed_saved", errors.New("execution failure"), nil, Closure{DrainComplete: true, ResultReady: true}, Failed},
		{"cancelled_saved", context.Canceled, nil, Closure{DrainComplete: true, ResultReady: true}, Cancelled},
		{"save_failed", nil, errors.New("save failure"), Closure{DrainComplete: true, ResultReady: false}, Unknown},
		{"http_drain_failed", nil, errors.New("drain failure"), Closure{DrainComplete: false, ResultReady: true}, Unknown},
		// 실행 결과 없이 마감만 확인되면 완료라고 하지 않는다(실행 결과 상태 값 계약).
		{"completed_without_result", nil, nil, Closure{DrainComplete: true, ResultReady: true}, Unknown},
		{"completed_inconsistent_result", nil, nil, Closure{DrainComplete: true, ResultReady: true, Result: &RunResult{Outcome: OutcomeComplete, IntentOutcomes: IntentOutcomes{Done: 1, Incomplete: 1}}}, Unknown},
		{"completed_partial", nil, nil, Closure{DrainComplete: true, ResultReady: true, Result: &RunResult{Outcome: OutcomePartial, IntentOutcomes: IntentOutcomes{Done: 1, Incomplete: 1}}}, Completed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &testRunner{finish: make(chan struct{}), runErr: tc.runErr, closeErr: tc.closeErr, closure: &tc.closure}
			s := newTestService(t, runner)
			_, _ = s.Call(context.Background(), request(Start))
			close(runner.finish)
			awaitState(t, s, tc.state)
			response, _ := s.Call(context.Background(), request(Status))
			if response.Snapshot.DrainComplete != tc.closure.DrainComplete || response.Snapshot.ResultReady != tc.closure.ResultReady {
				t.Fatal("closure evidence overwritten", response)
			}
			if !validSnapshot(*response.Snapshot) {
				t.Fatal("valid closure evidence rejected")
			}
		})
	}
}

// 중지된 실행은 마감 때 읽은 intent 결과 개수만 싣는다(outcome·closing_reason 없음, 계약 §3.2·§6 2-2).
// 개수가 없거나 틀리면 개수 없이 중지다. 개수만 있는 결과는 완료로 받지 않는다.
func TestStoppedRunCarriesIntentOutcomesOnly(t *testing.T) {
	counts := IntentOutcomes{Done: 1, Incomplete: 2, Stopped: 1}
	for _, tc := range []struct {
		name   string
		runErr error
		result *RunResult
		state  State
		want   *RunResult
	}{
		{"stopped_counts", context.Canceled, &RunResult{IntentOutcomes: counts}, Cancelled, &RunResult{IntentOutcomes: counts}},
		{"stop_after_completion", context.Canceled, &RunResult{Outcome: OutcomePartial, ClosingReason: ClosingBudgetLimited, IntentOutcomes: counts}, Cancelled, &RunResult{IntentOutcomes: counts}},
		{"stopped_without_counts", context.Canceled, nil, Cancelled, nil},
		{"stopped_invalid_counts", context.Canceled, &RunResult{IntentOutcomes: IntentOutcomes{Done: -1}}, Cancelled, nil},
		{"counts_only_not_completed", nil, &RunResult{IntentOutcomes: counts}, Unknown, nil},
		{"failed_drops_counts", errors.New("execution failure"), &RunResult{IntentOutcomes: counts}, Failed, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closure := Closure{DrainComplete: true, ResultReady: true, Result: tc.result}
			runner := &testRunner{finish: make(chan struct{}), runErr: tc.runErr, closure: &closure}
			s := newTestService(t, runner)
			_, _ = s.Call(context.Background(), request(Start))
			close(runner.finish)
			awaitState(t, s, tc.state)
			response, _ := s.Call(context.Background(), request(Status))
			got := response.Snapshot.Result
			if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
				t.Fatalf("result=%+v want=%+v", got, tc.want)
			}
			if !validSnapshot(*response.Snapshot) {
				t.Fatal("snapshot rejected")
			}
		})
	}
	if !(RunResult{IntentOutcomes: counts}).StoppedValid() || (RunResult{Outcome: OutcomeComplete, IntentOutcomes: IntentOutcomes{Done: 1}}).StoppedValid() {
		t.Fatal("stopped result rule")
	}
	base := Snapshot{TaskID: "1", PreparationRef: "prep-1", State: Cancelled, Started: true, DrainComplete: true, ResultReady: true}
	withOutcome := base
	withOutcome.Result = &RunResult{Outcome: OutcomeComplete, IntentOutcomes: IntentOutcomes{Done: 1}}
	failed := base
	failed.State, failed.Result = Failed, &RunResult{IntentOutcomes: counts}
	if !validSnapshot(base) || validSnapshot(withOutcome) || validSnapshot(failed) {
		t.Fatal("cancelled with outcome or failed with counts accepted")
	}
}

// 취소 요청 뒤 Runner가 nil을 반환해도 중지 사실이 정상 완료로 바뀌면 안 된다.
type controlledExitRunner struct{ started, finish, closing, releaseClose chan struct{} }

func (r *controlledExitRunner) Run(context.Context) error { close(r.started); <-r.finish; return nil }
func (r *controlledExitRunner) Close(context.Context) (Closure, error) {
	close(r.closing)
	<-r.releaseClose
	return Closure{DrainComplete: true, ResultReady: true, Result: completeResult()}, nil
}
func TestStoppingNormalReturnAndShutdownWait(t *testing.T) {
	r := &controlledExitRunner{make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})}
	s, e := NewPrepared(t.Context(), "23", "operator-prepared", r, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = s.Call(t.Context(), request(Start))
	<-r.started
	response, _ := s.Call(t.Context(), request(Stop))
	if response.Snapshot.State != Stopping {
		t.Fatal(response)
	}
	finished := make(chan error, 1)
	go func() { finished <- s.Shutdown(context.Background()) }()
	select {
	case <-finished:
		t.Fatal("Shutdown returned before Run exited")
	case <-time.After(20 * time.Millisecond):
	}
	close(r.finish)
	<-r.closing
	select {
	case <-finished:
		t.Fatal("Shutdown returned before Close finished")
	case <-time.After(20 * time.Millisecond):
	}
	close(r.releaseClose)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not finish")
	}
	s.mu.Lock()
	snapshot := s.snapshot
	s.mu.Unlock()
	if snapshot.State != Cancelled || !snapshot.DrainComplete || !snapshot.ResultReady {
		t.Fatal(snapshot)
	}
}
func TestPreStartStopAndParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &testRunner{finish: make(chan struct{})}
	s, e := NewPrepared(ctx, "23", "operator-prepared", r, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	response, e := s.Call(t.Context(), request(Stop))
	if e != nil || response.Error != "not_started" || r.calls.Load() != 0 || r.closes.Load() != 0 {
		t.Fatal(response, e)
	}
	cancel()
	response, e = s.Call(t.Context(), request(Start))
	if e != nil || response.Error != "service_closed" || r.calls.Load() != 0 {
		t.Fatal(response, e)
	}
	if err := s.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func TestPreStartShutdownClosureFailure(t *testing.T) {
	r := &testRunner{finish: make(chan struct{}), closeErr: errors.New("closure failed")}
	s, e := NewPrepared(t.Context(), "23", "operator-prepared", r, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	if err := s.Shutdown(t.Context()); err == nil {
		t.Fatal("closure error hidden")
	}
	s.mu.Lock()
	snapshot := s.snapshot
	s.mu.Unlock()
	if snapshot.State != Unknown || snapshot.Started || snapshot.DrainComplete || snapshot.ResultReady || r.calls.Load() != 0 || r.closes.Load() != 1 {
		t.Fatal(snapshot)
	}
}
func TestParentCancellationStopsActiveExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &testRunner{finish: make(chan struct{})}
	s, e := NewPrepared(ctx, "23", "operator-prepared", r, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = s.Call(t.Context(), request(Start))
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := s.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	snapshot := s.snapshot
	s.mu.Unlock()
	if snapshot.State != Cancelled || !snapshot.ResultReady || r.closes.Load() != 1 {
		t.Fatal(snapshot)
	}
}

// 목표 달성으로 중단(GoalMet)은 남은 작업이 아니다: 그것만 있으면 complete이고, 음수면 받지 않는다(#875 발견 6).
func TestIntentOutcomesGoalMet(t *testing.T) {
	if (IntentOutcomes{GoalMet: 1}).Outcome() != OutcomeComplete || (IntentOutcomes{GoalMet: 1, Stopped: 1}).Outcome() != OutcomePartial {
		t.Fatal("goal_met outcome rule")
	}
	if (IntentOutcomes{Done: 1, GoalMet: -1}).Valid() || !(IntentOutcomes{Done: 1, GoalMet: 2}).Valid() {
		t.Fatal("goal_met validity")
	}
	// 합계는 칸마다 한 번씩 센다(서로 다른 2의 거듭제곱으로 빠진 칸·두 번 센 칸을 가린다).
	if got := (IntentOutcomes{Done: 1, PolicyLimited: 2, Incomplete: 4, Unknown: 8, Stopped: 16, GoalMet: 32}).Total(); got != 63 {
		t.Fatalf("total=%d", got)
	}
	if !(RunResult{Outcome: OutcomeComplete, IntentOutcomes: IntentOutcomes{Done: 1, GoalMet: 2}}).Valid() {
		t.Fatal("complete with goal_met rejected")
	}
	if (RunResult{Outcome: OutcomePartial, IntentOutcomes: IntentOutcomes{Done: 1, GoalMet: 2}}).Valid() {
		t.Fatal("partial without remaining work accepted")
	}
}

// 실행이 분류한 실패 사유는 failed 상태까지 실리고, 마감·결과를 확인하지 못한 unknown은 그 사유가 붙는다(B1). 사유 없는 실패는
// 사유 없이 failed다. 형식이 틀린 사유는 싣지 않는다. 완료·중지에는 사유가 없다.
func TestRunReasonReachesSnapshot(t *testing.T) {
	saved := Closure{DrainComplete: true, ResultReady: true}
	for _, tc := range []struct {
		name     string
		runErr   error
		closeErr error
		closure  Closure
		state    State
		reason   string
	}{
		{"failed_with_reason", WithReason(errors.New("private failure"), "planning_exploration_failed"), nil, saved, Failed, "planning_exploration_failed"},
		{"failed_wrapped_reason", fmt.Errorf("outer: %w", WithReason(errors.New("private failure"), "timeout")), nil, saved, Failed, "timeout"},
		{"failed_without_reason", errors.New("private failure"), nil, saved, Failed, ""},
		{"failed_invalid_reason", WithReason(errors.New("private failure"), "Bad Reason"), nil, saved, Failed, ""},
		{"closure_unconfirmed", WithReason(errors.New("private failure"), "planning_exploration_failed"), errors.New("close failure"), Closure{DrainComplete: true, ResultReady: false}, Unknown, ReasonClosureUnconfirmed},
		{"drain_incomplete", nil, nil, Closure{DrainComplete: false, ResultReady: true}, Unknown, ReasonClosureUnconfirmed},
		{"result_unconfirmed", nil, nil, saved, Unknown, ReasonResultUnconfirmed},
		{"cancelled", WithReason(context.Canceled, "user_cancelled"), nil, saved, Cancelled, ""},
		{"completed", WithReason(nil, "planning_exploration_failed"), nil, Closure{DrainComplete: true, ResultReady: true, Result: completeResult()}, Completed, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &testRunner{finish: make(chan struct{}), runErr: tc.runErr, closeErr: tc.closeErr, closure: &tc.closure}
			s := newTestService(t, runner)
			_, _ = s.Call(context.Background(), request(Start))
			close(runner.finish)
			awaitState(t, s, tc.state)
			response, _ := s.Call(context.Background(), request(Status))
			if response.Snapshot.ReasonCode != tc.reason || !validSnapshot(*response.Snapshot) {
				t.Fatalf("사유 %q, 기대 %q (유효 %v)", response.Snapshot.ReasonCode, tc.reason, validSnapshot(*response.Snapshot))
			}
		})
	}
}

// 상대 구현이 보낸 사유는 failed·unknown에만, 정한 형식으로만 받는다.
func TestSnapshotReasonValidation(t *testing.T) {
	base := Snapshot{TaskID: "23", PreparationRef: "operator-prepared", Started: true, DrainComplete: true, ResultReady: true}
	for _, tc := range []struct {
		name  string
		edit  func(*Snapshot)
		valid bool
	}{
		{"failed", func(s *Snapshot) { s.State, s.ReasonCode = Failed, "engine_task_failed" }, true},
		{"unknown", func(s *Snapshot) { s.State, s.ReasonCode = Unknown, ReasonClosureUnconfirmed }, true},
		{"completed", func(s *Snapshot) { s.State, s.Result, s.ReasonCode = Completed, completeResult(), "engine_task_failed" }, false},
		{"cancelled", func(s *Snapshot) { s.State, s.ReasonCode = Cancelled, "stop_requested" }, false},
		{"running", func(s *Snapshot) {
			s.State, s.DrainComplete, s.ResultReady, s.ReasonCode = Running, false, false, "engine_task_failed"
		}, false},
		{"upper case", func(s *Snapshot) { s.State, s.ReasonCode = Failed, "Engine_failed" }, false},
		{"too long", func(s *Snapshot) { s.State, s.ReasonCode = Failed, "a"+strings.Repeat("b", 64) }, false},
		{"raw error", func(s *Snapshot) { s.State, s.ReasonCode = Failed, "dial tcp: refused" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			tc.edit(&s)
			if validSnapshot(s) != tc.valid {
				t.Fatalf("유효 %v, 기대 %v", validSnapshot(s), tc.valid)
			}
		})
	}
	if err := errors.New("x"); WithReason(err, "") != err || WithReason(err, "a b") != err || WithReason(nil, "timeout") != nil {
		t.Fatal("형식이 틀린 사유나 오류 없음을 감쌈")
	}
	wrapped := WithReason(context.Canceled, "timeout")
	if !errors.Is(wrapped, context.Canceled) || wrapped.Error() != context.Canceled.Error() {
		t.Fatal("감싼 오류가 원래 오류로 보이지 않음")
	}
}
