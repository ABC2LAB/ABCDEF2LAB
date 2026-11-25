package server

import (
	"context"
	"errors"
	"log"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/candidateverify"
)

// ExecuteSelectedCandidateActions starts no Planner or model worker. It sends
// only the server-validated plan through the new E² authority and returns
// after drain and seal. The semantic verdict belongs to a later model call.
func ExecuteSelectedCandidateActions(ctx context.Context, manager *Manager, options FirstDiagnosisHTTPStartOptions,
	taskID int64, runRef string, plan CandidateLLMPlan) ([]candidateverify.StoredObservationV2, error) {
	if ctx == nil || ctx.Err() != nil || manager == nil || taskID <= 0 || runRef == "" ||
		plan.Version != "candidate-llm-plan/v1" || len(plan.Actions) < 1 || len(plan.Actions) > 3 ||
		plan.MaxRequests != len(plan.Actions) || !candidateVerificationOrigin.MatchString(plan.Origin) {
		return nil, ErrCandidateExecutionBinding
	}
	for _, action := range plan.Actions {
		if action.Method != "GET" && action.Method != "HEAD" || !validSelectedExactPath(action.ExactPath) {
			return nil, ErrCandidateExecutionBinding
		}
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
	intentID, err := task.Store.AddIntent(map[string]any{"summary": "Verify one selected candidate within an approved read plan"}, 0, nil, "candidate_llm_verification_v1")
	if err != nil || intentID <= 0 {
		return nil, ErrCandidateExecutionBinding
	}
	if _, err := manager.StampTaskFirstRun(id); err != nil || manager.SetTaskPaused(id, false) != nil || manager.SetTaskStatus(id, "running") != nil {
		return nil, ErrCandidateExecutionBinding
	}
	results, runErr := runtime.RunSelectedCandidateActions(ctx, intentID, plan.Origin, plan.Actions)
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), options.CleanupTimeout)
	defer cancel()
	pauseErr := manager.SetTaskPaused(id, true)
	closeErr := runtime.Close(cleanup)
	if pauseErr != nil || closeErr != nil || runErr != nil || len(results) != len(plan.Actions) {
		log.Printf("[candidate-e2] action finalization unavailable: paused=%t closed=%t run=%t count=%t",
			pauseErr == nil, closeErr == nil, runErr == nil, len(results) == len(plan.Actions))
		return nil, errors.Join(ErrCandidateExecutionBinding, pauseErr, closeErr, runErr)
	}
	for _, result := range results {
		if !result.Final || !result.ProcessDrained || !result.ProxyDrained || !result.Execution.AuditComplete || result.Classification != "completed" {
			log.Printf("[candidate-e2] result incomplete: final=%t process=%t proxy=%t audit=%t classified=%t",
				result.Final, result.ProcessDrained, result.ProxyDrained, result.Execution.AuditComplete, result.Classification == "completed")
			return nil, ErrCandidateExecutionBinding
		}
	}
	projected, err := candidateverify.ProjectTrustedSelected(taskID, runRef, results)
	if err != nil || len(projected) != len(plan.Actions) {
		log.Printf("[candidate-e2] observation projection unavailable: valid=%t count=%t", err == nil, len(projected) == len(plan.Actions))
		return nil, ErrCandidateExecutionBinding
	}
	if won, err := manager.SetTaskStatusGuarded(id, "done"); err != nil || !won {
		return nil, ErrCandidateExecutionBinding
	}
	completed = true
	return projected, nil
}
