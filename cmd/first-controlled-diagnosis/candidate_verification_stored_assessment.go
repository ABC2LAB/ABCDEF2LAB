package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/server"
)

// An interrupted final model call has an unknown provider outcome and is
// never resent. An analysis_ready request has no final send fence yet.
func (b *urlPreparationRuntime) recoverCandidateStoredAssessments(ctx context.Context, pool *sql.DB) error {
	rows, err := pool.QueryContext(ctx, `SELECT q.id,e.step,e.process_state FROM diagnostic_candidate_verification_requests q
JOIN LATERAL (SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
LEFT JOIN diagnostic_candidate_verification_plans p ON p.request_id=q.id
WHERE q.method='llm/v1' AND p.request_id IS NULL AND
 ((e.step=3 AND e.process_state='analysis_ready') OR (e.step=4 AND e.process_state='assessing'))
ORDER BY q.id LIMIT 1025`)
	if err != nil {
		return errPreparation
	}
	type pending struct {
		id    int64
		step  int
		state string
	}
	var items []pending
	for rows.Next() {
		var item pending
		if rows.Scan(&item.id, &item.step, &item.state) != nil {
			rows.Close()
			return errPreparation
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(items) > 1024 {
		return errPreparation
	}
	for _, item := range items {
		if item.step == 4 {
			if candidateLLMTerminalEvent(ctx, pool, item.id, "failed", "final_model_interrupted") != nil {
				return errPreparation
			}
			continue
		}
		if err := b.scheduleCandidateStoredAssessment(ctx, item.id); err != nil &&
			!errors.Is(err, server.ErrCandidateStoredLLMEvidence) {
			log.Printf("[candidate-stored-assessment] request %d recovery schedule unavailable", item.id)
		}
	}
	return nil
}

func (b *urlPreparationRuntime) cancelCandidateStoredAssessment(ctx context.Context, idText, digest string) (string, error) {
	id, err := strconv.ParseInt(idText, 10, 64)
	if b == nil || err != nil || id <= 0 || strconv.FormatInt(id, 10) != idText || len(digest) != 64 {
		return "", errPreparation
	}
	pool, err := b.candidateDatabase(ctx)
	if err != nil {
		return "", errPreparation
	}
	defer pool.Close()
	var storedDigest, state string
	var step int
	if pool.QueryRowContext(ctx, `SELECT q.original_evidence_digest,e.step,e.process_state
FROM diagnostic_candidate_verification_requests q
JOIN LATERAL (SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
LEFT JOIN diagnostic_candidate_verification_plans p ON p.request_id=q.id
WHERE q.id=$1 AND q.method='llm/v1' AND p.request_id IS NULL`, id).Scan(&storedDigest, &step, &state) != nil ||
		storedDigest != digest || step != 5 || state != "cancelled" {
		return "", errPreparation
	}
	b.mu.RLock()
	cancel := b.candidateWorkers[id]
	b.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	return "cancelled", nil
}

// A stored-only assessment can be queued only while the analysis is ready and
// the original run has independent, sealed observations. It sends no E² call.
func (b *urlPreparationRuntime) scheduleCandidateStoredAssessment(ctx context.Context, requestID int64) error {
	if b == nil || b.ctx == nil || b.ctx.Err() != nil || requestID <= 0 {
		return errPreparation
	}
	pool, err := b.candidateDatabase(ctx)
	if err != nil {
		return errPreparation
	}
	_, err = server.ReadCandidateStoredAssessmentContext(ctx, pool, requestID)
	pool.Close()
	if err != nil {
		return err
	}
	b.mu.Lock()
	if b.candidateWorkers == nil {
		b.candidateWorkers = map[int64]context.CancelFunc{}
	}
	if _, running := b.candidateWorkers[requestID]; running {
		b.mu.Unlock()
		return nil
	}
	bounded, cancel := context.WithTimeout(b.ctx, selectedCandidateFinalTimeout)
	b.candidateWorkers[requestID] = cancel
	b.candidateWG.Add(1)
	b.mu.Unlock()
	go func() {
		defer b.candidateWG.Done()
		defer func() { b.mu.Lock(); delete(b.candidateWorkers, requestID); b.mu.Unlock() }()
		defer cancel()
		if b.executeCandidateStoredAssessment(bounded, requestID) != nil {
			log.Printf("[candidate-stored-assessment] request %d ended without a verdict", requestID)
		}
	}()
	return nil
}

func (b *urlPreparationRuntime) executeCandidateStoredAssessment(ctx context.Context, requestID int64) (resultErr error) {
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
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			state, reason = "expired", "final_model_timeout"
		}
		if candidateLLMTerminalEvent(finish, pool, requestID, state, reason) != nil {
			log.Printf("[candidate-stored-assessment] request %d failure record unavailable", requestID)
		}
	}()
	selected, err := server.ReadCandidateStoredAssessmentContext(ctx, pool, requestID)
	if err != nil {
		return errPreparation
	}
	var requester string
	if pool.QueryRowContext(ctx, `SELECT requester FROM diagnostic_candidate_verification_requests WHERE id=$1 AND method='llm/v1'`, requestID).Scan(&requester) != nil || requester != "ARTEX" {
		return errPreparation
	}
	owner := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-stored-final-owner", Command: diagnosisprep.AuthorizeTask,
		Principal: requester, TaskID: strconv.FormatInt(selected.Original.TaskID, 10)}
	response, err := b.endpoint.Call(ctx, owner)
	if err != nil || diagnosisprep.ValidateResponse(owner, response) != nil || response.Authorized == nil || !*response.Authorized ||
		response.Job == nil || response.Job.Scope == nil || response.Job.TaskID != owner.TaskID {
		return errPreparation
	}
	l, err := b.load(requester, response.Job.Input)
	if err != nil || l == nil {
		return errPreparation
	}
	defer l.clearMaterials()
	if l.productTarget == nil || l.current() != nil || l.launch.ModelProfileID != selected.Analysis.ProfileID ||
		response.Job.Scope.ExpiresAt.Before(time.Now().UTC()) {
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
	jobID := "candidate-llm-" + strconv.FormatInt(requestID, 10)
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
			l.profile.OutputTokensPerCall, l.launch.OpenRouterManifestPath, jobID, observe)
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
	if err != nil || server.BeginStoredCandidateAssessment(bounded, pool, selected) != nil {
		return errPreparation
	}
	admit := func(call context.Context) error {
		if l.current() != nil || call.Err() != nil {
			return errPreparation
		}
		var step int
		var state string
		if pool.QueryRowContext(call, `SELECT step,process_state FROM diagnostic_candidate_verification_events
WHERE request_id=$1 ORDER BY step DESC LIMIT 1`, requestID).Scan(&step, &state) != nil || step != 4 || state != "assessing" {
			return errPreparation
		}
		fresh, err := b.endpoint.Call(call, owner)
		if err != nil || diagnosisprep.ValidateResponse(owner, fresh) != nil || fresh.Authorized == nil || !*fresh.Authorized ||
			fresh.Job == nil || fresh.Job.Scope == nil || fresh.Job.TaskID != owner.TaskID ||
			!time.Now().UTC().Before(fresh.Job.Scope.ExpiresAt) {
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
	if server.CompleteStoredCandidateAssessment(finish, pool, selected, l.launch.ModelProfileID, verdict, budget.Snapshot()) != nil {
		return errPreparation
	}
	return nil
}
