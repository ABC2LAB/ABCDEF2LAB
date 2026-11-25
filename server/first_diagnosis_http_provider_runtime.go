package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/internal/privateinput"
)

type FirstDiagnosisHTTPStartOptions struct {
	Host                      FirstDiagnosisHTTPHostConfig
	SkillDir, DataDir, KeyDir string
	LocalLLMManifestPath      string
	ModelExecutionMode        string
	ModelBudget               *FirstDiagnosisModelBudget
	OpenRouterManifestPath    string
	Supervisor                FirstDiagnosisHTTPHostSupervisor
	Hooks                     FirstDiagnosisHTTPHooks
	Shutdown                  ControlledReadAssemblyShutdown
	CleanupTimeout            time.Duration
}

func (FirstDiagnosisHTTPStartOptions) String() string {
	return "server.FirstDiagnosisHTTPStartOptions[protected]"
}
func (FirstDiagnosisHTTPStartOptions) GoString() string {
	return "server.FirstDiagnosisHTTPStartOptions[protected]"
}

type FirstDiagnosisHTTPRuntime struct {
	server           *Server
	handler          http.Handler
	host             *FirstDiagnosisHTTPHost
	cancel           context.CancelFunc
	shutdown         ControlledReadAssemblyShutdown
	admissionStopped atomic.Bool
	closing          atomic.Bool
	closed           atomic.Bool
	phase            int
	resultErr        error
}

func (*FirstDiagnosisHTTPRuntime) String() string {
	return "server.FirstDiagnosisHTTPRuntime[protected]"
}
func (*FirstDiagnosisHTTPRuntime) GoString() string {
	return "server.FirstDiagnosisHTTPRuntime[protected]"
}

// 실제 DB 행만 연결한다. 새 task/intent 생성이나 기존 실행의 재개는 하지 않는다.
func firstDiagnosisHTTPTask(ctx context.Context, m *Manager, host *FirstDiagnosisHTTPHost) (*Task, error) {
	if ctx == nil || ctx.Err() != nil || m == nil || m.pg == nil || m.pg.DB == nil || m.tasks == nil || host == nil || host.CheckRoot(ctx) != nil {
		return nil, ErrFirstDiagnosisHTTPHost
	}
	taskID, expID := host.ExpectedTask()
	number, err := strconv.ParseInt(taskID, 10, 64)
	if err != nil || number <= 0 || expID <= 0 {
		return nil, ErrFirstDiagnosisHTTPHost
	}
	m.taskStateMu.Lock()
	defer m.taskStateMu.Unlock()
	row, err := m.pg.GetTask(number)
	if err != nil || row == nil || row.ID != number || row.ExplorationID != expID || !row.Paused || row.Queued || db.IsTerminal(row.Status) || row.FirstRunAt != nil || row.CompletedAt != nil || ctx.Err() != nil || host.CheckRoot(ctx) != nil {
		return nil, ErrFirstDiagnosisHTTPHost
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tasks[taskID]
	if t == nil {
		t = taskFromPG(row, m.pg.Exploration(expID), m.interceptor)
		m.tasks[taskID] = t
	}
	state := t.lifecycleSnapshot()
	if t.ID != taskID || t.ExpID != expID || t.Store == nil || t.Store.ID() != expID || !state.Paused || state.Queued || state.FirstRunAt != 0 || state.CompletedAt != 0 || db.IsTerminal(state.Status) {
		return nil, ErrFirstDiagnosisHTTPHost
	}
	if err = host.runtime.admitTaskWithWorkers(ctx, t, m.Workers()); err != nil {
		return nil, ErrFirstDiagnosisHTTPHost
	}
	return t, nil
}

// firstDiagnosisHTTPModelConfig는 모델 설정을 검사하고, 외부 API(openrouter) 여부를 호스트 설정에 싣는다. 호스트는 그 값으로
// 인증 준비(auth)가 있는 정책을 거부한다(CLAUDE.md 규칙 2, C14).
func firstDiagnosisHTTPModelConfig(options FirstDiagnosisHTTPStartOptions) (FirstDiagnosisModelOptions, FirstDiagnosisHTTPHostConfig, error) {
	modelOptions := FirstDiagnosisModelOptions{Mode: options.ModelExecutionMode, LocalManifestPath: options.LocalLLMManifestPath, OpenRouterManifestPath: options.OpenRouterManifestPath, Budget: options.ModelBudget}
	_, external, err := modelOptions.load()
	if err != nil {
		return modelOptions, FirstDiagnosisHTTPHostConfig{}, err
	}
	host := options.Host
	host.ExternalModel = external != nil
	// 검색은 검색 제공자로 나가므로 외부 API 실행에서만 켠다. 완전 로컬 모드는 외부 송신 전면 차단이다(규칙 8).
	if !host.WebSearch.Valid() || host.WebSearch.Enabled() && !host.ExternalModel {
		return modelOptions, FirstDiagnosisHTTPHostConfig{}, ErrFirstDiagnosisModel
	}
	return modelOptions, host, nil
}

func StartFirstDiagnosisHTTP(ctx context.Context, manager *Manager, options FirstDiagnosisHTTPStartOptions) (*FirstDiagnosisHTTPRuntime, error) {
	if ctx == nil || ctx.Err() != nil || manager == nil || manager.pg == nil || manager.pg.DB == nil || e2HTTPNil(options.Supervisor) || options.CleanupTimeout <= 0 || options.CleanupTimeout > 2*time.Minute || options.Shutdown.BlockAdmission == nil || options.Shutdown.CancelAndDrain == nil || options.Shutdown.CheckResults == nil {
		return nil, ErrFirstDiagnosisHTTPHost
	}
	modelOptions, hostConfig, err := firstDiagnosisHTTPModelConfig(options)
	if err != nil {
		return nil, ErrFirstDiagnosisHTTPHost
	}
	options.Host = hostConfig
	if privateinput.CheckDirectory(options.SkillDir, false) != nil || privateinput.CheckDirectory(options.DataDir, false) != nil || privateinput.CheckDirectory(options.KeyDir, true) != nil {
		return nil, ErrFirstDiagnosisHTTPHost
	}
	if CheckFirstDiagnosisHTTPRuntimeDatabase(ctx, manager.pg.DB) != nil {
		return nil, ErrFirstDiagnosisHTTPHost
	}
	run, cancel := context.WithCancel(ctx)
	options.Hooks.RequestApprovals = newRequestApprovalService(manager.pg)
	options.Hooks.SealChecked = func(ctx context.Context, s E2HTTPSealCheck) error {
		return manager.pg.RecordSealCheck(ctx, e2HTTPSealCheckRecord(s))
	}
	host, err := NewFirstDiagnosisHTTPHost(run, options.Host, options.Supervisor, options.Hooks)
	if err != nil {
		cancel()
		return nil, ErrFirstDiagnosisHTTPHost
	}
	r := &FirstDiagnosisHTTPRuntime{host: host, cancel: cancel, shutdown: options.Shutdown}
	attached := false
	fail := func() (*FirstDiagnosisHTTPRuntime, error) {
		r.admissionStopped.Store(true)
		host.Revoke()
		cancel()
		// Attach 전 생성자는 엔진을 시작하지 않으며 감독자도 아직 서버를 소유하지 않는다.
		if !attached {
			r.phase = 3
		}
		cleanup, finish := context.WithTimeout(context.WithoutCancel(ctx), options.CleanupTimeout)
		defer finish()
		if err := r.Close(cleanup); err != nil {
			return r, controlledReadAssemblyStageError{"http_startup_cleanup", err}
		}
		return nil, ErrFirstDiagnosisHTTPHost
	}
	task, err := firstDiagnosisHTTPTask(run, manager, host)
	if err != nil {
		return fail()
	}
	r.server, err = newFirstDiagnosisHTTPServer(run, manager, options.SkillDir, options.DataDir, options.KeyDir, host.runtime, modelOptions)
	if err != nil || run.Err() != nil || host.CheckRoot(run) != nil {
		return fail()
	}
	// 실제 task의 profile/provider 구성이 가능해야 한다. 여기서는 모델 요청을 보내지 않는다.
	if r.server.bindFirstDiagnosisModel(run, task) != nil {
		return fail()
	}
	if r.server.engine == nil || !r.server.engine.ReadyFor(task) {
		return fail()
	}
	if options.Hooks.Exploration != nil {
		stage := options.Hooks.Exploration(r.server)
		if stage == nil {
			return fail()
		}
		// 아직 Attach/Run 전이라 task가 이 options를 동시에 읽지 않는다.
		host.runtime.options.Exploration = stage
	}
	if err = options.Supervisor.Attach(r.server); err != nil {
		return fail()
	}
	attached = true
	if run.Err() != nil || host.CheckRoot(run) != nil {
		return fail()
	}
	r.handler = r.server.Handler()
	return r, nil
}

// TaskID는 검증·등록이 끝난 실제 task의 공개 식별자이며 새 권위가 아니다.
func (r *FirstDiagnosisHTTPRuntime) TaskID() int64 {
	if r == nil || r.host == nil || r.host.authority == nil || r.server == nil {
		return 0
	}
	return r.host.authority.approval().TaskID
}

// RunSelectedCandidateGET uses the already admitted, fresh E² task without
// starting the diagnosis Planner or model worker loop. The caller must bind
// the approved plan, create exactly one intent and close the runtime before
// assessing results. The protected supervisor checks the binding on acquire.
func (r *FirstDiagnosisHTTPRuntime) RunSelectedCandidateGET(ctx context.Context, intentID int64, exactURL string) ([]e2runner.TrustedResult, error) {
	if r == nil || r.server == nil || r.server.m == nil || r.host == nil || r.closing.Load() || r.closed.Load() || intentID <= 0 || r.host.CheckRoot(ctx) != nil {
		return nil, ErrCandidateExecutionBinding
	}
	taskID := strconv.FormatInt(r.TaskID(), 10)
	task := r.server.m.ResolveTask(taskID)
	if task == nil || task.Store == nil {
		return nil, ErrCandidateExecutionBinding
	}
	intent, err := task.Store.GetNode(intentID)
	if err != nil || intent == nil || intent.Kind != db.KindIntent {
		return nil, ErrCandidateExecutionBinding
	}
	return r.host.runtime.runSelectedCandidateGET(ctx, task, intent, exactURL)
}

func (r *FirstDiagnosisHTTPRuntime) SelectedCandidateResults() []e2runner.TrustedResult {
	if r == nil || r.host == nil || r.host.runtime == nil || r.TaskID() <= 0 {
		return nil
	}
	return r.host.runtime.Results(strconv.FormatInt(r.TaskID(), 10))
}

func (r *FirstDiagnosisHTTPRuntime) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if r == nil || r.handler == nil || r.host == nil || r.admissionStopped.Load() || r.host.CheckRoot(request.Context()) != nil {
			http.Error(w, "일반 HTTP 통제 실행을 사용할 수 없습니다.", http.StatusServiceUnavailable)
			return
		}
		r.handler.ServeHTTP(w, request)
	})
}
func (r *FirstDiagnosisHTTPRuntime) Close(ctx context.Context) error {
	if r == nil || ctx == nil || r.host == nil || r.cancel == nil {
		return ErrFirstDiagnosisHTTPHost
	}
	if !r.closing.CompareAndSwap(false, true) {
		return ErrE2HTTPBusy
	}
	defer r.closing.Store(false)
	if r.closed.Load() {
		return r.resultErr
	}
	if ctx.Err() != nil {
		r.admissionStopped.Store(true)
		r.host.Revoke()
		r.cancel()
		return ctx.Err()
	}
	if r.phase == 0 {
		r.admissionStopped.Store(true)
		r.host.Revoke()
		r.cancel()
		if err := r.shutdown.BlockAdmission(ctx, r.server); err != nil {
			return controlledReadAssemblyStageError{"http_block_admission", err}
		}
		r.phase = 1
	}
	if r.phase == 1 {
		if err := r.shutdown.CancelAndDrain(ctx, r.server); err != nil {
			return controlledReadAssemblyStageError{"http_drain", err}
		}
		r.phase = 2
	}
	if r.phase == 2 {
		// drain이 증명된 뒤 결과 실패도 보존한다. 성공으로 승격하거나 검사를 자동 재실행하지 않는다.
		if err := r.shutdown.CheckResults(ctx, r.server); err != nil {
			if !errors.Is(err, ErrFirstDiagnosisOutcome) {
				return controlledReadAssemblyStageError{"http_results", err}
			}
			r.resultErr = controlledReadAssemblyStageError{"http_results", err}
		}
		r.phase = 3
	}
	if err := r.host.Close(ctx); err != nil {
		return errors.Join(r.resultErr, controlledReadAssemblyStageError{"http_release", err})
	}
	r.closed.Store(true)
	return r.resultErr
}

// ResourcesClosed는 의미상 실패와 실제 소유 자원 반환을 구분한다.
func (r *FirstDiagnosisHTTPRuntime) ResourcesClosed() bool { return r != nil && r.closed.Load() }

// AccountAuth는 이 실행의 시험 계정별 인증 준비 결과다(계정을 결속하지 않았으면 nil). 자격·세션 값은 없다.
func (r *FirstDiagnosisHTTPRuntime) AccountAuth() map[string]string {
	if r == nil || r.host == nil {
		return nil
	}
	return r.host.runtime.AccountAuth()
}

// AccountAuthForE3는 현재 회차 세션 결속만 E③ 시작 검사에 제공한다. 저장되는 최초 인증 결과는 AccountAuth가 담당한다.
func (r *FirstDiagnosisHTTPRuntime) AccountAuthForE3() map[string]string {
	if r == nil || r.host == nil || r.host.runtime == nil {
		return nil
	}
	return r.host.runtime.AccountAuthForE3()
}

// RecordProductVerdict는 제품 확정 단계 판정 하나를 이 실행의 DB 역할로 저장한다(INSERT만, 바꾸지 않음, docs/contracts/
// e3-product-verdict-store.md §4·§5).
func (r *FirstDiagnosisHTTPRuntime) RecordProductVerdict(ctx context.Context, v db.ProductVerdict) (db.ProductVerdictResult, error) {
	if r == nil || r.server == nil || r.server.m == nil || r.server.m.pg == nil {
		return db.ProductVerdictResult{}, ErrFirstDiagnosisHTTPHost
	}
	return r.server.m.pg.RecordProductVerdict(ctx, v)
}

// E3Handle은 실행 중인 로컬 E②와 C14 저장소만 제품 감독자에 제공한다.
func (r *FirstDiagnosisHTTPRuntime) E3Handle(taskID string) (*e2proxy.Execution, E2HTTPAccountBindings, bool) {
	exec, accounts, reason := r.E3HandleReason(taskID)
	return exec, accounts, reason == ""
}

// E3HandleReason은 E3Handle과 같고, 연결을 주지 못한 사유 코드를 함께 돌려준다(E2HTTPRuntime.E3HandleReason).
func (r *FirstDiagnosisHTTPRuntime) E3HandleReason(taskID string) (*e2proxy.Execution, E2HTTPAccountBindings, string) {
	switch {
	case r == nil || r.host == nil:
		return nil, nil, E3HandleRuntimeUnavailable
	case r.closing.Load():
		return nil, nil, E3HandleRuntimeClosing
	case r.closed.Load():
		return nil, nil, E3HandleRuntimeClosed
	}
	return r.host.runtime.E3HandleReason(taskID)
}

// e2HTTPSealCheckRecord는 Host 봉인 확인 결과를 저장 기록으로 옮긴다(E1 계약 §6). 봉인 응답은 phase sealed만 온다.
func e2HTTPSealCheckRecord(s E2HTTPSealCheck) db.SealCheckRecord {
	h := s.Result.Head
	out := db.SealCheckRecord{TaskID: s.TaskID, RunRef: s.RunRef, Head: db.ProductCheckpoint{JournalID: h.JournalID, Sequence: h.Sequence, MAC: h.MAC},
		Checks: make([]db.SealCheck, 0, len(s.Result.Checks))}
	for _, c := range s.Result.Checks {
		out.Checks = append(out.Checks, db.SealCheck{Sequence: c.Sequence, MAC: c.MAC, Included: c.Included})
	}
	return out
}
