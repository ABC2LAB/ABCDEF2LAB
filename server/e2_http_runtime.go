package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosis"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/e2host"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"github.com/Autumn-27/artex/loginattempt"
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
	// WorkerCount는 Manager에서 결속한 실제 회차 Worker 수이며 외부 입력이 아니다.
	WorkerCount   int `json:"-"`
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
func (r localE2HTTPRun) RedactRecord(data []byte) []byte {
	return r.runner.Execution().RedactRecord(data)
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
	// VerifySelected asks the audit Host to authenticate one ordinary result
	// against the current journal head before it can become candidate evidence.
	VerifySelected func(context.Context, e2host.ObservationRef) (e2host.Verification, error)
	Runner         E2HTTPLeaseRunner
	Release        func(context.Context) error
	// AccountAuth는 인증 준비의 계정별 결과(라벨 → authenticated·login_failed·session_not_found·result_unknown·not_sent),
	// Sessions는 인증된 시험 계정의 보낼 세션 헤더다(실행 메모리에만, C14). 인증 준비가 없으면 둘 다 비어 있다.
	AccountAuth map[string]string
	Sessions    E2HTTPSessions
	// RefreshSession은 이 실행의 시험 계정 하나를 인증 준비 경로로 다시 로그인시킨다(refresh_session, C14). 시험 계정
	// 실행이고 인증된 계정이 있을 때만 채운다.
	RefreshSession func(ctx context.Context, label string) (http.Header, string)
	// Credentials는 결속된 등록 계정의 로그인 자격이다(모델 직접 로그인, 사용자 결정 2026-11-26). Worker 지침에만 들어간다.
	Credentials []agent.E2HTTPAccountCredential
	// LoginAttempts는 모델 직접 로그인 회차의 로그인 시도 판정기다. 계정별 인증 상태(account_auth)를 여기서 읽는다.
	LoginAttempts *loginattempt.Tracker
	// sessionStore는 다시 로그인을 반영한 지금 세션이다. 있으면 Worker 지침은 이 사본으로 만든다.
	sessionStore *e2HTTPSessionStore
	// programCalls는 서버가 연 E② 호출(인증 준비 등)이다. 마감 대조가 Worker 결과 없이 따로 센다.
	programCalls *e2HTTPProgramCalls
}

// AccountBindings는 이 실행의 계정 결속 참조 조회다(E③ 프로그램 요청에 세션을 붙이는 E²가 쓴다). 세션 저장소가 없으면 nil이다.
func (o *E2HTTPRunOptions) AccountBindings() E2HTTPAccountBindings {
	if o == nil || o.sessionStore == nil {
		return nil
	}
	return o.sessionStore
}

// CurrentSessions는 실행이 지금 들고 있는 시험 계정 세션 사본이다(다시 로그인 반영). 저장소가 없으면 처음 인증 준비의 세션이다.
func (o *E2HTTPRunOptions) CurrentSessions() E2HTTPSessions {
	if o == nil {
		return nil
	}
	if o.sessionStore != nil {
		return o.sessionStore.snapshot()
	}
	return o.Sessions
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
	// CaptureSelectedProof asks the audit Host for per-call checkpoints only
	// in URL diagnosis runs that preserve candidate observations.
	CaptureSelectedProof bool
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
	// PlannerLimits는 Planner 한 번(Plan)의 턴·시간 상한이다(C26·C32). 0인 칸은 agent 기본값(16턴·170초)이다.
	// E② 정책 밖 값이라 정책 지문에 들어가지 않고, 제품 설치는 launch 설정(설치 기록 해시)에 묶는다.
	PlannerLimits agent.E2HTTPPlannerLimits
	// WebSearch는 진단 Planner·Worker의 원본 검색 도구 설정이다(루프 복원 결정안 §2-1). 비어 있으면 검색 도구가 없다.
	WebSearch agent.E2HTTPWebSearch
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
	// accountAuth는 실행을 잡을 때 받은 시험 계정별 인증 준비 결과다(라벨 → 결과, 값 없음). 자원을 닫은 뒤에도
	// 실행 결과에 싣도록 남긴다. 다른 잠금과 섞이지 않게 따로 잠근다.
	accountAuthMu sync.Mutex
	accountAuth   map[string]string
	// loginAttempts는 모델 직접 로그인 회차의 판정기다. 있으면 계정 상태는 처음 결과(자격을 못 읽은 계정의 not_sent)에
	// 판정기 값을 더한 것이다(계약 §4 표).
	loginAttempts *loginattempt.Tracker
	// planResults는 작업별 마지막 계획(Plan) 한 번이 오류 없이 끝났는지다(작업 ID → bool). 흐름 모드 마감이 '계획은
	// 정상인데 의도 0'과 '계획 호출이 실패해 의도 0'을 가르는 데 쓴다.
	planResults sync.Map
	// planTimeBudget은 계획 준비가 시간 상한 마감(agent.AbortTimeBudgetDrain)으로 멈춘 작업이다(작업 ID → true). 범위 선택을
	// 기다리다 멈춘 흐름 모드 회차를 마감 검사가 실패가 아니라 실행 상한 마감으로 가른다(#964,
	// firstDiagnosisSelectionWaitClosure).
	planTimeBudget sync.Map
}

// recordPlanResult는 계획 한 번의 결과를 남긴다(준비 단계의 첫 계획과 실행 단계의 계획 반복 모두).
func (r *E2HTTPRuntime) recordPlanResult(taskID string, ok bool) {
	if r != nil {
		r.planResults.Store(taskID, ok)
	}
}

// lastPlanSucceeded는 그 작업의 마지막 계획이 오류 없이 끝났는지 본다. 계획 기록이 없으면 거짓이다.
// planningStoppedByTimeBudget은 그 작업의 계획 준비가 시간 상한 마감으로 멈췄는지다.
func (r *E2HTTPRuntime) planningStoppedByTimeBudget(taskID string) bool {
	if r == nil {
		return false
	}
	_, ok := r.planTimeBudget.Load(taskID)
	return ok
}

func (r *E2HTTPRuntime) lastPlanSucceeded(taskID string) bool {
	if r == nil {
		return false
	}
	v, ok := r.planResults.Load(taskID)
	return ok && v.(bool)
}

// AccountAuth는 이 실행의 시험 계정별 인증 준비 결과의 사본이다(계약 §3.4). 계정을 결속하지 않았으면 nil이다.
func (r *E2HTTPRuntime) AccountAuth() map[string]string {
	if r == nil {
		return nil
	}
	r.accountAuthMu.Lock()
	defer r.accountAuthMu.Unlock()
	if r.loginAttempts != nil {
		merged := map[string]string{}
		for label, result := range r.accountAuth {
			merged[label] = result
		}
		for label, result := range r.loginAttempts.AccountAuth() {
			if _, unread := merged[label]; !unread {
				merged[label] = result
			}
		}
		return diagnosis.CloneAccountAuth(merged)
	}
	return diagnosis.CloneAccountAuth(r.accountAuth)
}

// currentAccountAuth는 최초 인증 결과에 지금 회차의 세션 결속을 덮은 값이다(갱신 성공으로 새 세션을 얻은 계정은
// authenticated, 최초에 authenticated였지만 지금 세션이 없는 계정은 session_not_found). current는 지금 lease를 볼 수
// 있었는지다. 볼 수 없으면 최초 결과 사본을 그대로 돌려준다(덮지 않음).
func (r *E2HTTPRuntime) currentAccountAuth() (auth map[string]string, current bool) {
	auth = r.AccountAuth()
	if r == nil || len(auth) == 0 {
		return auth, false
	}
	r.mu.Lock()
	task := r.task
	r.mu.Unlock()
	if task == nil {
		return auth, false
	}
	task.mu.Lock()
	lease := task.lease
	usable := !task.closed && !task.blocked && task.validLease
	task.mu.Unlock()
	if !usable || lease == nil || lease.sessionStore == nil {
		return auth, false
	}
	for label, initial := range auth {
		if _, ok := lease.sessionStore.AccountBindingRef(r.options.Expected.RunRef, label); ok {
			auth[label] = E2HTTPAuthAuthenticated
		} else if initial == E2HTTPAuthAuthenticated {
			auth[label] = E2HTTPAuthSessionNotFound
		}
	}
	return auth, true
}

// AccountAuthForE3는 최초 인증 결과를 덮어쓰지 않고, E③ 직전에 같은 회차에 현재 결속된 세션이 있는지 다시 본다.
// 갱신 성공으로 새 세션을 얻은 계정도 허용하되 토큰의 현재 유효성·신원은 이후 E③ 재로그인과 신원 요청이 검증한다.
// 지금 lease를 볼 수 없으면 세션을 쓸 수 없는 것으로 본다(authenticated → session_not_found).
func (r *E2HTTPRuntime) AccountAuthForE3() map[string]string {
	auth, current := r.currentAccountAuth()
	if !current {
		for label, initial := range auth {
			if initial == E2HTTPAuthAuthenticated {
				auth[label] = E2HTTPAuthSessionNotFound
			}
		}
	}
	return auth
}

// AccountAuthNow는 실행 중 상태 응답(diagnosisprep.Job account_auth)에 싣는 계정별 인증 상태다. 확정 단계와 같은 기준
// (최초 인증 결과 + 지금 세션 결속)이다. 인증 준비 전이거나(계정 결속 없음 포함) 지금 lease를 볼 수 없으면 ok가 거짓이고,
// 호출자는 앞에 실은 값을 유지한다(모르는 순간을 session_not_found로 바꾸지 않는다). 자격·세션 값은 없다.
func (r *E2HTTPRuntime) AccountAuthNow() (map[string]string, bool) {
	auth, current := r.currentAccountAuth()
	if !current {
		return nil, false
	}
	return auth, true
}

type e2HTTPRuntimeTask struct {
	notSentCommands map[string]bool

	workerCount       int
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
	// modelClosed는 모델 단계가 마감됐는지다(CloseModelStage). 뒤늦게 잡은 실행도 같은 마감을 받는다.
	modelClosed   bool
	control       *agent.E2HTTPControl
	results       []e2runner.TrustedResult
	resultsFailed bool
	// untrusted는 최종 신뢰 결과가 같은 호출의 E② 실행 기록과 내용이 달랐던 intent다(C09). 그 호출의 결과는
	// 저장하지 않고, intent는 결과 불명으로 끝나야 마감된다. mu로 보호한다.
	untrusted map[int64]bool
	// untrustedCalls는 대조 실패로 표시한 호출이다. 같은 호출을 다시 관찰하면 중복 기록 실패로 본다. mu로 보호한다.
	untrustedCalls map[string]bool
	// observedWake는 다음 결과 관찰 때 닫힌다. intent 마감이 이 intent의 호출 결과 관찰을 기다릴 때 쓴다. mu로 보호한다.
	observedWake  chan struct{}
	releaseCalled bool
	releaseErr    error

	promptFailed     bool // mu로 보호한다. 나머지 등록 상태는 gate 안에서만 접근한다.
	promptRegistered bool
	promptOwned      bool
	// promptRegisterErr는 실행 등록 실패의 원인이다(로그 사유용). gate 안에서만 접근한다.
	promptRegisterErr error
}

func NewE2HTTPRuntime(parent context.Context, options E2HTTPRuntimeOptions) (*E2HTTPRuntime, error) {
	if parent == nil || parent.Err() != nil || options.Acquire == nil || options.MaxWorkerTurns <= 0 || options.MaxWorkerTurns > 1000 || options.MaxToolCalls <= 0 || options.MaxToolCalls > agent.E2HTTPMaxTaskCalls || options.WorkerTimeout <= 0 || options.WorkerTimeout > time.Hour || options.MaxPlannerRounds == 0 || options.MaxPlannerRounds > 100 || options.MaxIntentRuns == 0 || options.MaxIntentRuns > 10 || options.DrainTimeout <= 0 || options.DrainTimeout > 2*time.Minute || !options.PlannerLimits.Valid() || !options.WebSearch.Valid() {
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
	return r.admitTaskWithWorkers(ctx, task, 1)
}

// 실제 Engine/Manager 시작 경로만 Worker 수를 결속한다. 같은 회차에서 바꾸지 않는다.
func (r *E2HTTPRuntime) admitTaskWithWorkers(ctx context.Context, task *Task, workers int) error {
	number, err := e2HTTPTaskIdentity(task)
	if err != nil || r == nil || ctx == nil || ctx.Err() != nil || workers < 1 {
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
		if r.task.workerCount != workers || r.task.id != task.ID || r.task.number != number || r.task.exploration != task.ExpID || r.task.store != task.Store {
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
	r.task = &e2HTTPRuntimeTask{workerCount: workers, id: task.ID, number: number, exploration: task.ExpID, store: task.Store, ctx: taskctx, cancel: cancel, gate: make(chan struct{}, 1), closeGate: make(chan struct{}, 1), runs: make(map[int64]uint64), active: make(map[int64]chan struct{}), control: control}
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

// planningFailure는 검사 분기만 고정 어휘로 기록한다. 호출자가 준 task ID·오류 원문은 쓰지 않고 로거의 기존 결속을 쓴다.
// 진단 로그가 없거나 기록에 실패해도 기존 오류·예산·마감 의미는 바꾸지 않는다(C10).
func (r *E2HTTPRuntime) planningFailure(ctx context.Context, status, reason string) {
	logContext := ctx
	if diagnosticlog.From(logContext) == nil && r != nil {
		logContext = r.ctx
	}
	_ = diagnosticlog.Emit(logContext, diagnosticlog.Event{Component: "planner", Event: "stage_finished", Stage: "planning", Status: status, ReasonCode: reason})
}

// planningFailed는 계획 준비 실패를 진단 로그에 남기고, 같은 고정 사유를 오류에 싣는다. 엔진이 그 오류로 작업을 실패로
// 표시하면 감독자가 이 사유를 실행 상태(reason_code)까지 싣는다(B1). 감싼 오류는 errors.Is로 원래 오류와 같게 보인다.
func (r *E2HTTPRuntime) planningFailed(ctx context.Context, status, reason string, err error) error {
	r.planningFailure(ctx, status, reason)
	return firstDiagnosisCheckFailed(reason, err)
}

// timeBudgetDrained는 계획 준비 중(탐색 단계·범위 선택이나 흐름 일시정지 대기)에 감독자가 시간 상한 마감 유예를 다 써서
// 실행을 멈춘 경우인지 본다(agent.AbortTimeBudgetDrain).
func timeBudgetDrained(ctx context.Context) bool {
	return ctx != nil && ctx.Err() != nil && errors.Is(context.Cause(ctx), agent.AbortTimeBudgetDrain)
}

// planningTimeBudget은 시간 상한 마감으로 멈춘 계획 준비를 실패가 아니라 실행 상한으로 돌려준다. 그래야 탐색 결과가 있는
// 회차가 failed가 아니라 정한 마감 상태(completed·budget_limited)로 끝난다(계약 §3.4 시간 상한 마감, 2026-11-26 NodeGoat
// 회차 2 발견 5: 범위 선택을 기다리다 시간 상한에 닿아 planning_workflow_blocked → engine_task_failed).
func (r *E2HTTPRuntime) planningTimeBudget(ctx context.Context, task *Task) error {
	task.firstDiagnosisBudgetLimited.Store(true)
	r.planTimeBudget.Store(task.ID, true)
	r.planningFailure(ctx, "blocked", "planning_time_budget")
	return ErrE2HTTPBudgetLimited
}

func (r *E2HTTPRuntime) PreparePlanning(ctx context.Context, task *Task) (context.Context, error) {
	t, err := r.taskFor(task)
	if err != nil {
		return ctx, r.planningFailed(ctx, "failed", "planning_task_unavailable", ErrE2HTTPHost)
	}
	if ctx == nil || ctx.Err() != nil {
		status := "failed"
		if ctx != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				status = "timeout"
			} else if errors.Is(ctx.Err(), context.Canceled) {
				status = "cancelled"
			}
		}
		return ctx, r.planningFailed(ctx, status, "planning_context_closed", ErrE2HTTPHost)
	}
	if r.options.CheckAuthority != nil && r.options.CheckAuthority(ctx) != nil {
		return ctx, r.planningFailed(ctx, "failed", "planning_authority_rejected", ErrE2HTTPHost)
	}
	if err := r.prepareExploration(ctx, task, t); err != nil {
		if timeBudgetDrained(ctx) {
			return ctx, r.planningTimeBudget(ctx, task)
		}
		return ctx, r.planningFailed(ctx, "failed", "planning_exploration_failed", err)
	}
	if r.options.Workflow != nil {
		if err := r.options.Workflow.BeforePlanning(ctx); err != nil {
			if timeBudgetDrained(ctx) {
				return ctx, r.planningTimeBudget(ctx, task)
			}
			return ctx, r.planningFailed(ctx, "blocked", "planning_workflow_blocked", err)
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return ctx, r.planningFailed(ctx, "failed", "planning_task_closed", ErrE2HTTPHost)
	}
	if t.blocked {
		return ctx, r.planningFailed(ctx, "blocked", "planning_task_blocked", ErrE2HTTPHost)
	}
	if t.ctx.Err() != nil {
		return ctx, r.planningFailed(ctx, "cancelled", "planning_task_cancelled", ErrE2HTTPHost)
	}
	if t.plannerRounds >= r.options.MaxPlannerRounds {
		task.firstDiagnosisBudgetLimited.Store(true)
		r.planningFailure(ctx, "blocked", "planning_round_limit")
		return ctx, ErrE2HTTPBudgetLimited
	}
	if t.control.Snapshot().Calls >= r.options.MaxToolCalls {
		task.firstDiagnosisBudgetLimited.Store(true)
		r.planningFailure(ctx, "blocked", "planning_tool_limit")
		return ctx, ErrE2HTTPBudgetLimited
	}
	// Worker가 실행 중이어도 Planner를 깨운다(C27). 모델 동시성·예산·기존 lease 판정은 바꾸지 않는다.
	if t.lease != nil {
		if !t.validLease {
			return ctx, r.planningFailed(ctx, "failed", "planning_lease_invalid", ErrE2HTTPHost)
		}
		if err := r.leaseAdmission(task, t, *t.lease); err != nil {
			status, reason := "failed", "planning_lease_invalid"
			if errors.Is(err, ErrE2HTTPBudgetLimited) {
				status, reason = "blocked", "planning_request_budget"
			}
			return ctx, r.planningFailed(ctx, status, reason, err)
		}
	}
	t.plannerRounds++
	// 계획 준비가 다시 정상으로 통과했으면 앞의 시간 상한 멈춤 표시는 지금 상태가 아니다. 지우지 않으면 그 뒤 실제 계획 실패도
	// 마감 검사에서 계획 성공처럼 보인다(#964 윤재상 리뷰). 계획 결과 기록 때 지우지 않는 것은, 시간 상한 마감 때 함께 취소된
	// 다른 계획이 실패로 기록되며 표시를 지워 원래 결함(범위 대기 회차의 failed)으로 돌아가지 않게 하려는 것이다.
	r.planTimeBudget.Delete(task.ID)
	// 실제 intent가 생기기 전 Planner의 Bash/probe는 허용하지 않는다.
	return agent.WithE2HTTPWebSearch(agent.WithE2HTTPPlannerLimits(agent.WithE2HTTPPlanning(e2HTTPTaskContext(ctx, t.ctx)), r.options.PlannerLimits), r.options.WebSearch), nil
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
		acquired, acquireErr := r.options.Acquire(t.ctx, E2HTTPRequest{WorkerCount: t.workerCount, TaskID: t.id, TaskIDNumber: t.number, ExplorationID: t.exploration, IntentID: actual.ID, ExplorationRequests: explored, ExplorationUnknown: chargeUnknown})
		// 인증 준비 결과는 실행을 잡지 못해도(실패로 끝나도) 실행 결과에 싣는다. 형식이 틀리면 싣지 않는다.
		if auth := diagnosis.CloneAccountAuth(acquired.AccountAuth); auth != nil {
			r.accountAuthMu.Lock()
			r.accountAuth = auth
			r.accountAuthMu.Unlock()
		}
		if acquired.LoginAttempts != nil {
			r.accountAuthMu.Lock()
			r.loginAttempts = acquired.LoginAttempts
			r.accountAuthMu.Unlock()
		}
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
		modelClosed := t.modelClosed
		t.mu.Unlock()
		if modelClosed {
			// 모델 단계 마감 뒤에 잡은 실행이면 같은 마감을 건다(모델 단계 요청은 나가지 않는다).
			if local, ok := acquired.Runner.(localE2HTTPRun); ok {
				local.runner.Execution().CloseModelStage()
			}
		}
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
	workCtx := agent.WithE2HTTPWebSearch(e2HTTPTaskContext(ctx, t.ctx), r.options.WebSearch)
	observePrompt := r.promptObserver(ctx, t, actual.ID)
	bound := agent.WithE2HTTPExecution(workCtx, agent.E2HTTPExecution{Sessions: e2HTTPWorkerSessions(r.options.HeaderMode, lease), RefreshSession: e2HTTPRefreshFor(r.options.HeaderMode, lease), Credentials: e2HTTPWorkerCredentials(r.options.HeaderMode, lease), Workflow: r.options.Workflow, CaptureSelectedProof: r.options.CaptureSelectedProof, ObservePrompt: observePrompt, Runner: lease.Runner, Control: t.control, TaskID: t.number, IntentID: actual.ID, TaskRef: t.id, RunRef: r.options.Expected.RunRef, MaxTurns: r.options.MaxWorkerTurns, MaxDuration: r.options.WorkerTimeout, ApprovedOrigins: append([]string(nil), r.options.ApprovedOrigins...), ObserveResult: func(observeCtx context.Context, result e2runner.Result) error {
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
			finalErr = r.attemptUnknownIntentError(cleanupCtx, t, lease, actual.ID, finalErr)
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

// plannerWorkflowInput은 Planner 목표에 붙일 지시 문장과 가린 고른 끝점 데이터다. 데이터 상한은 가린 뒤 바이트로 재고,
// 지시의 "빠진 수"도 같은 행 목록에서 나온다. 가림 함수를 얻지 못하면(획득 전·닫힘) 데이터는 비운다.
func (r *E2HTTPRuntime) plannerWorkflowInput(taskID string, workflow *diagnosisflow.Controller) (string, string) {
	if workflow == nil {
		return "", ""
	}
	runner := r.plannerRedactor(taskID)
	if runner == nil {
		return workflow.ModelInput(nil)
	}
	return workflow.ModelInput(runner)
}

// plannerRedactor는 Planner가 쓸 그 실행의 출력 가림이다. 없으면 nil이다.
func (r *E2HTTPRuntime) plannerRedactor(taskID string) func([]byte) []byte {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	t, closed := r.task, r.closed
	r.mu.Unlock()
	if closed || t == nil || t.id != taskID {
		return nil
	}
	t.mu.Lock()
	if t.closed || t.lease == nil || !t.validLease || e2HTTPNil(t.lease.Runner) {
		t.mu.Unlock()
		return nil
	}
	runner := t.lease.Runner
	t.mu.Unlock()
	return runner.RedactOutput
}

// redactPlannerTargetData는 Planner가 쓸 그 실행의 가림으로 data를 가린다. 획득 전·닫힘·다른 작업이면 비운다(원문 경로나
// 다른 작업의 자격으로 대신하지 않는다). Planner·Worker는 같은 결속 러너의 가림을 쓴다(원격 임대 포함).
func (r *E2HTTPRuntime) redactPlannerTargetData(taskID, data string) string {
	if data == "" {
		return ""
	}
	redact := r.plannerRedactor(taskID)
	if redact == nil {
		return ""
	}
	return string(redact([]byte(data)))
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

// E3Handle은 모델 단계 뒤 감독자에게만 현재 로컬 E② 실행과 C14 저장소를 함께 돌려준다.
func (r *E2HTTPRuntime) E3Handle(taskID string) (*e2proxy.Execution, E2HTTPAccountBindings, bool) {
	exec, accounts, reason := r.E3HandleReason(taskID)
	return exec, accounts, reason == ""
}

// E3Handle이 연결을 주지 못한 사유(고정 코드)다. 확정 단계가 not_wired로 꺼졌을 때 서버 로그에 남겨 실제 제품에서
// 어느 조건인지 가린다(#805 뒤 합성 회차: 가짜 handle 시험만으로는 못 잡음).
const (
	E3HandleRuntimeUnavailable = "runtime_unavailable"
	E3HandleRuntimeClosing     = "runtime_closing"
	E3HandleRuntimeClosed      = "runtime_closed"
	E3HandleTaskMissing        = "task_missing"
	E3HandleTaskMismatch       = "task_mismatch"
	E3HandleTaskFenced         = "task_fenced"
	E3HandleLeaseMissing       = "lease_missing"
	E3HandleLeaseInvalid       = "lease_invalid"
	E3HandleRunnerMissing      = "runner_missing"
	E3HandleRunnerNotLocal     = "runner_not_local"
	E3HandleAccountsMissing    = "accounts_missing"
	E3HandleExecutionStopped   = "execution_stopped"
)

// E3HandleReason은 E3Handle과 같고, 연결을 주지 못하면 그 사유 코드를 돌려준다(성공이면 빈 문자열). 실행 문맥이 이미
// 끝났으면(작업 문맥 취소 등) 핸들이 있어도 송신할 수 없으므로 주지 않는다.
func (r *E2HTTPRuntime) E3HandleReason(taskID string) (*e2proxy.Execution, E2HTTPAccountBindings, string) {
	if r == nil {
		return nil, nil, E3HandleRuntimeUnavailable
	}
	r.mu.Lock()
	t := r.task
	r.mu.Unlock()
	switch {
	case t == nil:
		return nil, nil, E3HandleTaskMissing
	case t.id != taskID:
		return nil, nil, E3HandleTaskMismatch
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case t.closed:
		return nil, nil, E3HandleTaskFenced
	case t.lease == nil:
		return nil, nil, E3HandleLeaseMissing
	case !t.validLease:
		return nil, nil, E3HandleLeaseInvalid
	case t.lease.Runner == nil:
		return nil, nil, E3HandleRunnerMissing
	}
	local, ok := t.lease.Runner.(localE2HTTPRun)
	if !ok {
		return nil, nil, E3HandleRunnerNotLocal
	}
	accounts := t.lease.AccountBindings()
	if accounts == nil {
		return nil, nil, E3HandleAccountsMissing
	}
	exec := local.runner.Execution()
	select {
	case <-exec.Done():
		return nil, nil, E3HandleExecutionStopped
	default:
	}
	return exec, accounts, ""
}

// CloseModelStage는 이 작업의 E② 실행에서 모델 단계를 마감한다(e2proxy.Execution.CloseModelStage). 그 뒤로는 확정 단계
// 요청만 나가고, 진행 중 모델 단계 요청은 끊긴다. 실행 전체를 닫는 것(FenceTask)은 감독자만 한다: 확정 단계를 돌리는
// 마감이면 확정 단계 뒤에, 그 밖의 마감은 마감 정리에서. 실행을 아직 잡지 않았으면 뒤에 잡는 실행에 같은 마감을 건다.
// 다른 작업이면 할 일이 없다. 여러 번 불러도 같다.
func (r *E2HTTPRuntime) CloseModelStage(taskID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	t := r.task
	r.mu.Unlock()
	if t == nil || t.id != taskID {
		return
	}
	t.mu.Lock()
	t.modelClosed = true
	var exec *e2proxy.Execution
	if t.lease != nil {
		if local, ok := t.lease.Runner.(localE2HTTPRun); ok {
			exec = local.runner.Execution()
		}
	}
	t.mu.Unlock()
	exec.CloseModelStage()
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
	if value.AdmissionNotSent != nil {
		receipt := *value.AdmissionNotSent
		value.AdmissionNotSent = &receipt
	}
	value.Call = e2proxy.CloneCallResult(value.Call)
	value.Execution.IntentIDs = append([]int64(nil), value.Execution.IntentIDs...)
	value.Execution.Evaluation = e2HTTPCloneEvaluation(value.Execution.Evaluation)
	return value
}

// 결과는 agent가 concrete Runner에서 직접 받은 callback으로만 기록한다. 원문 출력은 보관하지 않는다.
func (r *E2HTTPRuntime) observeResult(ctx context.Context, t *e2HTTPRuntimeTask, lease *E2HTTPRunOptions, intentID int64, result e2runner.Result) error {
	// 저장·대조 실패 표시가 끝난 뒤 이 결과를 기다리는 intent 마감을 깨운다.
	defer t.wakeObservers()
	result.ToolOutput = ""
	trusted := result.Trusted
	if trusted.AdmissionNotSent != nil {
		valid := lease != nil && lease.Runner != nil && e2runner.AdmissionNotSent(result) &&
			trusted.Execution.Scope.TaskRef == t.id && trusted.Execution.IntentID == intentID
		if valid {
			if lease.Execution != nil {
				valid = lease.Execution.HasAdmissionNotSent(*trusted.AdmissionNotSent)
			} else if remote, ok := lease.Runner.(interface{ takeAdmissionNotSent(e2proxy.Receipt) bool }); ok {
				valid = remote.takeAdmissionNotSent(*trusted.AdmissionNotSent)
			} else {
				valid = false
			}
			view := lease.Runner.SnapshotForIntent(intentID)
			valid = valid && view.Scope == trusted.Execution.Scope && view.PolicyRevision == trusted.Execution.PolicyRevision && view.HeaderMode == trusted.Execution.HeaderMode
		}
		t.mu.Lock()
		if t.notSentCommands == nil {
			t.notSentCommands = make(map[string]bool)
		}
		if !valid || t.notSentCommands[trusted.AdmissionNotSent.CommandRef] || len(t.notSentCommands) >= r.options.MaxToolCalls {
			t.resultsFailed = true
			t.mu.Unlock()
			return ErrE2HTTPHost
		}
		t.notSentCommands[trusted.AdmissionNotSent.CommandRef] = true
		t.mu.Unlock()
		if lease.ObserveResult != nil {
			if err := lease.ObserveResult(ctx, result); err != nil {
				t.mu.Lock()
				t.resultsFailed = true
				t.mu.Unlock()
				return err
			}
		}
		// This control event is already durably published by ObserveEvent. It
		// has no Call and must not enter the closed-call result projection.
		return nil
	}
	var recordErr, intentErr error
	if lease == nil || lease.Runner == nil {
		recordErr = ErrE2HTTPHost
	} else if trusted.Call.CallRef != "" {
		execution := lease.Runner
		{
			view := execution.Snapshot()
			valid := trusted.Version == e2proxy.Version && trusted.Execution.Scope == view.Scope && view.Scope.TaskRef == t.id && trusted.Execution.PolicyRevision == view.PolicyRevision && trusted.Execution.HeaderMode == view.HeaderMode && trusted.Execution.IntentID == intentID && trusted.Call.Program == "" && trusted.Call.IntentID == intentID && execution.HasIntent(intentID)
			found, sameCall := false, false
			for _, call := range execution.Calls() {
				if e2proxy.EqualCallResults(call, trusted.Call) {
					found = true
					break
				}
				sameCall = sameCall || call.CallRef == trusted.Call.CallRef && call.IntentID == intentID
			}
			// 신뢰 결과가 실행기 쪽에서 마감·드레인·감사까지 끝났다는 표시다. 이 중 하나라도 빠졌으면 내용 대조 실패로
			// 낮추지 않고 실행 결과 기록 실패로 둔다(#492 이동찬 지적: Final만 보면 드레인·감사 미완료가 빠져나감).
			closed := trusted.Final && trusted.ProcessDrained && trusted.ProxyDrained && trusted.Call.Final && trusted.Call.AuditComplete && trusted.Execution.AuditComplete
			if r.options.CaptureSelectedProof && valid && found && closed && lease.VerifySelected != nil && len(trusted.Call.Observations) == 1 {
				observation := trusted.Call.Observations[0]
				verified, err := lease.VerifySelected(ctx, e2host.ObservationRef{
					CallRef: trusted.Call.CallRef, AttemptID: observation.AttemptRef, Sequence: observation.Sequence,
				})
				if err == nil && selectedVerifiedResult(trusted, verified) {
					trusted.SelectedProof = &e2runner.SelectedAuditProof{Event: verified.Event, Checkpoint: verified.Checkpoint}
				}
			}
			t.mu.Lock()
			// 같은 호출을 두 번 최종 관찰하면 내용과 관계없이 기록 실패다. 저장한 결과와 대조 실패로 표시한 호출을 함께 본다.
			seen := t.untrustedCalls[trusted.Call.CallRef]
			for _, prior := range t.results {
				if prior.Call.CallRef == trusted.Call.CallRef {
					seen = true
					break
				}
			}
			switch {
			case seen:
				recordErr = ErrE2HTTPHost
			case valid && !found && sameCall && closed:
				// 같은 호출의 최종 신뢰 결과가 E② 실행 기록과 다르다. 그 intent만 결과 불명으로 두고 실행은 계속한다(C09).
				// 감사 기록은 E② 실행 쪽이 정본이고 마감은 그 기록으로 대조한다. 모르는 호출·실행 범위 불일치·
				// 비최종·미드레인·감사 미완료 결과는 아래처럼 실행 실패로 둔다.
				if t.untrusted == nil {
					t.untrusted = make(map[int64]bool)
					t.untrustedCalls = make(map[string]bool)
				}
				t.untrusted[intentID] = true
				t.untrustedCalls[trusted.Call.CallRef] = true
				intentErr = agent.ErrE2HTTPResultUntrusted
			case !valid || !found:
				recordErr = ErrE2HTTPHost
			case len(t.results) >= r.options.MaxToolCalls:
				recordErr = ErrE2HTTPHost
			default:
				t.results = append(t.results, e2HTTPCloneTrusted(trusted))
			}
			t.mu.Unlock()
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

// ProgramCalls는 서버가 연 E② 호출(인증 준비 등)의 CallRef 사본이다. Worker Bash 결과가 없는 호출이다. 다른 task면 nil이다.
func (r *E2HTTPRuntime) ProgramCalls(taskID string) map[string]bool {
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
	if t.lease == nil {
		return map[string]bool{}
	}
	if remote, ok := t.lease.Runner.(interface{ ProgramCalls() map[string]bool }); ok {
		return remote.ProgramCalls()
	}
	return t.lease.programCalls.snapshot()
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

// observedWaitLocked는 다음 결과 관찰 때 닫히는 채널이다. 호출자는 mu를 보유한다.
func (t *e2HTTPRuntimeTask) observedWaitLocked() <-chan struct{} {
	if t.observedWake == nil {
		t.observedWake = make(chan struct{})
	}
	return t.observedWake
}

// wakeObservers는 결과 관찰을 기다리는 쪽을 깨운다.
func (t *e2HTTPRuntimeTask) wakeObservers() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.observedWake != nil {
		close(t.observedWake)
		t.observedWake = nil
	}
}

// attemptUnknownIntentError는 멈춘 명령(Planner kill_work·목표 달성 취소·시간 상한 정리·호출 시간 상한 등)의 시도 단위
// 기록에 대상에 전달됐는데 응답을 끝까지 받지 못한 시도가 있으면 그 intent를 결과 불명으로 고정한다. 마감 판정과 같은
// 규칙(firstDiagnosisStoppedToolOutcome의 firstDiagnosisAttemptUnknown)이라 그 intent는 미완료·정책 제한으로 마감되지
// 않는다(C01·C08). 연결 뒤 끊긴 송신(결과 불명 송신 수가 있는 호출)은 e2HTTPFinalIntentError가 이미 고정했고, 연결 전
// 송신 실패(transport_failed)는 전달하지 않은 것이라 결과 불명이 아니다(C02). 이 검사는 그 밖의 전달된 시도까지 같은
// 기준으로 본다. 어느 결과가 기존 분류 검사로 받혔는지는 신뢰 결과의 종료 상태가 있어야 알 수 있다. Worker 세션은 도구 완료보다 먼저 끝날 수 있어서
// (취소 때 SDK가 중단 결과를 먼저 만든다) 이 intent의 최종 호출 결과가 모두 관찰될 때까지 ctx 기한 안에서 기다린다.
// 기다리다 기한이 지나면 관찰된 결과만 본다. 관찰되지 않은 호출은 마감 대조가 따로 실패시킨다.
func (r *E2HTTPRuntime) attemptUnknownIntentError(ctx context.Context, t *e2HTTPRuntimeTask, lease *E2HTTPRunOptions, intentID int64, finalErr error) error {
	// 원격 실행은 이 프로세스에 시도 기록이 없다. 마감 판정도 원격 실행을 받지 않는다(closure_execution_missing).
	if errors.Is(finalErr, agent.ErrE2HTTPUnknown) || t == nil || lease == nil || lease.Execution == nil || e2HTTPNil(lease.Runner) {
		return finalErr
	}
	program := lease.programCalls.snapshot()
	var pending []string
	for _, call := range lease.Runner.Calls() {
		// 서버가 연 호출(인증 준비·다시 로그인)은 Worker 결과가 없다.
		if call.IntentID == intentID && call.Final && !program[call.CallRef] {
			pending = append(pending, call.CallRef)
		}
	}
	var attempts map[string][]e2proxy.Receipt
	for _, result := range t.awaitIntentResults(ctx, intentID, pending) {
		if firstDiagnosisClosedToolOutcome(result) {
			continue
		}
		if attempts == nil {
			attempts = firstDiagnosisCallAttempts(lease.Execution)
		}
		if accepted, unknown := firstDiagnosisStoppedToolOutcome(result, attempts[result.Call.CallRef]); accepted && unknown {
			return errors.Join(finalErr, agent.ErrE2HTTPUnknown)
		}
	}
	return finalErr
}

// awaitIntentResults는 pending 호출이 모두 관찰(저장 또는 대조 실패 표시)되거나 결과 기록이 실패하거나 ctx가 끝날 때까지
// 기다린 뒤 이 intent의 저장된 신뢰 결과 사본을 돌려준다. 기한이 지나면 그때까지 저장된 것만 돌려준다.
func (t *e2HTTPRuntimeTask) awaitIntentResults(ctx context.Context, intentID int64, pending []string) []e2runner.TrustedResult {
	for {
		t.mu.Lock()
		stored := make(map[string]bool, len(t.results))
		var mine []e2runner.TrustedResult
		for _, result := range t.results {
			stored[result.Call.CallRef] = true
			if result.Call.IntentID == intentID {
				mine = append(mine, e2HTTPCloneTrusted(result))
			}
		}
		done := t.resultsFailed
		if !done {
			done = true
			for _, ref := range pending {
				if !stored[ref] && !t.untrustedCalls[ref] {
					done = false
					break
				}
			}
		}
		wake := t.observedWaitLocked()
		t.mu.Unlock()
		if done {
			return mine
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return mine
		}
	}
}

// e2HTTPWorkerSessions는 Worker를 시작할 때 줄 시험 계정 세션이다. 다시 로그인이 바꾼 지금 세션을 읽는다(#550).
func e2HTTPWorkerSessions(mode e2proxy.HeaderMode, lease *E2HTTPRunOptions) map[string]http.Header {
	return e2HTTPSessionsFor(mode, lease.CurrentSessions())
}

// e2HTTPRefreshFor는 Worker의 refresh_session 경로다. 세션을 주는 실행(시험 계정 실행, 인증된 계정 있음)에만 준다.
// e2HTTPWorkerCredentials는 Worker 지침에 넣을 등록 계정 자격이다. 시험 계정 실행(자격 헤더 통과)에서만 준다: 모델이 받은
// 세션을 스스로 실어 보내야 하기 때문이다.
func e2HTTPWorkerCredentials(mode e2proxy.HeaderMode, lease *E2HTTPRunOptions) []agent.E2HTTPAccountCredential {
	if lease == nil || mode != e2proxy.HeaderTestAccountPassthrough {
		return nil
	}
	return append([]agent.E2HTTPAccountCredential(nil), lease.Credentials...)
}

func e2HTTPRefreshFor(mode e2proxy.HeaderMode, lease *E2HTTPRunOptions) agent.E2HTTPRefreshSession {
	if lease == nil || mode != e2proxy.HeaderTestAccountPassthrough || len(lease.CurrentSessions()) == 0 || lease.RefreshSession == nil {
		return nil
	}
	return lease.RefreshSession
}

// e2HTTPSessionsFor는 Worker 지침에 줄 시험 계정 세션이다. 시험 계정 실행(test_account_passthrough)에서만 준다 — 다른
// 헤더 모드에서는 모델이 쓴 자격 헤더를 E②가 거부하므로 세션을 줄 이유가 없다.
func e2HTTPSessionsFor(mode e2proxy.HeaderMode, sessions E2HTTPSessions) map[string]http.Header {
	if mode != e2proxy.HeaderTestAccountPassthrough || len(sessions) == 0 {
		return nil
	}
	out := make(map[string]http.Header, len(sessions))
	for label, header := range sessions {
		out[label] = header.Clone()
	}
	return out
}
