package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

// 실제 DB/runner 인수시험과 별개로, 획득 전 admission과 종료 불명 상태를 확인한다.
func e2HTTPRuntimeFixture(t *testing.T, alter func(*E2HTTPRuntimeOptions)) (*E2HTTPRuntime, *Task) {
	t.Helper()
	opts := E2HTTPRuntimeOptions{Expected: E2HTTPExpectedBinding{RunRef: "fixture-run", ResetRef: "fixture-reset", PolicyRef: "fixture-policy", CaseRef: "fixture-case", PolicyRevision: "fixture-revision", BudgetRef: "fixture-budget"}, Acquire: func(context.Context, E2HTTPRequest) (E2HTTPRunOptions, error) {
		t.Error("unexpected provider acquisition before a real intent")
		return E2HTTPRunOptions{}, ErrE2HTTPHost
	}, MaxWorkerTurns: 2, MaxToolCalls: 3, WorkerTimeout: time.Second, MaxPlannerRounds: 2, MaxIntentRuns: 1, DrainTimeout: time.Second}
	if alter != nil {
		alter(&opts)
	}
	runtime, err := NewE2HTTPRuntime(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	task := &Task{ID: "7", ExpID: 11, Store: (&db.DB{}).Exploration(11)}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = runtime.Close(ctx)
	})
	return runtime, task
}

func TestE2HTTPRuntimePlannerAdmissionIsBoundedAndCannotBeReused(t *testing.T) {
	runtime, task := e2HTTPRuntimeFixture(t, nil)
	for _, id := range []string{"", "0", "07", "+7", "-7", "other"} {
		bad := Task{ID: id, ExpID: task.ExpID, Store: task.Store}
		if runtime.AdmitTask(context.Background(), &bad) == nil {
			t.Fatal("noncanonical task admitted")
		}
	}
	if err := runtime.AdmitTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := runtime.PreparePlanning(context.Background(), task); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runtime.PreparePlanning(context.Background(), task); !errors.Is(err, ErrE2HTTPHost) {
		t.Fatal("planner limit did not stop further rounds")
	}
	changed := Task{ID: task.ID, ExpID: task.ExpID, Store: (&db.DB{}).Exploration(11)}
	if _, err := runtime.PreparePlanning(context.Background(), &changed); !errors.Is(err, ErrE2HTTPHost) {
		t.Fatal("replacement task store reused approval")
	}
	if err := runtime.CloseTask(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	if runtime.AdmitTask(context.Background(), task) == nil {
		t.Fatal("closed admission reopened")
	}
	next := Task{ID: "8", ExpID: task.ExpID, Store: task.Store}
	if runtime.AdmitTask(context.Background(), &next) == nil {
		t.Fatal("runtime reused for another task")
	}
}

func TestE2HTTPRuntimeAuthorityRevocationStopsPlanning(t *testing.T) {
	authority, cancel := context.WithCancel(context.Background())
	defer cancel()
	var revoked atomic.Bool
	runtime, task := e2HTTPRuntimeFixture(t, func(opts *E2HTTPRuntimeOptions) {
		opts.AuthorityContext = authority
		opts.CheckAuthority = func(context.Context) error {
			if revoked.Load() {
				return errors.New("private root reason")
			}
			return nil
		}
	})
	revoked.Store(true)
	if runtime.AdmitTask(context.Background(), task) == nil {
		t.Fatal("revoked authority admitted a task")
	}
	revoked.Store(false)
	if err := runtime.AdmitTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	planning, err := runtime.PreparePlanning(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	revoked.Store(true)
	if _, err := runtime.PreparePlanning(context.Background(), task); !errors.Is(err, ErrE2HTTPHost) || err.Error() != "e2_http_host_unavailable" {
		t.Fatal("authority failure escaped or planning fell through")
	}
	cancel()
	select {
	case <-planning.Done():
	case <-time.After(time.Second):
		t.Fatal("planning outlived actual root authority")
	}
}

func TestE2HTTPRuntimeReleaseFailureIsLatchedAndNotRepeated(t *testing.T) {
	runtime, task := e2HTTPRuntimeFixture(t, nil)
	if err := runtime.AdmitTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	runtime.task.lease = &E2HTTPRunOptions{Release: func(context.Context) error { calls.Add(1); return errors.New("ambiguous private close") }}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !errors.Is(runtime.CloseTask(context.Background(), task.ID), ErrE2HTTPClosure) {
				t.Error("unknown release declared successful")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("ambiguous release retried %d times", calls.Load())
	}
}

func TestE2HTTPRuntimeWaitsForOwnedWorkBeforeRelease(t *testing.T) {
	runtime, task := e2HTTPRuntimeFixture(t, nil)
	if err := runtime.AdmitTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	runtime.task.lease = &E2HTTPRunOptions{Release: func(context.Context) error { calls.Add(1); return nil }}
	done := make(chan struct{})
	runtime.task.active[19] = done
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(runtime.CloseTask(ctx, task.ID), ErrE2HTTPClosure) || calls.Load() != 0 {
		t.Fatal("unfinished work released as complete")
	}
	close(done)
	if err := runtime.CloseTask(context.Background(), task.ID); err != nil || calls.Load() != 1 {
		t.Fatal("finished owned work did not close once")
	}
}

func TestE2HTTPRuntimeResultsArePrivateHistoryCopies(t *testing.T) {
	runtime, task := e2HTTPRuntimeFixture(t, nil)
	if err := runtime.AdmitTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	runtime.task.results = []e2runner.TrustedResult{{Execution: e2proxy.ExecutionView{IntentIDs: []int64{19}, Evaluation: &e2proxy.EvaluationCallerContext{RunRef: "fixture-run", LLMBackend: "local"}}, Call: e2proxy.CallResult{CallRef: "fixture-call", IntentID: 19}, Classification: "tool_failed"}}
	result := runtime.Results(task.ID)
	if len(result) != 1 || result[0].Classification != "tool_failed" {
		t.Fatal("failed result was promoted or lost")
	}
	result[0].Execution.IntentIDs[0] = 999
	result[0].Execution.Evaluation.LLMBackend = "rewritten"
	result[0].Classification = "complete"
	again := runtime.Results(task.ID)
	if len(again) != 1 || again[0].Execution.IntentIDs[0] != 19 || again[0].Execution.Evaluation.LLMBackend != "local" || again[0].Classification != "tool_failed" || runtime.Results("8") != nil {
		t.Fatal("result consumer changed another task or stored history")
	}
}

func TestE2HTTPRuntimeRejectsUnboundResultAndPreservesObserverFailure(t *testing.T) {
	runtime, task := e2HTTPRuntimeFixture(t, nil)
	if err := runtime.AdmitTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("private observer rejection fixture")
	calls := 0
	lease := &E2HTTPRunOptions{ObserveResult: func(_ context.Context, result e2runner.Result) error {
		calls++
		if result.ToolOutput != "" {
			t.Error("raw output reached observer")
		}
		return sentinel
	}}
	result := e2runner.Result{ToolOutput: "private raw tool output", Trusted: e2runner.TrustedResult{Final: true, Call: e2proxy.CallResult{CallRef: "invented-call", IntentID: 19}}}
	err := runtime.observeResult(context.Background(), runtime.task, lease, 19, result)
	if !errors.Is(err, ErrE2HTTPHost) || !errors.Is(err, sentinel) || calls != 1 || runtime.Results(task.ID) != nil {
		t.Fatal("unbound result accepted or existing observer failure swallowed")
	}
}
