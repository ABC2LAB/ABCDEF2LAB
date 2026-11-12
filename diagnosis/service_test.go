package diagnosis

import (
	"context"
	"errors"
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
