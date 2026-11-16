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
	"github.com/Autumn-27/artex/diagnosis"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
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
	runLogErr        error
	completed        bool
	// result는 감사가 닫힌 실행의 결과다(실행 결과 상태 값 계약). completed일 때만 밖에 보인다.
	result *diagnosis.RunResult
	// stopped는 정상 마감하지 못한 실행의 마감 때 intent 결과 개수다(중지된 실행에 싣는다).
	stopped         *diagnosis.IntentOutcomes
	operations      sync.WaitGroup
	ownedTask       string
	operationCancel context.CancelFunc
	joinDone        chan struct{}
	// timeCloseAt·timeDrain은 회차 실행 시간 상한의 마감 시작 시각과 진행 중 작업 마무리 유예다(SetTimeBudget).
	// 0이면 시간 마감을 하지 않는다(엔진 자체 기한과 실행 문맥만 남음).
	timeCloseAt time.Time
	timeDrain   time.Duration
}

// FirstDiagnosisTimeBudgetDrain은 시간 상한 마감 때 진행 중 작업을 기다리는 유예의 상한이다. 엔진 원래 정리 순서의
// 유예(settleDrainGrace)와 같다. 호출자는 회차 실행 시간 안에 마감이 끝나도록 이보다 짧게 줄 수 있다.
const FirstDiagnosisTimeBudgetDrain = settleDrainGrace

// SetTimeBudget은 회차 실행 시간 상한의 마감을 정한다(실행 결과 상태 값 계약 §3.4). closeAt부터 새 계획·새 intent를
// 시작하지 않고(budget_limited), 진행 중 작업은 drain 동안 마무리한 뒤 끝나지 않았으면 멈춘다. 진행 중 작업이 끝나면
// 실행을 budget_limited로 마감한다. Run 전에 한 번만 부른다.
func (s *FirstDiagnosisSupervisor) SetTimeBudget(closeAt time.Time, drain time.Duration) error {
	if s == nil || closeAt.IsZero() || drain < 0 || drain > FirstDiagnosisTimeBudgetDrain {
		return ErrFirstDiagnosisSupervisor
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.closed || s.running || s.runStarted || !s.timeCloseAt.IsZero() {
		return ErrFirstDiagnosisSupervisor
	}
	s.timeCloseAt, s.timeDrain = closeAt, drain
	return nil
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

func (s *FirstDiagnosisSupervisor) PrepareIntents(ctx context.Context, taskID string) (resultErr error) {
	started := time.Now()
	preparationStage := "task_binding"
	var observedContextErr error
	defer func() {
		loggedErr := resultErr
		if observedContextErr != nil {
			loggedErr = observedContextErr
		}
		reason := "planner_failed"
		if preparationStage == "task_binding" {
			reason = "task_binding_failed"
		}
		if preparationStage == "model_profile" {
			reason = "model_profile_invalid"
		}
		diagnosisStage(ctx, mustFirstDiagnosisTaskNumber(taskID), preparationStage, loggedErr, reason, started)
	}()
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
	defer captureDiagnosisContextError(prepare, &observedContextErr)
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
	preparationStage = "model_profile"
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
	preparationStage = "planning"
	planning := server.firstDiagnosisPlanningContext(server.engine.withRuntimeHooks(prepare))
	emit := func(activity db.Activity) { server.engine.emitActivity(task, activity) }
	// 분해가 실패해도 계속하므로(C11), 분해 중 task 상태가 바뀌었는지 계획 전에 다시 본다.
	if err := server.createFirstDiagnosisGoals(planning, task, emit); err != nil || planning.Err() != nil || s.checkPreparationTask(planning, task) != nil {
		return ErrFirstDiagnosisPreparation
	}
	lastIntent, observed := s.plannerLogStart(planning, task)
	planStarted := time.Now()
	server.engine.BeginLLMCall(task.ID)
	_, _, planErr := planner.Plan(planning, mustFirstDiagnosisTaskNumber(task.ID), server.m.Assets(), task.Store, task.Goal, nil, emit)
	server.engine.EndLLMCall(task.ID)
	server.engine.plannerLogFinish(planning, task, lastIntent, observed, planErr, planStarted)
	// 첫 계획에서 intent가 0개여도 실패가 아니다. 실행 단계의 Planner가 다시 계획하고, 그래도 없으면 no_intents로
	// 마감한다(C11 결정 2). 저장소에서 못 읽은 경우만 실패다.
	_, readErr := task.Store.HasActiveIntent()
	if readErr != nil {
		_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "planner", Event: "stage_finished", TaskID: mustFirstDiagnosisTaskNumber(taskID), Stage: "planning", Status: "failed", ReasonCode: "intent_storage_failed"})
	}
	if planErr != nil || planning.Err() != nil || readErr != nil || s.checkPreparationTask(planning, task) != nil || server.checkFirstDiagnosisModel(planning, task) != nil {
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
	var observedRunErr error
	runStartedAt := time.Now()
	stageStartedAt := runStartedAt
	setStage := func(next string) {
		diagnosisStage(ctx, mustFirstDiagnosisTaskNumber(taskID), diagnosisLogStage(stage), nil, "execution_failed", stageStartedAt)
		stage, stageStartedAt = next, time.Now()
		_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "supervisor", Event: "stage_started", TaskID: mustFirstDiagnosisTaskNumber(taskID), Stage: diagnosisLogStage(stage), Status: "started"})
	}
	s.running = true
	s.runStarted = true
	s.permit = taskID
	s.runTask = taskID
	s.runIntent = intentID
	s.registered = snapshotRef != ""
	timeCloseAt, timeDrain := s.timeCloseAt, s.timeDrain
	s.operations.Add(1)
	s.mu.Unlock()
	_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "supervisor", Event: "run_started", TaskID: mustFirstDiagnosisTaskNumber(taskID), IntentID: intentID, Stage: "admission", Status: "started"})
	defer func() {
		loggedErr := err
		if observedRunErr != nil {
			loggedErr = observedRunErr
		}
		diagnosisStage(ctx, mustFirstDiagnosisTaskNumber(taskID), diagnosisLogStage(stage), loggedErr, diagnosisLogStageReason(stage), stageStartedAt)
		status, reason := diagnosisLogOutcome(ctx, loggedErr, diagnosisLogStageReason(stage))
		_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "supervisor", Event: "run_finished", TaskID: mustFirstDiagnosisTaskNumber(taskID), IntentID: intentID, Stage: diagnosisLogStage(stage), Status: status, ReasonCode: reason, DurationMS: time.Since(runStartedAt).Milliseconds()})
		if err != nil {
			log.Printf("[first-diagnosis] task %s 단계 %s 실패", taskID, stage)
		}
		s.mu.Lock()
		s.running = false
		s.admissionGranted = false
		s.operationCancel = nil
		s.permit = ""
		s.runErr = err
		s.runLogErr = loggedErr
		s.completed = err == nil
		s.mu.Unlock()
		s.operations.Done()
	}()
	run, cancel := firstDiagnosisRunContext(ctx)
	s.trackCancel(cancel)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	setStage("task")
	task, err := s.task(run, taskID, false)
	if err != nil {
		return err
	}
	number := mustFirstDiagnosisTaskNumber(taskID)
	if logger := diagnosticlog.From(ctx); logger != nil {
		_ = logger.BindTask(number)
		if snapshotRef == "" {
			state := server.requestApprovals.runState(number)
			if state.RunRef != "" {
				_ = logger.BindRun(state.RunRef)
			}
		}
	}
	if snapshotRef == "" {
		server.requestApprovals.runStarted(number)
	}
	// 획득 전 중지도 동일한 감독자 마감 경로를 거친다.
	defer func() {
		if snapshotRef != "" || !server.requestApprovals.runFinished(number, err, stage == "audit-close" || stage == "drain") {
			return
		}
		stage, stageStartedAt = "user-stop", time.Now()
		s.mu.Lock()
		s.admissionGranted = false
		s.mu.Unlock()
		cleanup, finish := context.WithTimeout(context.WithoutCancel(run), 30*time.Second)
		defer finish()
		drainErr := s.drainTask(cleanup, server, task)
		closeErr := server.engine.e2HTTP.CloseTask(cleanup, taskID)
		server.requestApprovals.finishStop(number, errors.Join(drainErr, closeErr))
		err = firstDiagnosisFailure(errors.Join(err, context.Canceled, drainErr, closeErr))
	}()
	if snapshotRef == "" && server.requestApprovals.userStopped(number) {
		return firstDiagnosisFailure(context.Canceled)
	}
	setStage("quiescence")
	if err = s.CheckTaskQuiescent(run, number); err != nil {
		return err
	}
	setStage("model")
	if err := server.bindFirstDiagnosisModel(run, task); err != nil {
		return firstDiagnosisFailure(err)
	}
	setStage("intent-binding")
	var node *db.Node
	if intentID > 0 {
		node, err = task.Store.GetNode(intentID)
		if err != nil || node == nil || node.Kind != db.KindIntent || node.State != "open" {
			return ErrFirstDiagnosisSupervisor
		}
	}
	setStage("e2-policy")
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
	setStage("admission")
	queued, admissionErr := server.admitPausedTask(task)
	if admissionErr != nil || queued {
		return ErrFirstDiagnosisSupervisor
	}
	setStage("execution")
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var timeClosing, timeStopped bool
	var drainUntil time.Time
	for {
		if !registered && server.requestApprovals.userStopped(number) {
			return firstDiagnosisFailure(context.Canceled)
		}
		if run.Err() != nil {
			server.engine.Pause(taskID, agent.AbortPausedRaceGuard)
			return firstDiagnosisFailure(run.Err())
		}
		if !registered && !timeCloseAt.IsZero() {
			now := time.Now()
			if !timeClosing && !now.Before(timeCloseAt) {
				// 시간 상한(계약 §3.4): 엔진의 원래 정리 순서를 켜서 새 계획·새 intent를 시작하지 않고, 실행 상한으로
				// 표시해 진행 중 작업이 끝나면 budget_limited로 마감한다. 엔진 자체 기한의 정리는 이미 켜져 있으면 하지 않는다.
				timeClosing, drainUntil = true, now.Add(timeDrain)
				server.engine.markSettling(taskID)
				task.firstDiagnosisBudgetLimited.Store(true)
				_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "supervisor", Event: "stage_started", TaskID: mustFirstDiagnosisTaskNumber(taskID), Stage: "drain", Status: "started", ReasonCode: "time_budget_reached"})
			}
			if timeClosing && !timeStopped && !now.Before(drainUntil) && server.engine.inflightCount(taskID) > 0 {
				// 유예 안에 끝나지 않은 작업은 엔진 정리 순서의 마지막 단계처럼 멈춘다. 그 intent는 exhausted(미완료)로 남는다.
				timeStopped = true
				server.engine.cancelExec(taskID, agent.AbortSettleDrainTimeout)
			}
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
			observedRunErr = diagnosisRunObservedError(ErrFirstDiagnosisSupervisor, state.Status, intentState)
			return ErrFirstDiagnosisSupervisor
		}
		terminal := registered && intentState == "done"
		if !registered && server.engine.inflightCount(taskID) == 0 && server.engine.ActiveLLMCalls(taskID) == 0 {
			var completionErr error
			terminal, _, completionErr = s.generalCompletion(run, server, task)
			if completionErr != nil {
				return completionErr
			}
		}
		if terminal && server.engine.inflightCount(taskID) == 0 && server.engine.ActiveLLMCalls(taskID) == 0 {
			s.mu.Lock()
			s.admissionGranted = false
			s.mu.Unlock()
			if !registered {
				setStage("fence")
				if err = server.engine.e2HTTP.FenceTask(taskID); err != nil {
					return firstDiagnosisFailure(err)
				}
			}
			setStage("drain")
			if err = s.drainTask(run, server, task); err != nil {
				return err
			}
			if registered {
				setStage("registered-result")
				if err = s.checkRegisteredResult(run, task, intentID); err != nil {
					return err
				}
				// 등록 경로는 intent 하나가 done이어야 마감된다.
				s.setRunResult(diagnosis.RunResult{Outcome: diagnosis.OutcomeComplete, IntentOutcomes: diagnosis.IntentOutcomes{Done: 1}})
			} else {
				ready, result, completionErr := s.generalCompletion(run, server, task)
				if completionErr != nil || !ready {
					return ErrFirstDiagnosisSupervisor
				}
				setStage("audit-close")
				if err = server.engine.e2HTTP.CloseTask(run, taskID); err != nil {
					return firstDiagnosisFailure(err)
				}
				s.setRunResult(result)
			}
			return nil
		}
		select {
		case <-run.Done():
		case <-ticker.C:
		}
	}
}

// firstDiagnosisProgramCall은 호출이 서버가 연 호출(인증 준비 등)인지와, 그렇다면 마감해도 되는지(최종·감사 완료이고
// Worker 신뢰 결과가 붙지 않음) 본다.
func firstDiagnosisProgramCall(call e2proxy.CallResult, program map[string]bool, observed map[string]e2proxy.CallResult) (settled, isProgram bool) {
	if !program[call.CallRef] {
		return false, false
	}
	return call.Final && call.AuditComplete && observed[call.CallRef].CallRef == "", true
}

// 일반 HTTP의 마감 판정이다. ready는 감사가 닫힌 실행 마감을 할 수 있다는 뜻이며 목표 충족이나 E③ 판정이 아니다.
// intent 결과는 실행 결과 상태 값 계약 §2로 센다. incomplete·stopped·unknown intent는 실패가 아니라 부분 완료다(C08).
// 결과 불명 송신은 그 intent만 멈추므로(C01) 결과 불명 intent도 부분 완료로 센다(계약 §6.3). 단 결과 불명 intent
// 수는 E②가 결과 불명 송신을 기록한 intent 수와 같아야 한다.
// 실행 상한(Planner 라운드·도구 호출·승인 모델 예산)에 닿았으면 실행 중 intent가 끝나는 대로 마감하고, 시작하지
// 못한 intent는 incomplete로 센다(closing_reason budget_limited).
func (s *FirstDiagnosisSupervisor) generalCompletion(ctx context.Context, server *Server, task *Task) (bool, diagnosis.RunResult, error) {
	var none diagnosis.RunResult
	if flow := server.engine.e2HTTP.options.Workflow; flow != nil && flow.Failed() {
		return false, none, ErrFirstDiagnosisOutcome
	}
	counts, err := firstDiagnosisIntentOutcomes(ctx, s.m.pg.DB, task.ExpID)
	outcomes := counts.outcomes
	if err != nil {
		return false, none, ErrFirstDiagnosisSupervisor
	}
	budgetLimited := firstDiagnosisBudgetLimited(task, server.firstModelBudget)
	// 실행 상한이나 목표 달성이면 Planner를 기다리지 않고, 실행 중 intent가 끝나는 대로 마감한다. 마감 사유
	// budget_limited는 실행 상한일 때만 붙는다.
	outcomes, closing := firstDiagnosisClosingOutcomes(counts, firstDiagnosisForcedClose(budgetLimited, task), firstDiagnosisPlanned(task))
	if !closing {
		return false, none, nil
	}
	// 탐색 원장이 예산을 실제 소진하고 진단 권위를 획득하지 않은 종료만 별도로 마감한다.
	// DeepRuns/receipt/Execution을 합성하지 않으며 기존 진단의 E②·감사 검증은 아래 그대로 둔다.
	if result, ok := server.engine.e2HTTP.explorationBudgetOutcome(task, server.firstModelBudget, outcomes); ok {
		return true, result, nil
	}
	if flow := server.engine.e2HTTP.options.Workflow; flow != nil && !flow.DeepReady() {
		return false, none, ErrFirstDiagnosisOutcome
	}
	settled := outcomes.Done + outcomes.PolicyLimited + outcomes.Incomplete + outcomes.Unknown + outcomes.Stopped
	if settled == 0 {
		// 계획을 거쳤는데 intent가 하나도 없다. 실패가 아니라 no_intents로 마감한다(C11 결정 2). 작업이 없으므로
		// 대조할 E② 호출도 없어야 한다.
		if execution, ok := server.engine.e2HTTP.Execution(task.ID); ok && execution != nil && len(execution.Calls()) != 0 {
			return false, none, ErrFirstDiagnosisSupervisor
		}
		result := diagnosis.RunResult{Outcome: diagnosis.OutcomeNoIntents}
		if budgetLimited {
			result.ClosingReason = diagnosis.ClosingBudgetLimited
		}
		return true, result, nil
	}
	execution, ok := server.engine.e2HTTP.Execution(task.ID)
	if !ok || execution == nil {
		return false, none, ErrFirstDiagnosisSupervisor
	}
	calls := execution.Calls()
	// 끝낸(done)·정책 제한 intent가 있으면 실제 E② 호출이 있어야 한다. 모두 불완전·중지면 호출이 없을 수 있다.
	if len(calls) == 0 && outcomes.Done+outcomes.PolicyLimited > 0 {
		return false, none, ErrFirstDiagnosisSupervisor
	}
	results := server.engine.e2HTTP.Results(task.ID)
	// 신뢰 결과가 같은 호출의 E② 기록과 맞지 않았던 intent다(C09). 그 호출에는 저장된 신뢰 결과가 없다.
	untrusted := server.engine.e2HTTP.UntrustedIntents(task.ID)
	if results == nil && len(calls) != 0 {
		return false, none, ErrFirstDiagnosisSupervisor
	}
	observed := make(map[string]e2proxy.CallResult, len(results))
	limited := make(map[int64]map[string]bool)
	for _, result := range results {
		validToolOutcome := firstDiagnosisClosedToolOutcome(result)
		if !result.Final || !result.Call.Final || !result.Call.AuditComplete || !result.ProcessDrained || !result.ProxyDrained || !validToolOutcome || result.Execution.Scope.TaskRef != task.ID || observed[result.Call.CallRef].CallRef != "" {
			return false, none, ErrFirstDiagnosisSupervisor
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
	// 결과 불명 송신을 E②가 기록한 intent와 신뢰 결과 대조가 깨진 intent다. 그 intent는 결과 불명으로 끝나야 하고
	// (다시 보내지 않음, C01·C09), 결과 불명 intent는 모두 여기 있어야 한다.
	unknownIntents := make(map[int64]bool)
	for id := range untrusted {
		intent, err := task.Store.GetNode(id)
		if err != nil || intent == nil || intent.Kind != db.KindIntent || firstDiagnosisIntentOutcome(intent.State, intent.BlockedReason) != intentOutcomeUnknown || intent.BlockedReason != "e2_http_result_unknown" {
			return false, none, ErrFirstDiagnosisSupervisor
		}
		unknownIntents[id] = true
	}
	// 서버가 연 호출(인증 준비 등)은 Worker Bash 결과가 없다. E② 기록이 최종·감사 완료인지만 보고 intent 결과 대조에서
	// 뺀다(C14: 시험 계정을 결속한 실행이 마감 대조에서 늘 실패하던 것, 11/13 S07 실제 회차).
	program := server.engine.e2HTTP.ProgramCalls(task.ID)
	programCalls := 0
	missing := 0
	for _, call := range calls {
		if settled, isProgram := firstDiagnosisProgramCall(call, program, observed); isProgram {
			if !settled {
				return false, none, ErrFirstDiagnosisSupervisor
			}
			programCalls++
			continue
		}
		result, ok := observed[call.CallRef]
		if !call.Final || !call.AuditComplete || ok && !e2proxy.EqualCallResults(result, call) || !ok && !untrusted[call.IntentID] {
			return false, none, ErrFirstDiagnosisSupervisor
		}
		if !ok {
			// 대조가 깨진 호출이다. 그 intent는 위에서 결과 불명인지 확인했다. E② 실행 기록은 위처럼 최종·감사 완료여야 한다.
			missing++
			continue
		}
		intent, err := task.Store.GetNode(call.IntentID)
		if err != nil || intent == nil || intent.Kind != db.KindIntent {
			return false, none, ErrFirstDiagnosisSupervisor
		}
		outcome := firstDiagnosisIntentOutcome(intent.State, intent.BlockedReason)
		if call.Counts.Unknown != 0 {
			if outcome != intentOutcomeUnknown || intent.BlockedReason != "e2_http_result_unknown" {
				return false, none, ErrFirstDiagnosisSupervisor
			}
			unknownIntents[intent.ID] = true
			continue
		}
		switch outcome {
		case intentOutcomeDone, intentOutcomeIncomplete, intentOutcomeStopped:
		case intentOutcomePolicyLimited:
			// 정책 제한 사유는 E②가 실제로 거부·미지원한 호출로 뒷받침돼야 한다.
			if !limited[intent.ID][intent.BlockedReason] {
				return false, none, ErrFirstDiagnosisSupervisor
			}
		case intentOutcomeUnknown:
			// 결과 불명 송신·대조 실패 앞의 호출이다. 결과 불명 근거는 아래에서 intent 단위로 대조한다. 모르는 그래프 상태는 받지 않는다.
			if intent.BlockedReason != "e2_http_result_unknown" {
				return false, none, ErrFirstDiagnosisSupervisor
			}
		default:
			return false, none, ErrFirstDiagnosisSupervisor
		}
	}
	// 저장된 신뢰 결과는 모두 서로 다른 실제 호출이어야 한다.
	if len(results)+missing+programCalls != len(calls) {
		return false, none, ErrFirstDiagnosisSupervisor
	}
	// 결과 불명 intent 수가 E② 근거로 확인한 수와 같아야 한다. 결과 불명 송신 없이 결과 불명으로 표시된 intent(모르는
	// 그래프 상태 포함)는 근거가 없으므로 마감하지 않는다.
	if int64(len(unknownIntents)) != outcomes.Unknown {
		return false, none, ErrFirstDiagnosisSupervisor
	}
	// 정책 제한으로 표시된 intent 수가 E② 근거로 확인한 수와 같아야 한다.
	var matched int64
	for intentID := range limited {
		intent, readErr := task.Store.GetNode(intentID)
		if readErr != nil {
			return false, none, ErrFirstDiagnosisSupervisor
		}
		if intent != nil && firstDiagnosisIntentOutcome(intent.State, intent.BlockedReason) == intentOutcomePolicyLimited && limited[intentID][intent.BlockedReason] {
			matched++
		}
	}
	if matched != outcomes.PolicyLimited {
		return false, none, ErrFirstDiagnosisSupervisor
	}
	result := diagnosis.RunResult{Outcome: outcomes.Outcome(), IntentOutcomes: outcomes}
	if budgetLimited {
		result.ClosingReason = diagnosis.ClosingBudgetLimited
	}
	return true, result, nil
}

// 감사가 닫힌 호출 결과가 마감할 수 있는 분류인지 본다. 정책 제한은 진단 범위의 제한이고, 결과 불명 송신·연결 실패는
// 그 호출과 intent의 결과다(C01·C02). 미완료·알 수 없는 분류는 마감 실패다.
// 실행 전체 누계(Execution.Counts)의 결과 불명은 다른 intent의 것이라 보지 않는다(C03). 이 intent의 앞선 호출에
// 결과 불명이 있었다면 그 intent는 더 보내지 않으므로(C01) 이 호출도 결과 불명이어야 한다.
func firstDiagnosisClosedToolOutcome(result e2runner.TrustedResult) bool {
	c := result.Call.Counts
	if !result.Execution.AuditComplete {
		return false
	}
	if result.Classification == "result_unknown" {
		// 최종 호출의 결과 불명 분류는 결과 불명 송신 수로만 생긴다. 사유는 마지막 요청의 것이다.
		switch result.Call.Reason {
		case e2proxy.Complete, e2proxy.Withheld, e2proxy.PolicyDenied, e2proxy.Unsupported, e2proxy.Unknown, e2proxy.TransportFailed, e2proxy.BudgetExhausted, e2proxy.AttemptLimit:
			return c.Unknown > 0
		}
		return false
	}
	if c.Unknown != 0 || result.Execution.IntentCounts.Unknown != 0 {
		return false
	}
	if result.Classification == "terminated" {
		// 호스트가 명령을 멈췄다(Planner의 kill_work 등). bash가 끝나기 전에 끊겼지만 프로세스·프록시 드레인과 감사는
		// 위·호출자가 확인한다. 결과 불명 송신은 없으므로 그 intent의 미완료다(C27).
		switch result.Call.Reason {
		case e2proxy.Complete, e2proxy.Withheld, e2proxy.PolicyDenied, e2proxy.Unsupported, e2proxy.TransportFailed, e2proxy.BudgetExhausted, e2proxy.AttemptLimit:
			return !result.ProcessBashExited
		}
		return false
	}
	if result.Classification == "transient_failure" && result.Termination == e2runner.TerminationTimeout {
		// 명령 시간 상한에 닿아 호스트가 멈춘 명령이다(계약 열린 항목 2). 결과 불명 송신은 위에서 걸렀고 프로세스·프록시
		// 드레인과 감사는 위·호출자가 확인하므로 그 intent의 미완료다. 프록시 장애(proxy_unavailable)는 집행 장애라
		// 그대로 마감 실패다.
		switch result.Call.Reason {
		case e2proxy.Complete, e2proxy.Withheld, e2proxy.PolicyDenied, e2proxy.Unsupported, e2proxy.TransportFailed, e2proxy.BudgetExhausted, e2proxy.AttemptLimit:
			return true
		}
		return false
	}
	// 아래 분류는 bash가 끝까지 실행돼 종료 코드가 있는 명령에만 붙는다. 예외: 정책 거부·미지원 뒤 명령 시간 상한에 닿은
	// 명령은 실행기가 거부·미지원 분류를 먼저 붙인다(종료 원인 timeout). 그 분류는 종료 코드가 아니라 E② 수로 정해지므로
	// 아래에서 같은 수 대조로 받는다(#578 이동찬 리뷰).
	if !result.ProcessBashExited && !(result.Termination == e2runner.TerminationTimeout && (result.Classification == "policy_not_executed" || result.Classification == "unsupported")) {
		return false
	}
	if result.Classification == "transient_failure" {
		// 대상 연결을 맺지 못한 송신 실패만 받는다. 송신 실패 수가 없는 transient_failure(프록시 장애·명령 시간 초과)는
		// 신뢰 결과에서 원인을 나눌 수 없어 마감 실패로 둔다.
		switch result.Call.Reason {
		case e2proxy.Complete, e2proxy.Withheld, e2proxy.PolicyDenied, e2proxy.Unsupported, e2proxy.TransportFailed, e2proxy.BudgetExhausted, e2proxy.AttemptLimit:
			return c.Transient > 0
		}
		return false
	}
	// 예산 소진·송신 시도 상한은 그 요청만 보내지 않은 거부다(C01 뒤 실행을 멈추지 않음). 마지막 요청의 사유가 이것이면
	// 거부 수가 있어야 하고, 아래 정책 제한 분류로만 받는다.
	requestLimit := result.Call.Reason == e2proxy.BudgetExhausted || result.Call.Reason == e2proxy.AttemptLimit
	// 보류(withheld)는 대상이 답한 응답이고 표현만 주지 않은 것이라 완료와 같이 본다(C16).
	answered := result.Call.Reason == e2proxy.Complete || result.Call.Reason == e2proxy.Withheld
	if requestLimit && c.Denied == 0 || !requestLimit && !answered && result.Call.Reason != e2proxy.PolicyDenied && result.Call.Reason != e2proxy.Unsupported {
		return false
	}
	switch result.Classification {
	case "completed":
		return result.ExitCode == 0 && c.Denied == 0 && c.Unsupported == 0 && answered
	case "tool_failure":
		return result.ExitCode != 0 && c.Denied == 0 && c.Unsupported == 0 && answered
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
		if task != nil {
			s.recordStopped(ctx, task)
		}
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

// firstDiagnosisPlanningContext는 준비 Planner 문맥이다. 준비 서버에는 E② 실행 Runtime이 없어 준비 설정의 Plan당 상한과
// 검색 설정(루프 복원 결정안 §2-1)을 싣는다. 실행 계획은 Runtime.PreparePlanning이 싣는다.
func (s *Server) firstDiagnosisPlanningContext(ctx context.Context) context.Context {
	return agent.WithE2HTTPWebSearch(agent.WithE2HTTPPlannerLimits(agent.WithE2HTTPPlanning(ctx), s.firstPlannerLimits), s.firstWebSearch)
}

// firstDiagnosisRunContext는 감독자 Run 하나의 문맥이다(뒷받침 기한 FirstDiagnosisRunTimeout).
func firstDiagnosisRunContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, FirstDiagnosisRunTimeout)
}
