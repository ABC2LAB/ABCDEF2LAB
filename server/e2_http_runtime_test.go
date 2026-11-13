package server

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/transmit"
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
	// 라운드 상한은 실패가 아니라 실행 상한 마감 사유다(budget_limited). 새 라운드는 받지 않는다.
	if task.firstDiagnosisBudgetLimited.Load() {
		t.Fatal("budget limit marked before the limit")
	}
	if _, err := runtime.PreparePlanning(context.Background(), task); !errors.Is(err, ErrE2HTTPBudgetLimited) || errors.Is(err, ErrE2HTTPHost) || !task.firstDiagnosisBudgetLimited.Load() {
		t.Fatal("planner limit did not stop further rounds as a budget limit")
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

// Worker가 취소를 먼저 반환해 unknown 표식이 없어도, drain에서 확정된 감사가 우선한다.
func TestE2HTTPFinalIntentErrorPreservesLateUnknown(t *testing.T) {
	for _, drainErr := range []error{nil, context.Canceled, e2runner.ErrIncomplete} {
		earlyWorkerErr := context.Canceled
		finalCalls := []e2proxy.CallResult{{IntentID: 7, Final: true, Counts: e2proxy.Counts{Unknown: 1}}}
		err := errors.Join(earlyWorkerErr, e2HTTPFinalIntentError(drainErr, finalCalls, 7))
		if !errors.Is(err, agent.ErrE2HTTPUnknown) || !errors.Is(err, context.Canceled) {
			t.Fatal("late unknown lost", err)
		}
		if drainErr != nil && !errors.Is(err, drainErr) {
			t.Fatal("drain error lost", err)
		}
	}
}
func TestE2HTTPFinalIntentErrorDoesNotInventUnknown(t *testing.T) {
	calls := []e2proxy.CallResult{
		{IntentID: 8, Final: true, Counts: e2proxy.Counts{Unknown: 1}},
		{IntentID: 7, Final: true, Reason: e2proxy.PolicyDenied, Counts: e2proxy.Counts{Denied: 1}},
		{IntentID: 7, Final: true, Reason: e2proxy.Unsupported, Counts: e2proxy.Counts{Unsupported: 1}},
		{IntentID: 7, Final: true, Reason: e2proxy.AuditFailed, AuditComplete: false},
	}
	if err := e2HTTPFinalIntentError(nil, calls, 7); err != nil {
		t.Fatal("foreign unknown or denial reclassified", err)
	}
	if err := e2HTTPFinalIntentError(e2runner.ErrIncomplete, calls, 7); !errors.Is(err, e2runner.ErrIncomplete) || errors.Is(err, agent.ErrE2HTTPUnknown) {
		t.Fatal(err)
	}
}

type plannerRedactionLease struct {
	E2HTTPLeaseRunner
	seen string
	fail bool
}

func (l *plannerRedactionLease) RedactOutput(data []byte) []byte {
	l.seen = string(data)
	if l.fail {
		return nil
	}
	return bytes.ReplaceAll(data, []byte("owned-session-value"), []byte("[REDACTED]"))
}
func TestPlannerTargetDataUsesOnlyTheBoundLeaseRedactor(t *testing.T) {
	runtime, task := e2HTTPRuntimeFixture(t, nil)
	const data = `[{"endpoint_ref":"endpoint-owned","path":"/read/owned-session-value"}]`
	if got := runtime.redactPlannerTargetData(task.ID, data); got != "" {
		t.Fatal("unacquired task exposed raw target data")
	}
	if err := runtime.AdmitTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	lease := &plannerRedactionLease{}
	t.Cleanup(func() { runtime.task.lease = nil })
	runtime.task.lease = &E2HTTPRunOptions{Runner: lease}
	if got := runtime.redactPlannerTargetData(task.ID, data); got != "" || lease.seen != "" {
		t.Fatal("unvalidated lease used")
	}
	runtime.task.validLease = true
	got := runtime.redactPlannerTargetData(task.ID, data)
	if strings.Contains(got, "owned-session-value") || !strings.Contains(got, "[REDACTED]") || !strings.Contains(got, "endpoint-owned") || lease.seen != data {
		t.Fatal("planner bypassed its runner redactor")
	}
	lease.seen = ""
	if got = runtime.redactPlannerTargetData("other-task", data); got != "" || lease.seen != "" {
		t.Fatal("foreign task used bound credentials")
	}
	lease.fail = true
	if got = runtime.redactPlannerTargetData(task.ID, data); got != "" {
		t.Fatal("redactor failure fell back to raw target data")
	}
	runtime.task.closed = true
	lease.seen = ""
	if got = runtime.redactPlannerTargetData(task.ID, data); got != "" || lease.seen != "" {
		t.Fatal("closed task exposed target data")
	}
	// This fixture has no actual execution resources to close.
	runtime.task.lease = nil
}

// Plan당 상한(C26·C32)은 실행 Runtime을 만들 때 검사한다. 범위 밖 값이면 Runtime을 만들지 않는다.
func TestE2HTTPRuntimeRejectsOutOfRangePlannerLimits(t *testing.T) {
	runtime, _ := e2HTTPRuntimeFixture(t, func(o *E2HTTPRuntimeOptions) {
		o.PlannerLimits = agent.E2HTTPPlannerLimits{MaxTurns: 40, MaxDuration: 5 * time.Minute}
	})
	if runtime.options.PlannerLimits.MaxTurns != 40 {
		t.Fatal("유효한 Plan당 상한을 Runtime에 남기지 않음")
	}
	for _, limits := range []agent.E2HTTPPlannerLimits{{MaxTurns: -1}, {MaxTurns: 1001}, {MaxDuration: time.Hour + time.Nanosecond}} {
		opts := runtime.options
		opts.PlannerLimits = limits
		if bad, err := NewE2HTTPRuntime(context.Background(), opts); err == nil {
			_ = bad.Close(context.Background())
			t.Fatalf("범위 밖 Plan당 상한으로 Runtime을 만듦: %+v", limits)
		}
	}
}

// C09 시험용 lease 실행기다. 신뢰 결과 기록이 쓰는 조회(Snapshot·Calls·HasIntent)만 구현한다.
type untrustedResultLease struct {
	E2HTTPLeaseRunner
	view  e2proxy.ExecutionView
	calls []e2proxy.CallResult
}

func (l *untrustedResultLease) Snapshot() e2proxy.ExecutionView { return l.view }
func (l *untrustedResultLease) Calls() []e2proxy.CallResult     { return l.calls }
func (l *untrustedResultLease) HasIntent(id int64) bool         { return id == 19 }

// C09: 같은 호출의 최종 신뢰 결과가 E② 실행 기록과 다르면 그 intent만 결과 불명으로 표시하고 실행 결과 기록은 유지한다.
// 비최종 결과·모르는 호출·실행 범위 불일치·관찰자 실패는 지금처럼 실행 전체의 기록 실패다.
func TestE2HTTPRuntimeUntrustedResultMarksOnlyItsIntent(t *testing.T) {
	recorded := e2proxy.CallResult{CallRef: "recorded-call", IntentID: 19, Final: true, AuditComplete: true, Reason: e2proxy.Complete, Counts: e2proxy.Counts{Calls: 1, Attempts: 1, Allowed: 1, ForwardAttempted: 1, Responses: 1}}
	setup := func(t *testing.T, observerErr error) (*E2HTTPRuntime, *Task, *E2HTTPRunOptions, e2runner.Result) {
		runtime, task := e2HTTPRuntimeFixture(t, nil)
		if err := runtime.AdmitTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		view := e2proxy.ExecutionView{Version: e2proxy.Version, Scope: transmit.Scope{TaskRef: task.ID}, IntentID: 19, PolicyRevision: "fixture-revision", HeaderMode: e2proxy.HeaderPreserve, AuditComplete: true}
		lease := &E2HTTPRunOptions{Runner: &untrustedResultLease{view: view, calls: []e2proxy.CallResult{recorded}}}
		if observerErr != nil {
			lease.ObserveResult = func(context.Context, e2runner.Result) error { return observerErr }
		}
		result := e2runner.Result{Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Execution: view, Call: e2proxy.CloneCallResult(recorded), Final: true, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, Classification: "completed"}}
		return runtime, task, lease, result
	}
	t.Run("matching-result-recorded", func(t *testing.T) {
		runtime, task, lease, result := setup(t, nil)
		if err := runtime.observeResult(context.Background(), runtime.task, lease, 19, result); err != nil || len(runtime.Results(task.ID)) != 1 || len(runtime.UntrustedIntents(task.ID)) != 0 {
			t.Fatal("matching trusted result was not recorded", err)
		}
	})
	t.Run("same-call-different-content", func(t *testing.T) {
		runtime, task, lease, result := setup(t, nil)
		result.Trusted.Call.Counts.Unknown = 1
		err := runtime.observeResult(context.Background(), runtime.task, lease, 19, result)
		results := runtime.Results(task.ID)
		if err != agent.ErrE2HTTPResultUntrusted || results == nil || len(results) != 0 || !runtime.UntrustedIntents(task.ID)[19] || runtime.UntrustedIntents("other") != nil {
			t.Fatalf("content mismatch was not intent-scoped: err=%v results=%v untrusted=%v", err, results, runtime.UntrustedIntents(task.ID))
		}
	})
	for name, change := range map[string]func(*e2runner.Result){
		"not-final":    func(r *e2runner.Result) { r.Trusted.Call.Counts.Unknown = 1; r.Trusted.Final = false },
		"unknown-call": func(r *e2runner.Result) { r.Trusted.Call.CallRef = "invented-call" },
		"other-intent": func(r *e2runner.Result) { r.Trusted.Call.Counts.Unknown = 1; r.Trusted.Call.IntentID = 20 },
		"scope-mismatch": func(r *e2runner.Result) {
			r.Trusted.Call.Counts.Unknown = 1
			r.Trusted.Execution.PolicyRevision = "other-revision"
		},
		"version-mismatch": func(r *e2runner.Result) { r.Trusted.Call.Counts.Unknown = 1; r.Trusted.Version = "other" },
		// Final만 남고 마감·드레인·감사 필드가 하나라도 빠진 결과는 내용 대조 실패로 낮추지 않는다(#492 이동찬 지적 1).
		"process-not-drained": func(r *e2runner.Result) { r.Trusted.Call.Counts.Unknown = 1; r.Trusted.ProcessDrained = false },
		"proxy-not-drained":   func(r *e2runner.Result) { r.Trusted.Call.Counts.Unknown = 1; r.Trusted.ProxyDrained = false },
		"call-not-final":      func(r *e2runner.Result) { r.Trusted.Call.Counts.Unknown = 1; r.Trusted.Call.Final = false },
		"call-audit-open":     func(r *e2runner.Result) { r.Trusted.Call.Counts.Unknown = 1; r.Trusted.Call.AuditComplete = false },
		"execution-audit-open": func(r *e2runner.Result) {
			r.Trusted.Call.Counts.Unknown = 1
			r.Trusted.Execution.AuditComplete = false
		},
	} {
		t.Run(name, func(t *testing.T) {
			runtime, task, lease, result := setup(t, nil)
			change(&result)
			err := runtime.observeResult(context.Background(), runtime.task, lease, 19, result)
			if !errors.Is(err, ErrE2HTTPHost) || errors.Is(err, agent.ErrE2HTTPResultUntrusted) || runtime.Results(task.ID) != nil || len(runtime.UntrustedIntents(task.ID)) != 0 {
				t.Fatalf("%s was narrowed to an intent outcome: %v", name, err)
			}
		})
	}
	// 같은 호출을 두 번 최종 관찰하면 내용과 관계없이 기록 실패다(#492 이동찬 지적 2). 저장한 뒤 내용만 바꿔 다시
	// 보내거나, 대조 실패로 표시한 호출을 다시 보내도 대조 실패로 낮추지 않는다.
	for name, second := range map[string]func(*e2runner.Result){
		"stored-then-different": func(r *e2runner.Result) { r.Trusted.Call.Counts.Unknown = 1 },
		"stored-then-same":      func(*e2runner.Result) {},
	} {
		t.Run(name, func(t *testing.T) {
			runtime, task, lease, result := setup(t, nil)
			if err := runtime.observeResult(context.Background(), runtime.task, lease, 19, result); err != nil {
				t.Fatal("first observation", err)
			}
			again := result
			again.Trusted.Call = e2proxy.CloneCallResult(result.Trusted.Call)
			second(&again)
			err := runtime.observeResult(context.Background(), runtime.task, lease, 19, again)
			if !errors.Is(err, ErrE2HTTPHost) || errors.Is(err, agent.ErrE2HTTPResultUntrusted) || runtime.Results(task.ID) != nil || len(runtime.UntrustedIntents(task.ID)) != 0 {
				t.Fatalf("duplicate observation was narrowed: %v", err)
			}
		})
	}
	t.Run("untrusted-then-again", func(t *testing.T) {
		runtime, task, lease, result := setup(t, nil)
		result.Trusted.Call.Counts.Unknown = 1
		if err := runtime.observeResult(context.Background(), runtime.task, lease, 19, result); err != agent.ErrE2HTTPResultUntrusted {
			t.Fatal("first mismatch", err)
		}
		err := runtime.observeResult(context.Background(), runtime.task, lease, 19, result)
		if !errors.Is(err, ErrE2HTTPHost) || errors.Is(err, agent.ErrE2HTTPResultUntrusted) || runtime.Results(task.ID) != nil {
			t.Fatalf("repeated mismatch was narrowed again: %v", err)
		}
	})
	t.Run("observer-failure-not-narrowed", func(t *testing.T) {
		sentinel := errors.New("private observer failure fixture")
		runtime, task, lease, result := setup(t, sentinel)
		result.Trusted.Call.Counts.Unknown = 1
		err := runtime.observeResult(context.Background(), runtime.task, lease, 19, result)
		if !errors.Is(err, sentinel) || err == agent.ErrE2HTTPResultUntrusted || runtime.Results(task.ID) != nil {
			t.Fatal("observer failure was narrowed to an intent outcome", err)
		}
	})
}

// 요청 예산 시험용 lease 실행기다. lease 판정이 쓰는 조회(Snapshot·Policy·BudgetRemaining)만 구현한다.
type budgetLease struct {
	E2HTTPLeaseRunner
	view      e2proxy.ExecutionView
	budgetRef string
	remaining uint64
	err       error
}

func (l *budgetLease) Snapshot() e2proxy.ExecutionView  { return l.view }
func (l *budgetLease) Policy() e2proxy.PolicyView       { return e2proxy.PolicyView{BudgetRef: l.budgetRef} }
func (l *budgetLease) BudgetRemaining() (uint64, error) { return l.remaining, l.err }

// 결속한 lease의 승인 요청 예산이 다 쓰였으면 결속 오류가 아니라 실행 상한(budget_limited)이다. 예산 조회 실패·결속
// 불일치는 지금처럼 결속 오류다(C08).
func TestE2HTTPRuntimeLeaseAdmissionSeparatesSpentBudget(t *testing.T) {
	runtime, task := e2HTTPRuntimeFixture(t, nil)
	if err := runtime.AdmitTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	expected := runtime.options.Expected
	view := e2proxy.ExecutionView{Version: e2proxy.Version, State: "active", AuditComplete: true, PolicyRevision: expected.PolicyRevision,
		Scope: transmit.Scope{TaskRef: task.ID, RunRef: expected.RunRef, ResetRef: expected.ResetRef, PolicyRef: expected.PolicyRef, CaseRef: expected.CaseRef}}
	release := func(context.Context) error { return nil }
	for name, tc := range map[string]struct {
		lease   budgetLease
		want    error
		limited bool
	}{
		"usable":            {budgetLease{view: view, budgetRef: expected.BudgetRef, remaining: 1}, nil, false},
		"spent":             {budgetLease{view: view, budgetRef: expected.BudgetRef}, ErrE2HTTPBudgetLimited, true},
		"budget-read-fail":  {budgetLease{view: view, budgetRef: expected.BudgetRef, err: errors.New("budget fixture failure")}, ErrE2HTTPHost, false},
		"other-budget":      {budgetLease{view: view, budgetRef: "other-budget"}, ErrE2HTTPHost, false},
		"blocked-and-spent": {budgetLease{view: func() e2proxy.ExecutionView { v := view; v.State = "blocked"; return v }(), budgetRef: expected.BudgetRef}, ErrE2HTTPHost, false},
	} {
		t.Run(name, func(t *testing.T) {
			task.firstDiagnosisBudgetLimited.Store(false)
			lease := tc.lease
			err := runtime.leaseAdmission(task, runtime.task, E2HTTPRunOptions{Runner: &lease, Release: release})
			if !errors.Is(err, tc.want) || err == nil && tc.want != nil || task.firstDiagnosisBudgetLimited.Load() != tc.limited {
				t.Fatalf("lease admission=%v want=%v limited=%v", err, tc.want, task.firstDiagnosisBudgetLimited.Load())
			}
		})
	}
}

// 실행 Runtime은 설치가 켠 검색 설정을 진단 Planner 문맥에 싣고, 잘못된 설정으로는 만들어지지 않는다(루프 복원 결정안 §2-1).
func TestE2HTTPRuntimeCarriesWebSearchToPlanner(t *testing.T) {
	for _, backend := range []string{"", "ddgs"} {
		runtime, task := e2HTTPRuntimeFixture(t, func(o *E2HTTPRuntimeOptions) { o.WebSearch = agent.E2HTTPWebSearch{Backend: backend} })
		if err := runtime.AdmitTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		ctx, err := runtime.PreparePlanning(context.Background(), task)
		if err != nil || agent.E2HTTPWebSearchBackend(ctx) != backend {
			t.Fatalf("planner search backend=%q want %q err=%v", agent.E2HTTPWebSearchBackend(ctx), backend, err)
		}
	}
	opts := E2HTTPRuntimeOptions{Acquire: func(context.Context, E2HTTPRequest) (E2HTTPRunOptions, error) {
		return E2HTTPRunOptions{}, ErrE2HTTPHost
	}, MaxWorkerTurns: 2, MaxToolCalls: 3, WorkerTimeout: time.Second, MaxPlannerRounds: 2, MaxIntentRuns: 1, DrainTimeout: time.Second, WebSearch: agent.E2HTTPWebSearch{Backend: "brave-free"}}
	if runtime, err := NewE2HTTPRuntime(context.Background(), opts); err == nil {
		_ = runtime.Close(context.Background())
		t.Fatal("runtime accepted an invalid search setting")
	}
}
