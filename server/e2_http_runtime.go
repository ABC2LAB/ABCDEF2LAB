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
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

var (
	ErrE2HTTPHost    = errors.New("e2_http_host_unavailable")
	ErrE2HTTPBusy    = errors.New("e2_http_work_active")
	ErrE2HTTPClosure = errors.New("e2_http_host_closure_incomplete")
	// ErrE2HTTPBudgetLimited는 실행 상한(Planner 라운드·도구 호출·intent 재실행)에 닿아 새 작업을 받지 않는다는
	// 뜻이다. 실행 실패가 아니라 마감 사유다(실행 결과 상태 값 계약 §3.2 budget_limited).
	ErrE2HTTPBudgetLimited = errors.New("e2_http_budget_limited")
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
	// ExplorationRequests는 같은 회차의 탐색 경계가 승인 대상으로 전달한 요청 수다. 공급자는 진단 E② 예산을
	// 회차 승인 수 N에서 이 몫을 뺀 값으로만 등록한다. ExplorationUnknown이면 탐색 몫을 확인하지 못한 것이고
	// 진단 예산을 열지 않는다(탐색+진단 ≤ N, P1-E).
	ExplorationRequests uint64
	ExplorationUnknown  bool
}

// Release는 성공한 프로세스/감사 drain 뒤 공급자가 가진 자원을 해제한다.
// 오류의 의미가 불명일 수 있어 Runtime은 Release를 자동 재실행하지 않는다.
// E2HTTPLeaseRunner는 런타임이 회차 실행 자원을 쓰는 경계다. 로컬 Runner와 원격 E²가 구현한다.
type E2HTTPLeaseRunner interface {
	agent.E2HTTPRunner
	Snapshot() e2proxy.ExecutionView
	Policy() e2proxy.PolicyView
	BudgetRemaining() (uint64, error)
	RegisterIntent(context.Context, int64) error
	Calls() []e2proxy.CallResult
	Stop(context.Context) error
	DrainIntent(context.Context, int64) error
	Close(context.Context) error
}

// localE2HTTPRun은 같은 프로세스의 Runner를 경계에 맞춘다.
type localE2HTTPRun struct{ runner *e2runner.Runner }

// nil이면 nil 인터페이스를 돌려준다. 실패 경로가 만든 빈 lease를 있는 것처럼 보이게 하면 안 된다.
func newLocalE2HTTPRun(runner *e2runner.Runner) E2HTTPLeaseRunner {
	if runner == nil || runner.Execution() == nil {
		return nil
	}
	return localE2HTTPRun{runner: runner}
}

func (r localE2HTTPRun) SnapshotForIntent(id int64) e2proxy.ExecutionView {
	return r.runner.Execution().SnapshotForIntent(id)
}
func (r localE2HTTPRun) HasIntent(id int64) bool { return r.runner.Execution().HasIntent(id) }
func (r localE2HTTPRun) RedactOutput(data []byte) []byte {
	return r.runner.Execution().RedactOutput(data)
}
func (r localE2HTTPRun) RunForIntent(ctx context.Context, id int64, command string) (e2runner.Result, error) {
	return r.runner.RunForIntent(ctx, id, command)
}
func (r localE2HTTPRun) Snapshot() e2proxy.ExecutionView { return r.runner.Execution().Snapshot() }
func (r localE2HTTPRun) Policy() e2proxy.PolicyView      { return r.runner.Execution().Policy() }
func (r localE2HTTPRun) BudgetRemaining() (uint64, error) {
	return r.runner.Execution().BudgetRemaining()
}
func (r localE2HTTPRun) RegisterIntent(ctx context.Context, id int64) error {
	return r.runner.Execution().RegisterIntent(ctx, id)
}
func (r localE2HTTPRun) Calls() []e2proxy.CallResult { return r.runner.Execution().Calls() }
func (r localE2HTTPRun) Stop(ctx context.Context) error {
	return r.runner.Execution().Stop(ctx)
}
func (r localE2HTTPRun) DrainIntent(ctx context.Context, id int64) error {
	return r.runner.DrainIntent(ctx, id)
}
func (r localE2HTTPRun) Close(ctx context.Context) error { return r.runner.Close(ctx) }

type E2HTTPRunOptions struct {
	Execution     *e2proxy.Execution
	ObserveResult func(context.Context, e2runner.Result) error
	Runner        E2HTTPLeaseRunner
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

// ExplorationCharge는 탐색 단계가 회차 대상 요청 예산에서 쓴 몫이다. 경계를 열지 않았거나 경계가 마감 상태를
// 확정했을 때만 Known이다. 영값은 확인하지 못한 몫이라 진단 예산을 열지 않는다(P1-E).
type ExplorationCharge struct {
	Requests uint64
	Known    bool
}

// E2HTTPExplorationStage는 호스트가 준비한 별도 탐색 단계다. 진단 runner나 토큰을 받지 않는다.
type E2HTTPExplorationStage func(context.Context, *Task) (ExplorationCharge, error)

type E2HTTPRuntimeOptions struct {
	Exploration E2HTTPExplorationStage
	// RunRequests는 회차 승인 요청 수 N이다. 탐색 단계가 있으면 탐색 몫을 뺀 나머지만 진단에 쓴다.
	// 0이면 탐색 몫을 나눌 근거가 없어 탐색 뒤 진단 예산을 열지 않는다.
	RunRequests             uint64
	Workflow                *diagnosisflow.Controller
	PromptProjection        bool
	RequestApprovals        *RequestApprovalService
	PolicyFingerprintSHA256 string
	PolicyConfigSHA256      string
	HeaderMode              e2proxy.HeaderMode
	Evaluation              *e2proxy.EvaluationCallerContext
	AuthorityContext        context.Context
	CheckAuthority          func(context.Context) error
	AuthorizeIntent         func(context.Context, E2HTTPRequest) error
	Expected                E2HTTPExpectedBinding
	ResetBoundRemotely      bool
	Acquire                 func(context.Context, E2HTTPRequest) (E2HTTPRunOptions, error)
	MaxWorkerTurns          int
	MaxToolCalls            int
	WorkerTimeout           time.Duration
	MaxPlannerRounds        uint64
	MaxIntentRuns           uint64
	DrainTimeout            time.Duration
	// ApprovedOrigins는 신뢰한 E② 정책 규칙의 origin이다. 진단 Worker의 insert_assets가 이 안의 자산만 받는다(C24).
	// 비어 있으면(원격 평가 실행 등) insert_assets를 붙이지 않는다.
	ApprovedOrigins []string
	// PlannerLimits는 Planner 한 번(Plan)의 턴·시간 상한이다(C26·C32). 0인 칸은 agent 기본값(16턴·2분)이다.
	// E② 정책 밖 값이라 정책 지문에 들어가지 않고, 제품 설치는 launch 설정(설치 기록 해시)에 묶는다.
	PlannerLimits agent.E2HTTPPlannerLimits
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
	id                string
	number            int64
	exploration       int64
	store             *db.ExplorationStore
	ctx               context.Context
	cancel            context.CancelFunc
	gate              chan struct{}
	closeGate         chan struct{}
	mu                sync.Mutex
	closed            bool
	blocked           bool
	plannerRounds     uint64
	explorationDone   bool
	explorationErr    error
	explorationCharge ExplorationCharge
	explorationBudget *explorationBudgetClosure
	runs              map[int64]uint64
	active            map[int64]chan struct{}
	lease             *E2HTTPRunOptions
	validLease        bool
	control           *agent.E2HTTPControl
	results           []e2runner.TrustedResult
	resultsFailed     bool
	// untrusted는 최종 신뢰 결과가 같은 호출의 E② 실행 기록과 내용이 달랐던 intent다(C09). 그 호출의 결과는
	// 저장하지 않고, intent는 결과 불명으로 끝나야 마감된다. mu로 보호한다.
	untrusted     map[int64]bool
	releaseCalled bool
	releaseErr    error

	promptFailed     bool // mu로 보호한다. 나머지 등록 상태는 gate 안에서만 접근한다.
	promptRegistered bool
	promptOwned      bool
}

func NewE2HTTPRuntime(parent context.Context, options E2HTTPRuntimeOptions) (*E2HTTPRuntime, error) {
	if parent == nil || parent.Err() != nil || options.Acquire == nil || options.MaxWorkerTurns <= 0 || options.MaxWorkerTurns > 1000 || options.MaxToolCalls <= 0 || options.MaxToolCalls > agent.E2HTTPMaxTaskCalls || options.WorkerTimeout <= 0 || options.WorkerTimeout > time.Hour || options.MaxPlannerRounds == 0 || options.MaxPlannerRounds > 100 || options.MaxIntentRuns == 0 || options.MaxIntentRuns > 10 || options.DrainTimeout <= 0 || options.DrainTimeout > 2*time.Minute || !options.PlannerLimits.Valid() {
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

// 별도 탐색이 끝나고 모든 lease가 닫혀야 Planner/진단 Worker를 받는다. nil은 기존 경로다.
func (r *E2HTTPRuntime) prepareExploration(ctx context.Context, task *Task, t *e2HTTPRuntimeTask) error {
	if r.options.Exploration == nil {
		return nil
	}
	if r.options.CheckAuthority != nil && r.options.CheckAuthority(ctx) != nil {
		return ErrE2HTTPHost
	}
	select {
	case t.gate <- struct{}{}:
		defer func() { <-t.gate }()
	case <-ctx.Done():
		return ErrE2HTTPHost
	case <-t.ctx.Done():
		return ErrE2HTTPHost
	}
	t.mu.Lock()
	if t.closed || t.blocked || t.ctx.Err() != nil || ctx.Err() != nil {
		t.mu.Unlock()
		return ErrE2HTTPHost
	}
	if t.explorationDone {
		err := t.explorationErr
		t.mu.Unlock()
		return err
	}
	if len(t.active) != 0 || t.lease != nil {
		t.mu.Unlock()
		return ErrE2HTTPBusy
	}
	t.mu.Unlock()
	bound, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(t.ctx, cancel)
	charge, err := r.options.Exploration(bound, task)
	if errors.Is(err, ErrE2HTTPBudgetLimited) {
		task.firstDiagnosisBudgetLimited.Store(true)
	}
	if err == nil && bound.Err() != nil {
		err = ErrE2HTTPHost
	}
	stop()
	cancel()
	proof, typedProof := err.(*explorationBudgetClosure)
	validProof := typedProof && proof.validFlow()
	t.mu.Lock()
	// typed proof가 같은 task/run/Workflow에 결속되고 진단 자원이 전혀 없을 때만 보관한다.
	if validProof && proof.workflow == r.options.Workflow && proof.binding.TaskID == t.id && proof.binding.RunRef == r.options.Expected.RunRef && t.lease == nil && !t.validLease && len(t.active) == 0 && len(t.runs) == 0 && len(t.results) == 0 && !t.resultsFailed && t.control.Snapshot().Calls == 0 {
		t.explorationBudget = proof
	}
	t.explorationDone = true
	t.explorationErr = err
	t.explorationCharge = charge
	t.mu.Unlock()
	return err
}

// diagnosisAllowanceLocked는 탐색 몫을 뺀 진단 요청 수를 판정한다. 탐색 단계가 없으면 기존 E② 예산 그대로다.
// 탐색 몫을 모르거나 회차 승인 수를 다 썼으면 진단 송신을 열지 않는다. 호출자는 t.mu를 보유한다.
func (r *E2HTTPRuntime) diagnosisAllowanceLocked(t *e2HTTPRuntimeTask) (requests uint64, unknown bool, exhausted bool) {
	if r.options.Exploration == nil {
		return 0, false, false
	}
	charge := t.explorationCharge
	if !charge.Known || r.options.RunRequests == 0 {
		return 0, true, true
	}
	return charge.Requests, false, charge.Requests >= r.options.RunRequests
}

func (r *E2HTTPRuntime) PreparePlanning(ctx context.Context, task *Task) (context.Context, error) {
	t, err := r.taskFor(task)
	if err != nil || ctx == nil || ctx.Err() != nil {
		return ctx, ErrE2HTTPHost
	}
	if r.options.CheckAuthority != nil && r.options.CheckAuthority(ctx) != nil {
		return ctx, ErrE2HTTPHost
	}
	if err := r.prepareExploration(ctx, task, t); err != nil {
		return ctx, err
	}
	if r.options.Workflow != nil {
		if err := r.options.Workflow.BeforePlanning(ctx); err != nil {
			return ctx, err
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.blocked || t.ctx.Err() != nil {
		return ctx, ErrE2HTTPHost
	}
	if t.plannerRounds >= r.options.MaxPlannerRounds || t.control.Snapshot().Calls >= r.options.MaxToolCalls {
		task.firstDiagnosisBudgetLimited.Store(true)
		return ctx, ErrE2HTTPBudgetLimited
	}
	if len(t.active) != 0 {
		return ctx, ErrE2HTTPBusy
	}
	if t.lease != nil {
		if !t.validLease {
			return ctx, ErrE2HTTPHost
		}
		// 승인 요청 예산을 다 썼으면 새 계획 없이, 진행 중 작업이 끝나는 대로 감독자가 budget_limited로 마감한다.
		if err := r.leaseAdmission(task, t, *t.lease); err != nil {
			return ctx, err
		}
	}
	t.plannerRounds++
	// 실제 intent가 생기기 전 Planner의 Bash/probe는 허용하지 않는다.
	return agent.WithE2HTTPPlannerLimits(agent.WithE2HTTPPlanning(e2HTTPTaskContext(ctx, t.ctx)), r.options.PlannerLimits), nil
}

// 원격 실행에서는 러너가 대상 컨테이너 ID를 eval-e2에 bind한다. 엔진은 그 값을 알 수 없다.
func (r *E2HTTPRuntime) resetMatches(actual string) bool {
	if r.options.ResetBoundRemotely {
		return actual != ""
	}
	return actual == r.options.Expected.ResetRef
}

// lease 판정 결과다. 예산 소진은 결속이 유효한 실행의 실행 상한이라 결속 오류와 나눈다.
type e2HTTPLeaseState int

const (
	e2HTTPLeaseInvalid e2HTTPLeaseState = iota
	e2HTTPLeaseUsable
	e2HTTPLeaseBudgetSpent
)

// leaseAdmission은 이미 결속한 lease로 새 계획·작업을 열 수 있는지 본다. 승인 요청 예산 소진은 결속 오류가 아니라
// 실행 상한이라 task에 표시하고 ErrE2HTTPBudgetLimited를 돌려준다(실행 결과 상태 값 계약 §3.1). 엔진은 이를
// 실패로 바꾸지 않는다.
func (r *E2HTTPRuntime) leaseAdmission(task *Task, t *e2HTTPRuntimeTask, lease E2HTTPRunOptions) error {
	switch r.leaseState(t, lease, 0) {
	case e2HTTPLeaseUsable:
		return nil
	case e2HTTPLeaseBudgetSpent:
		task.firstDiagnosisBudgetLimited.Store(true)
		return ErrE2HTTPBudgetLimited
	}
	return ErrE2HTTPHost
}

func (r *E2HTTPRuntime) validLease(t *e2HTTPRuntimeTask, lease E2HTTPRunOptions, initialIntent int64) bool {
	return r.leaseState(t, lease, initialIntent) == e2HTTPLeaseUsable
}

// leaseState는 lease가 이 실행에 결속돼 있는지 보고, 결속이 맞으면 남은 승인 요청 예산이 있는지 본다. 예산 조회
// 실패는 소진이 아니라 결속 오류다.
func (r *E2HTTPRuntime) leaseState(t *e2HTTPRuntimeTask, lease E2HTTPRunOptions, initialIntent int64) e2HTTPLeaseState {
	if lease.Runner == nil || lease.Release == nil {
		return e2HTTPLeaseInvalid
	}
	if local, ok := lease.Runner.(localE2HTTPRun); ok && lease.Execution != nil && lease.Execution != local.runner.Execution() {
		return e2HTTPLeaseInvalid
	}
	view := lease.Runner.Snapshot()
	expected := r.options.Expected
	if view.Version != e2proxy.Version || view.State != "active" || !view.AuditComplete || view.Scope.TaskRef != t.id || view.Scope.RunRef != expected.RunRef || !r.resetMatches(view.Scope.ResetRef) || view.Scope.PolicyRef != expected.PolicyRef || view.Scope.CaseRef != expected.CaseRef || view.PolicyRevision != expected.PolicyRevision || initialIntent > 0 && (view.IntentID != initialIntent || !lease.Runner.HasIntent(initialIntent)) {
		return e2HTTPLeaseInvalid
	}
	policy := lease.Runner.Policy()
	remaining, err := lease.Runner.BudgetRemaining()
	switch {
	case policy.BudgetRef != expected.BudgetRef || err != nil:
		return e2HTTPLeaseInvalid
	case remaining == 0:
		return e2HTTPLeaseBudgetSpent
	}
	return e2HTTPLeaseUsable
}

func (r *E2HTTPRuntime) PrepareWork(ctx context.Context, task *Task, intent *db.Node) (context.Context, func() error, error) {
	t, err := r.taskFor(task)
	if err != nil || ctx == nil || ctx.Err() != nil || intent == nil || intent.ID <= 0 || intent.Kind != db.KindIntent {
		return ctx, nil, ErrE2HTTPHost
	}
	if err := r.prepareExploration(ctx, task, t); err != nil {
		return ctx, nil, err
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
	if t.control.Snapshot().Calls >= r.options.MaxToolCalls {
		t.mu.Unlock()
		task.firstDiagnosisBudgetLimited.Store(true)
		return ctx, nil, ErrE2HTTPBudgetLimited
	}
	if t.runs[intent.ID] >= r.options.MaxIntentRuns {
		// 이 intent만 다시 실행할 수 없다. 실행 전체 상한은 아니다.
		t.mu.Unlock()
		return ctx, nil, ErrE2HTTPBudgetLimited
	}
	if _, _, exhausted := r.diagnosisAllowanceLocked(t); t.lease == nil && exhausted {
		// 탐색이 회차 요청 예산을 다 썼거나 몫을 확인하지 못했다. 진단 자원·DB 조회 없이 예산 한도로 마감한다(P1-E).
		t.mu.Unlock()
		task.firstDiagnosisBudgetLimited.Store(true)
		return ctx, nil, ErrE2HTTPBudgetLimited
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
	// 공급자도 같은 몫으로 다시 판정한다. 위 검사와 어긋나도 공급자가 N-E 넘게 등록하지 않는다.
	explored, chargeUnknown, _ := r.diagnosisAllowanceLocked(t)
	t.mu.Unlock()
	if lease == nil {
		acquired, acquireErr := r.options.Acquire(t.ctx, E2HTTPRequest{TaskID: t.id, TaskIDNumber: t.number, ExplorationID: t.exploration, IntentID: actual.ID, ExplorationRequests: explored, ExplorationUnknown: chargeUnknown})
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
	// 승인 요청 예산을 다 썼으면 이 intent는 시작하지 않고(마감 때 incomplete) 실행 상한으로 마감한다.
	if err := r.leaseAdmission(task, t, *lease); err != nil {
		return ctx, nil, err
	}
	if err := lease.Runner.RegisterIntent(ctx, actual.ID); err != nil {
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
	observePrompt := r.promptObserver(ctx, t, actual.ID)
	bound := agent.WithE2HTTPExecution(workCtx, agent.E2HTTPExecution{Workflow: r.options.Workflow, ObservePrompt: observePrompt, Runner: lease.Runner, Control: t.control, TaskID: t.number, IntentID: actual.ID, TaskRef: t.id, RunRef: r.options.Expected.RunRef, MaxTurns: r.options.MaxWorkerTurns, MaxDuration: r.options.WorkerTimeout, ApprovedOrigins: append([]string(nil), r.options.ApprovedOrigins...), ObserveResult: func(observeCtx context.Context, result e2runner.Result) error {
		return r.observeResult(observeCtx, t, lease, actual.ID, result)
	}})
	var once sync.Once
	var finalErr error
	finish := func() error {
		once.Do(func() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.options.DrainTimeout)
			finalErr = lease.Runner.DrainIntent(cleanupCtx, actual.ID)
			// 세션 취소는 도구 완료보다 먼저 반환할 수 있다. drain 뒤의 실제 call을 다시 확인한다.
			finalErr = e2HTTPFinalIntentError(finalErr, lease.Runner.Calls(), actual.ID)
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

// Planner and Worker use the same bound runner's credential redactor, including
// remote leases. Before acquisition (or after closure), omit optional target
// data rather than falling back to raw paths or another task's credentials.
func (r *E2HTTPRuntime) redactPlannerTargetData(taskID, data string) string {
	if r == nil || data == "" {
		return ""
	}
	r.mu.Lock()
	t, closed := r.task, r.closed
	r.mu.Unlock()
	if closed || t == nil || t.id != taskID {
		return ""
	}
	t.mu.Lock()
	if t.closed || t.lease == nil || !t.validLease || e2HTTPNil(t.lease.Runner) {
		t.mu.Unlock()
		return ""
	}
	runner := t.lease.Runner
	t.mu.Unlock()
	return string(runner.RedactOutput([]byte(data)))
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
	// 원격 실행은 이 프로세스에 Execution 객체가 없다.
	local, ok := t.lease.Runner.(localE2HTTPRun)
	if !ok {
		return nil, false
	}
	return local.runner.Execution(), true
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
	var stopErr, drainErr error
	if lease.Runner != nil {
		if local, ok := lease.Runner.(localE2HTTPRun); ok && lease.Execution != nil && lease.Execution != local.runner.Execution() {
			return ErrE2HTTPClosure
		}
		stopErr = lease.Runner.Stop(ctx)
		drainErr = lease.Runner.Close(ctx)
	} else if lease.Execution != nil {
		stopErr = lease.Execution.Stop(ctx)
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
	r.sealPromptProjection(ctx, t, result == nil)
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
	var recordErr, intentErr error
	if lease == nil || lease.Runner == nil {
		recordErr = ErrE2HTTPHost
	} else if trusted.Call.CallRef != "" {
		execution := lease.Runner
		{
			view := execution.Snapshot()
			valid := trusted.Version == e2proxy.Version && trusted.Execution.Scope == view.Scope && view.Scope.TaskRef == t.id && trusted.Execution.PolicyRevision == view.PolicyRevision && trusted.Execution.HeaderMode == view.HeaderMode && trusted.Execution.IntentID == intentID && trusted.Call.IntentID == intentID && execution.HasIntent(intentID)
			found, sameCall := false, false
			for _, call := range execution.Calls() {
				if e2proxy.EqualCallResults(call, trusted.Call) {
					found = true
					break
				}
				sameCall = sameCall || call.CallRef == trusted.Call.CallRef && call.IntentID == intentID
			}
			switch {
			case valid && !found && sameCall && trusted.Final:
				// 같은 호출의 최종 신뢰 결과가 E② 실행 기록과 다르다. 그 intent만 결과 불명으로 두고 실행은 계속한다(C09).
				// 감사 기록은 E② 실행 쪽이 정본이고 마감은 그 기록으로 대조한다. 모르는 호출·실행 범위 불일치·
				// 비최종 결과는 아래처럼 실행 실패로 둔다.
				t.mu.Lock()
				if t.untrusted == nil {
					t.untrusted = make(map[int64]bool)
				}
				t.untrusted[intentID] = true
				t.mu.Unlock()
				intentErr = agent.ErrE2HTTPResultUntrusted
			case !valid || !found:
				recordErr = ErrE2HTTPHost
			default:
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
		return errors.Join(recordErr, observerErr, intentErr)
	}
	// 대조 실패만 있으면 같은 값을 돌려준다. Worker는 이 값일 때만 intent 단위 결과 불명으로 처리한다.
	return intentErr
}

// UntrustedIntents는 최종 신뢰 결과가 E② 실행 기록과 맞지 않았던 intent의 사본이다(C09). 다른 task면 nil이다.
func (r *E2HTTPRuntime) UntrustedIntents(taskID string) map[int64]bool {
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
	out := make(map[int64]bool, len(t.untrusted))
	for id := range t.untrusted {
		out[id] = true
	}
	return out
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
