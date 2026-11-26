package server

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/candidateverify"
)

// ExecuteSelectedCandidateGET starts no Planner or model loop. It uses an
// independently issued, candidate-only E² authority and returns only after
// drain and seal. An error never becomes a negative vulnerability verdict.
func ExecuteSelectedCandidateGET(ctx context.Context, manager *Manager, options FirstDiagnosisHTTPStartOptions, taskID int64, runRef, exactURL string) ([]candidateverify.StoredObservationV2, error) {
	if ctx == nil || ctx.Err() != nil || manager == nil || taskID <= 0 || runRef == "" || !candidateFixedGETURL.MatchString(exactURL) {
		return nil, ErrCandidateExecutionBinding
	}
	if options.CleanupTimeout <= 0 {
		options.CleanupTimeout = 30 * time.Second
	}
	options.Shutdown = ControlledReadAssemblyShutdown{
		BlockAdmission: func(_ context.Context, s *Server) error {
			if s == nil || s.engine == nil {
				return ErrCandidateExecutionBinding
			}
			s.firstDiagnosisAdmissionStopped.Store(true)
			return nil
		},
		CancelAndDrain: func(_ context.Context, s *Server) error {
			if s == nil || s.engine == nil {
				return ErrCandidateExecutionBinding
			}
			started := false
			s.engine.started.Range(func(_, _ any) bool { started = true; return false })
			if started {
				return ErrCandidateExecutionBinding
			}
			return nil
		},
		CheckResults: func(context.Context, *Server) error { return nil },
	}
	runtime, err := StartFirstDiagnosisHTTP(ctx, manager, options)
	if err != nil {
		if runtime != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), options.CleanupTimeout)
			_ = runtime.Close(cleanup)
			cancel()
		}
		return nil, ErrCandidateExecutionBinding
	}
	id := strconv.FormatInt(taskID, 10)
	completed := false
	defer func() {
		if !runtime.ResourcesClosed() {
			_ = manager.SetTaskPaused(id, true)
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), options.CleanupTimeout)
			_ = runtime.Close(cleanup)
			cancel()
		}
		if !completed {
			state := "failed"
			if errors.Is(ctx.Err(), context.Canceled) {
				state = "cancelled"
			}
			_, _ = manager.SetTaskStatusGuarded(id, state)
		}
	}()
	if runtime.TaskID() != taskID {
		return nil, ErrCandidateExecutionBinding
	}
	task := manager.ResolveTask(id)
	if task == nil || task.Store == nil {
		return nil, ErrCandidateExecutionBinding
	}
	intentID, err := task.Store.AddIntent(map[string]any{"summary": "Verify one selected candidate with two exact GET observations"}, 0, nil, "candidate_verification_v1")
	if err != nil || intentID <= 0 {
		return nil, ErrCandidateExecutionBinding
	}
	if _, err := manager.StampTaskFirstRun(id); err != nil || manager.SetTaskPaused(id, false) != nil || manager.SetTaskStatus(id, "running") != nil {
		return nil, ErrCandidateExecutionBinding
	}
	_, runErr := runtime.RunSelectedCandidateGET(ctx, intentID, exactURL)
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), options.CleanupTimeout)
	defer cancel()
	pauseErr := manager.SetTaskPaused(id, true)
	closeErr := runtime.Close(cleanup)
	results := runtime.SelectedCandidateResults()
	if pauseErr != nil || closeErr != nil || runErr != nil || len(results) != 2 {
		return nil, errors.Join(ErrCandidateExecutionBinding, pauseErr, closeErr, runErr)
	}
	// Every accepted source must be a finalized, audited E² observation. The
	// projection is bounded and contains no body, URL query or credential.
	for _, result := range results {
		if !result.Final || !result.ProcessDrained || !result.ProxyDrained || !result.Execution.AuditComplete || result.Classification != "completed" {
			return nil, ErrCandidateExecutionBinding
		}
	}
	projected, err := candidateverify.ProjectTrustedV2(taskID, runRef, results)
	if err != nil || len(projected) != 2 {
		return nil, ErrCandidateExecutionBinding
	}
	if won, err := manager.SetTaskStatusGuarded(id, "done"); err != nil || !won {
		return nil, ErrCandidateExecutionBinding
	}
	completed = true
	return projected, nil
}
