//go:build linux

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"github.com/Autumn-27/artex/transmit"
)

func planningLogRows(t *testing.T, logger *diagnosticlog.Logger) []map[string]any {
	t.Helper()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logger.Path())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("synthetic-private-session")) || bytes.Contains(raw, []byte("private-query")) {
		t.Fatal("원문 오류가 운영 기록에 남음")
	}
	rows := []map[string]any{}
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var row map[string]any
		if json.Unmarshal(line, &row) != nil {
			t.Fatal("진단 JSON 파손")
		}
		if row["stage"] == "planning" {
			rows = append(rows, row)
		}
	}
	return rows
}
func TestPlanningFailureReasonsPreserveReturnAndBudgets(t *testing.T) {
	private := errors.New("Authorization: synthetic-private-session private-query")
	cases := []struct {
		name, reason, status string
		want                 error
		limited              bool
		setup                func(*E2HTTPRuntime, *Task, context.Context) (*Task, context.Context)
	}{
		{"normal", "", "", nil, false, nil},
		{"unbound", "planning_task_unavailable", "failed", ErrE2HTTPHost, false, func(r *E2HTTPRuntime, task *Task, ctx context.Context) (*Task, context.Context) {
			return &Task{ID: "untrusted-task"}, ctx
		}},
		{"nil-context", "planning_context_closed", "failed", ErrE2HTTPHost, false, func(r *E2HTTPRuntime, task *Task, _ context.Context) (*Task, context.Context) { return task, nil }},
		{"cancelled", "planning_context_closed", "cancelled", ErrE2HTTPHost, false, func(r *E2HTTPRuntime, task *Task, ctx context.Context) (*Task, context.Context) {
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			return task, ctx
		}},
		{"deadline", "planning_context_closed", "timeout", ErrE2HTTPHost, false, func(r *E2HTTPRuntime, task *Task, ctx context.Context) (*Task, context.Context) {
			ctx, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
			cancel()
			return task, ctx
		}},
		{"authority", "planning_authority_rejected", "failed", ErrE2HTTPHost, false, func(r *E2HTTPRuntime, task *Task, ctx context.Context) (*Task, context.Context) {
			r.options.CheckAuthority = func(context.Context) error { return private }
			return task, ctx
		}},
		{"exploration", "planning_exploration_failed", "failed", private, false, func(r *E2HTTPRuntime, task *Task, ctx context.Context) (*Task, context.Context) {
			r.options.Exploration = func(context.Context, *Task) (ExplorationCharge, error) { return ExplorationCharge{}, private }
			return task, ctx
		}},
		{"workflow", "planning_workflow_blocked", "blocked", diagnosisflow.ErrUnavailable, false, func(r *E2HTTPRuntime, task *Task, ctx context.Context) (*Task, context.Context) {
			blocked := false
			flow, err := diagnosisflow.New(ctx, diagnosisflow.Binding{JobID: "planning-log", TaskID: task.ID, RunRef: "owned", SourceDigest: strings.Repeat("a", 64), Owner: "ARTEX", Mode: diagnosisflow.Auto, TargetURL: "https://owned.test/read", PathPrefixes: []string{"/read"}, ReadonlyPrefixes: []string{"/read"}, Methods: []string{"GET"}, Limits: diagnosisflow.Limits{Format: diagnosisflow.Version, MaxPages: 1, MaxEndpoints: 1, MaxResponseBytes: 4096}, Current: func() error {
				if blocked {
					return private
				}
				return nil
			}}, workflowLoopStore{})
			if err != nil {
				t.Fatal(err)
			}
			blocked = true
			r.options.Workflow = flow
			return task, ctx
		}},
		// 흐름이 일시정지(범위 선택 대기와 같은 기다림)인 동안 시간 상한 마감 유예가 끝나 감독자가 멈추면 실패가 아니라 실행
		// 상한이다(2026-11-26 NodeGoat 회차 2 발견 5). 다른 까닭의 취소는 지금처럼 workflow 차단이다.
		{"time-budget-wait", "planning_time_budget", "blocked", ErrE2HTTPBudgetLimited, true, func(r *E2HTTPRuntime, task *Task, ctx context.Context) (*Task, context.Context) {
			return task, pausedWorkflowCancelled(t, r, task, ctx, agent.AbortTimeBudgetDrain)
		}},
		{"other-cancel-wait", "planning_workflow_blocked", "blocked", context.Canceled, false, func(r *E2HTTPRuntime, task *Task, ctx context.Context) (*Task, context.Context) {
			return task, pausedWorkflowCancelled(t, r, task, ctx, agent.AbortPausedByUser)
		}},
		{"time-budget-exploration", "planning_time_budget", "blocked", ErrE2HTTPBudgetLimited, true, func(r *E2HTTPRuntime, task *Task, ctx context.Context) (*Task, context.Context) {
			ctx, cancel := context.WithCancelCause(ctx)
			r.options.Exploration = func(stage context.Context, _ *Task) (ExplorationCharge, error) {
				cancel(agent.AbortTimeBudgetDrain)
				<-stage.Done()
				return ExplorationCharge{}, private
			}
			return task, ctx
		}},
		{"closed", "planning_task_closed", "failed", ErrE2HTTPHost, false, func(r *E2HTTPRuntime, task *Task, ctx context.Context) (*Task, context.Context) {
			r.task.closed = true
			return task, ctx
		}},
		{"blocked", "planning_task_blocked", "blocked", ErrE2HTTPHost, false, func(r *E2HTTPRuntime, task *Task, ctx context.Context) (*Task, context.Context) {
			r.task.blocked = true
			return task, ctx
		}},
		{"task-context", "planning_task_cancelled", "cancelled", ErrE2HTTPHost, false, func(r *E2HTTPRuntime, task *Task, ctx context.Context) (*Task, context.Context) {
			r.task.cancel()
			return task, ctx
		}},
		{"rounds", "planning_round_limit", "blocked", ErrE2HTTPBudgetLimited, true, func(r *E2HTTPRuntime, task *Task, ctx context.Context) (*Task, context.Context) {
			r.task.plannerRounds = r.options.MaxPlannerRounds
			return task, ctx
		}},
		{"invalid-lease-flag", "planning_lease_invalid", "failed", ErrE2HTTPHost, false, func(r *E2HTTPRuntime, task *Task, ctx context.Context) (*Task, context.Context) {
			r.task.lease = &E2HTTPRunOptions{}
			return task, ctx
		}},
		{"invalid-lease-binding", "planning_lease_invalid", "failed", ErrE2HTTPHost, false, func(r *E2HTTPRuntime, task *Task, ctx context.Context) (*Task, context.Context) {
			r.task.lease = &E2HTTPRunOptions{}
			r.task.validLease = true
			return task, ctx
		}},
		{"request-budget", "planning_request_budget", "blocked", ErrE2HTTPBudgetLimited, true, func(r *E2HTTPRuntime, task *Task, ctx context.Context) (*Task, context.Context) {
			expected := r.options.Expected
			runner := &budgetLease{view: e2proxy.ExecutionView{Version: e2proxy.Version, State: "active", AuditComplete: true, PolicyRevision: expected.PolicyRevision, Scope: transmit.Scope{TaskRef: task.ID, RunRef: expected.RunRef, ResetRef: expected.ResetRef, PolicyRef: expected.PolicyRef, CaseRef: expected.CaseRef}}, budgetRef: expected.BudgetRef}
			r.task.lease = &E2HTTPRunOptions{Runner: runner, Release: func(context.Context) error { return nil }}
			r.task.validLease = true
			return task, ctx
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger, err := diagnosticlog.Create(firstDiagnosisPrivateTemp(t), "run", 7, "")
			if err != nil {
				t.Fatal(err)
			}
			defer logger.Close()
			ctx := diagnosticlog.With(t.Context(), logger)
			runtime, task := e2HTTPRuntimeFixture(t, nil)
			runtime.ctx = diagnosticlog.With(runtime.ctx, logger)
			if err := runtime.AdmitTask(ctx, task); err != nil {
				t.Fatal(err)
			}
			// 분기 시험의 합성 lease는 실제 Runner 자원이 아니다. fixture 종료가 이를 실행기로 사용하지 않게 한다.
			defer func() { runtime.task.lease = nil; runtime.task.closed = false }()
			inputTask, inputCtx := task, ctx
			if tc.setup != nil {
				inputTask, inputCtx = tc.setup(runtime, task, ctx)
			}
			rounds := runtime.task.plannerRounds
			out, err := runtime.PreparePlanning(inputCtx, inputTask)
			if (tc.want == nil) != (err == nil) || !errors.Is(err, tc.want) || tc.want != nil && out != inputCtx || task.firstDiagnosisBudgetLimited.Load() != tc.limited {
				t.Fatal("관측이 기존 반환·예산 의미를 변경함", err)
			}
			// 엔진이 작업을 실패로 표시하는 분기는 진단 로그와 같은 고정 사유를 오류에 싣는다(B1). 원문 오류는 사유가 아니다.
			if reason, ok := diagnosisCheckReason(err); tc.want != nil && !tc.limited && (!ok || reason != tc.reason) {
				t.Fatalf("실패 오류의 사유 %q, 기대 %q", reason, tc.reason)
			}
			if tc.want != nil && !tc.limited {
				failed := &Task{ID: task.ID}
				failed.recordFirstDiagnosisFailure(err)
				if failed.firstDiagnosisFailure() != tc.reason {
					t.Fatal("작업에 실패 사유가 남지 않음")
				}
			}
			wantRounds := rounds
			if tc.want == nil {
				wantRounds++
			}
			if runtime.task.plannerRounds != wantRounds || runtime.task.control.Snapshot().Calls != 0 {
				t.Fatal("관측이 라운드·호출 수를 변경함")
			}
			rows := planningLogRows(t, logger)
			if tc.reason == "" {
				if len(rows) != 0 {
					t.Fatal("정상 경로를 실패로 기록")
				}
				return
			}
			if len(rows) != 1 || rows[0]["reason_code"] != tc.reason || rows[0]["status"] != tc.status || rows[0]["task_id"] != float64(7) {
				t.Fatal("고정 분기 사유·결속 불일치", rows)
			}
		})
	}
}
func TestPlanningDiagnosticFailureDoesNotChangeReturn(t *testing.T) {
	logger, err := diagnosticlog.Create(firstDiagnosisPrivateTemp(t), "run", 7, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := diagnosticlog.With(t.Context(), logger)
	runtime, task := e2HTTPRuntimeFixture(t, nil)
	if err := runtime.AdmitTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	runtime.options.CheckAuthority = func(context.Context) error { return errors.New("synthetic-private-session") }
	if _, err := runtime.PreparePlanning(ctx, task); !errors.Is(err, ErrE2HTTPHost) || runtime.task.plannerRounds != 0 || task.firstDiagnosisBudgetLimited.Load() {
		t.Fatal("운영 기록 장애가 실행 판정을 변경함")
	}
}

// 실제 Worker 도구 admission으로 Control 호출 수를 소모한다. Runner·모델은 합성이며 Bash·HTTP는 실행하지 않는다.
type planningCountRunner struct{ view e2proxy.ExecutionView }

func (r *planningCountRunner) SnapshotForIntent(int64) e2proxy.ExecutionView { return r.view }
func (r *planningCountRunner) HasIntent(id int64) bool                       { return r.view.IntentID == id }
func (*planningCountRunner) RedactOutput(value []byte) []byte                { return bytes.Clone(value) }
func (*planningCountRunner) RedactRecord(value []byte) []byte                { return bytes.Clone(value) }
func (r *planningCountRunner) RunForIntent(_ context.Context, id int64, _ string) (e2runner.Result, error) {
	r.view.Counts.Calls++
	return e2runner.Result{ToolOutput: "owned synthetic tool observation", Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Execution: r.view, Call: e2proxy.CallResult{CallRef: "planning-count-call", IntentID: id, Final: true, AuditComplete: true, Reason: e2proxy.Complete, Counts: e2proxy.Counts{Calls: 1}}, Final: true, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, Classification: "completed"}}, nil
}
func TestPlanningToolLimitAfterActualWorkerAdmissionOwnedPostgres(t *testing.T) {
	_, task, intent := firstDiagnosisActualTask(t)
	runtime, _ := e2HTTPRuntimeFixture(t, func(o *E2HTTPRuntimeOptions) { o.MaxToolCalls = 1 })
	logger, err := diagnosticlog.Create(firstDiagnosisPrivateTemp(t), "run", mustFirstDiagnosisTaskNumber(task.ID), "")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	ctx := diagnosticlog.With(t.Context(), logger)
	if err := runtime.AdmitTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	expected := runtime.options.Expected
	runner := &planningCountRunner{view: e2proxy.ExecutionView{Version: e2proxy.Version, State: "active", IntentID: intent.ID, AuditComplete: true, PolicyRevision: expected.PolicyRevision, Scope: transmit.Scope{TaskRef: task.ID, RunRef: expected.RunRef, ResetRef: expected.ResetRef, PolicyRef: expected.PolicyRef, CaseRef: expected.CaseRef}}}
	binding := agent.E2HTTPExecution{Runner: runner, Control: runtime.task.control, TaskID: mustFirstDiagnosisTaskNumber(task.ID), IntentID: intent.ID, TaskRef: task.ID, RunRef: expected.RunRef, MaxTurns: 4, MaxDuration: 5 * time.Second}
	worker := agent.NewWorker(&firstDiagnosisE2Provider{command: "printf synthetic-observation"}, "synthetic", t.TempDir(), nil, 10000, 4)
	worker.SetNonStreaming(func() bool { return true })
	_, _, err = worker.Execute(agent.WithE2HTTPExecution(ctx, binding), "worker", binding.TaskID, nil, task.Store, intent, nil, func(db.Activity) {}, nil, nil)
	if err != nil && !errors.Is(err, agent.ErrE2HTTPPolicyLimited) {
		t.Fatal("합성 Worker 도구 경계 실패", err)
	}
	if runtime.task.control.Snapshot().Calls != 1 {
		t.Fatal("실제 도구 admission이 호출 수를 소모하지 않음")
	}
	if _, err := runtime.PreparePlanning(ctx, task); err != ErrE2HTTPBudgetLimited || !task.firstDiagnosisBudgetLimited.Load() {
		t.Fatal("호출 상한 반환 의미 변경", err)
	}
	found := false
	for _, row := range planningLogRows(t, logger) {
		if row["reason_code"] == "planning_tool_limit" {
			found = true
		}
	}
	if !found {
		t.Fatal("도구 상한의 고정 사유 누락")
	}
}

// pausedWorkflowCancelled는 일시정지한 흐름을 붙이고, 계획 준비가 흐름을 기다리기 시작한 뒤 cause로 취소하는 문맥을 돌려준다.
func pausedWorkflowCancelled(t *testing.T, r *E2HTTPRuntime, task *Task, ctx context.Context, cause error) context.Context {
	t.Helper()
	flow, err := diagnosisflow.New(ctx, diagnosisflow.Binding{JobID: "planning-log", TaskID: task.ID, RunRef: "owned", SourceDigest: strings.Repeat("a", 64), Owner: "ARTEX", Mode: diagnosisflow.Auto, TargetURL: "https://owned.test/read", PathPrefixes: []string{"/read"}, ReadonlyPrefixes: []string{"/read"}, Methods: []string{"GET"}, Limits: diagnosisflow.Limits{Format: diagnosisflow.Version, MaxPages: 1, MaxEndpoints: 1, MaxResponseBytes: 4096}, Current: func() error { return nil }}, workflowLoopStore{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := flow.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = flow.Command(ctx, "ARTEX", "pause", "pause-1", snapshot.Revision, nil); err != nil {
		t.Fatal("pause", err)
	}
	r.options.Workflow = flow
	waiting, cancel := context.WithCancelCause(ctx)
	time.AfterFunc(50*time.Millisecond, func() { cancel(cause) })
	return waiting
}

// 시간 상한 멈춤 표시는 계획 준비가 다시 정상으로 통과하면 지운다. 지난 표시가 그 뒤 마감 검사를 바꾸지 않게 한다
// (#964 윤재상 리뷰).
func TestPlanningTimeBudgetMarkClearedByLaterPlanning(t *testing.T) {
	runtime, task := e2HTTPRuntimeFixture(t, nil)
	if err := runtime.AdmitTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	defer func() { runtime.task.lease = nil; runtime.task.closed = false }()
	if err := runtime.planningTimeBudget(t.Context(), task); !errors.Is(err, ErrE2HTTPBudgetLimited) || !runtime.planningStoppedByTimeBudget(task.ID) {
		t.Fatal("시간 상한 표시가 없음", err)
	}
	if _, err := runtime.PreparePlanning(t.Context(), task); err != nil {
		t.Fatal("계획 준비가 통과하지 않음", err)
	}
	if runtime.planningStoppedByTimeBudget(task.ID) {
		t.Fatal("다시 통과한 계획 준비 뒤에도 시간 상한 표시가 남음")
	}
}
