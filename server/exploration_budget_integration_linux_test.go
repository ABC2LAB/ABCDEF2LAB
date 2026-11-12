//go:build linux

package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosis"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/exploration"
)

// 실제 LOGIN 역할·production constructor·Supervisor.Run과 종료/영속화를 통과한다. 모델은 명시적 fixture다.
func firstDiagnosisSupervisorExplorationBudget(t *testing.T, manager *Manager, installer *db.DB, task *Task) {
	t.Helper()
	firstDiagnosisSupervisorConstructorClose(t, manager, installer, task, func(runtime *FirstDiagnosisHTTPRuntime) {
		host := runtime.server
		supervisor := runtime.host.supervisor.(*FirstDiagnosisSupervisor)
		e2 := runtime.host.runtime
		if diagnosisflow.Migrate(t.Context(), installer.DB) != nil {
			t.Fatal("owned workflow migrate failed")
		}
		fixtureServer, fixtureTask, options, boundary, browser, mcp, provider := pipelineFixtureWithStore(t, diagnosisflow.Postgres{DB: installer.DB}, task.ID, e2.options.Expected.RunRef)
		profile, err := installer.ActiveProfile()
		if err != nil || profile == nil {
			t.Fatal("owned model profile missing")
		}
		profile.ContextWindowK = 64
		profile.MaxTokens = 128
		if _, err = installer.SaveProfile(profile); err != nil {
			t.Fatal("owned model budget config failed")
		}
		budget, err := NewFirstDiagnosisModelBudget(diagnosisprep.Budget{ModelRequests: 2, InputTokens: 2 * 65536, OutputTokens: 256}, 65536, 128, func(diagnosisprep.ModelUsage) error { return nil })
		if err != nil || budget.ConfigureAdmission(options.Workflow.Wait) != nil {
			t.Fatal("owned shared model budget failed")
		}
		host.firstModelBudget = budget
		host.invalidateProfileAgents()
		firstDiagnosisInjectProfileProvider(t, host, profile.ID, provider)
		e2.options.Workflow = options.Workflow
		// host factory가 받은 Worker는 production task resolver의 실제 기존 pointer다.
		factory := options.OpenHost
		fixtureWorker, workerErr := fixtureServer.ExplorationWorkerFor(fixtureTask)
		if workerErr != nil {
			t.Fatal("owned fixture worker unavailable")
		}
		options.OpenHost = func(ctx context.Context, binding exploration.Binding, w *agent.Worker) (ExplorationPipelineHost, error) {
			if w == nil || !w.ExplorationReady() {
				t.Error("approved production worker unavailable")
			}
			// fixture factory의 pointer 동일성 검사는 자체 fixture pointer에 대해 수행하고, 제품 pipeline이 실제 pointer로 결속한다.
			result, err := factory(ctx, binding, fixtureWorker)
			return result, err
		}
		var acquired atomic.Int32
		acquire := e2.options.Acquire
		e2.options.Acquire = func(ctx context.Context, request E2HTTPRequest) (E2HTTPRunOptions, error) {
			acquired.Add(1)
			return acquire(ctx, request)
		}
		e2.options.Exploration = func(ctx context.Context, actual *Task) error {
			_, err := host.RunExplorationPipeline(ctx, actual, options)
			return err
		}
		if err = supervisor.Run(t.Context(), task.ID, 0, ""); err != nil {
			snap, _ := options.Workflow.Snapshot()
			usage := budget.Snapshot()
			e2.task.mu.Lock()
			proof := e2.task.explorationBudget
			stageErr := e2.task.explorationErr
			done := e2.task.explorationDone
			e2.task.mu.Unlock()
			counts, countErr := firstDiagnosisIntentOutcomes(t.Context(), manager.pg.DB, task.ExpID)
			out, closing := firstDiagnosisClosingOutcomes(counts, firstDiagnosisBudgetLimited(task, budget), firstDiagnosisPlanned(task))
			_, allowed := e2.explorationBudgetOutcome(task, budget, out)
			t.Fatalf("supervisor failed: outcome_error=%v stage_budget=%v stage_done=%v proof=%v valid=%v allowed=%v count_error=%v closing=%v incomplete=%d running=%d unknown=%d task_status=%s provider=%d requests=%d known=%v limited=%v status=%s reason=%s phase=%s observed=%d", errors.Is(err, ErrFirstDiagnosisOutcome), errors.Is(stageErr, ErrE2HTTPBudgetLimited), done, proof != nil, proof != nil && proof.validFlow(), allowed, countErr != nil, closing, out.Incomplete, counts.running, out.Unknown, task.lifecycleSnapshot().Status, provider.calls.Load(), usage.Requests, usage.UsageKnown, budget.LimitReached(), snap.ExplorationStatus, snap.ExplorationReason, snap.Stage, snap.Counts.ExplorationRequests)
		}
		result := supervisor.RunResult()
		if result == nil || result.Outcome != diagnosis.OutcomePartial || result.ClosingReason != diagnosis.ClosingBudgetLimited || result.IntentOutcomes.Incomplete != 1 || result.IntentOutcomes.Done != 0 {
			t.Fatal("actual supervisor did not preserve partial budget disposition")
		}
		snapshot, _ := options.Workflow.Snapshot()
		usage := budget.Snapshot()
		if snapshot.Counts.ExplorationRequests != 3 || snapshot.Counts.Sent != 0 || snapshot.Counts.Attempted != 0 || snapshot.DeepRuns != 0 || snapshot.DeepClosed != 0 || snapshot.ExplorationReason != "cap_exhausted" || provider.calls.Load() != 2 || !usage.UsageKnown || usage.Requests != 2 || !budget.LimitReached() {
			t.Fatal("exploration evidence or actual shared budget was fabricated")
		}
		if len(options.Workflow.DiscoveryInventory()) < 4 || boundary.lease.closed() != 1 || browser.closes.Load() != 1 || mcp.deleted.Load() != 1 || acquired.Load() != 0 {
			t.Fatal("observed/inferred preservation or cleanup was incomplete")
		}
		if execution, ok := e2.Execution(task.ID); ok || execution != nil || len(e2.Results(task.ID)) != 0 || e2.task.control.Snapshot().Calls != 0 {
			t.Fatal("budget closure invented diagnostic authority/receipt")
		}

		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err = runtime.Close(closeCtx); err != nil || !runtime.ResourcesClosed() {
			t.Fatal("actual supervisor drain/authority release failed")
		}
		stored, err := supervisor.PersistRunOutcomeStored(closeCtx, nil)
		if err != nil || !stored {
			t.Fatal("actual partial budget outcome did not persist")
		}
		runState := host.requestApprovals.runState(mustFirstDiagnosisTaskNumber(task.ID))
		if runState.Phase != "closed" || !runState.DrainComplete || runState.ClosureFailed || runState.Outcome != "completed" {
			t.Fatal("unstarted diagnostic audit/drain phase was not closed truthfully")
		}
		// 같은 nil Execution이라도 private proof가 없거나 ledger가 불명이면 기존 거부를 유지한다.
		e2.task.mu.Lock()
		proof := e2.task.explorationBudget
		e2.task.explorationBudget = nil
		e2.task.mu.Unlock()
		if ready, _, err := supervisor.generalCompletion(closeCtx, host, task); ready || err == nil {
			t.Fatal("nil execution/generic budget flag accepted without proof")
		}
		e2.task.mu.Lock()
		e2.task.explorationBudget = proof
		e2.task.mu.Unlock()
		budget.mu.Lock()
		budget.blocked = true
		budget.mu.Unlock()
		if ready, _, err := supervisor.generalCompletion(closeCtx, host, task); ready || err == nil {
			t.Fatal("unknown budget ledger accepted")
		}
		budget.mu.Lock()
		budget.blocked = false
		budget.mu.Unlock()
		if _, ok := e2.explorationBudgetOutcome(task, budget, diagnosis.IntentOutcomes{Done: 1, Incomplete: 1}); ok {
			t.Fatal("unexecuted diagnostic done accepted")
		}
	})
}
