package server

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

var (
	ErrE2HTTPHost    = errors.New("e2_http_host_unavailable")
	ErrE2HTTPBusy    = errors.New("e2_http_work_active")
	ErrE2HTTPClosure = errors.New("e2_http_host_closure_incomplete")
)

var e2HTTPHostRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// 참조는 승인 출처가 아니다. Acquire는 실제 호스트의 보호 입력과 권위를 검증한다.
type E2HTTPExpectedBinding struct {
	RunRef         string
	ResetRef       string
	PolicyRef      string
	CaseRef        string
	PolicyRevision string
	BudgetRef      string
}

type E2HTTPRequest struct {
	TaskID        string
	TaskIDNumber  int64
	ExplorationID int64
	IntentID      int64
}

// Release는 성공한 프로세스/감사 drain 뒤 공급자가 가진 자원을 해제한다.
// 오류의 의미가 불명일 수 있어 Runtime은 Release를 자동 재실행하지 않는다.
type E2HTTPRunOptions struct {
	Execution     *e2proxy.Execution
	ObserveResult func(context.Context, e2runner.Result) error
	Runner        *e2runner.Runner
	Release       func(context.Context) error
}

type E2HTTPPolicyMetadata struct {
	PolicyFingerprintSHA256 string
	PolicyConfigSHA256      string
	HeaderMode              e2proxy.HeaderMode
	Evaluation              *e2proxy.EvaluationCallerContext
}

func (E2HTTPPolicyMetadata) String() string   { return "server.E2HTTPPolicyMetadata[protected]" }
func (E2HTTPPolicyMetadata) GoString() string { return "server.E2HTTPPolicyMetadata[protected]" }

func e2HTTPCloneEvaluation(value *e2proxy.EvaluationCallerContext) *e2proxy.EvaluationCallerContext {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

type E2HTTPRuntimeOptions struct {
	RequestApprovals        *RequestApprovalService
	PolicyFingerprintSHA256 string
	PolicyConfigSHA256      string
	HeaderMode              e2proxy.HeaderMode
	Evaluation              *e2proxy.EvaluationCallerContext
	AuthorityContext        context.Context
	CheckAuthority          func(context.Context) error
	AuthorizeIntent         func(context.Context, E2HTTPRequest) error
	Expected                E2HTTPExpectedBinding
	Acquire                 func(context.Context, E2HTTPRequest) (E2HTTPRunOptions, error)
	MaxWorkerTurns          int
	MaxToolCalls            int
	WorkerTimeout           time.Duration
	MaxPlannerRounds        uint64
	MaxIntentRuns           uint64
	DrainTimeout            time.Duration
}

func (E2HTTPRunOptions) String() string       { return "server.E2HTTPRunOptions[protected]" }
func (E2HTTPRunOptions) GoString() string     { return "server.E2HTTPRunOptions[protected]" }
func (E2HTTPRuntimeOptions) String() string   { return "server.E2HTTPRuntimeOptions[protected]" }
func (E2HTTPRuntimeOptions) GoString() string { return "server.E2HTTPRuntimeOptions[protected]" }

// E2HTTPRuntime은 일반 HTTP 전용이다. 등록 GET의 Config/마커/판정 권위를 빌리지 않는다.
// 한 Runtime에는 한 실제 task만 등록하며 닫힌 task를 새 권위로 재사용하지 않는다.
type E2HTTPRuntime struct {
	ctx     context.Context
	cancel  context.CancelFunc
	options E2HTTPRuntimeOptions
	mu      sync.Mutex
	closed  bool
	task    *e2HTTPRuntimeTask
}

type e2HTTPRuntimeTask struct {
	id            string
	number        int64
	exploration   int64
	store         *db.ExplorationStore
	ctx           context.Context
	cancel        context.CancelFunc
	gate          chan struct{}
	closeGate     chan struct{}
	mu            sync.Mutex
	closed        bool
	blocked       bool
	plannerRounds uint64
	runs          map[int64]uint64
	active        map[int64]chan struct{}
	lease         *E2HTTPRunOptions
	validLease    bool
	control       *agent.E2HTTPControl
	results       []e2runner.TrustedResult
	resultsFailed bool
	releaseCalled bool
	releaseErr    error
}

func NewE2HTTPRuntime(parent context.Context, options E2HTTPRuntimeOptions) (*E2HTTPRuntime, error) {
	if parent == nil || parent.Err() != nil || options.Acquire == nil || options.MaxWorkerTurns <= 0 || options.MaxWorkerTurns > 1000 || options.MaxToolCalls <= 0 || options.MaxToolCalls > agent.E2HTTPMaxTaskCalls || options.WorkerTimeout <= 0 || options.WorkerTimeout > time.Hour || options.MaxPlannerRounds == 0 || options.MaxPlannerRounds > 100 || options.MaxIntentRuns == 0 || options.MaxIntentRuns > 10 || options.DrainTimeout <= 0 || options.DrainTimeout > 2*time.Minute {
		return nil, ErrE2HTTPHost
	}
	for _, ref := range []string{options.Expected.RunRef, options.Expected.ResetRef, options.Expected.PolicyRef, options.Expected.CaseRef, options.Expected.PolicyRevision, options.Expected.BudgetRef} {
		if !e2HTTPHostRef.MatchString(ref) {
			return nil, ErrE2HTTPHost
		}
	}
	if options.AuthorityContext != nil {
		if options.AuthorityContext.Err() != nil {
			return nil, ErrE2HTTPHost
		}
		parent = e2HTTPTaskContext(parent, options.AuthorityContext)
	}
	options.Evaluation = e2HTTPCloneEvaluation(options.Evaluation)
	ctx, cancel := context.WithCancel(parent)
	return &E2HTTPRuntime{ctx: ctx, cancel: cancel, options: options}, nil
}

func e2HTTPTaskIdentity(task *Task) (int64, error) {
	if task == nil || task.Store == nil || task.ExpID <= 0 || task.Store.ID() != task.ExpID {
		return 0, ErrE2HTTPHost
	}
	id, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != task.ID {
		return 0, ErrE2HTTPHost
	}
	return id, nil
}

// AdmitTask는 실제 Manager task가 생성된 뒤, launch 전에 한 번 실행한다.
// 호출자의 짧은 HTTP context를 실행의 권위 수명으로 저장하지 않는다.
func (r *E2HTTPRuntime) AdmitTask(ctx context.Context, task *Task) error {
	number, err := e2HTTPTaskIdentity(task)
	if err != nil || r == nil || ctx == nil || ctx.Err() != nil {
		return ErrE2HTTPHost
	}
	if r.options.CheckAuthority != nil && r.options.CheckAuthority(ctx) != nil {
		return ErrE2HTTPHost
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.ctx.Err() != nil {
		return ErrE2HTTPHost
	}
	if r.task != nil {
		if r.task.id != task.ID || r.task.number != number || r.task.exploration != task.ExpID || r.task.store != task.Store {
			return ErrE2HTTPHost
		}
		r.task.mu.Lock()
		closed := r.task.closed || r.task.blocked || r.task.ctx.Err() != nil
		r.task.mu.Unlock()
		if closed {
			return ErrE2HTTPHost
		}
		return nil
	}
	control, err := agent.NewE2HTTPControl(task.ID, r.options.MaxToolCalls)
	if err != nil {
		return ErrE2HTTPHost
	}
	taskctx, cancel := context.WithCancel(r.ctx)
	if r.options.RequestApprovals != nil {
		if err := r.options.RequestApprovals.admitRun(taskctx, number, r.options.Expected.RunRef, cancel); err != nil {
			cancel()
			return err
		}
	}
	r.task = &e2HTTPRuntimeTask{id: task.ID, number: number, exploration: task.ExpID, store: task.Store, ctx: taskctx, cancel: cancel, gate: make(chan struct{}, 1), closeGate: make(chan struct{}, 1), runs: make(map[int64]uint64), active: make(map[int64]chan struct{}), control: control}
	return nil
}

func (r *E2HTTPRuntime) taskFor(task *Task) (*e2HTTPRuntimeTask, error) {
	if r == nil {
		return nil, ErrE2HTTPHost
	}
	number, err := e2HTTPTaskIdentity(task)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	t, closed := r.task, r.closed
	r.mu.Unlock()
	if closed || r.ctx.Err() != nil || t == nil || t.id != task.ID || t.number != number || t.exploration != task.ExpID || t.store != task.Store {
		return nil, ErrE2HTTPHost
	}
	return t, nil
}

// task 취소는 Planner/Worker에 전달한다. 등록 조회의 trust context는 사용하지 않는다.
func e2HTTPTaskContext(ctx context.Context, taskctx context.Context) context.Context {
	bound, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(taskctx, cancel)
	context.AfterFunc(bound, func() { stop() })
	return bound
}

func (r *E2HTTPRuntime) PreparePlanning(ctx context.Context, task *Task) (context.Context, error) {
	t, err := r.taskFor(task)
	if err != nil || ctx == nil || ctx.Err() != nil {
		return ctx, ErrE2HTTPHost
	}
	if r.options.CheckAuthority != nil && r.options.CheckAuthority(ctx) != nil {
		return ctx, ErrE2HTTPHost
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.blocked || t.ctx.Err() != nil || t.plannerRounds >= r.options.MaxPlannerRounds || t.control.Snapshot().Calls >= r.options.MaxToolCalls {
		return ctx, ErrE2HTTPHost
	}
	if len(t.active) != 0 {
		return ctx, ErrE2HTTPBusy
	}
	if t.lease != nil {
		if !t.validLease || !r.validLease(t, *t.lease, 0) {
			return ctx, ErrE2HTTPHost
		}
	}
	t.plannerRounds++
	// 실제 intent가 생기기 전 Planner의 Bash/probe는 허용하지 않는다.
	return agent.WithE2HTTPPlanning(e2HTTPTaskContext(ctx, t.ctx)), nil
}

func (r *E2HTTPRuntime) validLease(t *e2HTTPRuntimeTask, lease E2HTTPRunOptions, initialIntent int64) bool {
	if lease.Runner == nil || lease.Release == nil {
		return false
	}
	execution := lease.Runner.Execution()
	if execution == nil || lease.Execution != nil && lease.Execution != execution {
		return false
	}
	view := execution.Snapshot()
	expected := r.options.Expected
	if view.Version != e2proxy.Version || view.State != "active" || !view.AuditComplete || view.Scope.TaskRef != t.id || view.Scope.RunRef != expected.RunRef || view.Scope.ResetRef != expected.ResetRef || view.Scope.PolicyRef != expected.PolicyRef || view.Scope.CaseRef != expected.CaseRef || view.PolicyRevision != expected.PolicyRevision || initialIntent > 0 && (view.IntentID != initialIntent || !execution.HasIntent(initialIntent)) {
		return false
	}
	policy := execution.Policy()
	remaining, err := execution.BudgetRemaining()
	return policy.BudgetRef == expected.BudgetRef && err == nil && remaining > 0
}

func (r *E2HTTPRuntime) PrepareWork(ctx context.Context, task *Task, intent *db.Node) (context.Context, func() error, error) {
	t, err := r.taskFor(task)
	if err != nil || ctx == nil || ctx.Err() != nil || intent == nil || intent.ID <= 0 || intent.Kind != db.KindIntent {
		return ctx, nil, ErrE2HTTPHost
	}
	select {
	case t.gate <- struct{}{}:
		defer func() { <-t.gate }()
	case <-ctx.Done():
		return ctx, nil, ErrE2HTTPHost
	case <-t.ctx.Done():
		return ctx, nil, ErrE2HTTPHost
	}
	t.mu.Lock()
	if t.closed || t.blocked || t.ctx.Err() != nil || ctx.Err() != nil {
		t.mu.Unlock()
		return ctx, nil, ErrE2HTTPHost
	}
	if _, busy := t.active[intent.ID]; busy {
		t.mu.Unlock()
		return ctx, nil, ErrE2HTTPBusy
	}
	if t.runs[intent.ID] >= r.options.MaxIntentRuns || t.control.Snapshot().Calls >= r.options.MaxToolCalls {
		t.mu.Unlock()
		return ctx, nil, ErrE2HTTPHost
	}
	t.mu.Unlock()
	actual, readErr := t.store.GetNode(intent.ID)
	if readErr != nil || actual == nil || actual.ID != intent.ID || actual.Kind != db.KindIntent || (actual.State != "open" && actual.State != "running") || !bytes.Equal(actual.Payload, intent.Payload) {
		return ctx, nil, ErrE2HTTPHost
	}
	if r.options.AuthorizeIntent != nil && r.options.AuthorizeIntent(ctx, E2HTTPRequest{TaskID: t.id, TaskIDNumber: t.number, ExplorationID: t.exploration, IntentID: actual.ID}) != nil {
		return ctx, nil, ErrE2HTTPHost
	}
	t.mu.Lock()
	lease := t.lease
	t.mu.Unlock()
	if lease == nil {
		acquired, acquireErr := r.options.Acquire(t.ctx, E2HTTPRequest{TaskID: t.id, TaskIDNumber: t.number, ExplorationID: t.exploration, IntentID: actual.ID})
		t.mu.Lock()
		t.lease = &acquired
		lease = t.lease
		if acquireErr != nil || t.closed || t.ctx.Err() != nil || !r.validLease(t, acquired, actual.ID) {
			t.blocked = true
			t.cancel()
			t.mu.Unlock()
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(r.ctx), r.options.DrainTimeout)
			cleanupErr := r.closeTaskResources(cleanupCtx, t)
			cancel()
			return ctx, nil, errors.Join(ErrE2HTTPHost, cleanupErr)
		}
		t.validLease = true
		t.mu.Unlock()
	}
	if !r.validLease(t, *lease, 0) {
		return ctx, nil, ErrE2HTTPHost
	}
	if err := lease.Runner.Execution().RegisterIntent(ctx, actual.ID); err != nil {
		return ctx, nil, ErrE2HTTPHost
	}
	t.mu.Lock()
	if t.closed || t.blocked || t.ctx.Err() != nil || ctx.Err() != nil {
		t.mu.Unlock()
		return ctx, nil, ErrE2HTTPHost
	}
	done := make(chan struct{})
	t.active[actual.ID] = done
	t.runs[actual.ID]++
	t.mu.Unlock()
	workCtx := e2HTTPTaskContext(ctx, t.ctx)
	bound := agent.WithE2HTTPExecution(workCtx, agent.E2HTTPExecution{Runner: lease.Runner, Control: t.control, TaskID: t.number, IntentID: actual.ID, TaskRef: t.id, RunRef: r.options.Expected.RunRef, MaxTurns: r.options.MaxWorkerTurns, MaxDuration: r.options.WorkerTimeout, ObserveResult: func(observeCtx context.Context, result e2runner.Result) error {
		return r.observeResult(observeCtx, t, lease, actual.ID, result)
	}})
	var once sync.Once
	var finalErr error
	finish := func() error {
		once.Do(func() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.options.DrainTimeout)
			finalErr = lease.Runner.DrainIntent(cleanupCtx, actual.ID)
			// 세션 취소는 도구 완료보다 먼저 반환할 수 있다. drain 뒤의 실제 call을 다시 확인한다.
			finalErr = e2HTTPFinalIntentError(finalErr, lease.Runner.Execution().Calls(), actual.ID)
			cancel()
			t.mu.Lock()
			delete(t.active, actual.ID)
			close(done)
			t.mu.Unlock()
		})
		return finalErr
	}
	return bound, finish, nil
}

func (r *E2HTTPRuntime) TaskID() (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.task == nil {
		return "", false
	}
	return r.task.id, true
}

// Execution은 신뢰한 Go 조립/마감 경로의 실제 handle이다. HTTP 참조가 이를 만들지 않는다.
func (r *E2HTTPRuntime) Execution(taskID string) (*e2proxy.Execution, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	t := r.task
	r.mu.Unlock()
	if t == nil || t.id != taskID {
		return nil, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lease == nil || !t.validLease || t.lease.Runner == nil {
		return nil, false
	}
	return t.lease.Runner.Execution(), true
}

func (r *E2HTTPRuntime) FenceTask(taskID string) error {
	if r == nil {
		return ErrE2HTTPHost
	}
	r.mu.Lock()
	t := r.task
	r.mu.Unlock()
	if t == nil || t.id != taskID {
		return ErrE2HTTPHost
	}
	t.mu.Lock()
	t.closed = true
	t.cancel()
	t.mu.Unlock()
	return nil
}

func (r *E2HTTPRuntime) Fence() error {
	if r == nil {
		return ErrE2HTTPHost
	}
	r.mu.Lock()
	r.closed = true
	r.cancel()
	t := r.task
	r.mu.Unlock()
	if t != nil {
		t.mu.Lock()
		t.closed = true
		t.cancel()
		t.mu.Unlock()
	}
	return nil
}

func (r *E2HTTPRuntime) closeTaskResources(ctx context.Context, t *e2HTTPRuntimeTask) error {
	if ctx == nil {
		return ErrE2HTTPClosure
	}
	select {
	case t.closeGate <- struct{}{}:
		defer func() { <-t.closeGate }()
	case <-ctx.Done():
		return ErrE2HTTPClosure
	}
	t.mu.Lock()
	lease := t.lease
	released, releaseErr := t.releaseCalled, t.releaseErr
	t.mu.Unlock()
	if released {
		return releaseErr
	}
	if lease == nil || lease.Runner == nil && lease.Execution == nil && lease.Release == nil {
		return nil
	}
	execution := lease.Execution
	if lease.Runner != nil {
		if execution != nil && execution != lease.Runner.Execution() {
			return ErrE2HTTPClosure
		}
		execution = lease.Runner.Execution()
	}
	var stopErr, drainErr error
	if execution != nil {
		stopErr = execution.Stop(ctx)
	}
	if lease.Runner != nil {
		drainErr = lease.Runner.Close(ctx)
	}
	if stopErr != nil || drainErr != nil {
		return ErrE2HTTPClosure
	}
	t.mu.Lock()
	active := make([]chan struct{}, 0, len(t.active))
	for _, done := range t.active {
		active = append(active, done)
	}
	t.mu.Unlock()
	for _, done := range active {
		select {
		case <-done:
		case <-ctx.Done():
			return ErrE2HTTPClosure
		}
	}
	if ctx.Err() != nil {
		return ErrE2HTTPClosure
	}
	if lease.Release == nil {
		t.mu.Lock()
		t.releaseCalled = true
		t.releaseErr = ErrE2HTTPClosure
		t.mu.Unlock()
		return ErrE2HTTPClosure
	}
	err := lease.Release(ctx)
	t.mu.Lock()
	t.releaseCalled = true
	if err != nil {
		t.releaseErr = ErrE2HTTPClosure
	}
	result := t.releaseErr
	t.mu.Unlock()
	return result
}

func (r *E2HTTPRuntime) CloseTask(ctx context.Context, taskID string) error {
	if err := r.FenceTask(taskID); err != nil {
		return err
	}
	r.mu.Lock()
	t := r.task
	r.mu.Unlock()
	if ctx == nil {
		return ErrE2HTTPClosure
	}
	select {
	case t.gate <- struct{}{}:
		defer func() { <-t.gate }()
	case <-ctx.Done():
		return ErrE2HTTPClosure
	}
	return r.closeTaskResources(ctx, t)
}

func (r *E2HTTPRuntime) Close(ctx context.Context) error {
	if err := r.Fence(); err != nil {
		return err
	}
	taskID, ok := r.TaskID()
	if !ok {
		return nil
	}
	return r.CloseTask(ctx, taskID)
}

// 정적 지문은 실제 권위·예산·intent 수용을 대신하지 않는다.
func (r *E2HTTPRuntime) PolicyMetadata() E2HTTPPolicyMetadata {
	if r == nil {
		return E2HTTPPolicyMetadata{}
	}
	return E2HTTPPolicyMetadata{PolicyFingerprintSHA256: r.options.PolicyFingerprintSHA256, PolicyConfigSHA256: r.options.PolicyConfigSHA256, HeaderMode: r.options.HeaderMode, Evaluation: e2HTTPCloneEvaluation(r.options.Evaluation)}
}

func e2HTTPCloneTrusted(value e2runner.TrustedResult) e2runner.TrustedResult {
	value.Call = e2proxy.CloneCallResult(value.Call)
	value.Execution.IntentIDs = append([]int64(nil), value.Execution.IntentIDs...)
	value.Execution.Evaluation = e2HTTPCloneEvaluation(value.Execution.Evaluation)
	return value
}

// 결과는 agent가 concrete Runner에서 직접 받은 callback으로만 기록한다. 원문 출력은 보관하지 않는다.
func (r *E2HTTPRuntime) observeResult(ctx context.Context, t *e2HTTPRuntimeTask, lease *E2HTTPRunOptions, intentID int64, result e2runner.Result) error {
	result.ToolOutput = ""
	trusted := result.Trusted
	var recordErr error
	if lease == nil || lease.Runner == nil {
		recordErr = ErrE2HTTPHost
	} else if trusted.Call.CallRef != "" {
		execution := lease.Runner.Execution()
		if execution == nil {
			recordErr = ErrE2HTTPHost
		} else {
			view := execution.Snapshot()
			valid := trusted.Version == e2proxy.Version && trusted.Execution.Scope == view.Scope && view.Scope.TaskRef == t.id && trusted.Execution.PolicyRevision == view.PolicyRevision && trusted.Execution.HeaderMode == view.HeaderMode && trusted.Execution.IntentID == intentID && trusted.Call.IntentID == intentID && execution.HasIntent(intentID)
			found := false
			for _, call := range execution.Calls() {
				if e2proxy.EqualCallResults(call, trusted.Call) {
					found = true
					break
				}
			}
			if !valid || !found {
				recordErr = ErrE2HTTPHost
			} else {
				t.mu.Lock()
				if len(t.results) >= r.options.MaxToolCalls {
					recordErr = ErrE2HTTPHost
				}
				for _, prior := range t.results {
					if prior.Call.CallRef == trusted.Call.CallRef {
						recordErr = ErrE2HTTPHost
						break
					}
				}
				if recordErr == nil {
					t.results = append(t.results, e2HTTPCloneTrusted(trusted))
				}
				t.mu.Unlock()
			}
		}
	} else if !e2proxy.EqualCallResults(trusted.Call, e2proxy.CallResult{}) || trusted.Final || trusted.ProcessBashExited || trusted.ProcessDrained || trusted.ProxyDrained {
		recordErr = ErrE2HTTPHost
	}
	var observerErr error
	if lease != nil && lease.ObserveResult != nil {
		observerErr = lease.ObserveResult(ctx, result)
	}
	if recordErr != nil || observerErr != nil {
		t.mu.Lock()
		t.resultsFailed = true
		t.mu.Unlock()
	}
	return errors.Join(recordErr, observerErr)
}

// Results는 실제 call의 신뢰 결과 사본이다. 비최종·실패·불명 분류도 그대로 보존한다.
// observer 결속/보관이 실패하면 nil을 반환하며, 호출자는 실제 Calls와 개수를 대조해야 한다.
func (r *E2HTTPRuntime) Results(taskID string) []e2runner.TrustedResult {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	t := r.task
	r.mu.Unlock()
	if t == nil || t.id != taskID {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.resultsFailed {
		return nil
	}
	out := make([]e2runner.TrustedResult, len(t.results))
	for i, result := range t.results {
		out[i] = e2HTTPCloneTrusted(result)
	}
	return out
}

// 실행 전체 누계가 아니라 현재 intent의 실제 call만 판정한다. 거부·미지원은 불명으로 승격하지 않는다.
func e2HTTPFinalIntentError(drainErr error, calls []e2proxy.CallResult, intentID int64) error {
	for _, call := range calls {
		if call.IntentID == intentID && call.Counts.Unknown > 0 {
			return errors.Join(drainErr, agent.ErrE2HTTPUnknown)
		}
	}
	return drainErr
}
