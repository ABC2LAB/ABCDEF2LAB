// 승인된 기존 공급자의 수명과 실제 검증 경계만 조립하며 권한을 발급하지 않는다.
package server

import (
	"context"
	"errors"
	"github.com/Autumn-27/artex/internal/privateinput"
	"net/http"
	"sync/atomic"
	"time"
)

var ErrControlledReadAssemblyClosing = errors.New("controlled_read_assembly_close_in_progress")

type ControlledReadAssemblyShutdown struct {
	BlockAdmission func(context.Context, *Server) error
	CancelAndDrain func(context.Context, *Server) error
	CheckResults   func(context.Context, *Server) error
}

type ControlledReadAssemblyStartOptions struct {
	InputAuthority            ControlledReadAssemblyInputAuthority
	Host                      ControlledReadAssemblyHostConfig
	SkillDir, DataDir, KeyDir string
	SnapshotDir               string
	Shutdown                  ControlledReadAssemblyShutdown
	CleanupTimeout            time.Duration
}

type ControlledReadAssemblyRuntime struct {
	server           *Server
	handler          http.Handler
	host             *ControlledReadAssemblyHost
	cancel           context.CancelFunc
	shutdown         ControlledReadAssemblyShutdown
	admissionStopped atomic.Bool
	closing          atomic.Bool
	closed           atomic.Bool
	resultErr        error
	phase            int // Close 배타 CAS를 획득한 호출만 접근한다.
}

type controlledReadAssemblyStageError struct {
	stage string
	cause error
}

func (e controlledReadAssemblyStageError) Error() string {
	return "controlled_read_assembly_" + e.stage
}
func (e controlledReadAssemblyStageError) Unwrap() error    { return e.cause }
func (e controlledReadAssemblyStageError) GoString() string { return e.Error() }

func StartControlledReadAssembly(ctx context.Context, manager *Manager, options ControlledReadAssemblyStartOptions) (*ControlledReadAssemblyRuntime, error) {
	transferred := false
	defer func() {
		if !transferred && !nilFinalizationValue(options.Host.Sources.Root) {
			options.Host.Sources.Root.Release()
		}
	}()
	if ctx == nil || ctx.Err() != nil || manager == nil || manager.pg == nil || manager.pg.DB == nil ||
		options.SkillDir == "" || options.DataDir == "" || options.KeyDir == "" || options.CleanupTimeout <= 0 ||
		options.Shutdown.BlockAdmission == nil || options.Shutdown.CancelAndDrain == nil || options.Shutdown.CheckResults == nil {
		return nil, ErrControlledReadHostConfiguration
	}
	if privateinput.CheckDirectory(options.DataDir, false) != nil || privateinput.CheckDirectory(options.SkillDir, false) != nil || privateinput.CheckDirectory(options.KeyDir, true) != nil {
		return nil, controlledReadAssemblyStageError{"directories", ErrControlledReadHostConfiguration}
	}
	run, cancel := context.WithCancel(ctx)
	transferred = true // Host는 사전 검사 실패에도 Root를 소비한다.
	host, err := NewControlledReadAssemblyHost(run, options.Host)
	if err != nil {
		cancel()
		return nil, controlledReadAssemblyStageError{"providers", ErrControlledReadHostConfiguration}
	}
	runtime := &ControlledReadAssemblyRuntime{host: host, cancel: cancel, shutdown: options.Shutdown}
	runtime.server, err = newWithControlledReadAssembly(run, manager, options.SkillDir, options.DataDir, options.KeyDir, host.Options, options.SnapshotDir, options.InputAuthority)
	if err != nil {
		cancel()
		runtime.admissionStopped.Store(true)
		runtime.phase = 3
		cleanup, finish := context.WithTimeout(context.WithoutCancel(ctx), options.CleanupTimeout)
		defer finish()
		if closeErr := runtime.Close(cleanup); closeErr != nil {
			return runtime, controlledReadAssemblyStageError{"startup_cleanup", closeErr}
		}
		return nil, ErrControlledReadHostConfiguration
	}
	if run.Err() != nil || host.Lifetime.CheckRoot(run) != nil {
		cleanup, finish := context.WithTimeout(context.WithoutCancel(ctx), options.CleanupTimeout)
		defer finish()
		if closeErr := runtime.Close(cleanup); closeErr != nil {
			return runtime, controlledReadAssemblyStageError{"startup_cleanup", closeErr}
		}
		return nil, controlledReadAssemblyStageError{"startup_revoked", ErrControlledReadHostConfiguration}
	}
	runtime.handler = runtime.server.Handler()
	return runtime, nil
}

func (r *ControlledReadAssemblyRuntime) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if r == nil || r.handler == nil || r.host == nil || r.host.Lifetime == nil || r.admissionStopped.Load() || r.host.Lifetime.CheckRoot(request.Context()) != nil {
			http.Error(w, "controlled_read_unavailable", http.StatusServiceUnavailable)
			return
		}
		r.handler.ServeHTTP(w, request)
	})
}

func (r *ControlledReadAssemblyRuntime) Close(ctx context.Context) error {
	if r == nil || ctx == nil || r.host == nil || r.cancel == nil {
		return ErrControlledReadHostConfiguration
	}
	if !r.closing.CompareAndSwap(false, true) {
		return ErrControlledReadAssemblyClosing
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
			return controlledReadAssemblyStageError{"block_admission", err}
		}
		r.phase = 1
	}
	if r.phase == 1 {
		if err := r.shutdown.CancelAndDrain(ctx, r.server); err != nil {
			return controlledReadAssemblyStageError{"drain", err}
		}
		r.phase = 2
	}
	if r.phase == 2 {
		if err := r.shutdown.CheckResults(ctx, r.server); err != nil {
			if !errors.Is(err, ErrFirstDiagnosisOutcome) {
				return controlledReadAssemblyStageError{"results", err}
			}
			r.resultErr = controlledReadAssemblyStageError{"results", err}
		}
		r.phase = 3
	}
	if err := r.host.Close(ctx); err != nil {
		return controlledReadAssemblyStageError{"release", err}
	}
	r.closed.Store(true)
	return r.resultErr
}
