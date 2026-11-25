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

const selectedCandidateAnalysisTimeout = 180 * time.Second

func (b *urlPreparationRuntime) scheduleCandidateAnalysis(ctx context.Context, idText, digest string) (string, error) {
	id, err := strconv.ParseInt(idText, 10, 64)
	if b == nil || b.product == nil || b.ctx == nil || b.ctx.Err() != nil || err != nil || id <= 0 ||
		strconv.FormatInt(id, 10) != idText || len(digest) != 64 {
		return "", errPreparation
	}
	pool, err := b.candidateDatabase(ctx)
	if err != nil {
		return "", errPreparation
	}
	defer pool.Close()
	var storedDigest, state string
	err = pool.QueryRowContext(ctx, `SELECT q.original_evidence_digest,e.process_state
FROM diagnostic_candidate_verification_requests q
JOIN LATERAL (SELECT process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.id=$1 AND q.method='llm/v1'`, id).Scan(&storedDigest, &state)
	if err != nil || storedDigest != digest {
		return "", errPreparation
	}
	if state != "requested" {
		return state, nil
	}
	if _, err := server.ReadCandidateSelectedContext(ctx, pool, id); err != nil {
		return "", errPreparation
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ctx.Err() != nil {
		return "", errPreparation
	}
	if b.candidateWorkers == nil {
		b.candidateWorkers = map[int64]context.CancelFunc{}
	}
	if _, running := b.candidateWorkers[id]; running {
		return "scheduled", nil
	}
	bounded, cancel := context.WithTimeout(b.ctx, selectedCandidateAnalysisTimeout)
	b.candidateWorkers[id] = cancel
	b.candidateWG.Add(1)
	go func() {
		defer b.candidateWG.Done()
		defer func() { b.mu.Lock(); delete(b.candidateWorkers, id); b.mu.Unlock() }()
		defer cancel()
		if err := b.executeCandidateAnalysis(bounded, id, digest); err != nil {
			log.Printf("[candidate-analysis] request %d ended without a ready analysis", id)
		}
	}()
	return "scheduled", nil
}

func (b *urlPreparationRuntime) cancelCandidateAnalysis(ctx context.Context, idText, digest string) (string, error) {
	id, err := strconv.ParseInt(idText, 10, 64)
	if b == nil || err != nil || id <= 0 || strconv.FormatInt(id, 10) != idText {
		return "", errPreparation
	}
	pool, err := b.candidateDatabase(ctx)
	if err != nil {
		return "", errPreparation
	}
	defer pool.Close()
	var storedDigest, state string
	if pool.QueryRowContext(ctx, `SELECT q.original_evidence_digest,e.process_state FROM diagnostic_candidate_verification_requests q
JOIN LATERAL (SELECT process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.id=$1 AND q.method='llm/v1'`, id).Scan(&storedDigest, &state) != nil || storedDigest != digest {
		return "", errPreparation
	}
	b.mu.RLock()
	cancel := b.candidateWorkers[id]
	b.mu.RUnlock()
	if state == "cancelled" && cancel != nil {
		cancel()
	}
	return state, nil
}

func (b *urlPreparationRuntime) recoverCandidateAnalyses(ctx context.Context, pool *sql.DB) error {
	rows, err := pool.QueryContext(ctx, `SELECT q.id,q.original_evidence_digest,e.step,e.process_state FROM diagnostic_candidate_verification_requests q
JOIN LATERAL (SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.method='llm/v1' AND e.process_state IN ('requested','checking_stored_evidence') ORDER BY q.id LIMIT 1025`)
	if err != nil {
		return errPreparation
	}
	type pending struct {
		id     int64
		digest string
		step   int
		state  string
	}
	var items []pending
	for rows.Next() {
		var item pending
		if rows.Scan(&item.id, &item.digest, &item.step, &item.state) != nil {
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
		if item.step == 2 && item.state == "checking_stored_evidence" {
			if server.FailCandidateAnalysis(ctx, pool, item.id, "analysis_interrupted") != nil {
				return errPreparation
			}
		} else if item.step == 1 && item.state == "requested" {
			if _, err := b.scheduleCandidateAnalysis(ctx, strconv.FormatInt(item.id, 10), item.digest); err != nil {
				log.Printf("[candidate-analysis] request %d recovery schedule unavailable", item.id)
			}
		} else {
			return errPreparation
		}
	}
	return nil
}

// The installed profile and its remaining ledger are resolved only in the
// protected preparation process. No product request field selects a provider.
func (b *urlPreparationRuntime) executeCandidateAnalysis(ctx context.Context, requestID int64, digest string) (resultErr error) {
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
		reason := "analysis_unavailable"
		if errors.Is(ctx.Err(), context.Canceled) {
			reason = "analysis_interrupted"
		}
		if errors.Is(resultErr, diagnosisprep.ErrCandidateBudget) {
			reason = "budget_unavailable"
		}
		if server.FailCandidateAnalysis(finish, pool, requestID, reason) != nil {
			log.Printf("[candidate-analysis] request %d failure record unavailable", requestID)
		}
	}()
	selected, err := server.ReadCandidateSelectedContext(ctx, pool, requestID)
	if err != nil || selected.Candidate.EvidenceDigest != digest {
		return errPreparation
	}
	var requester string
	if pool.QueryRowContext(ctx, `SELECT requester FROM diagnostic_candidate_verification_requests WHERE id=$1 AND method='llm/v1'`, requestID).Scan(&requester) != nil || requester != "ARTEX" {
		return errPreparation
	}
	owner := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "selected-analysis-owner", Command: diagnosisprep.AuthorizeTask,
		Principal: requester, TaskID: strconv.FormatInt(selected.TaskID, 10)}
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
	if l.productTarget == nil || l.current() != nil {
		return errPreparation
	}
	ledger, ok := b.endpoint.(interface {
		ReserveCandidateBudget(context.Context, string, int64, string) error
		FinishCandidateBudget(int64)
	})
	if !ok {
		return errors.Join(errPreparation, diagnosisprep.ErrCandidateBudget)
	}
	if err := ledger.ReserveCandidateBudget(ctx, owner.TaskID, requestID, digest); err != nil {
		return errors.Join(errPreparation, diagnosisprep.ErrCandidateBudget)
	}
	defer ledger.FinishCandidateBudget(requestID)
	jobID := "candidate-llm-" + strconv.FormatInt(requestID, 10)
	bounded, cancel := context.WithCancel(ctx)
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
	limits.ModelRequests = 1
	limits.InputTokens = l.profile.InputTokensPerCall
	limits.OutputTokens = l.profile.OutputTokensPerCall
	if limits.CostUSDMicros > 200000 {
		limits.CostUSDMicros = 200000
	}
	limits.DurationSeconds = int64(selectedCandidateAnalysisTimeout / time.Second)
	observe := func(diagnosisprep.ModelUsage) error { return nil }
	var budget *server.FirstDiagnosisModelBudget
	if l.launch.ModelExecutionMode == "openrouter" {
		budget, err = server.NewFirstDiagnosisOpenRouterRunBudget(limits, l.profile.InputTokensPerCall, l.profile.OutputTokensPerCall, l.launch.OpenRouterManifestPath, jobID, observe)
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
	if server.BeginCandidateAnalysis(bounded, pool, selected) != nil {
		return errPreparation
	}
	admit := func(call context.Context) error {
		if l.current() != nil || call.Err() != nil {
			return errPreparation
		}
		var step int
		var state string
		if pool.QueryRowContext(call, `SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=$1 ORDER BY step DESC LIMIT 1`, requestID).Scan(&step, &state) != nil ||
			step != 2 || state != "checking_stored_evidence" {
			return errPreparation
		}
		fresh, err := b.endpoint.Call(call, owner)
		if err != nil || diagnosisprep.ValidateResponse(owner, fresh) != nil || fresh.Authorized == nil || !*fresh.Authorized || fresh.Job == nil || fresh.Job.TaskID != owner.TaskID {
			return errPreparation
		}
		return nil
	}
	proposal, err := model.Analyze(bounded, selected, admit)
	if err != nil {
		return errPreparation
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(b.ctx), 10*time.Second)
	defer stop()
	if err := server.CompleteCandidateAnalysis(finish, pool, selected, l.launch.ModelProfileID, proposal, budget.Snapshot()); err != nil {
		return errPreparation
	}
	return nil
}
