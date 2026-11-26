package main

import (
	"context"
	"errors"
	"log"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/server"
)

const selectedCandidateFinalTimeout = 170 * time.Second

func (b *urlPreparationRuntime) scheduleCandidateLLMAssessment(ctx context.Context, requestID int64) (string, error) {
	if b == nil || b.ctx == nil || b.ctx.Err() != nil || requestID <= 0 {
		return "", errPreparation
	}
	pool, err := b.candidateDatabase(ctx)
	if err != nil {
		return "", errPreparation
	}
	_, err = server.ReadCandidateAssessmentContext(ctx, pool, requestID)
	pool.Close()
	if err != nil {
		return "", errPreparation
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.candidateWorkers == nil {
		b.candidateWorkers = map[int64]context.CancelFunc{}
	}
	if _, running := b.candidateWorkers[requestID]; running {
		return "scheduled", nil
	}
	bounded, cancel := context.WithTimeout(b.ctx, selectedCandidateFinalTimeout)
	b.candidateWorkers[requestID] = cancel
	b.candidateWG.Add(1)
	go func() {
		defer b.candidateWG.Done()
		defer func() { b.mu.Lock(); delete(b.candidateWorkers, requestID); b.mu.Unlock() }()
		defer cancel()
		if err := b.executeCandidateLLMAssessment(bounded, requestID); err != nil {
			log.Printf("[candidate-llm-assessment] request %d recovery failed", requestID)
		}
	}()
	return "scheduled", nil
}

func (b *urlPreparationRuntime) executeCandidateLLMAssessment(ctx context.Context, requestID int64) (resultErr error) {
	pool, err := b.candidateDatabase(ctx)
	if err != nil {
		return errPreparation
	}
	defer pool.Close()
	defer func() {
		if resultErr == nil {
			return
		}
		finish, stop := context.WithTimeout(context.WithoutCancel(b.ctx), 10*time.Second)
		defer stop()
		state, reason := "failed", "final_model_unavailable"
		if errors.Is(ctx.Err(), context.Canceled) {
			state, reason = "cancelled", "candidate_execution_cancelled"
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			state, reason = "expired", "final_model_timeout"
		}
		if candidateLLMTerminalEvent(finish, pool, requestID, state, reason) != nil {
			log.Printf("[candidate-llm-assessment] request %d failure record unavailable", requestID)
		}
	}()
	selected, err := server.ReadCandidateAssessmentContext(ctx, pool, requestID)
	if err != nil {
		return errPreparation
	}
	owner := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-final-owner", Command: diagnosisprep.AuthorizeTask,
		Principal: "ARTEX", TaskID: strconv.FormatInt(selected.Original.TaskID, 10)}
	response, err := b.endpoint.Call(ctx, owner)
	if err != nil || diagnosisprep.ValidateResponse(owner, response) != nil || response.Authorized == nil || !*response.Authorized ||
		response.Job == nil || response.Job.Scope == nil || response.Job.TaskID != owner.TaskID {
		return errPreparation
	}
	l, err := b.load("ARTEX", response.Job.Input)
	if err != nil || l == nil {
		return errPreparation
	}
	defer l.clearMaterials()
	if l.productTarget == nil || l.productTarget.Target.ID != selected.Plan.TargetID || l.productTarget.Target.Revision != selected.Plan.TargetRevision ||
		l.current() != nil || l.launch.ModelProfileID != selected.Analysis.ProfileID {
		return errPreparation
	}
	current, _, err := b.product.targets.view("ARTEX", selected.Plan.TargetID)
	if err != nil || current == nil || !server.CandidateLLMPlanStillApproved(time.Now().UTC(), selected.Plan, response.Job.Input, *response.Job.Scope, *current) {
		return errPreparation
	}
	ledger, ok := b.endpoint.(interface {
		ReserveCandidateBudget(context.Context, string, int64, string) error
		FinishCandidateBudget(int64)
	})
	if !ok || ledger.ReserveCandidateBudget(ctx, owner.TaskID, requestID, selected.Original.Candidate.EvidenceDigest) != nil {
		return errPreparation
	}
	defer ledger.FinishCandidateBudget(requestID)
	jobID := "candidate-llm-final-" + strconv.FormatInt(requestID, 10)
	bounded, cancel := context.WithTimeout(ctx, selectedCandidateFinalTimeout)
	defer cancel()
	release, err := b.product.attach(*l.productTarget, jobID, cancel)
	if err != nil {
		return errPreparation
	}
	defer release()
	if l.hold() != nil {
		return errPreparation
	}
	defer l.releaseInputs()
	if l.current() != nil {
		return errPreparation
	}
	limits := l.profile.Budget
	limits.ModelRequests -= int64(selected.Analysis.ModelRequests)
	limits.InputTokens -= selected.Analysis.InputTokens
	limits.OutputTokens -= selected.Analysis.OutputTokens
	limits.CostUSDMicros -= selected.Analysis.CostUSDMicros
	if limits.ModelRequests < 1 || limits.InputTokens < l.profile.InputTokensPerCall ||
		limits.OutputTokens < l.profile.OutputTokensPerCall || limits.DurationSeconds <= 0 {
		return errPreparation
	}
	limits.ModelRequests = 1
	limits.InputTokens = l.profile.InputTokensPerCall
	limits.OutputTokens = l.profile.OutputTokensPerCall
	if limits.CostUSDMicros > 200000 {
		limits.CostUSDMicros = 200000
	}
	if limits.DurationSeconds > int64(selectedCandidateFinalTimeout/time.Second) {
		limits.DurationSeconds = int64(selectedCandidateFinalTimeout / time.Second)
	}
	observe := func(diagnosisprep.ModelUsage) error { return nil }
	var budget *server.FirstDiagnosisModelBudget
	if l.launch.ModelExecutionMode == "openrouter" {
		budget, err = server.NewFirstDiagnosisOpenRouterRunBudget(limits, l.profile.InputTokensPerCall,
			l.profile.OutputTokensPerCall, l.launch.OpenRouterManifestPath, "candidate-llm-"+strconv.FormatInt(requestID, 10), observe)
	} else {
		limits.CostUSDMicros = 0
		budget, err = server.NewFirstDiagnosisModelBudget(limits, l.profile.InputTokensPerCall, l.profile.OutputTokensPerCall, observe)
	}
	if err != nil {
		return errPreparation
	}
	options, err := l.launch.modelOptions()
	if err != nil {
		return errPreparation
	}
	options.Budget = budget
	model, err := server.NewSelectedCandidateModel(bounded, pool, l.launch.ModelProfileID, options)
	if err != nil {
		return errPreparation
	}
	if server.BeginCandidateAssessment(bounded, pool, selected) != nil {
		return errPreparation
	}
	admit := func(call context.Context) error {
		if l.current() != nil || call.Err() != nil {
			return errPreparation
		}
		var step int
		var state string
		if pool.QueryRowContext(call, `SELECT step,process_state FROM diagnostic_candidate_verification_events
WHERE request_id=$1 ORDER BY step DESC LIMIT 1`, requestID).Scan(&step, &state) != nil || step != 8 || state != "assessing" {
			return errPreparation
		}
		fresh, err := b.endpoint.Call(call, owner)
		if err != nil || diagnosisprep.ValidateResponse(owner, fresh) != nil || fresh.Authorized == nil || !*fresh.Authorized ||
			fresh.Job == nil || fresh.Job.Scope == nil || fresh.Job.TaskID != owner.TaskID {
			return errPreparation
		}
		target, _, err := b.product.targets.view("ARTEX", selected.Plan.TargetID)
		if err != nil || target == nil || !server.CandidateLLMPlanStillApproved(time.Now(), selected.Plan, fresh.Job.Input, *fresh.Job.Scope, *target) {
			return errPreparation
		}
		return nil
	}
	verdict, err := model.Assess(bounded, selected, admit)
	if err != nil {
		return errPreparation
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(b.ctx), 10*time.Second)
	defer stop()
	if server.CompleteCandidateAssessment(finish, pool, selected, l.launch.ModelProfileID, verdict, budget.Snapshot()) != nil {
		return errPreparation
	}
	return nil
}
