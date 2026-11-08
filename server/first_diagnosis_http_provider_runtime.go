package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/internal/privateinput"
)

type FirstDiagnosisHTTPStartOptions struct {
	Host                      FirstDiagnosisHTTPHostConfig
	SkillDir, DataDir, KeyDir string
	LocalLLMManifestPath      string
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
	if err = host.runtime.AdmitTask(ctx, t); err != nil {
		return nil, ErrFirstDiagnosisHTTPHost
	}
	return t, nil
}

func StartFirstDiagnosisHTTP(ctx context.Context, manager *Manager, options FirstDiagnosisHTTPStartOptions) (*FirstDiagnosisHTTPRuntime, error) {
	if ctx == nil || ctx.Err() != nil || manager == nil || manager.pg == nil || manager.pg.DB == nil || e2HTTPNil(options.Supervisor) || options.LocalLLMManifestPath == "" || options.CleanupTimeout <= 0 || options.CleanupTimeout > 2*time.Minute || options.Shutdown.BlockAdmission == nil || options.Shutdown.CancelAndDrain == nil || options.Shutdown.CheckResults == nil {
		return nil, ErrFirstDiagnosisHTTPHost
	}
	if privateinput.CheckDirectory(options.SkillDir, false) != nil || privateinput.CheckDirectory(options.DataDir, false) != nil || privateinput.CheckDirectory(options.KeyDir, true) != nil {
		return nil, ErrFirstDiagnosisHTTPHost
	}
	if CheckFirstDiagnosisHTTPRuntimeDatabase(ctx, manager.pg.DB) != nil {
		return nil, ErrFirstDiagnosisHTTPHost
	}
	run, cancel := context.WithCancel(ctx)
	options.Hooks.RequestApprovals = newRequestApprovalService(manager.pg)
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
	r.server, err = NewWithE2HTTPRuntime(run, manager, options.SkillDir, options.DataDir, options.KeyDir, host.runtime, options.LocalLLMManifestPath)
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
