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
	return Closure{DrainComplete: r.closeErr == nil, ResultReady: r.closeErr == nil}, r.closeErr
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
		{"failed_saved", errors.New("execution failure"), nil, Closure{true, true}, Failed},
		{"cancelled_saved", context.Canceled, nil, Closure{true, true}, Cancelled},
		{"save_failed", nil, errors.New("save failure"), Closure{true, false}, Unknown},
		{"http_drain_failed", nil, errors.New("drain failure"), Closure{false, true}, Unknown},
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
