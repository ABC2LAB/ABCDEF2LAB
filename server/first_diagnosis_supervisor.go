package server

import (
	"context"
	"errors"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/transmit"
)

var ErrFirstDiagnosisSupervisor = errors.New("첫 통합 진단의 실행·마감 상태를 확인하지 못했습니다")
var ErrFirstDiagnosisOutcome = errors.New("첫 통합 진단 실행 결과가 실패하거나 저장 완료를 확인하지 못했습니다")

var ErrFirstDiagnosisPreparation = errors.New("첫 통합 진단의 계획 준비를 완료하지 못했습니다")

// 원인은 errors.Is/Unwrap으로 보존하며 사람이 보는 출력에는 공급자 원문을 섞지 않는다.
type firstDiagnosisOperationFailure struct{ cause error }

func (firstDiagnosisOperationFailure) Error() string    { return ErrFirstDiagnosisSupervisor.Error() }
func (e firstDiagnosisOperationFailure) String() string { return e.Error() }
func (firstDiagnosisOperationFailure) GoString() string {
	return "server.firstDiagnosisOperationFailure[protected]"
}
func (firstDiagnosisOperationFailure) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }
func (e firstDiagnosisOperationFailure) Unwrap() error              { return e.cause }
func (firstDiagnosisOperationFailure) Is(target error) bool {
	return target == ErrFirstDiagnosisSupervisor
}
func firstDiagnosisFailure(cause error) error {
	if cause == nil {
		return ErrFirstDiagnosisSupervisor
	}
	return firstDiagnosisOperationFailure{cause: cause}
}

type firstDiagnosisOutcomeFailure struct{ cause error }

func (firstDiagnosisOutcomeFailure) Error() string    { return ErrFirstDiagnosisOutcome.Error() }
func (e firstDiagnosisOutcomeFailure) String() string { return e.Error() }
func (firstDiagnosisOutcomeFailure) GoString() string {
	return "server.firstDiagnosisOutcomeFailure[protected]"
}
func (firstDiagnosisOutcomeFailure) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }
func (e firstDiagnosisOutcomeFailure) Unwrap() error              { return e.cause }
func (firstDiagnosisOutcomeFailure) Is(target error) bool         { return target == ErrFirstDiagnosisOutcome }

type firstDiagnosisRegistrationKey struct{}
type firstDiagnosisRegistrationTicket struct{ taskID string }

// 이 ticket은 등록의 배타 busy 검사가 끝난 서버 내부에서만 발급한다.
func firstDiagnosisRegistrationContext(ctx context.Context, taskID string) context.Context {
	return context.WithValue(ctx, firstDiagnosisRegistrationKey{}, firstDiagnosisRegistrationTicket{taskID: taskID})
}

// FirstDiagnosisSupervisor는 한 서버의 실제 task·writer·실행 종료를 감독한다.
// 운영자 승인이나 대상 권한은 별도 호스트 공급자가 검사하며 이 타입이 생성하지 않는다.
type FirstDiagnosisSupervisor struct {
	ctx              context.Context
	m                *Manager
	mu               sync.Mutex
	server           *Server
	closing          bool
	closed           bool
	running          bool
	preparing        bool
	runStarted       bool
	admissionGranted bool
	permit           string
	runTask          string
	runIntent        int64
	registered       bool
	runErr           error
	completed        bool
	operations       sync.WaitGroup
	ownedTask        string
	operationCancel  context.CancelFunc
	joinDone         chan struct{}
}

func (*FirstDiagnosisSupervisor) String() string { return "server.FirstDiagnosisSupervisor[protected]" }
func (*FirstDiagnosisSupervisor) GoString() string {
	return "server.FirstDiagnosisSupervisor[protected]"
}

func NewFirstDiagnosisSupervisor(ctx context.Context, m *Manager) (*FirstDiagnosisSupervisor, error) {
	if ctx == nil || ctx.Err() != nil || m == nil || m.pg == nil || m.pg.DB == nil || m.tasks == nil {
		return nil, ErrFirstDiagnosisSupervisor
	}
	if err := m.pg.PingContext(ctx); err != nil {
		return nil, ErrFirstDiagnosisSupervisor
	}
	return &FirstDiagnosisSupervisor{ctx: ctx, m: m}, nil
}

func (s *FirstDiagnosisSupervisor) Attach(server *Server) error {
	if s == nil || server == nil || server.ctx == nil || server.ctx.Err() != nil || !server.firstDiagnosis || server.m != s.m || server.engine == nil || server.engine.m != s.m || !server.engine.firstDiagnosis {
		return ErrFirstDiagnosisSupervisor
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil || s.closing || s.closed || s.server != nil {
		return ErrFirstDiagnosisSupervisor
	}
	busy := false
	server.engine.started.Range(func(_, _ any) bool { busy = true; return false })
	for _, task := range s.m.List() {
		state := task.lifecycleSnapshot()
		busy = busy || state.Queued || (!state.Paused && !db.IsTerminal(state.Status))
	}
	if busy || server.engine.firstDiagnosisAdmission != nil {
		return ErrFirstDiagnosisSupervisor
	}
	// 생성자는 아직 task loop를 시작하지 않는다. 이 callback 설치 뒤에만 admission을 허용한다.
	server.engine.firstDiagnosisAdmission = func(task *Task) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if task == nil || s.ctx.Err() != nil || s.closing || s.closed || !s.running || !s.admissionGranted || s.ownedTask != task.ID || s.permit != task.ID || s.server != server || server.firstDiagnosisPreparation || server.firstDiagnosisAdmissionStopped.Load() {
			return ErrFirstDiagnosisSupervisor
		}
		current, ok := s.m.Task(task.ID)
		if !ok || current != task || task.Store == nil || task.ExpID <= 0 || task.Store.ID() != task.ExpID {
			return ErrFirstDiagnosisSupervisor
		}
		return nil
	}
	s.server = server
	return nil
}

func (s *FirstDiagnosisSupervisor) task(ctx context.Context, id string, load bool) (*Task, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || id == "" {
		return nil, ErrFirstDiagnosisSupervisor
	}
	number, err := strconv.ParseInt(id, 10, 64)
	if err != nil || number <= 0 || strconv.FormatInt(number, 10) != id {
		return nil, ErrFirstDiagnosisSupervisor
	}
	task, ok := s.m.Task(id)
	if !ok && load {
		row, readErr := s.m.pg.GetTask(number)
		if readErr != nil || row == nil || row.ID != number || row.ExplorationID <= 0 {
			return nil, ErrFirstDiagnosisSupervisor
		}
		s.m.mu.Lock()
		task = s.m.tasks[id]
		if task == nil {
			task = taskFromPG(row, s.m.pg.Exploration(row.ExplorationID), s.m.interceptor)
			s.m.tasks[id] = task
		}
		s.m.mu.Unlock()
	}
	if task == nil || task.ID != id || task.ExpID <= 0 || task.Store == nil || task.Store.ID() != task.ExpID {
		return nil, ErrFirstDiagnosisSupervisor
	}
	var actualExp int64
	if err := s.m.pg.QueryRowContext(ctx, "SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL", number).Scan(&actualExp); err != nil || actualExp != task.ExpID {
		return nil, ErrFirstDiagnosisSupervisor
	}
	return task, nil
}

func (s *FirstDiagnosisSupervisor) CheckTaskQuiescent(ctx context.Context, taskID int64) error {
	if s == nil || ctx == nil || ctx.Err() != nil || taskID <= 0 {
		return ErrFirstDiagnosisSupervisor
	}
	task, err := s.task(ctx, strconv.FormatInt(taskID, 10), true)
	if err != nil {
		return err
	}
	state := task.lifecycleSnapshot()
	if !state.Paused || state.Queued {
		return ErrFirstDiagnosisSupervisor
	}
	var paused, queued bool
	if err := s.m.pg.QueryRowContext(ctx, "SELECT paused,queued FROM tasks WHERE id=$1 AND deleted_at IS NULL", taskID).Scan(&paused, &queued); err != nil || !paused || queued {
		return ErrFirstDiagnosisSupervisor
	}
	s.mu.Lock()
	server := s.server
	closed := s.closed
	preparing := s.preparing
	s.mu.Unlock()
	if closed || preparing {
		return ErrFirstDiagnosisSupervisor
	}
	if server == nil {
		return s.pinTask(task.ID)
	} // Attach 이전에는 이 감독자에게 실행 admission이 없다.
	expected := int64(0)
	if ticket, ok := ctx.Value(firstDiagnosisRegistrationKey{}).(firstDiagnosisRegistrationTicket); ok && ticket.taskID == task.ID {
		expected = 1
	}
	if !server.engine.IsPaused(task.ID) || server.engine.inflightCount(task.ID) != expected || server.engine.ActiveLLMCalls(task.ID) != 0 {
		return ErrFirstDiagnosisSupervisor
	}
	server.engine.workMu.Lock()
	busy := len(server.engine.work) != 0
	server.engine.workMu.Unlock()
	server.chatMu.Lock()
	for _, active := range server.chatBusy {
		busy = busy || active
	}
	server.chatMu.Unlock()
	if busy {
		return ErrFirstDiagnosisSupervisor
	}
	return s.pinTask(task.ID)
}

func (s *FirstDiagnosisSupervisor) pinTask(taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ownedTask != "" && s.ownedTask != taskID {
		return ErrFirstDiagnosisSupervisor
	}
	s.ownedTask = taskID
	return nil
}
func (s *FirstDiagnosisSupervisor) owned() (*Task, error) {
	s.mu.Lock()
	id := s.ownedTask
	s.mu.Unlock()
	if id == "" {
		return nil, nil
	}
	task, ok := s.m.Task(id)
	if !ok || task == nil {
		return nil, ErrFirstDiagnosisSupervisor
	}
	return task, nil
}
func (s *FirstDiagnosisSupervisor) trackCancel(cancel context.CancelFunc) {
	s.mu.Lock()
	s.operationCancel = cancel
	closing := s.closing
	s.mu.Unlock()
	if closing {
		cancel()
	}
}

func (s *FirstDiagnosisSupervisor) AuthorizeExecution(ctx context.Context, scope transmit.Scope, intentID int64) error {
	if s == nil || ctx == nil || ctx.Err() != nil || s.ctx.Err() != nil || intentID <= 0 || scope.RunRef == "" || scope.ResetRef == "" || scope.PolicyRef == "" || scope.CaseRef == "" {
		return ErrFirstDiagnosisSupervisor
	}
	s.mu.Lock()
	server := s.server
	permitted := s.running && s.admissionGranted && !s.closing && !s.closed && s.permit == scope.TaskRef && s.ownedTask == scope.TaskRef
	s.mu.Unlock()
	if !permitted || server == nil || server.firstDiagnosisPreparation || server.firstDiagnosisAdmissionStopped.Load() {
		return ErrFirstDiagnosisSupervisor
	}
	task, err := s.task(ctx, scope.TaskRef, false)
	if err != nil || server.engine.IsPaused(task.ID) || server.engine.IsDeleting(task.ID) {
		return ErrFirstDiagnosisSupervisor
	}
	state := task.lifecycleSnapshot()
	if state.Paused || state.Queued || db.IsTerminal(state.Status) {
		return ErrFirstDiagnosisSupervisor
	}
	intent, err := task.Store.GetNode(intentID)
	if err != nil || intent == nil || intent.Kind != db.KindIntent || (intent.State != "open" && intent.State != "running") {
		return ErrFirstDiagnosisSupervisor
	}
	return nil
}

// 준비는 실행 권한을 만들지 않는다. 캐시와 실제 SQL 행이 모두 준비 상태여야 한다.
func (s *FirstDiagnosisSupervisor) checkPreparationTask(ctx context.Context, task *Task) error {
	if s == nil || ctx == nil || ctx.Err() != nil || s.ctx.Err() != nil || task == nil || task.Store == nil || task.Store.ID() != task.ExpID {
		return ErrFirstDiagnosisPreparation
	}
	state := task.lifecycleSnapshot()
	if !state.Paused || state.Queued || state.FirstRunAt != 0 || state.CompletedAt != 0 || db.IsTerminal(state.Status) {
		return ErrFirstDiagnosisPreparation
	}
	var explorationID int64
	var paused, queued, neverRun bool
	var status string
	err := s.m.pg.QueryRowContext(ctx, `SELECT exploration_id,paused,queued,status,first_run_at IS NULL AND completed_at IS NULL
 FROM tasks WHERE id=$1 AND deleted_at IS NULL`, task.ID).Scan(&explorationID, &paused, &queued, &status, &neverRun)
	if err != nil || explorationID != task.ExpID || !paused || queued || !neverRun || db.IsTerminal(status) || ctx.Err() != nil || s.ctx.Err() != nil {
		return ErrFirstDiagnosisPreparation
	}
	return nil
}

func (s *FirstDiagnosisSupervisor) PrepareIntents(ctx context.Context, taskID string) error {
	if s == nil || ctx == nil || ctx.Err() != nil || s.ctx.Err() != nil {
		return ErrFirstDiagnosisPreparation
	}
	s.mu.Lock()
	server := s.server
	if server == nil || !server.firstDiagnosisPreparation || s.closing || s.closed || s.running || s.preparing {
		s.mu.Unlock()
		return ErrFirstDiagnosisPreparation
	}
	s.preparing = true
	s.operations.Add(1)
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.preparing = false; s.mu.Unlock(); s.operations.Done() }()
	prepare, cancel := context.WithTimeout(ctx, 2*time.Minute)
	s.trackCancel(cancel)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	task, err := s.task(prepare, taskID, true)
	if err != nil {
		return ErrFirstDiagnosisPreparation
	}
	if s.checkPreparationTask(prepare, task) != nil {
		return ErrFirstDiagnosisPreparation
	}
	server.engine.Pause(task.ID, agent.AbortPausedRaceGuard)
	if server.engine.inflightCount(task.ID) != 0 || server.engine.ActiveLLMCalls(task.ID) != 0 {
		return ErrFirstDiagnosisPreparation
	}
	if active, err := task.Store.HasActiveIntent(); err != nil || active {
		return ErrFirstDiagnosisPreparation
	}
	if err := s.pinTask(task.ID); err != nil {
		return ErrFirstDiagnosisPreparation
	}
	if err := server.bindFirstDiagnosisModel(prepare, task); err != nil {
		return ErrFirstDiagnosisPreparation
	}
	planner, _ := server.engine.snapshotFor(task)
	if planner == nil {
		return ErrFirstDiagnosisPreparation
	}
	if !server.engine.beginTaskOperation(task.ID) {
		return ErrFirstDiagnosisPreparation
	}
	defer server.engine.decInflight(task.ID)
	planning := agent.WithE2HTTPPlanning(server.engine.withRuntimeHooks(prepare))
	emit := func(activity db.Activity) { server.engine.emitActivity(task, activity) }
	if err := server.createFirstDiagnosisGoals(planning, task, emit); err != nil || planning.Err() != nil {
		return ErrFirstDiagnosisPreparation
	}
	server.engine.BeginLLMCall(task.ID)
	_, _, planErr := planner.Plan(planning, mustFirstDiagnosisTaskNumber(task.ID), server.m.Assets(), task.Store, task.Goal, nil, emit)
	server.engine.EndLLMCall(task.ID)
	active, readErr := task.Store.HasActiveIntent()
	if planErr != nil || planning.Err() != nil || readErr != nil || !active || s.checkPreparationTask(planning, task) != nil || server.checkFirstDiagnosisModel(planning, task) != nil {
		return ErrFirstDiagnosisPreparation
	}
	return nil
}

func mustFirstDiagnosisTaskNumber(id string) int64 { n, _ := strconv.ParseInt(id, 10, 64); return n }

func (s *FirstDiagnosisSupervisor) Run(ctx context.Context, taskID string, intentID int64, snapshotRef string) (err error) {
	if s == nil || ctx == nil || intentID < 0 || (snapshotRef != "" && intentID == 0) {
		return ErrFirstDiagnosisSupervisor
	}
	if ctx.Err() != nil {
		return firstDiagnosisFailure(ctx.Err())
	}
	if s.ctx.Err() != nil {
		return firstDiagnosisFailure(s.ctx.Err())
	}
	s.mu.Lock()
	server := s.server
	if server == nil || server.firstDiagnosisPreparation || s.closing || s.closed || s.running || s.preparing || s.runStarted {
		s.mu.Unlock()
		return ErrFirstDiagnosisSupervisor
	}
	stage := "admission"
	s.running = true
	s.runStarted = true
	s.permit = taskID
	s.runTask = taskID
	s.runIntent = intentID
	s.registered = snapshotRef != ""
	s.operations.Add(1)
	s.mu.Unlock()
	defer func() {
		if err != nil {
			log.Printf("[first-diagnosis] task %s 단계 %s 실패", taskID, stage)
		}
		s.mu.Lock()
		s.running = false
		s.admissionGranted = false
		s.operationCancel = nil
		s.permit = ""
		s.runErr = err
		s.completed = err == nil
		s.mu.Unlock()
		s.operations.Done()
	}()
	run, cancel := context.WithTimeout(ctx, 30*time.Minute)
	s.trackCancel(cancel)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	stage = "task"
	task, err := s.task(run, taskID, false)
	if err != nil {
		return err
	}
	number := mustFirstDiagnosisTaskNumber(taskID)
	stage = "quiescence"
	if err = s.CheckTaskQuiescent(run, number); err != nil {
		return err
	}
	stage = "model"
	if err := server.bindFirstDiagnosisModel(run, task); err != nil {
		return firstDiagnosisFailure(err)
	}
	var node *db.Node
	if intentID > 0 {
		node, err = task.Store.GetNode(intentID)
		if err != nil || node == nil || node.Kind != db.KindIntent || node.State != "open" {
			return ErrFirstDiagnosisSupervisor
		}
	}
	registered := snapshotRef != ""
	if registered {
		if !safeSnapshotRef.MatchString(snapshotRef) || server.engine.controlledReads == nil || server.engine.e2HTTP != nil {
			return ErrFirstDiagnosisSupervisor
		}
		if !server.engine.beginControlledReadRegistration(taskID) {
			return ErrFirstDiagnosisSupervisor
		}
		registrationErr := server.engine.registerControlledReadMode(run, task, intentID, snapshotRef, node.State)
		server.engine.decInflight(taskID)
		if registrationErr != nil {
			return ErrFirstDiagnosisSupervisor
		}
	} else if server.engine.e2HTTP == nil || server.engine.controlledReads != nil {
		return ErrFirstDiagnosisSupervisor
	}
	s.mu.Lock()
	s.admissionGranted = true
	s.mu.Unlock()
	stage = "admission"
	queued, admissionErr := server.admitPausedTask(task)
	if admissionErr != nil || queued {
		return ErrFirstDiagnosisSupervisor
	}
	stage = "execution"
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if run.Err() != nil {
			server.engine.Pause(taskID, agent.AbortPausedRaceGuard)
			return firstDiagnosisFailure(run.Err())
		}
		intentState := ""
		if intentID > 0 {
			latest, readErr := task.Store.GetNode(intentID)
			if readErr != nil || latest == nil {
				return ErrFirstDiagnosisSupervisor
			}
			intentState = latest.State
		}
		state := task.lifecycleSnapshot()
		if (registered && (intentState == "failed" || intentState == "blocked" || intentState == "timeout")) || state.Status == "failed" || state.Status == "timeout" {
			return ErrFirstDiagnosisSupervisor
		}
		terminal := registered && intentState == "done"
		if !registered && server.engine.inflightCount(taskID) == 0 && server.engine.ActiveLLMCalls(taskID) == 0 {
			var completionErr error
			terminal, completionErr = s.generalCompletion(run, server, task)
			if completionErr != nil {
				return completionErr
			}
		}
		if terminal && server.engine.inflightCount(taskID) == 0 && server.engine.ActiveLLMCalls(taskID) == 0 {
			s.mu.Lock()
			s.admissionGranted = false
			s.mu.Unlock()
			if !registered {
				stage = "fence"
				if err = server.engine.e2HTTP.FenceTask(taskID); err != nil {
					return firstDiagnosisFailure(err)
				}
			}
			stage = "drain"
			if err = s.drainTask(run, server, task); err != nil {
				return err
			}
			if registered {
				stage = "registered-result"
				if err = s.checkRegisteredResult(run, task, intentID); err != nil {
					return err
				}
			} else {
				ready, completionErr := s.generalCompletion(run, server, task)
				if completionErr != nil || !ready {
					return ErrFirstDiagnosisSupervisor
				}
				stage = "audit-close"
				if err = server.engine.e2HTTP.CloseTask(run, taskID); err != nil {
					return firstDiagnosisFailure(err)
				}
			}
			return nil
		}
		select {
		case <-run.Done():
		case <-ticker.C:
		}
	}
}

// 일반 HTTP의 nil 반환은 감사가 닫힌 실행 마감이며 목표 충족이나 E③ 판정이 아니다.
func (s *FirstDiagnosisSupervisor) generalCompletion(ctx context.Context, server *Server, task *Task) (bool, error) {
	var active, done, bad int
	err := s.m.pg.QueryRowContext(ctx, `SELECT
 count(*) FILTER (WHERE state IN ('open','running')),
 count(*) FILTER (WHERE state='done' OR (state='blocked' AND blocked_reason IN ('e2_http_policy_limited','e2_http_unsupported'))),
 count(*) FILTER (WHERE state NOT IN ('open','running','done') AND NOT (state='blocked' AND blocked_reason IN ('e2_http_policy_limited','e2_http_unsupported')))
 FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent'`, task.ExpID).Scan(&active, &done, &bad)
	if err != nil || bad > 0 {
		return false, ErrFirstDiagnosisSupervisor
	}
	if active > 0 {
		return false, nil
	}
	if task.firstDiagnosisWorkSeq.Load() == 0 || task.firstDiagnosisPlannedSeq.Load() < task.firstDiagnosisWorkSeq.Load() {
		return false, nil
	}
	if done == 0 {
		if db.IsTerminal(task.lifecycleSnapshot().Status) {
			return false, ErrFirstDiagnosisSupervisor
		}
		return false, nil
	}
	execution, ok := server.engine.e2HTTP.Execution(task.ID)
	if !ok || execution == nil {
		return false, ErrFirstDiagnosisSupervisor
	}
	calls := execution.Calls()
	if len(calls) == 0 {
		return false, ErrFirstDiagnosisSupervisor
	}
	results := server.engine.e2HTTP.Results(task.ID)
	if len(results) != len(calls) {
		return false, ErrFirstDiagnosisSupervisor
	}
	observed := make(map[string]e2proxy.CallResult, len(results))
	limited := make(map[int64]map[string]bool)
	for _, result := range results {
		validToolOutcome := firstDiagnosisClosedToolOutcome(result)
		if !result.Final || !result.Call.Final || !result.Call.AuditComplete || !result.ProcessBashExited || !result.ProcessDrained || !result.ProxyDrained || !validToolOutcome || result.Execution.Scope.TaskRef != task.ID || observed[result.Call.CallRef].CallRef != "" {
			return false, ErrFirstDiagnosisSupervisor
		}
		observed[result.Call.CallRef] = result.Call
		if result.Call.Counts.Denied > 0 || result.Call.Counts.Unsupported > 0 {
			if limited[result.Call.IntentID] == nil {
				limited[result.Call.IntentID] = make(map[string]bool)
			}
			reason := "e2_http_policy_limited"
			if result.Call.Counts.Denied == 0 {
				reason = "e2_http_unsupported"
			}
			limited[result.Call.IntentID][reason] = true
		}
	}
	for _, call := range calls {
		if !e2proxy.EqualCallResults(observed[call.CallRef], call) || !call.Final || !call.AuditComplete || call.Counts.Unknown != 0 {
			return false, ErrFirstDiagnosisSupervisor
		}
		intent, err := task.Store.GetNode(call.IntentID)
		if err != nil || intent == nil || intent.Kind != db.KindIntent || (intent.State != "done" && !(intent.State == "blocked" && limited[intent.ID][intent.BlockedReason])) {
			return false, ErrFirstDiagnosisSupervisor
		}
	}
	var matchedBlocked int
	for intentID := range limited {
		intent, readErr := task.Store.GetNode(intentID)
		if readErr != nil {
			return false, ErrFirstDiagnosisSupervisor
		}
		if intent != nil && intent.State == "blocked" {
			matchedBlocked++
		}
	}
	var blocked int
	if err := s.m.pg.QueryRowContext(ctx, "SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent' AND state='blocked'", task.ExpID).Scan(&blocked); err != nil || blocked != matchedBlocked {
		return false, ErrFirstDiagnosisSupervisor
	}
	return true, nil
}

// 감사가 닫힌 정책 제한은 진단 범위의 제한이다. 불명·미완료·알 수 없는 분류는 마감 실패다.
func firstDiagnosisClosedToolOutcome(result e2runner.TrustedResult) bool {
	c := result.Call.Counts
	if c.Unknown != 0 || result.Execution.Counts.Unknown != 0 || !result.Execution.AuditComplete {
		return false
	}
	if result.Call.Reason != e2proxy.Complete && result.Call.Reason != e2proxy.PolicyDenied && result.Call.Reason != e2proxy.Unsupported {
		return false
	}
	switch result.Classification {
	case "completed":
		return result.ExitCode == 0 && c.Denied == 0 && c.Unsupported == 0 && result.Call.Reason == e2proxy.Complete
	case "tool_failure":
		return result.ExitCode != 0 && c.Denied == 0 && c.Unsupported == 0 && result.Call.Reason == e2proxy.Complete
	case "policy_not_executed":
		return c.Denied > 0 && c.ForwardAttempted == 0
	case "partial_policy_denial":
		return c.Denied > 0 && c.ForwardAttempted > 0
	case "unsupported":
		return c.Denied == 0 && c.Unsupported > 0 && c.ForwardAttempted == 0
	case "partial_unsupported":
		return c.Denied == 0 && c.Unsupported > 0 && c.ForwardAttempted > 0
	default:
		return false
	}
}

func (s *FirstDiagnosisSupervisor) drainTask(ctx context.Context, server *Server, task *Task) error {
	server.engine.Pause(task.ID, agent.AbortPausedRaceGuard)
	server.cancelTaskChat(task.ID, agent.AbortPausedRaceGuard)
	server.engine.runtimeMu.Lock()
	rt := server.engine.runtimes[task.ID]
	if rt != nil {
		rt.cancel()
	}
	server.engine.runtimeMu.Unlock()
	if rt != nil {
		done := make(chan struct{})
		go func() { rt.wg.Wait(); close(done) }()
		select {
		case <-ctx.Done():
			return firstDiagnosisFailure(ctx.Err())
		case <-done:
		}
	}
	if _, err := s.m.pg.ExecContext(ctx, "UPDATE tasks SET paused=true,queued=false WHERE id=$1 AND deleted_at IS NULL", task.ID); err != nil {
		return ErrFirstDiagnosisSupervisor
	}
	task.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true; state.Queued = false })
	return s.CheckTaskQuiescent(ctx, mustFirstDiagnosisTaskNumber(task.ID))
}

// 저장 완료는 취약점 confirmed와 별개다. nonconfirmed에는 finding을 합성하지 않는다.
func (s *FirstDiagnosisSupervisor) checkRegisteredResult(ctx context.Context, task *Task, intentID int64) error {
	var total, consistent int
	err := s.m.pg.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER (WHERE
   (v.oracle_value='confirmed' AND l.id IS NOT NULL AND f.id IS NOT NULL AND f.evidence_version=l.evidence_version)
   OR (v.oracle_value IN ('suspected','undecidable','allowed') AND l.id IS NULL AND f.id IS NULL))
 FROM evidence_revision_initials r
 JOIN e3_evidence_manifests m ON m.task_id=r.task_id AND m.execution_ref=r.execution_ref AND m.evidence_revision=r.revision
 JOIN e3_verdict_issuances i ON i.manifest_id=m.id AND i.task_id=r.task_id AND i.exploration_id=r.exploration_id AND i.intent_id=r.intent_id AND i.mode_version=r.mode_version AND i.execution_ref=r.execution_ref AND i.eval_run_id IS NULL
 JOIN e3_oracle_verdicts v ON v.id=i.verdict_id
 LEFT JOIN e3_verdict_links l ON l.issuance_id=i.id AND l.task_id=i.task_id AND l.purpose='initial'
 LEFT JOIN findings f ON f.id=l.finding_id AND f.task_id=l.task_id
 WHERE r.task_id=$1 AND r.exploration_id=$2 AND r.intent_id=$3`, task.ID, task.ExpID, intentID).Scan(&total, &consistent)
	if err != nil || total != 1 || consistent != 1 {
		return ErrFirstDiagnosisSupervisor
	}
	return nil
}

func (s *FirstDiagnosisSupervisor) Shutdown() ControlledReadAssemblyShutdown {
	return ControlledReadAssemblyShutdown{BlockAdmission: s.blockAdmission, CancelAndDrain: s.cancelAndDrain, CheckResults: s.checkResults}
}

func (s *FirstDiagnosisSupervisor) blockAdmission(ctx context.Context, server *Server) error {
	if s == nil || ctx == nil || ctx.Err() != nil {
		return ErrFirstDiagnosisSupervisor
	}
	s.mu.Lock()
	if server == nil || s.server != server {
		s.mu.Unlock()
		return ErrFirstDiagnosisSupervisor
	}
	s.closing = true
	cancel := s.operationCancel
	server.firstDiagnosisAdmissionStopped.Store(true)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if server.engine.e2HTTP != nil {
		if err := server.engine.e2HTTP.Fence(); err != nil {
			return ErrFirstDiagnosisSupervisor
		}
	}
	task, err := s.owned()
	if err != nil {
		return err
	}
	server.engine.deleteMu.Lock()
	if task != nil {
		server.engine.deleting.Store(task.ID, true)
	}
	server.engine.deleteMu.Unlock()
	return nil
}

func (s *FirstDiagnosisSupervisor) cancelAndDrain(ctx context.Context, server *Server) error {
	if s == nil || ctx == nil || ctx.Err() != nil {
		return ErrFirstDiagnosisSupervisor
	}
	s.mu.Lock()
	if server == nil || s.server != server || !s.closing {
		s.mu.Unlock()
		return ErrFirstDiagnosisSupervisor
	}
	if s.joinDone == nil {
		s.joinDone = make(chan struct{})
		done := s.joinDone
		taskID := s.ownedTask
		if taskID != "" {
			server.engine.Pause(taskID, agent.AbortPausedRaceGuard)
			server.cancelTaskChat(taskID, agent.AbortPausedRaceGuard)
		}
		server.engine.runtimeMu.Lock()
		runtimes := make([]*taskRuntime, 0, 1)
		if rt := server.engine.runtimes[taskID]; rt != nil {
			rt.cancel()
			runtimes = append(runtimes, rt)
		}
		server.engine.runtimeMu.Unlock()
		go func() {
			s.operations.Wait()
			for _, rt := range runtimes {
				rt.wg.Wait()
			}
			close(done)
		}()
	}
	done := s.joinDone
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return firstDiagnosisFailure(ctx.Err())
	case <-done:
	}
	task, err := s.owned()
	if err != nil {
		return err
	}
	if task != nil {
		if err := s.drainTask(ctx, server, task); err != nil {
			return err
		}
		if server.engine.e2HTTP != nil {
			if err := server.engine.e2HTTP.CloseTask(ctx, task.ID); err != nil {
				return firstDiagnosisFailure(err)
			}
		}
	}
	return nil
}

func (s *FirstDiagnosisSupervisor) checkResults(ctx context.Context, server *Server) error {
	if s == nil || ctx == nil || ctx.Err() != nil {
		return ErrFirstDiagnosisSupervisor
	}
	s.mu.Lock()
	if server == nil || s.server != server || !s.closing || s.running || s.preparing {
		s.mu.Unlock()
		return ErrFirstDiagnosisSupervisor
	}
	started, completed, runErr, registered, taskID, intentID := s.runStarted, s.completed, s.runErr, s.registered, s.runTask, s.runIntent
	s.mu.Unlock()
	task, err := s.owned()
	if err != nil {
		return err
	}
	if task != nil {
		if err := s.CheckTaskQuiescent(ctx, mustFirstDiagnosisTaskNumber(task.ID)); err != nil {
			return err
		}
	}
	if started && (!completed || runErr != nil) {
		return firstDiagnosisOutcomeFailure{cause: runErr}
	}
	if started && registered {
		task, err := s.task(ctx, taskID, false)
		if err != nil {
			return err
		}
		if err := s.checkRegisteredResult(ctx, task, intentID); err != nil {
			return firstDiagnosisOutcomeFailure{cause: err}
		}
	}
	return nil
}
